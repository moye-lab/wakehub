package scheduler

import (
	"testing"
	"time"

	"github.com/wakehub/wakehub/internal/audit"
	"github.com/wakehub/wakehub/internal/device"
)

// TestExecuteScheduleWritesScheduleRunAudit：每次定时执行完成
// （成功/失败/跳过均记）写一条 schedule_run 审计（契约 §1.6，M5）。
func TestExecuteScheduleWritesScheduleRunAudit(t *testing.T) {
	eng, repo, hub := setupTestScheduler(t)
	eng.Configure(5*time.Second, 1)
	eng.SetAuditLogger(audit.NewLogger(repo.DB()))
	setupLiveAgent(t, hub, "agent-audit-1")
	sinkPort := startUDPSink(t)
	dev := createTestDevice(t, repo, "PC-01", "AA:BB:CC:DD:EE:01", "agent-audit-1", "127.0.0.1", nil, sinkPort)
	item := createTestSchedule(t, eng, "晨起", "device", dev.ID, 1, false)

	// 成功执行一次。
	eng.executeSchedule(*item, false)
	// 空目标执行一次（目标设备不存在 → skipped）。
	skipped := &device.ScheduleItem{
		Name: "空目标", CronExpr: "0 8 * * *", Timezone: "UTC",
		TargetType: "device", TargetID: "no-such-device", Action: "wake",
		RetryCount: 0, Enabled: true,
	}
	if err := eng.CreateSchedule(skipped, false); err != nil {
		t.Fatalf("CreateSchedule 空目标任务: %v", err)
	}
	eng.executeSchedule(*skipped, false)

	var n int
	if err := repo.DB().QueryRow(
		`SELECT COUNT(*) FROM audit_logs WHERE action='schedule_run'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("应有 2 条 schedule_run 审计，实际 %d（%v）", n, err)
	}
	var actor, detail string
	if err := repo.DB().QueryRow(
		`SELECT actor, detail FROM audit_logs WHERE action='schedule_run' AND target_id=?`,
		skipped.ID).Scan(&actor, &detail); err != nil {
		t.Fatalf("跳过执行的 schedule_run 审计缺失: %v", err)
	}
	if actor != "system" {
		t.Fatalf("schedule_run 的 actor 应为 system，实际 %q", actor)
	}
	if detail == "" {
		t.Fatal("schedule_run 的 detail 不应为空")
	}
}

// TestEngineWithoutAuditLoggerDoesNotCrash：未注入审计 logger 时
// 执行仍正常，只是不写审计（测试构造的 Engine 不传 logger）。
func TestEngineWithoutAuditLoggerDoesNotCrash(t *testing.T) {
	eng, repo, hub := setupTestScheduler(t)
	eng.Configure(5*time.Second, 1)
	setupLiveAgent(t, hub, "agent-audit-2")
	sinkPort := startUDPSink(t)
	dev := createTestDevice(t, repo, "PC-02", "AA:BB:CC:DD:EE:02", "agent-audit-2", "127.0.0.1", nil, sinkPort)
	item := createTestSchedule(t, eng, "晨起", "device", dev.ID, 1, false)
	eng.executeSchedule(*item, false)
	var n int
	if err := repo.DB().QueryRow(
		`SELECT COUNT(*) FROM audit_logs WHERE action='schedule_run'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("未注入 logger 时不应写 schedule_run 审计，实际 %d（%v）", n, err)
	}
}
