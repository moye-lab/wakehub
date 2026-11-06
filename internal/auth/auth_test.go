package auth

import (
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

const testSecret = "test-secret-key-for-m1-tests-0123456789"

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	for _, f := range []string{"../../migrations/001_init.sql", "../../migrations/002_auth.sql"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := db.Exec(string(b)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func setupService(t *testing.T) *Service {
	t.Helper()
	svc := NewService(setupTestDB(t), testSecret, nil)
	if err := svc.EnsureAdmin("admin", "old-password-123"); err != nil {
		t.Fatalf("EnsureAdmin: %v", err)
	}
	return svc
}

func TestEnsureAdminUsernameValidation(t *testing.T) {
	for _, bad := range []string{
		"", "bad|user", "a b", "a/b", "a@b",
		strings.Repeat("a", 65),
	} {
		svc := NewService(setupTestDB(t), testSecret, nil)
		if err := svc.EnsureAdmin(bad, "some-password-123"); err == nil {
			t.Errorf("EnsureAdmin(%q) 应拒绝非法用户名", bad)
		}
	}
	for _, good := range []string{"admin", "admin_1", "a.b-c_d", "A1"} {
		svc := NewService(setupTestDB(t), testSecret, nil)
		if err := svc.EnsureAdmin(good, "some-password-123"); err != nil {
			t.Errorf("EnsureAdmin(%q) 应接受合法用户名，got %v", good, err)
		}
	}
	// 已有用户时再次调用为 no-op。
	svc := setupService(t)
	if err := svc.EnsureAdmin("other", "other-password-123"); err != nil {
		t.Fatalf("重复 EnsureAdmin 应为 no-op，got %v", err)
	}
}

func TestAuthenticateAndVerifyTokenRoundTrip(t *testing.T) {
	svc := setupService(t)
	token, sess, err := svc.Authenticate("admin", "old-password-123", "192.168.1.51")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if token == "" || sess.JTI == "" || sess.Username != "admin" {
		t.Fatalf("会话信息不完整：%+v", sess)
	}
	if sess.ExpiresAt.Sub(sess.IssuedAt) != sessionTTL {
		t.Errorf("TTL 应为 %v，got %v", sessionTTL, sess.ExpiresAt.Sub(sess.IssuedAt))
	}
	got, err := svc.VerifyToken(token)
	if err != nil {
		t.Fatalf("VerifyToken: %v", err)
	}
	if got.Username != "admin" || got.JTI != sess.JTI {
		t.Errorf("VerifyToken 返回会话不匹配：%+v vs %+v", got, sess)
	}
}

func TestVerifyTokenRejectsOldFormat(t *testing.T) {
	svc := setupService(t)
	// v0.1.0 旧格式：base64("username|expiry").sig，无 jti。
	payload := []byte("admin|" + time.Now().Add(time.Hour).Format(time.RFC3339))
	old := base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(svc.sign(payload))
	if _, err := svc.VerifyToken(old); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("旧格式 token 应被拒收，got %v", err)
	}
}

func TestVerifyTokenTampered(t *testing.T) {
	svc := setupService(t)
	token, _, err := svc.Authenticate("admin", "old-password-123", "192.168.1.51")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	parts := strings.Split(token, ".")
	// 篡改载荷（改中间字符；末尾字符可能只有部分比特有效，改动不一定改变解码结果）。
	tamper := func(s string) string {
		i := len(s) / 2
		if s[i] == 'A' {
			return s[:i] + "B" + s[i+1:]
		}
		return s[:i] + "A" + s[i+1:]
	}
	if _, err := svc.VerifyToken(tamper(parts[0]) + "." + parts[1]); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("篡改载荷应被拒收，got %v", err)
	}
	// 篡改签名。
	if _, err := svc.VerifyToken(parts[0] + "." + tamper(parts[1])); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("篡改签名应被拒收，got %v", err)
	}
	// 格式错误。
	for _, bad := range []string{"", "abc", "a.b.c", "!!!.@@@"} {
		if _, err := svc.VerifyToken(bad); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("VerifyToken(%q) 应返回 ErrInvalidToken，got %v", bad, err)
		}
	}
}

