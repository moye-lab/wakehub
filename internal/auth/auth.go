package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// 会话 TTL：24 小时（与 v0.1.0 一致，契约 §2.3）。
const sessionTTL = 24 * time.Hour

// 登录限流（契约 §2.5）：同一 IP 或同一账号 5 分钟内失败 ≥ 8 次即锁定。
const (
	loginFailureWindow    = 5 * time.Minute
	loginFailureThreshold = 8
	// 限流 map 硬上限，超限时淘汰最旧的 key（防内存无限增长）。
	maxRateLimitKeys = 10000
	// 限流过期记录清理间隔；撤销表过期行每小时清理一次。
	rateLimitCleanupInterval = 5 * time.Minute
)

var (
	ErrInvalidCredentials = errors.New("用户名或密码错误")
	ErrRateLimited        = errors.New("登录失败次数过多，请稍后再试")
	ErrInvalidToken       = errors.New("无效的登录凭证")
	ErrExpiredToken       = errors.New("登录凭证已过期")
	ErrRevokedToken       = errors.New("登录凭证已被撤销")
	ErrUserNotFound       = errors.New("用户不存在")
)

// 用户名字符集（契约 §2.3，修复 B-N14）：含 | 等字符的用户名在种子时即被拒绝。
var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func validUsername(username string) bool {
	return usernamePattern.MatchString(username)
}

// 新密码不得使用的占位值（契约 §2.4/§2.7）。
var placeholderPasswords = []string{
	"wakehub123",
	"replace-with-a-strong-admin-password",
	"change-me-in-production-wakehub-secret",
	"replace-with-strong-secret-key",
	"replace-with-random-secret-key",
	"local-agent-secret-token",
	"replace-with-random-agent-token",
}

