package device

import (
	"database/sql"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	dbpkg "github.com/wakehub/wakehub/internal/db"
	"github.com/wakehub/wakehub/internal/wol"
	_ "modernc.org/sqlite"
)

// Device.Enabled 为 *bool：JSON 未传 enabled 时为 nil，Create 时默认启用；
// 只有明确传 false 时才禁用。修复了旧版 bool 零值 false 导致新设备默认禁用的问题。
type Device struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	MAC     string `json:"mac"`
	IP      string `json:"ip"`
	AgentID string `json:"agent_id"`
	// NetworkID 是设备所属网络（v0.2.9 问题 2）：MAC 唯一范围为“网络内唯一”，
	// 不同网络允许相同 MAC；空值按 "default" 处理（仓库层兜底）。
	NetworkID        string     `json:"network_id"`
	BroadcastAddress string     `json:"broadcast_address"`
	WOLPort          int        `json:"wol_port"`
	DeviceType       string     `json:"device_type"`
	StatusMethod     string     `json:"status_method"`
	StatusPort       int        `json:"status_port"`
	Status           string     `json:"status"`
	LastCheckedAt    *time.Time `json:"last_checked_at"`
	LastStatusError  string     `json:"last_status_error"`
	GroupID          string     `json:"group_id"`
	Enabled          *bool      `json:"enabled"`
	SleepType        string     `json:"sleep_type"`
	SleepEndpoint    string     `json:"sleep_endpoint"`
	// SSH 关机适配器（契约 §6.3）：SSHUser/SSHPort 为连接信息；
	// SSHPassword 仅为入参明文（创建/更新时用），不落库、不返回；
	// SSHPasswordEnc 为 AES-256-GCM 密文（json:"-" 永不序列化）；
	// SSHPasswordSet 由密文是否为空推导；SSHHostKeySHA256 为 TOFU
	// 学到的主机密钥指纹（小写 hex），改密码即清空重新信任。
	SSHUser          string `json:"ssh_user"`
	SSHPort          int    `json:"ssh_port"`
	SSHPassword      string `json:"ssh_password,omitempty"`
	SSHPasswordEnc   string `json:"-"`
	SSHPasswordSet   bool   `json:"ssh_password_set"`
	SSHHostKeySHA256 string `json:"ssh_host_key_sha256"`
	// SSHOS 是 SSH 目标系统："linux" | "windows"，默认 linux。
	SSHOS string `json:"ssh_os"`
	// SSHUseSudo 是“使用 sudo 关机”（v0.2.12，Linux 专用）：勾选后
	// Agent 以单次会话 sudo -S 执行关机命令，密码经 stdin 一次喂给
	// sudo，校验与断电在同一个 sudo 调用里完成，不依赖跨会话的时间戳；
	// 目标机无需预先写 polkit 规则或改 sudoers；非 SSH 适配器或非
	// Linux 目标时由仓库层归零。
	SSHUseSudo bool      `json:"ssh_use_sudo"`
	Remark     string    `json:"remark"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Network 是多网络管理的网络实体（v0.2.9 问题 2）：MAC 唯一范围为
// “网络内唯一”，不同网络允许相同 MAC（如克隆设备、隔离网络中的虚拟机）。
// ID "default" 为内置默认网络，恒存在，不可删除。
type Network struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	CIDR        string    `json:"cidr"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// DefaultNetworkID 是内置默认网络的 ID。
const DefaultNetworkID = "default"

// ErrNetworkNotFound 表示网络不存在。
var ErrNetworkNotFound = errors.New("network not found")

// ErrNetworkCIDRConflict 表示非空 CIDR 已被另一网络占用。
var ErrNetworkCIDRConflict = errors.New("network cidr already exists")

// ErrNetworkInUse 表示网络仍被设备或 Agent 引用，拒绝删除。
var ErrNetworkInUse = errors.New("network in use")

// ErrNetworkDeleteDefault 表示默认网络不可删除。
var ErrNetworkDeleteDefault = errors.New("default network cannot be deleted")

// ErrNetworkNameRequired 表示网络名称必填。
var ErrNetworkNameRequired = errors.New("network name is required")

// ErrNetworkCIDRInvalid 表示网络 CIDR 非法。
var ErrNetworkCIDRInvalid = errors.New("network cidr is invalid")

// IsEnabled 对 nil（未指定）返回 true，保持“默认启用”语义。
func (d *Device) IsEnabled() bool {
	return d.Enabled == nil || *d.Enabled
}

type Group struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