func TestVerifyTokenExpired(t *testing.T) {
	svc := setupService(t)
	token, _, err := svc.issueToken("admin", -time.Hour)
	if err != nil {
		t.Fatalf("issueToken: %v", err)
	}
	if _, err := svc.VerifyToken(token); !errors.Is(err, ErrExpiredToken) {
		t.Errorf("过期 token 应返回 ErrExpiredToken，got %v", err)
	}
}

func TestVerifyTokenRevoked(t *testing.T) {
	svc := setupService(t)
	token, sess, err := svc.Authenticate("admin", "old-password-123", "192.168.1.51")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if err := svc.RevokeSession(sess.JTI, sess.Username, sess.ExpiresAt); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if _, err := svc.VerifyToken(token); !errors.Is(err, ErrRevokedToken) {
		t.Errorf("已撤销 token 应返回 ErrRevokedToken，got %v", err)
	}
	// 重复撤销应幂等（INSERT OR IGNORE）。
	if err := svc.RevokeSession(sess.JTI, sess.Username, sess.ExpiresAt); err != nil {
		t.Errorf("重复撤销应成功，got %v", err)
	}
}

// waitNextSecond 等到下一秒边界：token 的 iat 与 password_changed_at
// 均为秒精度，旧会话须在改密前至少一个整秒签发，改密后失效的断言
// 才与时间无关、稳定成立。
func waitNextSecond(t *testing.T) {
	t.Helper()
	next := time.Now().Truncate(time.Second).Add(time.Second).Add(10 * time.Millisecond)
	time.Sleep(time.Until(next))
}

