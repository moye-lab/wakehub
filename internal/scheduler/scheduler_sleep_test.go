package scheduler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wakehub/wakehub/internal/audit"
	"github.com/wakehub/wakehub/internal/device"
	"github.com/wakehub/wakehub/internal/secret"
	"github.com/wakehub/wakehub/internal/sleep"
)

// TestBuildSleepPayload 覆盖 buildSleepPayload 的校验链（v0.3.2 新增）。
// 反向验证：把 buildSleepPayload 的 sleep_type 检查换成“直接放行”后，
// “未配置睡眠适配器”与“适配器类型无效”两例立即失败；把解密步骤
// 删掉后，SSHUseSudo/SSHPassword 断言失败。
func TestBuildSleepPayload(t *testing.T) {
	eng, repo, hub := setupTestScheduler(t)
	const testKey = "test-secret-key-032"
	eng.SetSecretKey(testKey)
	// Agent 的管理网段 192.168.1.0/24：范围校验以此为准。
	if _, err := hub.Store().CreateAgentApproved("agent-scope", "agent-scope", "192.168.1.0/24"); err != nil {
		t.Fatalf("CreateAgentApproved: %v", err)
	}
	enc, err := secret.Encrypt(testKey, "pw123")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	mkSSH := func(mac, ip string, useSudo bool) *device.Device {
		d := &device.Device{
			Name: "NAS", MAC: mac, IP: ip, AgentID: "agent-scope",
			SleepType: "ssh", SSHUser: "sg", SSHPort: 22,
			SSHPasswordEnc: enc, SSHOS: "linux", SSHUseSudo: useSudo,
		}
		if err := repo.CreateDevice(d); err != nil {
			t.Fatalf("CreateDevice ssh %s: %v", mac, err)
		}
		return d
	}

	t.Run("http_sol 回环端点放行", func(t *testing.T) {
		d := &device.Device{
			Name: "NAS", MAC: "AA:BB:CC:DD:EE:41", IP: "192.168.1.3", AgentID: "agent-scope",
			SleepType: "http_sol", SleepEndpoint: "http://127.0.0.1:18080/sol",
		}
		if err := repo.CreateDevice(d); err != nil {
			t.Fatalf("CreateDevice: %v", err)
		}
		p, err := eng.buildSleepPayload(*d)
		if err != nil {
			t.Fatalf("buildSleepPayload: %v", err)
		}
		if p.Action != sleep.ActionShutdown || p.AdapterType != "http_sol" || p.Endpoint != "http://127.0.0.1:18080/sol" {
			t.Errorf("payload 异常: %+v", p)
		}
	})

	t.Run("http_sol 超出 Agent 管理网段", func(t *testing.T) {
		d := device.Device{
			Name: "NAS", MAC: "AA:BB:CC:DD:EE:42", IP: "10.0.0.5", AgentID: "agent-scope",
			SleepType: "http_sol", SleepEndpoint: "http://10.0.0.5/sol",
		}
		_, err := eng.buildSleepPayload(d)
		if err == nil || !strings.Contains(err.Error(), "不在执行 Agent 的管理网段内") {
			t.Errorf("应报网段外错误，实际: %v", err)
		}
	})

	t.Run("http_sol 公网端点拒绝", func(t *testing.T) {
		d := device.Device{
			Name: "NAS", MAC: "AA:BB:CC:DD:EE:43", IP: "192.168.1.3", AgentID: "agent-scope",
			SleepType: "http_sol", SleepEndpoint: "http://example.com/sol",
		}
		_, err := eng.buildSleepPayload(d)
		if err == nil || !strings.Contains(err.Error(), "睡眠端点无效") {
			t.Errorf("应报端点无效，实际: %v", err)
		}
	})

	t.Run("ssh 透传 sudo 与解密密码", func(t *testing.T) {
		d := mkSSH("AA:BB:CC:DD:EE:44", "192.168.1.50", true)
		p, err := eng.buildSleepPayload(*d)
		if err != nil {
			t.Fatalf("buildSleepPayload: %v", err)
		}
		if p.Action != sleep.ActionShutdown || p.AdapterType != "ssh" || p.SSHHost != "192.168.1.50" || p.SSHPort != 22 ||
			p.SSHUser != "sg" || p.SSHPassword != "pw123" || !p.SSHUseSudo || p.SSHTargetOS != "linux" {
			t.Errorf("SSH payload 异常: %+v", p)
		}
	})

	t.Run("ssh 超出 Agent 管理网段", func(t *testing.T) {
		d := mkSSH("AA:BB:CC:DD:EE:45", "10.0.0.3", false)
		_, err := eng.buildSleepPayload(*d)
		if err == nil || !strings.Contains(err.Error(), "不在执行 Agent 的管理网段内") {
			t.Errorf("应报网段外错误，实际: %v", err)
		}
	})

	t.Run("ssh 用户名为空", func(t *testing.T) {
		d := mkSSH("AA:BB:CC:DD:EE:46", "192.168.1.51", false)
		d.SSHUser = ""
		_, err := eng.buildSleepPayload(*d)
		if err == nil || !strings.Contains(err.Error(), "SSH 用户名未配置") {
			t.Errorf("应报用户名未配置，实际: %v", err)
		}
	})

	t.Run("ssh 密码无法解密", func(t *testing.T) {
		d := mkSSH("AA:BB:CC:DD:EE:47", "192.168.1.52", false)
		d.SSHPasswordEnc = "not-valid-ciphertext"
		_, err := eng.buildSleepPayload(*d)
		if err == nil || !strings.Contains(err.Error(), "SSH 密码无法解密") {
			t.Errorf("应报密码无法解密，实际: %v", err)
		}
	})

	t.Run("未配置睡眠适配器", func(t *testing.T) {
		d := createTestDevice(t, repo, "PC-无适配器", "AA:BB:CC:DD:EE:48", "agent-scope", "255.255.255.255", nil)
		_, err := eng.buildSleepPayload(*d)
		if err == nil || !strings.Contains(err.Error(), "未配置睡眠适配器") {
			t.Errorf("应报未配置适配器，实际: %v", err)
		}
		d2 := device.Device{SleepType: "", AgentID: "agent-scope"}
		if _, err := eng.buildSleepPayload(d2); err == nil || !strings.Contains(err.Error(), "未配置睡眠适配器") {
			t.Errorf("空 sleep_type 应报未配置适配器，实际: %v", err)
		}
	})

	t.Run("睡眠适配器类型无效", func(t *testing.T) {
		d := device.Device{SleepType: "bogus", AgentID: "agent-scope"}
		_, err := eng.buildSleepPayload(d)
		if err == nil || !strings.Contains(err.Error(), "睡眠适配器类型无效") {
			t.Errorf("应报适配器类型无效，实际: %v", err)
		}
	})
}