// Session 是校验通过的会话信息。
type Session struct {
	Username  string
	JTI       string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

type sessionContextKey struct{}

// WithSession 把会话存入请求上下文（withAuth 写入，logout/me 读取）。
func WithSession(ctx context.Context, sess Session) context.Context {
	return context.WithValue(ctx, sessionContextKey{}, sess)
}

// SessionFromContext 从请求上下文取出会话。
func SessionFromContext(ctx context.Context) (Session, bool) {
	sess, ok := ctx.Value(sessionContextKey{}).(Session)
	return sess, ok
}

// FieldError 是契约 §1.2 的字段级校验错误。
type FieldError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ValidationError 携带一组字段级错误。
type ValidationError struct {
	Errors []FieldError
}

func (e *ValidationError) Error() string {
	if len(e.Errors) == 0 {
		return "校验失败"
	}
	return e.Errors[0].Message
}

type Service struct {
	db             *sql.DB
	secretKey      []byte
	trustedProxies []*net.IPNet
	mu             sync.Mutex
	// failures 是登录失败记录的存储后端（v0.2.9 问题 9）：目前为内存实现，
	// 重启后记录清空；需要持久化时可替换 failureStore 的其他实现。
	failures failureStore
}

func NewService(db *sql.DB, secret string, trustedProxies []*net.IPNet) *Service {
	return &Service{
		db:             db,
		secretKey:      []byte(secret),
		trustedProxies: trustedProxies,
		failures:       newMemoryFailureStore(),
	}
}

// dummyHash 是用户不存在时用于时序填充的 bcrypt 哈希（修复 B-N6 的
// 用户名枚举时序侧信道）：不存在的用户也走一次完整 bcrypt 比较，
// 返回文案与密码错误完全一致。
var (
	dummyHashOnce sync.Once
	dummyHashVal  []byte
)

func dummyHash() []byte {
	dummyHashOnce.Do(func() {
		h, err := bcrypt.GenerateFromPassword([]byte("wakehub-dummy-credential-for-timing"), bcrypt.DefaultCost)
		if err == nil {
			dummyHashVal = h
		}
	})
	return dummyHashVal
}

func (s *Service) EnsureAdmin(username, password string) error {
	if !validUsername(username) {
		return fmt.Errorf("管理员用户名 %q 不合法：只允许字母、数字、点、下划线、连字符，长度 1–64", username)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM users`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO users (id, username, password_hash, role) VALUES (?, ?, ?, 'admin')`,
		uuid.NewString(), username, string(hash),
	)
	return err
}

// AdminUsername 返回首个管理员用户名（供 reset-password CLI 使用）。
func (s *Service) AdminUsername() (string, error) {
	var username string
	err := s.db.QueryRow(`SELECT username FROM users WHERE role = 'admin' ORDER BY created_at ASC LIMIT 1`).Scan(&username)
	if err == sql.ErrNoRows {
		return "", ErrUserNotFound
	}
	return username, err
}

// Authenticate 校验用户名密码，成功返回签发好的会话 token 与会话信息。
// 限流命中时即使密码正确也不放行（契约 §2.5）。
func (s *Service) Authenticate(username, password, clientIP string) (string, Session, error) {
	if s.isRateLimited(clientIP, username) {
		return "", Session{}, ErrRateLimited
	}
	var hash string
	err := s.db.QueryRow(`SELECT password_hash FROM users WHERE username = ?`, username).Scan(&hash)
	if err == sql.ErrNoRows {
		// 用户不存在：做一次 dummy bcrypt 再返回统一文案（B-N6）。
		_ = bcrypt.CompareHashAndPassword(dummyHash(), []byte(password))
		s.recordFailure(clientIP, username)
		return "", Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return "", Session{}, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		s.recordFailure(clientIP, username)
		return "", Session{}, ErrInvalidCredentials
	}
	s.recordSuccess(clientIP, username)
	return s.issueToken(username, sessionTTL)
}

// sessionPayload 是 token 的 JSON 载荷（契约 §2.3）。
type sessionPayload struct {
	Sub string `json:"sub"`
	JTI string `json:"jti"`
	IAT int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

// issueToken 签发新会话：base64url(JSON payload) + "." + base64url(HMAC)。
func (s *Service) issueToken(username string, ttl time.Duration) (string, Session, error) {
	now := time.Now().UTC().Truncate(time.Second)
	sess := Session{
		Username:  username,
		JTI:       uuid.NewString(),
		IssuedAt:  now,
		ExpiresAt: now.Add(ttl),
	}
	payload, err := json.Marshal(sessionPayload{
		Sub: sess.Username,
		JTI: sess.JTI,
		IAT: sess.IssuedAt.Unix(),
		Exp: sess.ExpiresAt.Unix(),
	})
	if err != nil {
		return "", Session{}, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + base64.RawURLEncoding.EncodeToString(s.sign(payload)), sess, nil
}

// VerifyToken 按契约 §2.3 的顺序校验：格式 → 签名（恒定时间比较）→
// exp → jti 不在撤销表 → iat >= password_changed_at。旧格式直接拒收。
func (s *Service) VerifyToken(tokenStr string) (Session, error) {
	parts := strings.Split(tokenStr, ".")
	if len(parts) != 2 {
		return Session{}, ErrInvalidToken
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Session{}, ErrInvalidToken
	}
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Session{}, ErrInvalidToken
	}
	// 恒定时间比较签名（subtle.ConstantTimeCompare；长度不同亦安全返回 false）。
	if subtle.ConstantTimeCompare(sigBytes, s.sign(payloadBytes)) != 1 {
		return Session{}, ErrInvalidToken
	}
	var payload sessionPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return Session{}, ErrInvalidToken
	}
	if payload.Sub == "" || !validUsername(payload.Sub) {
		return Session{}, ErrInvalidToken
	}
	if _, err := uuid.Parse(payload.JTI); err != nil {
		return Session{}, ErrInvalidToken
	}
	now := time.Now().UTC()
	if payload.Exp <= now.Unix() {
		return Session{}, ErrExpiredToken
	}
	if payload.IAT > now.Unix()+60 {
		return Session{}, ErrInvalidToken
	}
	sess := Session{
		Username:  payload.Sub,
		JTI:       payload.JTI,
		IssuedAt:  time.Unix(payload.IAT, 0).UTC(),
		ExpiresAt: time.Unix(payload.Exp, 0).UTC(),
	}
	var revoked int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM revoked_tokens WHERE jti = ?`, payload.JTI).Scan(&revoked); err != nil {
		return Session{}, err
	}
	if revoked > 0 {
		return Session{}, ErrRevokedToken
	}
	// 改密后旧会话失效：签发时间必须晚于最后一次改密时间。
	// password_changed_at 为 NULL（从未改密）时跳过。
	var changedAt sql.NullString
	err = s.db.QueryRow(`SELECT password_changed_at FROM users WHERE username = ?`, payload.Sub).Scan(&changedAt)
	if err == sql.ErrNoRows {
		return Session{}, ErrInvalidToken
	}
	if err != nil {
		return Session{}, err
	}
	if changedAt.Valid {
		if t, err := parseDBTime(changedAt.String); err == nil {
			// 契约 §2.3：要求 iat >= password_changed_at。两者均为秒精度，
			// 同一秒内签发的会话视为有效（改密后立即重新登录的常规流程）。
			if sess.IssuedAt.Before(t) {
				return Session{}, ErrInvalidToken
			}
		}
	}
	return sess, nil
}

// RevokeSession 把 jti 写入撤销表（logout，契约 §2.4）。
func (s *Service) RevokeSession(jti, username string, expiresAt time.Time) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO revoked_tokens (jti, username, expires_at, reason) VALUES (?, ?, ?, 'logout')`,
		jti, username, expiresAt.UTC().Format("2006-01-02 15:04:05"),
	)
	return err
}

// ChangePassword 校验当前密码并更换为新密码，成功后全部会话失效
// （password_changed_at 置为当前时间；契约 §2.4）。
func (s *Service) ChangePassword(username, currentPassword, newPassword string) error {
	var hash string
	err := s.db.QueryRow(`SELECT password_hash FROM users WHERE username = ?`, username).Scan(&hash)
	if err == sql.ErrNoRows {
		return ErrUserNotFound
	}
	if err != nil {
		return err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(currentPassword)); err != nil {
		return &ValidationError{Errors: []FieldError{
			{Field: "current_password", Code: "wrong_password", Message: "当前密码不正确"},
		}}
	}
	if fe := validateNewPassword(username, newPassword); fe != nil {
		return &ValidationError{Errors: []FieldError{*fe}}
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`UPDATE users SET password_hash = ?, password_changed_at = CURRENT_TIMESTAMP WHERE username = ?`,
		string(newHash), username,
	)
	return err
}

