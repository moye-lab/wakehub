package agent

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"regexp"
	"time"

	"github.com/google/uuid"
)

// Agent 是 agents 表的一行 + 派生字段（契约 §3.4）。
// ApprovalStatus 是审批状态机的存储列（pending | approved | disabled）；
// Authorized 是其派生镜像（approved 时为 true），标记废弃，仅供过渡期读取。
type Agent struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Subnet          string     `json:"subnet"`
	ApprovalStatus  string     `json:"approval_status"`
	Status          string     `json:"status"`
	Authorized      bool       `json:"authorized"`
	IP              string     `json:"ip"`
	OS              string     `json:"os"`
	Arch            string     `json:"arch"`
	Version         string     `json:"version"`
	LastSeenAt      *time.Time `json:"last_seen_at"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
	CreatedAt       time.Time  `json:"created_at"`
	AllowedSubnets  []string   `json:"allowed_subnets"`
}

var (
	// ErrAgentNotFound 表示 Agent 行不存在。
	ErrAgentNotFound = errors.New("agent not found")
	// ErrTokenNotFound 表示 per-agent token 查无此哈希（或恒定时间比较失败）。
	ErrTokenNotFound = errors.New("agent token not found")
	// ErrSubnetExists 表示白名单里已有该网段。
	ErrSubnetExists = errors.New("subnet already exists")
)

var agentIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// ValidAgentID 校验 Agent ID 格式（契约 §3.4）。
func ValidAgentID(id string) bool {
	return agentIDPattern.MatchString(id)
}

// ReservedAgentID 报告 id 是否为保留值。`local` 是内置节点 ID，
// 迁移 003 已删除种子行，任何远程 Agent 不得以此 ID 注册（修复 B-N8）。
func ReservedAgentID(id string) bool {
	return id == "local"
}

// Store 是 Agent 域的数据库访问层（从 internal/device/repository.go
// 抽出；设备/调度/审计的操作仍留在 repository.go）。
type Store struct {
	db *sql.DB
}

// NewStore 构造 Agent Store。db 须为已应用 001+002+003 迁移的库。
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// DB 暴露底层连接，供测试与调用方使用。
func (s *Store) DB() *sql.DB {
	return s.db
}

// ---- token 明文与哈希（契约 §3.3） ----

// GenerateToken 生成一个 per-agent token 明文及其 SHA-256 hex。
// 明文格式：`wha_` + base64url(32 字节随机数)。服务端只存哈希，
// 明文只在签发时返回一次。
func GenerateToken() (plaintext, hashHex string) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// crypto/rand 读取失败是致命环境问题，不静默降级。
		panic(fmt.Sprintf("agent: 生成 token 随机数失败: %v", err))
	}
	plaintext = "wha_" + base64.RawURLEncoding.EncodeToString(raw[:])
	return plaintext, hashToken(plaintext)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// tokensEqual 做恒定时间比较（修复 B-N7）。长度不同直接返回 false。
func tokensEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ---- Agent 行 ----

const agentColumns = `id, name, subnet, approval_status, status, authorized,
	ip, os, arch, version, last_seen_at, last_heartbeat_at, created_at`

func scanAgent(row interface {
	Scan(dest ...any) error
}) (Agent, error) {
	var a Agent
	var authInt int
	err := row.Scan(
		&a.ID, &a.Name, &a.Subnet, &a.ApprovalStatus, &a.Status, &authInt,
		&a.IP, &a.OS, &a.Arch, &a.Version, &a.LastSeenAt, &a.LastHeartbeatAt, &a.CreatedAt,
	)
	if err != nil {
		return a, err
	}
	a.Authorized = authInt == 1
	return a, nil
}

// GetAgent 按 ID 取一行（含白名单）。不存在返回 ErrAgentNotFound。
func (s *Store) GetAgent(id string) (*Agent, error) {
	a, err := scanAgent(s.db.QueryRow(
		`SELECT `+agentColumns+` FROM agents WHERE id = ?`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrAgentNotFound
		}
		return nil, err
	}
	subnets, err := s.ListAllowedSubnets(id)
	if err != nil {
		return nil, err
	}
	a.AllowedSubnets = subnets
	return &a, nil
}

// ListAgents 返回全部 Agent（按 created_at ASC，契约 §3.4）。
func (s *Store) ListAgents() ([]Agent, error) {
	rows, err := s.db.Query(`SELECT ` + agentColumns + ` FROM agents ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []Agent{}
	}
	for i := range out {
		subnets, err := s.ListAllowedSubnets(out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].AllowedSubnets = subnets
	}
	return out, nil
}

