package device

import (
	"database/sql"
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
	for _, f := range []string{"001_init.sql", "002_auth.sql", "003_agent.sql", "004_runs.sql", "005_indexes.sql", "006_ssh_sleep.sql", "007_ssh_windows.sql", "008_network_identity.sql"} {
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
