package device

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// setupTestRepo 建一个应用了 001–008 迁移的测试库并返回 Repository。
// M5 起迁移由 internal/db.Migrate 在服务端启动时应用；测试仍手动按序
// 应用全部迁移文件（与生产最终状态一致）。
func setupTestRepo(t *testing.T) *Repository {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, f := range []string{"001_init.sql", "002_auth.sql", "003_agent.sql", "004_runs.sql", "005_indexes.sql", "006_ssh_sleep.sql", "007_ssh_windows.sql", "008_network_identity.sql", "009_rename_seeded_networks.sql", "010_ssh_use_sudo.sql", "011_network_cidr_unique.sql", "012_task_runs_keep_history.sql"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "migrations", f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := db.Exec(string(b)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	return NewRepository(db)
}

// fakeResolver 是测试用 AgentResolver：用 map 模拟 Agent 行。
type fakeResolver struct {
	agents map[string]struct {
		approval string
		online   bool
	}
}

func (f fakeResolver) LookupAgent(id string) (string, bool, error) {
	a, ok := f.agents[id]
	if !ok {
		return "", false, ErrAgentNotFound
	}
	return a.approval, a.online, nil
}

func approvedOnlineResolver() fakeResolver {
	return fakeResolver{agents: map[string]struct {
		approval string
		online   bool
	}{"agent-home-lan": {approval: "approved", online: true}}}
}

func boolPtr(b bool) *bool { return &b }

func validDevice() *Device {
	return &Device{
		Name: "LAN-29", MAC: "AA:BB:CC:DD:EE:01", IP: "192.168.1.29",
		AgentID: "agent-home-lan", DeviceType: "pc", StatusMethod: "ping",
	}
}

func TestValidateDeviceValid(t *testing.T) {
	repo := setupTestRepo(t)
	res := ValidateDevice(repo, approvedOnlineResolver(), validDevice(), "")
	if res.HasErrors() {
		t.Fatalf("合法设备应通过校验，got %+v", res.Errors)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("在线 Agent 不应有警告，got %+v", res.Warnings)
	}
}

func TestValidateDeviceAgentOfflineWarning(t *testing.T) {
	repo := setupTestRepo(t)
	resolver := fakeResolver{agents: map[string]struct {
		approval string
		online   bool
	}{"agent-home-lan": {approval: "approved", online: false}}}
	res := ValidateDevice(repo, resolver, validDevice(), "")
	if res.HasErrors() {
		t.Fatalf("Agent 离线不应是硬性错误，got %+v", res.Errors)
	}
	if len(res.Warnings) != 1 || res.Warnings[0].Code != "agent_offline" {
		t.Fatalf("应有且仅有 agent_offline 警告，got %+v", res.Warnings)
	}
}

func TestValidateDeviceAgentStates(t *testing.T) {
	repo := setupTestRepo(t)
	cases := []struct {
		name     string
		agentID  string
		resolver fakeResolver
		wantCode string
	}{
		{"空 agent_id", "", approvedOnlineResolver(), "required"},
		{"local 已退役", "local", approvedOnlineResolver(), "not_found"},
		{"Agent 不存在", "agent-missing", approvedOnlineResolver(), "not_found"},
		{"pending 未获批", "agent-home-lan", fakeResolver{agents: map[string]struct {
			approval string
			online   bool
		}{"agent-home-lan": {approval: "pending", online: false}}}, "not_authorized"},
		{"disabled", "agent-home-lan", fakeResolver{agents: map[string]struct {
			approval string
			online   bool
		}{"agent-home-lan": {approval: "disabled", online: false}}}, "agent_disabled"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := validDevice()
			d.AgentID = c.agentID
			res := ValidateDevice(repo, c.resolver, d, "")
			if !res.HasErrors() {
				t.Fatalf("应校验失败，got 无错误")
			}
			found := false
			for _, e := range res.Errors {
				if e.Field == "agent_id" && e.Code == c.wantCode {
					found = true
				}
			}
			if !found {
				t.Fatalf("应有 agent_id/%s 错误，got %+v", c.wantCode, res.Errors)
			}
		})
	}
}

func TestValidateDeviceFormats(t *testing.T) {
	repo := setupTestRepo(t)
	resolver := approvedOnlineResolver()
	cases := []struct {
		name   string
		mutate func(*Device)
		field  string
		code   string
	}{
		{"名称为空", func(d *Device) { d.Name = "  " }, "name", "required"},
		{"名称超长", func(d *Device) { d.Name = string(make([]byte, 65)) }, "name", "out_of_range"},
		{"MAC 为空", func(d *Device) { d.MAC = "" }, "mac", "required"},
		{"MAC 格式错", func(d *Device) { d.MAC = "00:11:22:33:44" }, "mac", "invalid_format"},
		{"MAC 小写可规范化", func(d *Device) { d.MAC = "aa:bb:cc:dd:ee:ff" }, "", ""},
		{"IP 非法", func(d *Device) { d.IP = "999.1.1.1" }, "ip", "invalid_format"},
		{"IP 为 IPv6", func(d *Device) { d.IP = "::1" }, "ip", "invalid_format"},
		{"广播地址非法", func(d *Device) { d.BroadcastAddress = "not-ip" }, "broadcast_address", "invalid_format"},
		{"WOL 端口越界", func(d *Device) { d.WOLPort = 70000 }, "wol_port", "out_of_range"},
		{"WOL 端口负数", func(d *Device) { d.WOLPort = -1 }, "wol_port", "out_of_range"},
		{"检测端口越界", func(d *Device) { d.StatusPort = 70000 }, "status_port", "out_of_range"},
		{"设备类型非法", func(d *Device) { d.DeviceType = "phone" }, "device_type", "invalid_value"},
		{"检测方式非法", func(d *Device) { d.StatusMethod = "icmp" }, "status_method", "invalid_value"},
		{"睡眠类型非法", func(d *Device) { d.SleepType = "wol" }, "sleep_type", "invalid_value"},
		{"ssh 目标系统非法", func(d *Device) {
			d.SleepType = "ssh"
			d.SSHUser = "root"
			d.SSHPassword = "x"
			d.SSHOS = "macos"
		}, "ssh_os", "invalid_value"},
		{"http_sol 缺端点", func(d *Device) { d.SleepType = "http_sol" }, "sleep_endpoint", "required"},
		{"睡眠端点公网", func(d *Device) {
			d.SleepType = "http_sol"
			d.SleepEndpoint = "http://8.8.8.8:8080"
		}, "sleep_endpoint", "invalid_value"},
		{"睡眠端点元数据", func(d *Device) {
			d.SleepType = "http_sol"
			d.SleepEndpoint = "http://169.254.169.254/latest"
		}, "sleep_endpoint", "invalid_value"},
		{"ssh 缺用户名", func(d *Device) {
			d.SleepType = "ssh"
			d.SSHPassword = "pw"
		}, "ssh_user", "required"},
		{"ssh 缺密码", func(d *Device) {
			d.SleepType = "ssh"
			d.SSHUser = "root"
		}, "ssh_password", "required"},
		{"ssh 完整合法", func(d *Device) {
			d.SleepType = "ssh"
			d.SSHUser = "root"
			d.SSHPassword = "pw"
		}, "", ""},
		{"ssh 已存密码免填", func(d *Device) {
			d.SleepType = "ssh"
			d.SSHUser = "root"
			d.SSHPasswordEnc = "YWJj" // 更新时已存密文在行里，明文留空即保持原密码
		}, "", ""},
		{"ssh 端口越界", func(d *Device) {
			d.SleepType = "ssh"
			d.SSHUser = "root"
			d.SSHPassword = "pw"
			d.SSHPort = 70000
		}, "ssh_port", "out_of_range"},
		{"备注超长", func(d *Device) { d.Remark = string(make([]byte, 501)) }, "remark", "out_of_range"},
		{"分组不存在", func(d *Device) { d.GroupID = "group-missing" }, "group_id", "not_found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := validDevice()
			c.mutate(d)
			res := ValidateDevice(repo, resolver, d, "")
			if c.field == "" {
				if res.HasErrors() {
					t.Fatalf("应通过校验，got %+v", res.Errors)
				}
				return
			}
			if !res.HasErrors() {
				t.Fatalf("应校验失败，got 无错误")
			}
			found := false
			for _, e := range res.Errors {
				if e.Field == c.field && e.Code == c.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("应有 %s/%s 错误，got %+v", c.field, c.code, res.Errors)
			}
		})
	}
}