// CreateAgentPending 创建一条 pending 的自助注册行（路径 B）。
// name 为空时用默认展示名；subnet 若为合法 CIDR 则同时写入白名单。
func (s *Store) CreateAgentPending(id, name, subnet, version, osName, arch, ip string) (*Agent, error) {
	if name == "" {
		name = "Agent " + id
	}
	now := time.Now().UTC()
	_, err := s.db.Exec(
		`INSERT INTO agents (id, name, subnet, approval_status, status, authorized,
			ip, os, arch, version, last_seen_at, created_at)
		 VALUES (?, ?, ?, 'pending', 'offline', 0, ?, ?, ?, ?, ?, ?)`,
		id, name, subnet, ip, osName, arch, version, now, now)
	if err != nil {
		return nil, err
	}
	if _, _, err := net.ParseCIDR(subnet); err == nil {
		_ = s.addAllowedSubnetExec(s.db, id, subnet)
	}
	return s.GetAgent(id)
}

// CreateAgentApproved 创建一条已获批的 Agent 行（路径 A，管理员预签发）。
func (s *Store) CreateAgentApproved(id, name, subnet string) (*Agent, error) {
	now := time.Now().UTC()
	_, err := s.db.Exec(
		`INSERT INTO agents (id, name, subnet, approval_status, status, authorized, created_at)
		 VALUES (?, ?, ?, 'approved', 'offline', 1, ?)`,
		id, name, subnet, now)
	if err != nil {
		return nil, err
	}
	if _, _, err := net.ParseCIDR(subnet); err == nil {
		_ = s.addAllowedSubnetExec(s.db, id, subnet)
	}
	return s.GetAgent(id)
}

// CreateAgentApprovedWithToken 在一个事务里创建已获批 Agent 行并签发
// 其首个 per-agent token，返回 Agent 与仅此一次的明文 token。
func (s *Store) CreateAgentApprovedWithToken(id, name, subnet string) (*Agent, string, error) {
	plaintext, hashHex := GenerateToken()
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT INTO agents (id, name, subnet, approval_status, status, authorized, created_at)
		 VALUES (?, ?, ?, 'approved', 'offline', 1, ?)`,
		id, name, subnet, now); err != nil {
		return nil, "", err
	}
	if _, _, err := net.ParseCIDR(subnet); err == nil {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO agent_allowed_subnets (agent_id, cidr) VALUES (?, ?)`,
			id, subnet); err != nil {
			return nil, "", err
		}
	}
	if _, err := tx.Exec(
		`INSERT INTO agent_tokens (id, agent_id, token_hash, revoked, created_at, rotated_at)
		 VALUES (?, ?, ?, 0, ?, ?)`,
		uuid.NewString(), id, hashHex, now, now); err != nil {
		return nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}
	a, err := s.GetAgent(id)
	if err != nil {
		return nil, "", err
	}
	return a, plaintext, nil
}

// UpdateHandshake 更新握手上报的节点信息。已存在行的 name/subnet
// 不被覆盖（契约 §3.5：已存在行不覆盖已有 name）；仅当存储值为空
// 时才写入上报值。每次调用同时刷新 last_seen_at。
func (s *Store) UpdateHandshake(id, name, subnet, version, osName, arch, ip string) error {
	res, err := s.db.Exec(
		`UPDATE agents SET ip = ?, os = ?, arch = ?, version = ?, last_seen_at = ?,
			name = CASE WHEN name = '' AND ? <> '' THEN ? ELSE name END,
			subnet = CASE WHEN subnet = '' AND ? <> '' THEN ? ELSE subnet END
		 WHERE id = ?`,
		ip, osName, arch, version, time.Now().UTC(), name, name, subnet, subnet, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrAgentNotFound
	}
	return nil
}

// SetAgentStatus 更新在线状态（online/offline），同时刷新 last_seen_at。
func (s *Store) SetAgentStatus(id, statusStr string) error {
	res, err := s.db.Exec(
		`UPDATE agents SET status = ?, last_seen_at = ? WHERE id = ?`,
		statusStr, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrAgentNotFound
	}
	return nil
}

// SetAgentHeartbeat 记录一次应用层心跳：刷新 last_heartbeat_at 与
// last_seen_at，不改变在线状态本身（在线/离线由会话管理）。
func (s *Store) SetAgentHeartbeat(id string) error {
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`UPDATE agents SET last_heartbeat_at = ?, last_seen_at = ? WHERE id = ?`,
		now, now, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrAgentNotFound
	}
	return nil
}

// SetApprovalStatus 更新审批状态，同时同步 authorized 派生镜像
// （approved → 1，其余 → 0）。行不存在返回 ErrAgentNotFound。
func (s *Store) SetApprovalStatus(id, approval string) error {
	authInt := 0
	if approval == "approved" {
		authInt = 1
	}
	res, err := s.db.Exec(
		`UPDATE agents SET approval_status = ?, authorized = ? WHERE id = ?`,
		approval, authInt, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrAgentNotFound
	}
	return nil
}