// TestExecuteScheduleSleepWithLiveAgent：sleep 任务端到端（v0.3.2 新增）——
// 真 Agent + http_sol 受控端，断言端点收到 POST /shutdown、run 成功、
// 设备行 success 且无 BootStatus、device_sleep 审计与 schedule_run 审计在位。
// 反向验证：把 executeSchedule 的 sleep 分支改回只走 wake，端点收不到
// 请求、run 失败；把 buildSleepPayload 的 Action 改成 reboot，端点断言失败。
func TestExecuteScheduleSleepWithLiveAgent(t *testing.T) {
	eng, repo, hub := setupTestScheduler(t)
	eng.Configure(5*time.Second, 1)
	alog := audit.NewLogger(repo.DB())
	eng.SetAuditLogger(alog)
	setupLiveAgent(t, hub, "agent-live-sleep")

	var mu sync.Mutex
	var got []string
	ep := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ep.Close)

	dev := &device.Device{
		Name: "NAS-01", MAC: "AA:BB:CC:DD:EE:51", IP: "192.168.1.3", AgentID: "agent-live-sleep",
		SleepType: "http_sol", SleepEndpoint: ep.URL,
	}
	if err := repo.CreateDevice(dev); err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	item := createTestScheduleWithAction(t, eng, "午夜关机", "device", dev.ID, "sleep", 0, false)
	eng.executeSchedule(*item, false)

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != "POST /shutdown" {
		t.Fatalf("受控端应收到 1 次 POST /shutdown，实际: %v", got)
	}
	runs, _, err := eng.ListRuns(item.ID, 1, 10)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != "success" {
		t.Fatalf("run 应 success，实际: %+v", runs)
	}
	if runs[0].Output != "成功 1 / 失败 0 / 跳过 0" {
		t.Errorf("run 输出异常: %q", runs[0].Output)
	}
	if len(runs[0].Devices) != 1 || runs[0].Devices[0].Status != "success" {
		t.Fatalf("设备行应 success，实际: %+v", runs[0].Devices)
	}
	if runs[0].Devices[0].SentAt == nil {
		t.Error("sleep 成功行应有 SentAt")
	}
	if runs[0].Devices[0].BootStatus != nil {
		t.Errorf("sleep 成功行不应有 BootStatus，实际: %v", *runs[0].Devices[0].BootStatus)
	}
	entries, total, err := alog.ListFiltered(audit.Filter{Actions: []string{"device_sleep"}, TargetID: dev.ID}, 1, 10)
	if err != nil {
		t.Fatalf("ListFiltered device_sleep: %v", err)
	}
	if total != 1 || len(entries) != 1 || entries[0].Actor != "system" {
		t.Errorf("应有 1 条 device_sleep 审计（actor system），实际 total=%d entries=%+v", total, entries)
	}
	entries, total, err = alog.ListFiltered(audit.Filter{Actions: []string{"schedule_run"}, TargetID: item.ID}, 1, 10)
	if err != nil {
		t.Fatalf("ListFiltered schedule_run: %v", err)
	}
	if total != 1 {
		t.Errorf("应有 1 条 schedule_run 审计，实际 total=%d", total)
	}
}