func TestValidateDeviceMACUnique(t *testing.T) {
	repo := setupTestRepo(t)
	resolver := approvedOnlineResolver()
	first := validDevice()
	if err := repo.CreateDevice(first); err != nil {
		t.Fatalf("创建首台设备失败：%v", err)
	}
	// 同 MAC 新建 → conflict。
	second := validDevice()
	second.Name = "LAN-30"
	second.IP = "192.168.1.30"
	res := ValidateDevice(repo, resolver, second, "")
	if !res.HasErrors() {
		t.Fatalf("重复 MAC 应校验失败")
	}
	found := false
	for _, e := range res.Errors {
		if e.Field == "mac" && e.Code == "conflict" {
			found = true
		}
	}
	if !found {
		t.Fatalf("应有 mac/conflict 错误，got %+v", res.Errors)
	}
	// 更新自身（排除 selfID）→ 不冲突。
	res = ValidateDevice(repo, resolver, first, first.ID)
	if res.HasErrors() {
		t.Fatalf("更新自身不应报 MAC 冲突，got %+v", res.Errors)
	}
}

func TestValidateOperation(t *testing.T) {
	repo := setupTestRepo(t)
	online := fakeResolver{agents: map[string]struct {
		approval string
		online   bool
	}{"agent-home-lan": {approval: "approved", online: true}}}
	offline := fakeResolver{agents: map[string]struct {
		approval string
		online   bool
	}{"agent-home-lan": {approval: "approved", online: false}}}
	pending := fakeResolver{agents: map[string]struct {
		approval string
		online   bool
	}{"agent-home-lan": {approval: "pending", online: false}}}
	disabledAgent := fakeResolver{agents: map[string]struct {
		approval string
		online   bool
	}{"agent-home-lan": {approval: "disabled", online: false}}}

	d := validDevice()
	if err := repo.CreateDevice(d); err != nil {
		t.Fatalf("创建设备失败：%v", err)
	}

	// 不存在的设备 → 404 / skipped。
	if _, e := ValidateOperation(repo, online, "missing-id"); e == nil || e.HTTPStatus != 404 || e.BatchStatus != "skipped" || e.Code != "device_not_found" {
		t.Fatalf("不存在的设备应 404/skipped/device_not_found，got %+v", e)
	}
	// 全部通过。
	if _, e := ValidateOperation(repo, online, d.ID); e != nil {
		t.Fatalf("在线 Agent 应通过校验链，got %+v", e)
	}
	// Agent 离线 → 502 / failed。
	if _, e := ValidateOperation(repo, offline, d.ID); e == nil || e.HTTPStatus != 502 || e.BatchStatus != "failed" || e.Code != "agent_offline" {
		t.Fatalf("离线 Agent 应 502/failed/agent_offline，got %+v", e)
	}
	// pending → 502 / failed / agent_not_approved。
	if _, e := ValidateOperation(repo, pending, d.ID); e == nil || e.Code != "agent_not_approved" || e.BatchStatus != "failed" {
		t.Fatalf("pending Agent 应 agent_not_approved/failed，got %+v", e)
	}
	// disabled Agent → 502 / failed / agent_disabled。
	if _, e := ValidateOperation(repo, disabledAgent, d.ID); e == nil || e.Code != "agent_disabled" || e.BatchStatus != "failed" {
		t.Fatalf("禁用 Agent 应 agent_disabled/failed，got %+v", e)
	}
	// Agent 行不存在 → 502 / failed / agent_not_found。
	if _, e := ValidateOperation(repo, fakeResolver{agents: map[string]struct {
		approval string
		online   bool
	}{}}, d.ID); e == nil || e.Code != "agent_not_found" || e.BatchStatus != "failed" {
		t.Fatalf("不存在的 Agent 应 agent_not_found/failed，got %+v", e)
	}
	// 设备禁用 → 400 / skipped / device_disabled。
	merged := *d
	merged.Enabled = boolPtr(false)
	if err := repo.UpdateDevice(d.ID, &merged); err != nil {
		t.Fatalf("禁用设备失败：%v", err)
	}
	if _, e := ValidateOperation(repo, online, d.ID); e == nil || e.HTTPStatus != 400 || e.BatchStatus != "skipped" || e.Code != "device_disabled" {
		t.Fatalf("禁用设备应 400/skipped/device_disabled，got %+v", e)
	}
	// 遗留 local 归属 → agent_not_found。
	legacy := validDevice()
	legacy.Name = "LEGACY"
	legacy.MAC = "AA:BB:CC:DD:EE:02"
	legacy.AgentID = "local"
	if err := repo.CreateDevice(legacy); err != nil {
		t.Fatalf("创建遗留设备失败：%v", err)
	}
	if _, e := ValidateOperation(repo, online, legacy.ID); e == nil || e.Code != "agent_not_found" {
		t.Fatalf("local 归属设备应 agent_not_found，got %+v", e)
	}
}