func TestChangePasswordInvalidatesOldSession(t *testing.T) {
	svc := setupService(t)
	oldToken, _, err := svc.Authenticate("admin", "old-password-123", "192.168.1.51")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	waitNextSecond(t)
	if err := svc.ChangePassword("admin", "old-password-123", "new-password-456"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	// 旧会话立即失效。
	if _, err := svc.VerifyToken(oldToken); err == nil {
		t.Error("改密后旧会话应失效")
	}
	// 旧密码登录失败，新密码登录成功。
	if _, _, err := svc.Authenticate("admin", "old-password-123", "192.168.1.51"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("旧密码应登录失败，got %v", err)
	}
	token, _, err := svc.Authenticate("admin", "new-password-456", "192.168.1.51")
	if err != nil {
		t.Fatalf("新密码应登录成功，got %v", err)
	}
	if _, err := svc.VerifyToken(token); err != nil {
		t.Errorf("新会话应校验通过，got %v", err)
	}
}

func TestChangePasswordValidation(t *testing.T) {
	svc := setupService(t)
	// 当前密码错误。
	err := svc.ChangePassword("admin", "wrong-password", "new-password-456")
	var ve *ValidationError
	if !errors.As(err, &ve) || len(ve.Errors) != 1 || ve.Errors[0].Code != "wrong_password" {
		t.Errorf("当前密码错误应返回 wrong_password，got %v", err)
	}
	// 新密码太短。
	err = svc.ChangePassword("admin", "old-password-123", "short")
	if !errors.As(err, &ve) || ve.Errors[0].Code != "weak_password" {
		t.Errorf("短密码应返回 weak_password，got %v", err)
	}
	// 新密码等于用户名。
	err = svc.ChangePassword("admin", "old-password-123", "admin")
	if !errors.As(err, &ve) || ve.Errors[0].Code != "weak_password" {
		t.Errorf("与用户名相同的密码应返回 weak_password，got %v", err)
	}
	// 新密码为占位值。
	for _, p := range []string{"wakehub123", "change-me-in-production-wakehub-secret", "replace-with-random-agent-token"} {
		err = svc.ChangePassword("admin", "old-password-123", p)
		if !errors.As(err, &ve) || ve.Errors[0].Code != "weak_password" {
			t.Errorf("占位值密码 %q 应返回 weak_password，got %v", p, err)
		}
	}
	// 不存在的用户。
	if err := svc.ChangePassword("ghost", "x", "new-password-456"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("不存在的用户应返回 ErrUserNotFound，got %v", err)
	}
}

func TestAuthenticateUnknownUserDummyPath(t *testing.T) {
	svc := setupService(t)
	// 用户不存在与密码错误返回完全一致的文案（B-N6）。
	_, _, errGhost := svc.Authenticate("ghost", "whatever-123", "192.168.1.51")
	_, _, errWrong := svc.Authenticate("admin", "wrong-password", "192.168.1.51")
	if errGhost == nil || errWrong == nil {
		t.Fatal("两种失败都应返回错误")
	}
	if errGhost.Error() != errWrong.Error() {
		t.Errorf("用户不存在与密码错误的文案必须一致：%q vs %q", errGhost, errWrong)
	}
	if !errors.Is(errGhost, ErrInvalidCredentials) || !errors.Is(errWrong, ErrInvalidCredentials) {
		t.Errorf("两种失败都应为 ErrInvalidCredentials：%v / %v", errGhost, errWrong)
	}
}

func TestResetPassword(t *testing.T) {
	svc := setupService(t)
	oldToken, _, err := svc.Authenticate("admin", "old-password-123", "192.168.1.51")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	waitNextSecond(t)
	if err := svc.ResetPassword("admin", "reset-password-789"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if _, err := svc.VerifyToken(oldToken); err == nil {
		t.Error("重置后旧会话应失效")
	}
	if _, _, err := svc.Authenticate("admin", "old-password-123", "192.168.1.51"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("旧密码应登录失败，got %v", err)
	}
	if _, _, err := svc.Authenticate("admin", "reset-password-789", "192.168.1.51"); err != nil {
		t.Errorf("新密码应登录成功，got %v", err)
	}
	if err := svc.ResetPassword("admin", "short"); err == nil {
		t.Error("重置为弱密码应失败")
	}
}

func TestClientIP(t *testing.T) {
	trusted := parseTrustedForTest(t, "192.168.1.0/24")
	cases := []struct {
		name       string
		proxies    []*net.IPNet
		remoteAddr string
		xff        string
		want       string
	}{
		{"带端口的 RemoteAddr 取 host", nil, "192.168.1.51:64692", "", "192.168.1.51"},
		{"裸 IP 直接使用", nil, "192.168.1.51", "", "192.168.1.51"},
		{"IPv6 带端口", nil, "[2001:db8::1]:1234", "", "2001:db8::1"},
		{"默认不信任 XFF", nil, "192.168.1.51:64692", "203.0.113.7", "192.168.1.51"},
		{"受信代理取 XFF 第一个", trusted, "192.168.1.5:1234", "203.0.113.7, 192.168.1.1", "203.0.113.7"},
		{"直连方不在信任列表则忽略 XFF", trusted, "192.168.2.9:1234", "203.0.113.7", "192.168.2.9"},
		{"受信代理但 XFF 为空", trusted, "192.168.1.5:1234", "", "192.168.1.5"},
		{"受信代理但 XFF 非法", trusted, "192.168.1.5:1234", "not-an-ip", "192.168.1.5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(setupTestDB(t), testSecret, tc.proxies)
			req, _ := http.NewRequest("GET", "/", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := svc.ClientIP(req); got != tc.want {
				t.Errorf("ClientIP() = %q, want %q", got, tc.want)
			}
		})
	}
}

func parseTrustedForTest(t *testing.T, raw string) []*net.IPNet {
	t.Helper()
	var out []*net.IPNet
	for _, item := range strings.Split(raw, ",") {
		_, n, err := net.ParseCIDR(strings.TrimSpace(item))
		if err != nil {
			t.Fatalf("parse CIDR %q: %v", item, err)
		}
		out = append(out, n)
	}
	return out
}

func TestRateLimitIPAndAccount(t *testing.T) {
	svc := setupService(t)
	// 同一 IP、同一账号失败 8 次后锁定（此处传裸 IP，与 ClientIP 输出一致）。
	for i := 0; i < 8; i++ {
		if _, _, err := svc.Authenticate("admin", "wrong-password", "192.168.1.51"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("第 %d 次失败应为 ErrInvalidCredentials，got %v", i+1, err)
		}
	}
	// 第 9 次：即使密码正确也被锁定。
	if _, _, err := svc.Authenticate("admin", "old-password-123", "192.168.1.51"); !errors.Is(err, ErrRateLimited) {
		t.Errorf("锁定期间正确密码也应被拒（ErrRateLimited），got %v", err)
	}
	// 账号维度：换 IP 用同一账号登录同样被锁定。
	if _, _, err := svc.Authenticate("admin", "old-password-123", "192.168.1.99"); !errors.Is(err, ErrRateLimited) {
		t.Errorf("账号维度锁定应跨 IP 生效，got %v", err)
	}
	// 模拟 5 分钟过期后恢复。
	svc.pruneFailures(time.Now().Add(6 * time.Minute))
	if _, _, err := svc.Authenticate("admin", "old-password-123", "192.168.1.51"); err != nil {
		t.Errorf("过期后应恢复登录，got %v", err)
	}
	// 成功登录清除失败记录：失败 7 次后成功，再失败 1 次不应被锁定。
	for i := 0; i < 7; i++ {
		_, _, _ = svc.Authenticate("admin", "wrong-password", "192.168.1.77")
	}
	if _, _, err := svc.Authenticate("admin", "old-password-123", "192.168.1.77"); err != nil {
		t.Fatalf("第 8 次前成功登录应放行，got %v", err)
	}
	if _, _, err := svc.Authenticate("admin", "wrong-password", "192.168.1.77"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("成功登录后失败计数应清零，got %v", err)
	}
}

func TestRateLimitKeysCapped(t *testing.T) {
	svc2 := NewService(setupTestDB(t), testSecret, nil)
	for i := 0; i < maxRateLimitKeys+500; i++ {
		svc2.recordFailure(fmt.Sprintf("10.2.%d.%d", i/256, i%256), fmt.Sprintf("user-%d", i))
	}
	svc2.mu.Lock()
	n := svc2.failures.Len()
	svc2.mu.Unlock()
	if n > maxRateLimitKeys {
		t.Errorf("限流 map 应有容量上限 %d，got %d", maxRateLimitKeys, n)
	}
}

func TestCleanupRevokedTokens(t *testing.T) {
	svc := setupService(t)
	// 撤销一个已过期的会话与一个未过期的会话。
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	_ = svc.RevokeSession("jti-past", "admin", past)
	_ = svc.RevokeSession("jti-future", "admin", future)
	n, err := svc.CleanupRevokedTokens()
	if err != nil {
		t.Fatalf("CleanupRevokedTokens: %v", err)
	}
	if n != 1 {
		t.Errorf("应清理 1 行过期撤销记录，got %d", n)
	}
	var count int
	if err := svc.db.QueryRow(`SELECT COUNT(1) FROM revoked_tokens WHERE jti = 'jti-future'`).Scan(&count); err != nil || count != 1 {
		t.Errorf("未过期的撤销行应保留，count=%d err=%v", count, err)
	}
}

// TestRequestIsSecure 覆盖 v0.2.9 问题 4：直连 TLS 放行；X-Forwarded-Proto
// 只采信可信代理的；非可信方谎报 https 也不采信；多值按不安全处理。
func TestRequestIsSecure(t *testing.T) {
	trusted := parseTrustedForTest(t, "192.168.1.0/24")
	cases := []struct {
		name       string
		proxies    []*net.IPNet
		remoteAddr string
		xfp        string
		tls        bool
		want       bool
	}{
		{"直连 TLS", nil, "203.0.113.9:1234", "", true, true},
		{"直连 TLS 无视 XFP", trusted, "192.168.1.5:1234", "http", true, true},
		{"纯 HTTP 无代理", nil, "192.168.1.51:64692", "", false, false},
		{"纯 HTTP 谎报 https（无信任）", nil, "192.168.1.51:64692", "https", false, false},
		{"可信代理 + https", trusted, "192.168.1.5:1234", "https", false, true},
		{"可信代理 + HTTPS 大写", trusted, "192.168.1.5:1234", "HTTPS", false, true},
		{"可信代理 + http", trusted, "192.168.1.5:1234", "http", false, false},
		{"可信代理无 XFP", trusted, "192.168.1.5:1234", "", false, false},
		{"不可信直连方 + https", trusted, "192.168.2.9:1234", "https", false, false},
		{"可信代理 + 多值 XFP", trusted, "192.168.1.5:1234", "https, http", false, false},
		{"裸 IP 直连方受信", trusted, "192.168.1.5", "https", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(setupTestDB(t), testSecret, tc.proxies)
			req, _ := http.NewRequest("GET", "/", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.xfp != "" {
				req.Header.Set("X-Forwarded-Proto", tc.xfp)
			}
			if tc.tls {
				req.TLS = &tls.ConnectionState{}
			}
			if got := svc.RequestIsSecure(req); got != tc.want {
				t.Errorf("RequestIsSecure() = %v, want %v", got, tc.want)
			}
		})
	}
}