type ScheduleItem struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CronExpr   string     `json:"cron_expr"`
	Timezone   string     `json:"timezone"`
	TargetType string     `json:"target_type"`
	TargetID   string     `json:"target_id"`
	Action     string     `json:"action"`
	RetryCount int        `json:"retry_count"`
	RunOnce    bool       `json:"run_once"`
	Enabled    bool       `json:"enabled"`
	LastRunAt  *time.Time `json:"last_run_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

type DashboardStats struct {
	TotalDevices   int            `json:"total_devices"`
	OnlineDevices  int            `json:"online_devices"`
	OfflineDevices int            `json:"offline_devices"`
	UnknownDevices int            `json:"unknown_devices"`
	TotalAgents    int            `json:"total_agents"`
	OnlineAgents   int            `json:"online_agents"`
	PendingAgents  int            `json:"pending_agents"`
	ServerStatus   string         `json:"server_status"`
	DBStatus       string         `json:"db_status"`
	UptimeSeconds  int64          `json:"uptime_seconds"`
	Version        string         `json:"version"`
	UpcomingTasks  []UpcomingTask `json:"upcoming_tasks"`
}

// UpcomingTask 是 Dashboard 即将执行的任务（契约 §10）：真实计算
// 下次执行时间，取已启用任务中 next_run_at 最近的 5 条。
type UpcomingTask struct {
	ScheduleID string     `json:"schedule_id"`
	Name       string     `json:"name"`
	TargetName string     `json:"target_name"`
	NextRunAt  *time.Time `json:"next_run_at"`
}

// TaskRunDevice 是一次 run 内单个设备的执行结果（M4，迁移 004）。
// scheduler.RunDeviceResult 是本类型的别名，两边字段保持一致。
type TaskRunDevice struct {
	DeviceID   string     `json:"device_id"`
	Name       string     `json:"name"`
	Status     string     `json:"status"` // success | failed | skipped
	Reason     string     `json:"reason"`
	SentAt     *time.Time `json:"sent_at,omitempty"`
	BootStatus *string    `json:"boot_status,omitempty"` // sent：已发包（不等于已开机）
}

// TaskRun 是一次定时执行的完整记录（M4，迁移 004）。
// scheduler.RunRecord 是本类型的别名，两边字段保持一致。
type TaskRun struct {
	ID         string          `json:"id"`
	ScheduleID string          `json:"schedule_id"`
	Status     string          `json:"status"` // success | partial | failed | skipped
	Output     string          `json:"output"`
	Devices    []TaskRunDevice `json:"devices"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt time.Time       `json:"finished_at"`
	DurationMs int64           `json:"duration_ms"`
}

type Repository struct {
	db *sql.DB
}

// InitDB 打开数据库（M5：只负责建库与连接，不再执行迁移文件；
// 迁移由 internal/db.Migrate 在启动时按版本表应用）。
// DSN 附加审计第 17 项要求的 pragma：busy_timeout 5s（并发写等待）、
// foreign_keys（外键约束）与 journal_mode=WAL（单连接下实际生效）。
func InitDB(dbPath string) (*sql.DB, error) {
	// v0.2.9 问题 11：数据目录 0700（已存在的旧目录也显式收紧）。
	if err := dbpkg.EnsurePrivateDir(filepath.Dir(dbPath)); err != nil {
		return nil, err
	}
	dsn := dbPath
	if dbPath != ":memory:" {
		sep := "?"
		if strings.Contains(dbPath, "?") {
			sep = "&"
		}
		dsn = dbPath + sep + "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// Ping 探测数据库连通性（Dashboard 的 db_status 用，SELECT 1）。
func (r *Repository) Ping() error {
	return r.db.Ping()
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) DB() *sql.DB {
	return r.db
}

func scanDevice(rows *sql.Rows) (Device, error) {
	var d Device
	var enabledInt int
	var sudoInt int
	err := rows.Scan(
		&d.ID, &d.Name, &d.MAC, &d.IP, &d.AgentID, &d.BroadcastAddress, &d.WOLPort,
		&d.DeviceType, &d.StatusMethod, &d.StatusPort, &d.Status, &d.LastCheckedAt,
		&d.LastStatusError, &d.GroupID, &enabledInt, &d.SleepType, &d.SleepEndpoint,
		&d.Remark, &d.CreatedAt, &d.UpdatedAt,
		&d.SSHUser, &d.SSHPort, &d.SSHPasswordEnc, &d.SSHHostKeySHA256, &d.SSHOS, &sudoInt,
		&d.NetworkID,
	)
	if err != nil {
		return d, err
	}
	enabled := enabledInt == 1
	d.Enabled = &enabled
	d.SSHPasswordSet = d.SSHPasswordEnc != ""
	d.SSHUseSudo = sudoInt == 1
	return d, nil
}

func (r *Repository) ListDevices(groupID, search string) ([]Device, error) {
	query := `SELECT id, name, mac, ip, agent_id, broadcast_address, wol_port, device_type,
	          status_method, status_port, status, last_checked_at, last_status_error,
	          group_id, enabled, sleep_type, sleep_endpoint, remark, created_at, updated_at,
		  ssh_user, ssh_port, ssh_password_enc, ssh_host_key_sha256, ssh_os, ssh_use_sudo, network_id
	          FROM devices WHERE 1=1`
	var args []any
	if groupID != "" {
		query += ` AND group_id = ?`
		args = append(args, groupID)
	}
	if search != "" {
		query += ` AND (name LIKE ? OR ip LIKE ? OR mac LIKE ?)`
		pat := "%" + search + "%"
		args = append(args, pat, pat, pat)
	}
	query += ` ORDER BY created_at DESC`

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, d)
	}
	if list == nil {
		list = []Device{}
	}
	return list, nil
}

func (r *Repository) GetDevice(id string) (*Device, error) {
	var d Device
	var enabledInt int
	var sudoInt int
	err := r.db.QueryRow(
		`SELECT id, name, mac, ip, agent_id, broadcast_address, wol_port, device_type,
		 status_method, status_port, status, last_checked_at, last_status_error,
		 group_id, enabled, sleep_type, sleep_endpoint, remark, created_at, updated_at,
		  ssh_user, ssh_port, ssh_password_enc, ssh_host_key_sha256, ssh_os, ssh_use_sudo, network_id
		 FROM devices WHERE id = ?`, id,
	).Scan(
		&d.ID, &d.Name, &d.MAC, &d.IP, &d.AgentID, &d.BroadcastAddress, &d.WOLPort,
		&d.DeviceType, &d.StatusMethod, &d.StatusPort, &d.Status, &d.LastCheckedAt,
		&d.LastStatusError, &d.GroupID, &enabledInt, &d.SleepType, &d.SleepEndpoint,
		&d.Remark, &d.CreatedAt, &d.UpdatedAt,
		&d.SSHUser, &d.SSHPort, &d.SSHPasswordEnc, &d.SSHHostKeySHA256, &d.SSHOS, &sudoInt,
		&d.NetworkID,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrDeviceNotFound
		}
		return nil, err
	}
	enabled := enabledInt == 1
	d.Enabled = &enabled
	d.SSHPasswordSet = d.SSHPasswordEnc != ""
	d.SSHUseSudo = sudoInt == 1
	return &d, nil
}