// TestExecuteScheduleSleepAdapterMissingSkipped：sleep 任务目标未配置适配器
// → 设备 skipped（不重试、不发 dispatch）、run 状态 skipped。
// 反向验证：把“适配器缺失 → skipped”改成走 dispatch，run 会变成 failed。
func TestExecuteScheduleSleepAdapterMissingSkipped(t *testing.T) {
	eng, repo, hub := setupTestScheduler(t)
	eng.Configure(5*time.Second, 1)
	alog := audit.NewLogger(repo.DB())
	eng.SetAuditLogger(alog)
	setupLiveAgent(t, hub, "agent-live-nosleep")
	dev := createTestDevice(t, repo, "PC-无适配器", "AA:BB:CC:DD:EE:52", "agent-live-nosleep", "255.255.255.255", nil)
	item := createTestScheduleWithAction(t, eng, "午夜关机", "device", dev.ID, "sleep", 2, false)
	eng.executeSchedule(*item, false)

	runs, _, err := eng.ListRuns(item.ID, 1, 10)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != "skipped" {
		t.Fatalf("run 应 skipped，实际: %+v", runs)
	}
	if runs[0].Output != "成功 0 / 失败 0 / 跳过 1" {
		t.Errorf("run 输出异常: %q", runs[0].Output)
	}
	if len(runs[0].Devices) != 1 || runs[0].Devices[0].Status != "skipped" ||
		!strings.Contains(runs[0].Devices[0].Reason, "未配置睡眠适配器") {
		t.Fatalf("设备行应 skipped 且原因含未配置睡眠适配器，实际: %+v", runs[0].Devices)
	}
	_, total, err := alog.ListFiltered(audit.Filter{Actions: []string{"device_sleep", "device_sleep_failed"}, TargetID: dev.ID}, 1, 10)
	if err != nil {
		t.Fatalf("ListFiltered: %v", err)
	}
	if total != 0 {
		t.Errorf("配置错误不应写 device_sleep 审计，实际 total=%d", total)
	}
}

