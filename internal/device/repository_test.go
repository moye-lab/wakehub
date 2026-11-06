package device

import (
	"errors"
	"testing"
	"time"
)

func TestRepositoryCreateAndGet(t *testing.T) {
	repo := setupTestRepo(t)
	d := validDevice()
	if err := repo.CreateDevice(d); err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	if d.ID == "" {
		t.Fatalf("创建后应回填 ID")
	}
	got, err := repo.GetDevice(d.ID)
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if got.MAC != "AA:BB:CC:DD:EE:01" || got.Name != "LAN-29" || got.AgentID != "agent-home-lan" {
		t.Fatalf("读取的设备与写入不一致：%+v", got)
	}
	// 小写 MAC 入库应规范化为大写。
	lower := validDevice()
	lower.Name = "LAN-30"
	lower.IP = "192.168.1.30"
	lower.MAC = "aa:bb:cc:dd:ee:ff"
	if err := repo.CreateDevice(lower); err != nil {
		t.Fatalf("CreateDevice 小写 MAC: %v", err)
	}
	got2, err := repo.GetDeviceByMAC("AA:BB:CC:DD:EE:FF")
	if err != nil {
		t.Fatalf("GetDeviceByMAC: %v", err)
	}
	if got2.ID != lower.ID {
		t.Fatalf("GetDeviceByMAC 应返回同一设备")
	}
}

func TestRepositoryCreateDuplicateMAC(t *testing.T) {
	repo := setupTestRepo(t)
	if err := repo.CreateDevice(validDevice()); err != nil {
		t.Fatalf("首次创建失败：%v", err)
	}
	dup := validDevice()
	dup.Name = "LAN-30"
	dup.IP = "192.168.1.30"
	if err := repo.CreateDevice(dup); !errors.Is(err, ErrMACConflict) {
		t.Fatalf("重复 MAC 应返回 ErrMACConflict，got %v", err)
	}
}

func TestRepositoryUpdateDeleteNotFound(t *testing.T) {
	repo := setupTestRepo(t)
	// 更新不存在的设备 → 404 语义（ErrDeviceNotFound）。
	if err := repo.UpdateDevice("no-such-id", validDevice()); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("更新不存在设备应 ErrDeviceNotFound，got %v", err)
	}
	// 删除不存在的设备 → 404 语义。
	if err := repo.DeleteDevice("no-such-id"); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("删除不存在设备应 ErrDeviceNotFound，got %v", err)
	}
	if err := repo.DeleteSchedule("no-such-id"); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("删除不存在定时应 ErrScheduleNotFound，got %v", err)
	}
	// 正常更新 → 返回更新后的行。
	d := validDevice()
	if err := repo.CreateDevice(d); err != nil {
		t.Fatalf("创建失败：%v", err)
	}
	merged := *d
	merged.Name = "RENAMED"
	if err := repo.UpdateDevice(d.ID, &merged); err != nil {
		t.Fatalf("更新失败：%v", err)
	}
	got, err := repo.GetDevice(d.ID)
	if err != nil {
		t.Fatalf("重查失败：%v", err)
	}
	if got.Name != "RENAMED" {
		t.Fatalf("更新后名称应为 RENAMED，got %q", got.Name)
	}
	// 更新为他人已占用的 MAC → 冲突。
	other := validDevice()
	other.Name = "LAN-30"
	other.IP = "192.168.1.30"
	other.MAC = "AA:BB:CC:DD:EE:FF"
	if err := repo.CreateDevice(other); err != nil {
		t.Fatalf("创建第二台失败：%v", err)
	}
	merged.MAC = "AA:BB:CC:DD:EE:FF"
	if err := repo.UpdateDevice(d.ID, &merged); !errors.Is(err, ErrMACConflict) {
		t.Fatalf("更新为冲突 MAC 应 ErrMACConflict，got %v", err)
	}
}