// GetDeviceByMAC 按规范化后的 MAC 查设备；不存在时返回 ErrDeviceNotFound。
func (r *Repository) GetDeviceByMAC(mac string) (*Device, error) {
	normalized, err := wol.ValidateMAC(mac)
	if err != nil {
		return nil, err
	}
	var d Device
	var enabledInt int
	var sudoInt int
	err = r.db.QueryRow(
		`SELECT id, name, mac, ip, agent_id, broadcast_address, wol_port, device_type,
		 status_method, status_port, status, last_checked_at, last_status_error,
		 group_id, enabled, sleep_type, sleep_endpoint, remark, created_at, updated_at,
		  ssh_user, ssh_port, ssh_password_enc, ssh_host_key_sha256, ssh_os, ssh_use_sudo, network_id
		 FROM devices WHERE mac = ?`, normalized,
	).Scan(
		&d.ID, &d.Name, &d.MAC, &d.IP, &d.AgentID, &d.BroadcastAddress, &d.WOLPort,
		&d.DeviceType, &d.StatusMethod, &d.StatusPort, &d.Status, &d.LastCheckedAt,
		&d.LastStatusError, &d.GroupID, &enabledInt, &d.SleepType, &d.SleepEndpoint,
		&d.Remark, &d.CreatedAt, &d.UpdatedAt,
		&d.SSHUser, &d.SSHPort, &d.SSHPasswordEnc, &d.SSHHostKeySHA256, &d.SSHOS, &sudoInt,
		&d.NetworkID,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrDeviceNotFound
		}
		return nil, err
	}
	enabled := enabledInt == 1
	d.Enabled = &enabled
	d.SSHPasswordSet = d.SSHPasswordEnc != ""
	d.SSHUseSudo = sudoInt == 1
	return &d, nil
}

// GetDeviceByMACInNetwork 按网络身份 + 规范化后的 MAC 查设备；不存在时
// 返回 ErrDeviceNotFound。MAC 唯一范围为“网络内唯一”（v0.2.9 问题 2）。
func (r *Repository) GetDeviceByMACInNetwork(mac, networkID string) (*Device, error) {
	normalized, err := wol.ValidateMAC(mac)
	if err != nil {
		return nil, err
	}
	if networkID == "" {
		networkID = DefaultNetworkID
	}
	var d Device
	var enabledInt int
	var sudoInt int
	err = r.db.QueryRow(
		`SELECT id, name, mac, ip, agent_id, broadcast_address, wol_port, device_type,
		 status_method, status_port, status, last_checked_at, last_status_error,
		 group_id, enabled, sleep_type, sleep_endpoint, remark, created_at, updated_at,
		  ssh_user, ssh_port, ssh_password_enc, ssh_host_key_sha256, ssh_os, ssh_use_sudo, network_id
		 FROM devices WHERE mac = ? AND network_id = ?`, normalized, networkID,
	).Scan(
		&d.ID, &d.Name, &d.MAC, &d.IP, &d.AgentID, &d.BroadcastAddress, &d.WOLPort,
		&d.DeviceType, &d.StatusMethod, &d.StatusPort, &d.Status, &d.LastCheckedAt,
		&d.LastStatusError, &d.GroupID, &enabledInt, &d.SleepType, &d.SleepEndpoint,
		&d.Remark, &d.CreatedAt, &d.UpdatedAt,
		&d.SSHUser, &d.SSHPort, &d.SSHPasswordEnc, &d.SSHHostKeySHA256, &d.SSHOS, &sudoInt,
		&d.NetworkID,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrDeviceNotFound
		}
		return nil, err
	}
	enabled := enabledInt == 1
	d.Enabled = &enabled
	d.SSHPasswordSet = d.SSHPasswordEnc != ""
	d.SSHUseSudo = sudoInt == 1
	return &d, nil
}

// ---------------------------------------------------------------------------
// 网络实体（v0.2.9 问题 2）
// ---------------------------------------------------------------------------

// NetworkExists 报告网络是否存在（"default" 为内置网络，恒为真）。
func (r *Repository) NetworkExists(id string) (bool, error) {
	if id == DefaultNetworkID {
		return true, nil
	}
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM networks WHERE id = ?`, id).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// ListNetworks 返回全部网络（默认网络在首位，其余按创建时间）。
func (r *Repository) ListNetworks() ([]Network, error) {
	rows, err := r.db.Query(`SELECT id, name, cidr, description, created_at, updated_at
		FROM networks ORDER BY CASE WHEN id = 'default' THEN 0 ELSE 1 END, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	networks := []Network{}
	for rows.Next() {
		var n Network
		if err := rows.Scan(&n.ID, &n.Name, &n.CIDR, &n.Description, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, err
		}
		networks = append(networks, n)
	}
	return networks, nil
}