// TestExecuteScheduleSleepGroupMixed：分组 sleep 任务——一台 http_sol 成功、
// 一台未配置适配器跳过 → run success（跳过不降级状态，见 executeSchedule
// 的状态派生：仅失败才产生 partial/failed），输出“成功 1 / 失败 0 / 跳过 1”。
func TestExecuteScheduleSleepGroupMixed(t *testing.T) {
	eng, repo, hub := setupTestScheduler(t)
	eng.Configure(5*time.Second, 1)
	setupLiveAgent(t, hub, "agent-live-group")
	ep := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ep.Close)
	g, err := repo.CreateGroup("卧室", "")
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	okDev := &device.Device{
		Name: "NAS-01", MAC: "AA:BB:CC:DD:EE:53", IP: "192.168.1.3", AgentID: "agent-live-group",
		SleepType: "http_sol", SleepEndpoint: ep.URL, GroupID: g.ID,
	}
	if err := repo.CreateDevice(okDev); err != nil {
		t.Fatalf("CreateDevice ok: %v", err)
	}
	skipDev := createTestDevice(t, repo, "PC-无适配器", "AA:BB:CC:DD:EE:54", "agent-live-group", "255.255.255.255", nil)
	skipDev.GroupID = g.ID
	if err := repo.UpdateDevice(skipDev.ID, skipDev); err != nil {
		t.Fatalf("UpdateDevice group: %v", err)
	}
	item := createTestScheduleWithAction(t, eng, "分组关机", "group", g.ID, "sleep", 0, false)
	eng.executeSchedule(*item, false)

	runs, _, err := eng.ListRuns(item.ID, 1, 10)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != "success" {
		t.Fatalf("run 应 success（跳过不降级），实际: %+v", runs)
	}
	if runs[0].Output != "成功 1 / 失败 0 / 跳过 1" {
		t.Errorf("run 输出异常: %q", runs[0].Output)
	}
	byID := map[string]device.TaskRunDevice{}
	for _, d := range runs[0].Devices {
		byID[d.DeviceID] = d
	}
	if byID[okDev.ID].Status != "success" {
		t.Errorf("http_sol 设备应 success，实际: %+v", byID[okDev.ID])
	}
	if byID[skipDev.ID].Status != "skipped" || !strings.Contains(byID[skipDev.ID].Reason, "未配置睡眠适配器") {
		t.Errorf("无适配器设备应 skipped，实际: %+v", byID[skipDev.ID])
	}
}

// TestExecuteScheduleSleepRetryLimit：受控端不可达时按 retry_count 重试
// （2 次重试 → 3 次尝试，≥ 900ms），最终 failed 并写 device_sleep_failed。
// 反向验证：把 sleepDeviceWithRetry 的重试循环改成只发一次，耗时断言失败。
func TestExecuteScheduleSleepRetryLimit(t *testing.T) {
	eng, repo, hub := setupTestScheduler(t)
	eng.Configure(5*time.Second, 1)
	alog := audit.NewLogger(repo.DB())
	eng.SetAuditLogger(alog)
	setupLiveAgent(t, hub, "agent-live-retry")
	dev := &device.Device{
		Name: "NAS-坏", MAC: "AA:BB:CC:DD:EE:55", IP: "192.168.1.3", AgentID: "agent-live-retry",
		SleepType: "http_sol", SleepEndpoint: "http://127.0.0.1:1/sol",
	}
	if err := repo.CreateDevice(dev); err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	item := createTestScheduleWithAction(t, eng, "午夜关机", "device", dev.ID, "sleep", 2, false)
	start := time.Now()
	eng.executeSchedule(*item, false)
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Errorf("重试 2 次应至少耗时 900ms（2×500ms backoff），实际 %v", elapsed)
	}
	runs, _, err := eng.ListRuns(item.ID, 1, 10)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != "failed" {
		t.Fatalf("run 应 failed，实际: %+v", runs)
	}
	if len(runs[0].Devices) != 1 || runs[0].Devices[0].Status != "failed" ||
		!strings.Contains(runs[0].Devices[0].Reason, "受控执行端") {
		t.Fatalf("设备行应 failed 且原因含受控执行端，实际: %+v", runs[0].Devices)
	}
	_, total, err := alog.ListFiltered(audit.Filter{Actions: []string{"device_sleep_failed"}, TargetID: dev.ID}, 1, 10)
	if err != nil {
		t.Fatalf("ListFiltered: %v", err)
	}
	if total != 1 {
		t.Errorf("应有 1 条 device_sleep_failed 审计，实际 total=%d", total)
	}
}