func TestRepositoryListDevicesPage(t *testing.T) {
	repo := setupTestRepo(t)
	for i, ip := range []string{"192.168.1.11", "192.168.1.12", "192.168.1.13"} {
		d := validDevice()
		d.Name = "DEV-" + ip
		d.IP = ip
		d.MAC = "00:11:22:33:44:0" + string(rune('1'+i))
		if err := repo.CreateDevice(d); err != nil {
			t.Fatalf("创建设备 %d 失败：%v", i, err)
		}
	}
	// 第一页 2 条，总数 3。
	items, total, err := repo.ListDevicesPage("", "", "", "", "", 1, 2)
	if err != nil {
		t.Fatalf("ListDevicesPage: %v", err)
	}
	if total != 3 || len(items) != 2 {
		t.Fatalf("应 total=3 且本页 2 条，got total=%d len=%d", total, len(items))
	}
	// 第二页 1 条。
	items, total, err = repo.ListDevicesPage("", "", "", "", "", 2, 2)
	if err != nil {
		t.Fatalf("ListDevicesPage p2: %v", err)
	}
	if total != 3 || len(items) != 1 {
		t.Fatalf("第二页应 total=3 且本页 1 条，got total=%d len=%d", total, len(items))
	}
	// page_size 超过 200 钳制为 200（不报错）。
	if _, _, err := repo.ListDevicesPage("", "", "", "", "", 1, 9999); err != nil {
		t.Fatalf("超大 page_size 不应报错：%v", err)
	}
	// 按 Agent 过滤：不存在的 Agent → 0 条。
	items, total, err = repo.ListDevicesPage("", "", "", "agent-missing", "", 1, 50)
	if err != nil {
		t.Fatalf("按 Agent 过滤失败：%v", err)
	}
	if total != 0 || len(items) != 0 {
		t.Fatalf("不存在的 Agent 应返回 0 条，got total=%d len=%d", total, len(items))
	}
	// 按名称搜索。
	items, total, err = repo.ListDevicesPage("", "DEV-192.168.1.12", "", "", "", 1, 50)
	if err != nil {
		t.Fatalf("搜索失败：%v", err)
	}
	if total != 1 || items[0].IP != "192.168.1.12" {
		t.Fatalf("搜索应命中 1 条 192.168.1.12，got total=%d %+v", total, items)
	}
}

func TestRepositoryGroupExists(t *testing.T) {
	repo := setupTestRepo(t)
	if ok, err := repo.GroupExists("default"); err != nil || !ok {
		t.Fatalf("default 分组应恒为存在，got ok=%v err=%v", ok, err)
	}
	if ok, err := repo.GroupExists("group-missing"); err != nil || ok {
		t.Fatalf("不存在的分组应返回 false，got ok=%v err=%v", ok, err)
	}
}

func TestRepositoryDashboardStatsSQLAggregation(t *testing.T) {
	repo := setupTestRepo(t)
	// 设备：online 2、offline 1、unknown 1（含 legacy 空状态行）。
	mkDevice := func(name, mac, status string) {
		d := validDevice()
		d.Name = name
		d.MAC = mac
		if err := repo.CreateDevice(d); err != nil {
			t.Fatalf("创建设备 %s: %v", name, err)
		}
		if err := repo.UpdateDeviceStatus(d.ID, status, ""); err != nil {
			t.Fatalf("置状态 %s: %v", name, err)
		}
	}
	mkDevice("在线-1", "00:11:22:33:44:01", "online")
	mkDevice("在线-2", "00:11:22:33:44:02", "online")
	mkDevice("离线-1", "00:11:22:33:44:03", "offline")
	mkDevice("未知-1", "00:11:22:33:44:04", "unknown")
	// Agent：001 种子 local 已被 003 删除；直接插两行——approved 在线 1、
	// pending 1（另插一行 approved 离线，验证 online 计数）。
	db := repo.DB()
	if _, err := db.Exec(`INSERT INTO agents (id, name, subnet, status, authorized, approval_status) VALUES
		('agent-a', 'A', '192.168.1.0/24', 'online', 1, 'approved'),
		('agent-b', 'B', '192.168.1.0/24', 'offline', 1, 'approved'),
		('agent-c', 'C', '192.168.1.0/24', 'offline', 0, 'pending')`); err != nil {
		t.Fatalf("seed agents: %v", err)
	}
	stats, err := repo.GetDashboardStats()
	if err != nil {
		t.Fatalf("GetDashboardStats: %v", err)
	}
	if stats.TotalDevices != 4 || stats.OnlineDevices != 2 || stats.OfflineDevices != 1 || stats.UnknownDevices != 1 {
		t.Fatalf("设备计数错误：%+v", stats)
	}
	if stats.TotalAgents != 3 || stats.OnlineAgents != 1 || stats.PendingAgents != 1 {
		t.Fatalf("Agent 计数错误：%+v", stats)
	}
	if stats.UpcomingTasks == nil {
		t.Fatal("UpcomingTasks 应为 [] 而非 null")
	}
}