// ResetPassword 供 reset-password CLI 使用：不校验当前密码，但同样
// 走新密码强度校验并更新 password_changed_at（全部会话失效，不删库）。
func (s *Service) ResetPassword(username, newPassword string) error {
	if !validUsername(username) {
		return &ValidationError{Errors: []FieldError{
			{Field: "username", Code: "invalid_format", Message: "用户名不合法"},
		}}
	}
	if fe := validateNewPassword(username, newPassword); fe != nil {
		return &ValidationError{Errors: []FieldError{*fe}}
	}
	var exists int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM users WHERE username = ?`, username).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrUserNotFound
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`UPDATE users SET password_hash = ?, password_changed_at = CURRENT_TIMESTAMP WHERE username = ?`,
		string(newHash), username,
	)
	return err
}

// validateNewPassword 校验新密码强度（契约 §2.4）：长度 ≥ 8 位，
// 不得与用户名相同，不得等于任何已知占位值。
func validateNewPassword(username, password string) *FieldError {
	if utf8.RuneCountInString(password) < 8 {
		return &FieldError{Field: "new_password", Code: "weak_password", Message: "新密码长度至少 8 位"}
	}
	if password == username {
		return &FieldError{Field: "new_password", Code: "weak_password", Message: "新密码不得与用户名相同"}
	}
	for _, p := range placeholderPasswords {
		if password == p {
			return &FieldError{Field: "new_password", Code: "weak_password", Message: "新密码不得使用示例占位值"}
		}
	}
	return nil
}

func (s *Service) sign(data []byte) []byte {
	mac := hmac.New(sha256.New, s.secretKey)
	mac.Write(data)
	return mac.Sum(nil)
}

// ClientIP 提取客户端 IP（不含端口，契约 §2.5）：
// RemoteAddr 经 net.SplitHostPort 取 host；X-Forwarded-For 默认不信任，
// 仅当直连方 IP 在 WAKEHUB_TRUSTED_PROXIES 内时才取其第一个 IP。
func (s *Service) ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// RemoteAddr 本身就是裸 IP（或无法解析）时直接使用。
		if ip := net.ParseIP(strings.TrimSpace(r.RemoteAddr)); ip != nil {
			host = ip.String()
		} else {
			host = r.RemoteAddr
		}
	}
	if len(s.trustedProxies) == 0 {
		return host
	}
	remoteIP := net.ParseIP(host)
	if remoteIP == nil || !s.isTrustedProxy(remoteIP) {
		return host
	}
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return host
	}
	first := strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
	if ip := net.ParseIP(first); ip != nil {
		return ip.String()
	}
	return host
}

func (s *Service) isTrustedProxy(ip net.IP) bool {
	for _, network := range s.trustedProxies {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// RequestIsSecure 报告本次请求是否走 HTTPS（v0.2.9 问题 4）：直连 TLS，
// 或直连方是可信代理且 X-Forwarded-Proto 为 https。非可信代理送来的
// X-Forwarded-Proto 一律不采信（否则攻击者自报 https 就能让服务端
// 对明文请求下发 Secure Cookie，登录直接断掉）。X-Forwarded-Proto
// 含多个值时整体不等于 https，按不安全处理（fail-closed）。
func (s *Service) RequestIsSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if len(s.trustedProxies) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	remoteIP := net.ParseIP(strings.TrimSpace(host))
	if remoteIP == nil || !s.isTrustedProxy(remoteIP) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

// CleanupRevokedTokens 删除已过期的撤销行（expires_at < 当前时间）。
func (s *Service) CleanupRevokedTokens() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM revoked_tokens WHERE expires_at < CURRENT_TIMESTAMP`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// StartCleanup 启动定期清理：启动时清理一次，之后每 5 分钟清理限流
// 过期记录，每小时清理撤销表过期行；ctx 取消时退出。
func (s *Service) StartCleanup(ctx context.Context) {
	_, _ = s.CleanupRevokedTokens()
	s.pruneFailures(time.Now())
	ticker := time.NewTicker(rateLimitCleanupInterval)
	defer ticker.Stop()
	ticks := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pruneFailures(time.Now())
			ticks++
			if ticks%12 == 0 {
				_, _ = s.CleanupRevokedTokens()
			}
		}
	}
}

// parseDBTime 解析 SQLite DATETIME 字符串（CURRENT_TIMESTAMP 格式为主）。
func parseDBTime(raw string) (time.Time, error) {
	layouts := []string{
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		time.RFC3339,
	}
	var err error
	for _, layout := range layouts {
		var t time.Time
		t, err = time.ParseInLocation(layout, raw, time.UTC)
		if err == nil {
			return t, nil
		}
	}
	return time.Time{}, err
}