// GetNetwork 按 ID 取网络；不存在返回 ErrNetworkNotFound。
func (r *Repository) GetNetwork(id string) (*Network, error) {
	var n Network
	err := r.db.QueryRow(`SELECT id, name, cidr, description, created_at, updated_at
		FROM networks WHERE id = ?`, id).Scan(&n.ID, &n.Name, &n.CIDR, &n.Description, &n.CreatedAt, &n.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNetworkNotFound
		}
		return nil, err
	}
	return &n, nil
}

// GetNetworkByCIDR 按 CIDR 取网络（扫描导入时按扫描网段归属用）；
// 不存在返回 ErrNetworkNotFound。CIDR 为空时不匹配任何网络。
func (r *Repository) GetNetworkByCIDR(cidr string) (*Network, error) {
	if strings.TrimSpace(cidr) == "" {
		return nil, ErrNetworkNotFound
	}
	var n Network
	err := r.db.QueryRow(`SELECT id, name, cidr, description, created_at, updated_at
		FROM networks WHERE cidr = ? ORDER BY created_at ASC LIMIT 1`, strings.TrimSpace(cidr)).
		Scan(&n.ID, &n.Name, &n.CIDR, &n.Description, &n.CreatedAt, &n.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNetworkNotFound
		}
		return nil, err
	}
	return &n, nil
}