func TestRepositoryScheduleCRUDWithRunOnce(t *testing.T) {
	repo := setupTestRepo(t)
	s := &ScheduleItem{
		Name: "晨起唤醒", CronExpr: "0 8 * * *", Timezone: "UTC",
		TargetType: "device", TargetID: "dev-1", Action: "wake",
		RetryCount: 0, RunOnce: true, Enabled: true,
	}
	if err := repo.CreateSchedule(s); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if s.ID == "" {
		t.Fatal("创建后应回填 ID")
	}
	// retry_count=0（不重试）与 run_once 须原样存储（M5 修正旧钳制）。
	got, err := repo.GetSchedule(s.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if got.RetryCount != 0 || !got.RunOnce || !got.Enabled {
		t.Fatalf("存储值不一致：%+v", got)
	}
	// 更新：改重试次数与 run_once。
	got.RetryCount = 3
	got.RunOnce = false
	if err := repo.UpdateSchedule(got); err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}
	got2, err := repo.GetSchedule(s.ID)
	if err != nil {
		t.Fatalf("GetSchedule after update: %v", err)
	}
	if got2.RetryCount != 3 || got2.RunOnce {
		t.Fatalf("更新后值不一致：%+v", got2)
	}
	// 开关与不存在处理。
	if err := repo.SetScheduleEnabled(s.ID, false); err != nil {
		t.Fatalf("SetScheduleEnabled: %v", err)
	}
	got3, _ := repo.GetSchedule(s.ID)
	if got3.Enabled {
		t.Fatal("禁用后 Enabled 应为 false")
	}
	if _, err := repo.GetSchedule("no-such"); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("不存在的任务应 ErrScheduleNotFound，got %v", err)
	}
	if err := repo.UpdateSchedule(&ScheduleItem{ID: "no-such", Name: "x"}); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("更新不存在的任务应 ErrScheduleNotFound，got %v", err)
	}
	if err := repo.SetScheduleEnabled("no-such", true); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("开关不存在的任务应 ErrScheduleNotFound，got %v", err)
	}
	// ListSchedules 含 run_once。
	list, err := repo.ListSchedules()
	if err != nil || len(list) != 1 {
		t.Fatalf("ListSchedules 应返回 1 条，got %d（%v）", len(list), err)
	}
	if list[0].RetryCount != 3 || list[0].RunOnce {
		t.Fatalf("ListSchedules 行值不一致：%+v", list[0])
	}
}