// TestValidateDeviceAgentNetworkOwnership 覆盖 v0.3.3 问题 3：
// 设备只能归属其 Agent 管理的网络（default 网络任何 Agent 可用；
// 无任何网络的 Agent 只能用 default）。创建与更新共用同一校验。
func TestValidateDeviceAgentNetworkOwnership(t *testing.T) {
	repo := setupTestRepo(t)
	for _, id := range []string{"agent-a", "agent-b"} {
		if _, err := repo.DB().Exec(`INSERT INTO agents (id, name, subnet, approval_status, status, authorized) VALUES (?, ?, '', 'approved', 'offline', 1)`, id, id); err != nil {
			t.Fatalf("插入测试 Agent %s 失败：%v", id, err)
		}
	}
	netB := &Network{Name: "B 网", CIDR: "192.168.2.0/24"}
	if err := repo.CreateNetwork(netB); err != nil {
		t.Fatalf("创建网络失败：%v", err)
	}
	if err := repo.SetAgentNetworks("agent-b", []string{netB.ID}); err != nil {
		t.Fatalf("分配 Agent 网络失败：%v", err)
	}
	resolver := fakeResolver{agents: map[string]struct {
		approval string
		online   bool
	}{
		"agent-a": {approval: "approved", online: true},
		"agent-b": {approval: "approved", online: true},
	}}
	hasFieldError := func(res ValidationResult, field, code string) bool {
		for _, e := range res.Errors {
			if e.Field == field && e.Code == code {
				return true
			}
		}
		return false
	}

	// Agent A 的设备放进只属于 B 的网络 → 校验拒绝，仓库层同样拒绝。
	d := validDevice()
	d.AgentID = "agent-a"
	d.NetworkID = netB.ID
	res := ValidateDevice(repo, resolver, d, "")
	if !res.HasErrors() || !hasFieldError(res, "network_id", "not_managed") {
		t.Fatalf("Agent A 的设备进 B 的网络应报 network_id/not_managed，got %+v", res.Errors)
	}
	if err := repo.CreateDevice(d); !errors.Is(err, ErrAgentNetworkMismatch) {
		t.Fatalf("CreateDevice 应返回 ErrAgentNetworkMismatch，got %v", err)
	}

	// Agent B 自己的设备进同一网络 → 通过。
	dB := validDevice()
	dB.AgentID = "agent-b"
	dB.NetworkID = netB.ID
	dB.IP = "192.168.2.29"
	if res := ValidateDevice(repo, resolver, dB, ""); res.HasErrors() {
		t.Fatalf("Agent B 自己的设备应通过校验，got %+v", res.Errors)
	}
	if err := repo.CreateDevice(dB); err != nil {
		t.Fatalf("Agent B 自己的设备创建应成功，got %v", err)
	}

	// default 网络任何 Agent 可用。
	dDef := validDevice()
	dDef.AgentID = "agent-a"
	dDef.NetworkID = "default"
	dDef.IP = "192.168.1.30"
	dDef.MAC = "AA:BB:CC:DD:EE:02"
	if res := ValidateDevice(repo, resolver, dDef, ""); res.HasErrors() {
		t.Fatalf("default 网络设备应通过校验，got %+v", res.Errors)
	}
	if err := repo.CreateDevice(dDef); err != nil {
		t.Fatalf("default 网络设备创建应成功，got %v", err)
	}

	// 更新路径同样拒绝：把 default 网络的设备迁进 B 的网络。
	merged := *dDef
	merged.NetworkID = netB.ID
	if res := ValidateDevice(repo, resolver, &merged, dDef.ID); !res.HasErrors() || !hasFieldError(res, "network_id", "not_managed") {
		t.Fatalf("更新迁入非管理网络应报 network_id/not_managed，got %+v", res.Errors)
	}
	if err := repo.UpdateDevice(dDef.ID, &merged); !errors.Is(err, ErrAgentNetworkMismatch) {
		t.Fatalf("UpdateDevice 应返回 ErrAgentNetworkMismatch，got %v", err)
	}
	// 迁入 default 仍允许。
	merged.NetworkID = "default"
	if err := repo.UpdateDevice(dDef.ID, &merged); err != nil {
		t.Fatalf("更新迁回 default 应成功，got %v", err)
	}
}