// TestAgentScopeCIDRsLookupFailureFailsClosed 覆盖 v0.3.3 问题 9：
// 管理网络查询失败时 fail-closed（拒绝关机），不得退化为空作用域；
// Agent 确实未声明网络时保留基线回退（复现审查证明的 DROP TABLE 场景）。
func TestAgentScopeCIDRsLookupFailureFailsClosed(t *testing.T) {
	eng, repo, hub := setupTestScheduler(t)
	// Agent agent-9：无 subnet，仅通过 agent_networks 管理 192.168.1.0/24。
	if _, err := hub.Store().CreateAgentApproved("agent-9", "agent-9", ""); err != nil {
		t.Fatalf("CreateAgentApproved: %v", err)
	}
	net1 := &device.Network{Name: "LAN1", CIDR: "192.168.1.0/24"}
	if err := repo.CreateNetwork(net1); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}
	if err := repo.SetAgentNetworks("agent-9", []string{net1.ID}); err != nil {
		t.Fatalf("SetAgentNetworks: %v", err)
	}
	dev := device.Device{
		Name: "PC-9", MAC: "AA:BB:CC:DD:EE:09", IP: "192.168.2.50",
		AgentID: "agent-9", SleepType: "http_sol",
		SleepEndpoint: "http://192.168.2.50:8009",
	}
	// 健康时：网段外端点被拒绝。
	if _, err := eng.buildSleepPayload(dev); err == nil {
		t.Fatal("健康时网段外端点应被拒绝")
	} else if !strings.Contains(err.Error(), "不在执行 Agent 的管理网段内") {
		t.Fatalf("健康时应报网段外，got %v", err)
	}
	// 模拟管理网络读失败：删 agent_networks 表。
	if _, err := repo.DB().Exec(`DROP TABLE agent_networks`); err != nil {
		t.Fatalf("DROP agent_networks: %v", err)
	}
	// 查询失败时必须继续拒绝（fail-closed），不得放行网段外端点。
	if _, err := eng.buildSleepPayload(dev); err == nil {
		t.Fatal("管理网络查询失败时应拒绝关机，不得退化为空作用域放行")
	} else if !strings.Contains(err.Error(), "管理网段查询失败") {
		t.Fatalf("查询失败应报管理网段查询失败，got %v", err)
	}

	// 基线回退仍保留：Agent 确实未声明网络（且无 subnet）时只做基线校验。
	eng2, _, hub2 := setupTestScheduler(t)
	if _, err := hub2.Store().CreateAgentApproved("agent-plain", "agent-plain", ""); err != nil {
		t.Fatalf("CreateAgentApproved: %v", err)
	}
	dev2 := device.Device{
		Name: "PC-plain", MAC: "AA:BB:CC:DD:EE:0A", IP: "192.168.2.50",
		AgentID: "agent-plain", SleepType: "http_sol",
		SleepEndpoint: "http://192.168.2.50:8009",
	}
	if _, err := eng2.buildSleepPayload(dev2); err != nil {
		t.Fatalf("未声明网络的 Agent 应走基线校验放行，got %v", err)
	}
}