func TestRepositoryTaskRunRoundTrip(t *testing.T) {
	repo := setupTestRepo(t)
	s := &ScheduleItem{
		Name: "晨起唤醒", CronExpr: "0 8 * * *", Timezone: "UTC",
		TargetType: "device", TargetID: "dev-1", Action: "wake",
		RetryCount: 1, Enabled: true,
	}
	if err := repo.CreateSchedule(s); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	started := time.Now().UTC().Add(-2 * time.Second)
	finished := time.Now().UTC()
	runID, err := repo.RecordTaskRun(s.ID, "partial", "成功 1 / 失败 1 / 跳过 0", started, finished)
	if err != nil {
		t.Fatalf("RecordTaskRun: %v", err)
	}
	now := time.Now().UTC()
	boot := "sent"
	if err := repo.RecordTaskRunDevice(runID, TaskRunDevice{DeviceID: "dev-1", Name: "LAN-29", Status: "success", SentAt: &now, BootStatus: &boot}); err != nil {
		t.Fatalf("RecordTaskRunDevice success: %v", err)
	}
	if err := repo.RecordTaskRunDevice(runID, TaskRunDevice{DeviceID: "dev-2", Name: "LAN-30", Status: "failed", Reason: "Agent 离线"}); err != nil {
		t.Fatalf("RecordTaskRunDevice failed: %v", err)
	}
	if err := repo.UpdateScheduleLastRun(s.ID, finished); err != nil {
		t.Fatalf("UpdateScheduleLastRun: %v", err)
	}
	// 最近执行状态派生。
	if st, ok, err := repo.LastTaskRunStatus(s.ID); err != nil || !ok || st != "partial" {
		t.Fatalf("LastTaskRunStatus 应为 partial，got %q ok=%v err=%v", st, ok, err)
	}
	if _, ok, err := repo.LastTaskRunStatus("no-such"); err != nil || ok {
		t.Fatalf("无执行记录时 ok 应为 false，got ok=%v err=%v", ok, err)
	}
	// 执行历史分页与每设备结果。
	runs, total, err := repo.ListTaskRuns(s.ID, 1, 50)
	if err != nil || total != 1 || len(runs) != 1 {
		t.Fatalf("ListTaskRuns 应返回 1 条 total=1，got total=%d len=%d err=%v", total, len(runs), err)
	}
	if runs[0].Status != "partial" || len(runs[0].Devices) != 2 {
		t.Fatalf("run 记录不一致：%+v", runs[0])
	}
	if runs[0].Devices[0].Status != "success" || runs[0].Devices[1].Status != "failed" {
		t.Fatalf("每设备结果不一致：%+v", runs[0].Devices)
	}
	// last_run_at 已更新。
	got, _ := repo.GetSchedule(s.ID)
	if got.LastRunAt == nil {
		t.Fatal("UpdateScheduleLastRun 后 LastRunAt 不应为空")
	}
}

func TestRepositoryNetworkScopedMAC(t *testing.T) {
	repo := setupTestRepo(t)
	// 默认网络建一台；未指定 network_id 应默认 default。
	a := validDevice()
	if err := repo.CreateDevice(a); err != nil {
		t.Fatalf("默认网络创建失败：%v", err)
	}
	if a.NetworkID != "default" {
		t.Fatalf("未指定 network_id 应默认 default，got %q", a.NetworkID)
	}
	// 建第二个网络，同一 MAC 允许。
	if err := repo.CreateNetwork(&Network{Name: "实验室", CIDR: "192.168.2.0/24"}); err != nil {
		t.Fatalf("创建网络失败：%v", err)
	}
	lab, err := repo.GetNetworkByCIDR("192.168.2.0/24")
	if err != nil {
		t.Fatalf("按 CIDR 查网络失败：%v", err)
	}
	b := validDevice()
	b.Name = "LAB-29"
	b.IP = "192.168.2.29"
	b.NetworkID = lab.ID
	if err := repo.CreateDevice(b); err != nil {
		t.Fatalf("不同网络同一 MAC 应允许，got %v", err)
	}
	// 同网络再建同一 MAC → 冲突。
	c := validDevice()
	c.Name = "LAB-30"
	c.IP = "192.168.2.30"
	c.NetworkID = lab.ID
	if err := repo.CreateDevice(c); !errors.Is(err, ErrMACConflict) {
		t.Fatalf("同网络重复 MAC 应 ErrMACConflict，got %v", err)
	}
	// 按网络查：各网络返回各自的设备。
	gotA, err := repo.GetDeviceByMACInNetwork("AA:BB:CC:DD:EE:01", "default")
	if err != nil || gotA.ID != a.ID {
		t.Fatalf("默认网络应查到设备 A，got %+v, %v", gotA, err)
	}
	gotB, err := repo.GetDeviceByMACInNetwork("AA:BB:CC:DD:EE:01", lab.ID)
	if err != nil || gotB.ID != b.ID {
		t.Fatalf("实验室网络应查到设备 B，got %+v, %v", gotB, err)
	}
	// 把 B 迁回默认网络 → 冲突（迁移时重新检查）。
	merged := *b
	merged.NetworkID = "default"
	if err := repo.UpdateDevice(b.ID, &merged); !errors.Is(err, ErrMACConflict) {
		t.Fatalf("迁入已有同 MAC 的网络应 ErrMACConflict，got %v", err)
	}
	// 列表按网络过滤。
	items, total, err := repo.ListDevicesPage("", "", "", "", lab.ID, 1, 50)
	if err != nil || total != 1 || items[0].ID != b.ID {
		t.Fatalf("按网络过滤应只返回 B，got total=%d err=%v", total, err)
	}
}