// CreateNetwork 创建网络：名称必填；CIDR 为空表示未指定网段，否则须为
// 合法 CIDR；非空 CIDR 已被另一网络占用时返回 ErrNetworkCIDRConflict。
func (r *Repository) CreateNetwork(n *Network) error {
	n.Name = strings.TrimSpace(n.Name)
	if n.Name == "" {
		return ErrNetworkNameRequired
	}
	n.CIDR = strings.TrimSpace(n.CIDR)
	if n.CIDR != "" {
		if _, _, err := net.ParseCIDR(n.CIDR); err != nil {
			return ErrNetworkCIDRInvalid
		}
		if other, err := r.GetNetworkByCIDR(n.CIDR); err == nil && other != nil {
			return ErrNetworkCIDRConflict
		}
	}
	if n.ID == "" {
		n.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	n.CreatedAt = now
	n.UpdatedAt = now
	_, err := r.db.Exec(`INSERT INTO networks (id, name, cidr, description, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, n.ID, n.Name, n.CIDR, n.Description, n.CreatedAt, n.UpdatedAt)
	return err
}

// UpdateNetwork 全量更新网络的可变字段（名称/CIDR/描述）；不存在返回
// ErrNetworkNotFound；CIDR 与他人重复返回 ErrNetworkCIDRConflict。
func (r *Repository) UpdateNetwork(id string, n *Network) error {
	if _, err := r.GetNetwork(id); err != nil {
		return err
	}
	n.Name = strings.TrimSpace(n.Name)
	if n.Name == "" {
		return ErrNetworkNameRequired
	}
	n.CIDR = strings.TrimSpace(n.CIDR)
	if n.CIDR != "" {
		if _, _, err := net.ParseCIDR(n.CIDR); err != nil {
			return ErrNetworkCIDRInvalid
		}
		if other, err := r.GetNetworkByCIDR(n.CIDR); err == nil && other != nil && other.ID != id {
			return ErrNetworkCIDRConflict
		}
	}
	res, err := r.db.Exec(`UPDATE networks SET name=?, cidr=?, description=?, updated_at=? WHERE id=?`,
		n.Name, n.CIDR, n.Description, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	nn, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if nn == 0 {
		return ErrNetworkNotFound
	}
	return nil
}

// DeleteNetwork 删除网络：默认网络不可删；仍被设备引用时返回
// ErrNetworkInUse；仍被 Agent 引用时同样拒绝（先解除关联）。
func (r *Repository) DeleteNetwork(id string) error {
	if id == DefaultNetworkID {
		return ErrNetworkDeleteDefault
	}
	if _, err := r.GetNetwork(id); err != nil {
		return err
	}
	var used int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM devices WHERE network_id = ?`, id).Scan(&used); err != nil {
		return err
	}
	if used > 0 {
		return ErrNetworkInUse
	}
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM agent_networks WHERE network_id = ?`, id).Scan(&used); err != nil {
		return err
	}
	if used > 0 {
		return ErrNetworkInUse
	}
	res, err := r.db.Exec(`DELETE FROM networks WHERE id = ?`, id)
	if err != nil {
		return err
	}
	nn, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if nn == 0 {
		return ErrNetworkNotFound
	}
	return nil
}

// ListAgentNetworks 返回某 Agent 管理的网络（按创建时间）。
func (r *Repository) ListAgentNetworks(agentID string) ([]Network, error) {
	rows, err := r.db.Query(`SELECT n.id, n.name, n.cidr, n.description, n.created_at, n.updated_at
		FROM networks n JOIN agent_networks an ON an.network_id = n.id
		WHERE an.agent_id = ? ORDER BY n.created_at ASC`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	networks := []Network{}
	for rows.Next() {
		var n Network
		if err := rows.Scan(&n.ID, &n.Name, &n.CIDR, &n.Description, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, err
		}
		networks = append(networks, n)
	}
	return networks, nil
}

// SetAgentNetworks 全量替换某 Agent 管理的网络集合（事务内完成）：
// networkIDs 为空表示不管理任何网络；不存在的网络 ID 直接报错，
// 不做部分写入。
func (r *Repository) SetAgentNetworks(agentID string, networkIDs []string) error {
	for _, nid := range networkIDs {
		if ok, err := r.NetworkExists(nid); err != nil {
			return err
		} else if !ok {
			return ErrNetworkNotFound
		}
	}
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM agent_networks WHERE agent_id = ?`, agentID); err != nil {
		return err
	}
	for _, nid := range networkIDs {
		if _, err := tx.Exec(`INSERT INTO agent_networks (agent_id, network_id) VALUES (?, ?)`, agentID, nid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// resolveNetworkID 解析设备归属网络：显式值优先；为空时取 Agent 的首个
// 管理网络（按创建时间）；Agent 无网络时用默认网络。新建设备不填网络时
// 跟随其 Agent，比一律进默认网络更符合直觉。
func (r *Repository) resolveNetworkID(networkID, agentID string) string {
	if networkID != "" {
		return networkID
	}
	if agentID != "" {
		if networks, err := r.ListAgentNetworks(agentID); err == nil && len(networks) > 0 {
			return networks[0].ID
		}
	}
	return DefaultNetworkID
}
func (r *Repository) GroupExists(id string) (bool, error) {
	if id == "default" {
		return true, nil
	}
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM device_groups WHERE id = ?`, id).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// ListDevicesPage 是契约 §4.3 的分页设备列表：支持 group_id / q /
// status / agent_id / network_id 过滤，返回本页设备与过滤后的总数。
// page 从 1 开始（<1 视为 1）；pageSize 超出 1–200 时钳制为 50 / 200。
func (r *Repository) ListDevicesPage(groupID, search, status, agentID, networkID string, page, pageSize int) ([]Device, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	where := ` WHERE 1=1`
	var args []any
	if groupID != "" {
		where += ` AND group_id = ?`
		args = append(args, groupID)
	}
	if search != "" {
		where += ` AND (name LIKE ? OR ip LIKE ? OR mac LIKE ?)`
		pat := "%" + search + "%"
		args = append(args, pat, pat, pat)
	}
	if status != "" {
		where += ` AND status = ?`
		args = append(args, status)
	}
	if agentID != "" {
		where += ` AND agent_id = ?`
		args = append(args, agentID)
	}
	if networkID != "" {
		where += ` AND network_id = ?`
		args = append(args, networkID)
	}
	var total int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM devices`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.Query(
		`SELECT id, name, mac, ip, agent_id, broadcast_address, wol_port, device_type,
		 status_method, status_port, status, last_checked_at, last_status_error,
		 group_id, enabled, sleep_type, sleep_endpoint, remark, created_at, updated_at,
		  ssh_user, ssh_port, ssh_password_enc, ssh_host_key_sha256, ssh_os, ssh_use_sudo, network_id
		 FROM devices`+where+` ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		append(args, pageSize, (page-1)*pageSize)...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	list := []Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, 0, err
		}
		list = append(list, d)
	}
	return list, total, nil
}

// CreateDevice 持久化设备行。调用方（handler）须先过 ValidateDevice；
// 本函数只做最后一道防线：MAC 规范化、默认值填充、MAC 冲突检查（网络内）。
// agent_id 必填无默认值（契约 §4.1）：不再静默回落为 "local"。
func (r *Repository) CreateDevice(d *Device) error {
	mac, err := wol.ValidateMAC(d.MAC)
	if err != nil {
		return err
	}
	d.MAC = mac
	d.NetworkID = r.resolveNetworkID(d.NetworkID, d.AgentID)
	if other, err := r.GetDeviceByMACInNetwork(d.MAC, d.NetworkID); err == nil && other != nil {
		return ErrMACConflict
	}
	if d.ID == "" {
		d.ID = uuid.NewString()
	}
	if d.BroadcastAddress == "" {
		d.BroadcastAddress = "255.255.255.255"
	}
	if d.WOLPort <= 0 {
		d.WOLPort = 9
	}
	if d.DeviceType == "" {
		d.DeviceType = "pc"
	}
	if d.StatusMethod == "" {
		d.StatusMethod = "ping"
	}
	if d.StatusPort <= 0 {
		d.StatusPort = 22
	}
	if d.GroupID == "" {
		d.GroupID = "default"
	}
	if d.SleepType == "" {
		d.SleepType = "none"
	}
	if d.SSHPort <= 0 {
		d.SSHPort = 22
	}
	if d.SSHOS == "" {
		d.SSHOS = "linux"
	}
	// sudo 只对 SSH 适配器的 Linux 目标有意义；其余情况归零，
	// 避免陈旧标记残留（切适配器/切系统后不再生效）。
	if d.SleepType != "ssh" || d.SSHOS != "linux" {
		d.SSHUseSudo = false
	}
	now := time.Now().UTC()
	d.CreatedAt = now
	d.UpdatedAt = now
	d.Status = "unknown"

	// 默认启用：只有明确传 enabled=false 时才禁用。
	enabledInt := 1
	if d.Enabled != nil && !*d.Enabled {
		enabledInt = 0
	}
	sudoInt := 0
	if d.SSHUseSudo {
		sudoInt = 1
	}
	// 25 列对应 25 个占位符与 25 个参数（已核对）。
	_, err = r.db.Exec(
		`INSERT INTO devices (id, name, mac, ip, agent_id, broadcast_address, wol_port, device_type,
		 status_method, status_port, status, group_id, enabled, sleep_type, sleep_endpoint, remark, created_at, updated_at,
		 ssh_user, ssh_port, ssh_password_enc, ssh_host_key_sha256, ssh_os, ssh_use_sudo, network_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.Name, d.MAC, d.IP, d.AgentID, d.BroadcastAddress, d.WOLPort, d.DeviceType,
		d.StatusMethod, d.StatusPort, d.Status, d.GroupID, enabledInt, d.SleepType, d.SleepEndpoint, d.Remark, d.CreatedAt, d.UpdatedAt,
		d.SSHUser, d.SSHPort, d.SSHPasswordEnc, d.SSHHostKeySHA256, d.SSHOS, sudoInt, d.NetworkID,
	)
	if err != nil {
		return err
	}
	enabled := enabledInt == 1
	d.Enabled = &enabled
	return nil
}

// UpdateDevice 全行更新设备（调用方须先做部分更新合并与 ValidateDevice）。
// 不存在的 ID 返回 ErrDeviceNotFound（修复 B-N5）；MAC 与他人重复返回
// ErrMACConflict（按新 network_id 所在网络检查，迁移网络即重新检查）。
func (r *Repository) UpdateDevice(id string, d *Device) error {
	mac, err := wol.ValidateMAC(d.MAC)
	if err != nil {
		return err
	}
	d.MAC = mac
	d.NetworkID = r.resolveNetworkID(d.NetworkID, d.AgentID)
	if other, err := r.GetDeviceByMACInNetwork(d.MAC, d.NetworkID); err == nil && other != nil && other.ID != id {
		return ErrMACConflict
	}
	// sudo 只对 SSH 适配器的 Linux 目标有意义；其余情况归零。
	if d.SleepType != "ssh" || d.SSHOS != "linux" {
		d.SSHUseSudo = false
	}
	enabledInt := 1
	if d.Enabled != nil && !*d.Enabled {
		enabledInt = 0
	}
	sudoInt := 0
	if d.SSHUseSudo {
		sudoInt = 1
	}
	res, err := r.db.Exec(
		`UPDATE devices SET name=?, mac=?, ip=?, agent_id=?, broadcast_address=?, wol_port=?,
		 device_type=?, status_method=?, status_port=?, group_id=?, enabled=?, sleep_type=?,
		 sleep_endpoint=?, remark=?, updated_at=?, ssh_user=?, ssh_port=?, ssh_password_enc=?,
		 ssh_host_key_sha256=?, ssh_os=?, ssh_use_sudo=?, network_id=? WHERE id=?`,
		d.Name, d.MAC, d.IP, d.AgentID, d.BroadcastAddress, d.WOLPort, d.DeviceType,
		d.StatusMethod, d.StatusPort, d.GroupID, enabledInt, d.SleepType, d.SleepEndpoint,
		d.Remark, time.Now().UTC(), d.SSHUser, d.SSHPort, d.SSHPasswordEnc,
		d.SSHHostKeySHA256, d.SSHOS, sudoInt, d.NetworkID, id,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrDeviceNotFound
	}
	return nil
}

// SetSSHHostKeyFingerprint 持久化 TOFU 学到的 SSH 主机密钥指纹
// （首次关机成功后由 handler 调用；指纹不符时 Agent 直接拒绝，
// 不会走到这里；改密码时指纹由 handler 清空以便重新信任）。
func (r *Repository) SetSSHHostKeyFingerprint(id, fingerprint string) error {
	res, err := r.db.Exec(`UPDATE devices SET ssh_host_key_sha256 = ? WHERE id = ?`, fingerprint, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrDeviceNotFound
	}
	return nil
}

func (r *Repository) DeleteDevice(id string) error {
	res, err := r.db.Exec(`DELETE FROM devices WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrDeviceNotFound
	}
	return nil
}

func (r *Repository) UpdateDeviceStatus(id, statusStr, errReason string) error {
	now := time.Now().UTC()
	_, err := r.db.Exec(
		`UPDATE devices SET status=?, last_checked_at=?, last_status_error=?, updated_at=? WHERE id=?`,
		statusStr, now, errReason, now, id,
	)
	return err
}

func (r *Repository) RecordWakeTask(deviceID, agentID, triggerType, statusStr, errMsg string) {
	now := time.Now().UTC()
	_, _ = r.db.Exec(
		`INSERT INTO wake_tasks (id, device_id, agent_id, trigger_type, status, error_message, created_at, finished_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		uuid.NewString(), deviceID, agentID, triggerType, statusStr, errMsg, now, now,
	)
}

func (r *Repository) ListGroups() ([]Group, error) {
	rows, err := r.db.Query(`SELECT id, name, description, created_at FROM device_groups ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []Group{}
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, nil
}

func (r *Repository) CreateGroup(name, desc string) (*Group, error) {
	g := &Group{
		ID:          uuid.NewString(),
		Name:        name,
		Description: desc,
		CreatedAt:   time.Now().UTC(),
	}
	// 名称唯一（契约 §11：冲突 → 409）。先查后插；竞态下唯一约束
	// 兜底，同样映射为 ErrGroupNameConflict，不把原始 DB 错误外泄。
	var existing string
	if err := r.db.QueryRow(`SELECT id FROM device_groups WHERE name=?`, name).Scan(&existing); err == nil {
		return nil, ErrGroupNameConflict
	}
	_, err := r.db.Exec(`INSERT INTO device_groups (id, name, description, created_at) VALUES (?, ?, ?, ?)`,
		g.ID, g.Name, g.Description, g.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			return nil, ErrGroupNameConflict
		}
		return nil, err
	}
	return g, nil
}

func (r *Repository) ListSchedules() ([]ScheduleItem, error) {
	rows, err := r.db.Query(
		`SELECT id, name, cron_expr, timezone, target_type, target_id, action, retry_count, run_once, enabled, last_run_at, created_at
		 FROM schedules ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []ScheduleItem
	for rows.Next() {
		var s ScheduleItem
		var en, runOnce int
		if err := rows.Scan(&s.ID, &s.Name, &s.CronExpr, &s.Timezone, &s.TargetType, &s.TargetID, &s.Action, &s.RetryCount, &runOnce, &en, &s.LastRunAt, &s.CreatedAt); err != nil {
			return nil, err
		}
		s.Enabled = en == 1
		s.RunOnce = runOnce == 1
		list = append(list, s)
	}
	if list == nil {
		list = []ScheduleItem{}
	}
	return list, nil
}

// GetSchedule 按 ID 取任务（含 run_once）；不存在返回 ErrScheduleNotFound。
func (r *Repository) GetSchedule(id string) (*ScheduleItem, error) {
	var s ScheduleItem
	var en, runOnce int
	err := r.db.QueryRow(
		`SELECT id, name, cron_expr, timezone, target_type, target_id, action, retry_count, run_once, enabled, last_run_at, created_at
		 FROM schedules WHERE id = ?`, id,
	).Scan(&s.ID, &s.Name, &s.CronExpr, &s.Timezone, &s.TargetType, &s.TargetID, &s.Action, &s.RetryCount, &runOnce, &en, &s.LastRunAt, &s.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrScheduleNotFound
		}
		return nil, err
	}
	s.Enabled = en == 1
	s.RunOnce = runOnce == 1
	return &s, nil
}

// ListEnabledSchedules 返回全部已启用任务（Dashboard 即将执行列表用，
// 下次执行时间由调用方按 Cron 计算排序）。
func (r *Repository) ListEnabledSchedules() ([]ScheduleItem, error) {
	rows, err := r.db.Query(
		`SELECT id, name, cron_expr, timezone, target_type, target_id, action, retry_count, run_once, enabled, last_run_at, created_at
		 FROM schedules WHERE enabled = 1 ORDER BY created_at ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []ScheduleItem{}
	for rows.Next() {
		var s ScheduleItem
		var en, runOnce int
		if err := rows.Scan(&s.ID, &s.Name, &s.CronExpr, &s.Timezone, &s.TargetType, &s.TargetID, &s.Action, &s.RetryCount, &runOnce, &en, &s.LastRunAt, &s.CreatedAt); err != nil {
			return nil, err
		}
		s.Enabled = en == 1
		s.RunOnce = runOnce == 1
		list = append(list, s)
	}
	return list, nil
}

// CreateSchedule 创建任务：retry_count 按传入值原样存储（0 = 不重试，
// 契约 §8.1 允许 0–5；调用方须先经 ValidateSchedule 校验）。
// 旧版把 <=0 钳制为 1 的行为已修正（M4 的引擎曾为此做二次修正，现不再需要）。
func (r *Repository) CreateSchedule(s *ScheduleItem) error {
	s.ID = uuid.NewString()
	s.CreatedAt = time.Now().UTC()
	if s.Timezone == "" {
		s.Timezone = "UTC"
	}
	if s.Action == "" {
		s.Action = "wake"
	}
	en := 1
	if !s.Enabled {
		en = 0
	}
	runOnce := 0
	if s.RunOnce {
		runOnce = 1
	}
	_, err := r.db.Exec(
		`INSERT INTO schedules (id, name, cron_expr, timezone, target_type, target_id, action, retry_count, run_once, enabled, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.Name, s.CronExpr, s.Timezone, s.TargetType, s.TargetID, s.Action, s.RetryCount, runOnce, en, s.CreatedAt,
	)
	return err
}

// UpdateSchedule 全量更新任务的可变字段（调用方先做合并与校验）；
// 不存在的 ID 返回 ErrScheduleNotFound。
func (r *Repository) UpdateSchedule(s *ScheduleItem) error {
	en := 1
	if !s.Enabled {
		en = 0
	}
	runOnce := 0
	if s.RunOnce {
		runOnce = 1
	}
	res, err := r.db.Exec(
		`UPDATE schedules SET name=?, cron_expr=?, timezone=?, target_type=?,
		 target_id=?, action=?, retry_count=?, enabled=?, run_once=? WHERE id=?`,
		s.Name, s.CronExpr, s.Timezone, s.TargetType, s.TargetID, s.Action,
		s.RetryCount, en, runOnce, s.ID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrScheduleNotFound
	}
	return nil
}

// SetScheduleEnabled 开关任务（pause/resume 用）；不存在返回 ErrScheduleNotFound。
func (r *Repository) SetScheduleEnabled(id string, enabled bool) error {
	en := 0
	if enabled {
		en = 1
	}
	res, err := r.db.Exec(`UPDATE schedules SET enabled=? WHERE id=?`, en, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrScheduleNotFound
	}
	return nil
}

func (r *Repository) DeleteSchedule(id string) error {
	res, err := r.db.Exec(`DELETE FROM schedules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrScheduleNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// 任务运行记录（M4，迁移 004；M5 从 scheduler 迁回仓库层）
// ---------------------------------------------------------------------------

// RecordTaskRun 写一条执行记录（task_runs 一行），返回 runID。
func (r *Repository) RecordTaskRun(scheduleID, status, output string, startedAt, finishedAt time.Time) (string, error) {
	runID := uuid.NewString()
	durationMs := finishedAt.Sub(startedAt).Milliseconds()
	_, err := r.db.Exec(
		`INSERT INTO task_runs (id, schedule_id, status, output, executed_at,
		 started_at, finished_at, duration_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		runID, scheduleID, status, output, finishedAt, startedAt, finishedAt, durationMs)
	if err != nil {
		return "", err
	}
	return runID, nil
}

// RecordTaskRunDevice 写一条 run 内的单设备执行结果。
func (r *Repository) RecordTaskRunDevice(runID string, d TaskRunDevice) error {
	_, err := r.db.Exec(
		`INSERT INTO task_run_devices (run_id, device_id, device_name, status,
		 reason, sent_at, boot_status) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		runID, d.DeviceID, d.Name, d.Status, d.Reason, d.SentAt, d.BootStatus)
	return err
}

// UpdateScheduleLastRun 更新任务的最近执行时间。
func (r *Repository) UpdateScheduleLastRun(id string, t time.Time) error {
	_, err := r.db.Exec(`UPDATE schedules SET last_run_at=? WHERE id=?`, t, id)
	return err
}

// LastTaskRunStatus 返回某任务最新一次执行的 status；无执行记录时 ok=false。
func (r *Repository) LastTaskRunStatus(scheduleID string) (status string, ok bool, err error) {
	err = r.db.QueryRow(
		`SELECT status FROM task_runs WHERE schedule_id=?
		 ORDER BY COALESCE(started_at, executed_at) DESC LIMIT 1`,
		scheduleID).Scan(&status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return status, true, nil
}

// ListTaskRuns 返回某任务的执行历史（按开始时间 DESC），含每设备结果。
// 004 未应用的老库上 started_at/finished_at 为空时回退到 executed_at。
func (r *Repository) ListTaskRuns(scheduleID string, page, pageSize int) ([]TaskRun, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	var total int
	if err := r.db.QueryRow(
		`SELECT COUNT(*) FROM task_runs WHERE schedule_id=?`, scheduleID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.Query(
		`SELECT id, schedule_id, status, output, started_at, finished_at,
		        COALESCE(duration_ms, 0) AS duration_ms, executed_at
		 FROM task_runs WHERE schedule_id=?
		 ORDER BY COALESCE(started_at, executed_at) DESC LIMIT ? OFFSET ?`,
		scheduleID, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, err
	}
	runs := []TaskRun{}
	for rows.Next() {
		var run TaskRun
		var startedAt, finishedAt sql.NullTime
		var executedAt time.Time
		if err := rows.Scan(&run.ID, &run.ScheduleID, &run.Status, &run.Output,
			&startedAt, &finishedAt, &run.DurationMs, &executedAt); err != nil {
			rows.Close()
			return nil, 0, err
		}
		// 004 未应用的老行 started_at/finished_at 为空，回退到 executed_at。
		// 注意：不能用 COALESCE(...) AS started_at 再扫进 time.Time——
		// 表达式会丢失 DATETIME 类型亲和性，驱动返回 string 导致 Scan 失败。
		if startedAt.Valid {
			run.StartedAt = startedAt.Time
		} else {
			run.StartedAt = executedAt
		}
		if finishedAt.Valid {
			run.FinishedAt = finishedAt.Time
		} else {
			run.FinishedAt = executedAt
		}
		run.Devices = []TaskRunDevice{}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, 0, err
	}
	// 先关外层 rows 再查每设备结果：单连接池（MaxOpenConns(1)，与生产
	// InitDB 一致）下，rows 循环内再开查询会永久等不到连接而死锁。
	rows.Close()
	for i := range runs {
		devRows, err := r.db.Query(
			`SELECT device_id, device_name, status, reason, sent_at, boot_status
			 FROM task_run_devices WHERE run_id=? ORDER BY id ASC`, runs[i].ID)
		if err != nil {
			return nil, 0, err
		}
		for devRows.Next() {
			var d TaskRunDevice
			var sentAt sql.NullTime
			var bootStatus sql.NullString
			if err := devRows.Scan(&d.DeviceID, &d.Name, &d.Status, &d.Reason, &sentAt, &bootStatus); err != nil {
				devRows.Close()
				return nil, 0, err
			}
			if sentAt.Valid {
				d.SentAt = &sentAt.Time
			}
			if bootStatus.Valid {
				d.BootStatus = &bootStatus.String
			}
			runs[i].Devices = append(runs[i].Devices, d)
		}
		if err := devRows.Err(); err != nil {
			devRows.Close()
			return nil, 0, err
		}
		devRows.Close()
	}
	return runs, total, nil
}

func (r *Repository) GetDashboardStats() (DashboardStats, error) {
	var stats DashboardStats
	// 设备计数改走 SQL 聚合，不再全表加载后内存计数（修复 B-N13）。
	if err := r.db.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN status = 'online' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = 'offline' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status NOT IN ('online', 'offline') THEN 1 ELSE 0 END), 0)
		FROM devices`).Scan(&stats.TotalDevices, &stats.OnlineDevices, &stats.OfflineDevices, &stats.UnknownDevices); err != nil {
		return stats, err
	}
	// Agent 计数（含待审批数）同样走 SQL 聚合。
	if err := r.db.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN status = 'online' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN approval_status = 'pending' THEN 1 ELSE 0 END), 0)
		FROM agents`).Scan(&stats.TotalAgents, &stats.OnlineAgents, &stats.PendingAgents); err != nil {
		return stats, err
	}
	// 即将执行的任务由调用方（Dashboard handler）按 Cron 真实计算；
	// 此处只给空列表，不再把全部任务原样返回（v0.1.0 的死数据问题）。
	stats.UpcomingTasks = []UpcomingTask{}
	return stats, nil
}