// DeleteAgent 删除 Agent 行；token 与白名单由 FK 级联删除。
// 其设备不删除，agent_id 原样保留（契约 §3.4）。
func (s *Store) DeleteAgent(id string) error {
	res, err := s.db.Exec(`DELETE FROM agents WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrAgentNotFound
	}
	return nil
}

// ---- per-agent token（契约 §3.3） ----

// IssueToken 为 Agent 签发（或重新签发）per-agent token：原子替换
// 同行的哈希，旧 token 立即失效，无宽限期。返回仅此一次的明文。
// 重新签发 = 轮换（契约 §3.3 决策）。
func (s *Store) IssueToken(agentID string) (string, error) {
	plaintext, hashHex := GenerateToken()
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO agent_tokens (id, agent_id, token_hash, revoked, created_at, rotated_at)
		 VALUES (?, ?, ?, 0, ?, ?)
		 ON CONFLICT(agent_id) DO UPDATE SET
			token_hash = excluded.token_hash, revoked = 0, rotated_at = excluded.rotated_at`,
		uuid.NewString(), agentID, hashHex, now, now)
	if err != nil {
		return "", err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", ErrAgentNotFound
	}
	return plaintext, nil
}

// VerifyTokenHash 按明文 token 查哈希：返回绑定的 agent_id 与吊销标志。
// 查无此哈希返回 ErrTokenNotFound；存储哈希须经恒定时间比较复核。
func (s *Store) VerifyTokenHash(token string) (agentID string, revoked bool, err error) {
	hashHex := hashToken(token)
	var storedHash string
	var revokedInt int
	err = s.db.QueryRow(
		`SELECT agent_id, token_hash, revoked FROM agent_tokens WHERE token_hash = ?`,
		hashHex).Scan(&agentID, &storedHash, &revokedInt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, ErrTokenNotFound
		}
		return "", false, err
	}
	if !tokensEqual(storedHash, hashHex) {
		return "", false, ErrTokenNotFound
	}
	return agentID, revokedInt == 1, nil
}

// ErrAgentNotPending 表示 Agent 不处于 pending 状态，无法审批/驳回。
var ErrAgentNotPending = errors.New("agent is not pending")

// ApproveAgent 在一个事务里完成审批：审批状态 → approved，同时签发
// 新的 per-agent token（旧 token 若存在则作废）。返回仅此一次的明文。
func (s *Store) ApproveAgent(id string) (string, error) {
	plaintext, hashHex := GenerateToken()
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var approval string
	if err := tx.QueryRow(
		`SELECT approval_status FROM agents WHERE id = ?`, id).Scan(&approval); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrAgentNotFound
		}
		return "", err
	}
	if approval != "pending" {
		return "", ErrAgentNotPending
	}
	if _, err := tx.Exec(
		`UPDATE agents SET approval_status = 'approved', authorized = 1 WHERE id = ?`, id); err != nil {
		return "", err
	}
	if _, err := tx.Exec(
		`INSERT INTO agent_tokens (id, agent_id, token_hash, revoked, created_at, rotated_at)
		 VALUES (?, ?, ?, 0, ?, ?)
		 ON CONFLICT(agent_id) DO UPDATE SET
			token_hash = excluded.token_hash, revoked = 0, rotated_at = excluded.rotated_at`,
		uuid.NewString(), id, hashHex, now, now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return plaintext, nil
}

// RevokeToken 吊销该 Agent 的当前 token（revoked=1，不删行，保留审计线索）。
func (s *Store) RevokeToken(agentID string) error {
	res, err := s.db.Exec(
		`UPDATE agent_tokens SET revoked = 1 WHERE agent_id = ?`, agentID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrTokenNotFound
	}
	return nil
}

// TouchTokenLastUsed 记录 token 最近一次握手成功使用的时间。
func (s *Store) TouchTokenLastUsed(agentID string) {
	_, _ = s.db.Exec(
		`UPDATE agent_tokens SET last_used_at = ? WHERE agent_id = ?`,
		time.Now().UTC(), agentID)
}

// ---- 扫描白名单（契约 §7.2） ----

// ListAllowedSubnets 返回该 Agent 允许扫描的网段（空列表返回 []，永不为 nil）。
func (s *Store) ListAllowedSubnets(agentID string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT cidr FROM agent_allowed_subnets WHERE agent_id = ? ORDER BY cidr ASC`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var cidr string
		if err := rows.Scan(&cidr); err != nil {
			return nil, err
		}
		out = append(out, cidr)
	}
	return out, rows.Err()
}

func (s *Store) addAllowedSubnetExec(execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}, agentID, cidr string) error {
	_, err := execer.Exec(
		`INSERT INTO agent_allowed_subnets (agent_id, cidr) VALUES (?, ?)`,
		agentID, cidr)
	return err
}

// AddAllowedSubnet 向白名单加一个网段；已存在返回 ErrSubnetExists。
func (s *Store) AddAllowedSubnet(agentID, cidr string) error {
	res, err := s.db.Exec(
		`INSERT INTO agent_allowed_subnets (agent_id, cidr) VALUES (?, ?)
		 ON CONFLICT(agent_id, cidr) DO NOTHING`,
		agentID, cidr)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrSubnetExists
	}
	return nil
}

// RemoveAllowedSubnet 从白名单删一个网段；不存在返回 (false, nil)。
func (s *Store) RemoveAllowedSubnet(agentID, cidr string) (bool, error) {
	res, err := s.db.Exec(
		`DELETE FROM agent_allowed_subnets WHERE agent_id = ? AND cidr = ?`,
		agentID, cidr)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