// 未指定 network_id 时跟随 Agent 的首个管理网络；显式指定仍优先；
// Agent 无网络时回落默认网络（v0.2.9 问题 2 补充：新建设备不填网络时
// 应进其 Agent 的网络，而非一律进默认网络）。
func TestRepositoryNetworkAutoResolve(t *testing.T) {
	repo := setupTestRepo(t)
	if _, err := repo.DB().Exec(`INSERT INTO agents (id, name, subnet, approval_status, status, authorized) VALUES ('agent-home-lan', 'home', '192.168.1.0/24', 'approved', 'offline', 1)`); err != nil {
		t.Fatalf("插入测试 Agent 失败：%v", err)
	}
	if err := repo.CreateNetwork(&Network{Name: "实验室", CIDR: "192.168.2.0/24"}); err != nil {
		t.Fatalf("创建网络失败：%v", err)
	}
	lab, err := repo.GetNetworkByCIDR("192.168.2.0/24")
	if err != nil {
		t.Fatalf("按 CIDR 查网络失败：%v", err)
	}
	if err := repo.SetAgentNetworks("agent-home-lan", []string{lab.ID}); err != nil {
		t.Fatalf("分配 Agent 网络失败：%v", err)
	}
	// 未指定 network_id → 跟随 Agent 的实验室网络。
	d := validDevice()
	d.NetworkID = ""
	if err := repo.CreateDevice(d); err != nil {
		t.Fatalf("创建失败：%v", err)
	}
	if d.NetworkID != lab.ID {
		t.Fatalf("未指定 network_id 应跟随 Agent 网络 %q，got %q", lab.ID, d.NetworkID)
	}
	// 同 MAC 再建（仍不填网络）→ 同一网络冲突。
	dup := validDevice()
	dup.Name = "DUP"
	dup.IP = "192.168.2.30"
	dup.NetworkID = ""
	if err := repo.CreateDevice(dup); !errors.Is(err, ErrMACConflict) {
		t.Fatalf("跟随同一 Agent 网络的重复 MAC 应 ErrMACConflict，got %v", err)
	}
	// 显式指定 default 仍允许（不同网络）。
	explicit := validDevice()
	explicit.Name = "EXP"
	explicit.IP = "192.168.1.99"
	explicit.NetworkID = "default"
	if err := repo.CreateDevice(explicit); err != nil {
		t.Fatalf("显式默认网络创建失败：%v", err)
	}
	// ValidateDevice 同样解析：空 network_id 解析到 Agent 网络后再做 MAC 检查。
	res := ValidateDevice(repo, approvedOnlineResolver(), validDevice(), "")
	if !res.HasErrors() {
		t.Fatalf("同网络重复 MAC 校验应报错，got 无错误")
	}
	found := false
	for _, e := range res.Errors {
		if e.Field == "mac" {
			found = true
		}
	}
	if !found {
		t.Fatalf("校验错误应含 mac 字段，got %+v", res.Errors)
	}
}

func TestRepositoryNetworkCRUD(t *testing.T) {
	repo := setupTestRepo(t)
	// 默认网络恒存在。
	if ok, _ := repo.NetworkExists("default"); !ok {
		t.Fatal("默认网络应恒存在")
	}
	if _, err := repo.GetNetwork("no-such-network"); !errors.Is(err, ErrNetworkNotFound) {
		t.Fatalf("不存在的网络应 ErrNetworkNotFound，got %v", err)
	}
	n := &Network{Name: "办公室", CIDR: "192.168.10.0/24", Description: "办公网"}
	if err := repo.CreateNetwork(n); err != nil {
		t.Fatalf("创建网络失败：%v", err)
	}
	if n.ID == "" {
		t.Fatal("创建后应回填 ID")
	}
	// CIDR 重复 → 冲突；非法 CIDR → 报错；空名 → 报错。
	if err := repo.CreateNetwork(&Network{Name: "重复网段", CIDR: "192.168.10.0/24"}); !errors.Is(err, ErrNetworkCIDRConflict) {
		t.Fatalf("重复 CIDR 应 ErrNetworkCIDRConflict，got %v", err)
	}
	if err := repo.CreateNetwork(&Network{Name: "坏网段", CIDR: "not-a-cidr"}); !errors.Is(err, ErrNetworkCIDRInvalid) {
		t.Fatalf("非法 CIDR 应 ErrNetworkCIDRInvalid，got %v", err)
	}
	if err := repo.CreateNetwork(&Network{Name: ""}); !errors.Is(err, ErrNetworkNameRequired) {
		t.Fatalf("空名称应 ErrNetworkNameRequired，got %v", err)
	}
	// 更新、重查。
	n.Name = "办公室-改名"
	if err := repo.UpdateNetwork(n.ID, n); err != nil {
		t.Fatalf("更新网络失败：%v", err)
	}
	got, err := repo.GetNetwork(n.ID)
	if err != nil || got.Name != "办公室-改名" {
		t.Fatalf("更新后应读到新名称，got %+v, %v", got, err)
	}
	// 设备引用 → 拒绝删除。
	d := validDevice()
	d.Name = "OFFICE-11"
	d.IP = "192.168.10.11"
	d.NetworkID = n.ID
	if err := repo.CreateDevice(d); err != nil {
		t.Fatalf("在新网络创建设备失败：%v", err)
	}
	if err := repo.DeleteNetwork(n.ID); !errors.Is(err, ErrNetworkInUse) {
		t.Fatalf("被设备引用的网络删除应 ErrNetworkInUse，got %v", err)
	}
	if err := repo.DeleteDevice(d.ID); err != nil {
		t.Fatalf("删除设备失败：%v", err)
	}
	// Agent 关联：先插 Agent 行（外键），再关联、查、拒删、解除、删除。
	if _, err := repo.DB().Exec(`INSERT INTO agents (id, name, subnet, approval_status, status, authorized) VALUES ('agent-home-lan', 'home', '192.168.1.0/24', 'approved', 'offline', 1)`); err != nil {
		t.Fatalf("插入测试 Agent 失败：%v", err)
	}
	if err := repo.SetAgentNetworks("agent-home-lan", []string{"default", n.ID}); err != nil {
		t.Fatalf("设置 Agent 网络失败：%v", err)
	}
	an, err := repo.ListAgentNetworks("agent-home-lan")
	if err != nil || len(an) != 2 {
		t.Fatalf("Agent 应管理 2 个网络，got %d, %v", len(an), err)
	}
	if err := repo.DeleteNetwork(n.ID); !errors.Is(err, ErrNetworkInUse) {
		t.Fatalf("被 Agent 引用的网络删除应 ErrNetworkInUse，got %v", err)
	}
	if err := repo.SetAgentNetworks("agent-home-lan", []string{"default"}); err != nil {
		t.Fatalf("解除关联失败：%v", err)
	}
	if err := repo.SetAgentNetworks("agent-home-lan", []string{"no-such-network"}); !errors.Is(err, ErrNetworkNotFound) {
		t.Fatalf("关联不存在的网络应 ErrNetworkNotFound，got %v", err)
	}
	if err := repo.DeleteNetwork(n.ID); err != nil {
		t.Fatalf("解除关联后删除应成功，got %v", err)
	}
	if err := repo.DeleteNetwork("default"); !errors.Is(err, ErrNetworkDeleteDefault) {
		t.Fatalf("默认网络删除应 ErrNetworkDeleteDefault，got %v", err)
	}
}
