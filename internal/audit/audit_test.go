package audit

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupTestLogger(t *testing.T) *Logger {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, f := range []string{"001_init.sql"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "migrations", f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := db.Exec(string(b)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	return NewLogger(db)
}

func TestResultOf(t *testing.T) {
	cases := map[string]string{
		"device_wake":        "success",
		"device_wake_failed": "failed",
		"login_failed":       "failed",
		"login_success":      "success",
		"schedule_run":       "success",
		"audit_cleanup":      "success",
	}
	for action, want := range cases {
		if got := ResultOf(action); got != want {
			t.Errorf("ResultOf(%q) = %q, want %q", action, got, want)
		}
	}
}

func TestRecordReturnsError(t *testing.T) {
	l := setupTestLogger(t)
	if err := l.Record("admin", "device_wake", "device", "d1", "ok", "127.0.0.1"); err != nil {
		t.Fatalf("Record 正常写入应无错: %v", err)
	}
	// 审计表被删后写入应返回错误，不得静默丢弃（契约 §9.1）。
	db := l.db
	if _, err := db.Exec(`DROP TABLE audit_logs`); err != nil {
		t.Fatalf("drop audit_logs: %v", err)
	}
	if err := l.Record("admin", "device_wake", "device", "d1", "ok", "127.0.0.1"); err == nil {
		t.Fatal("审计表缺失时 Record 应返回错误")
	}
}

func seedLogs(t *testing.T, l *Logger) {
	t.Helper()
	now := time.Now().UTC()
	rows := []struct {
		actor, action, targetType, targetID string
		at                                  time.Time
	}{
		{"admin", "device_wake", "device", "d1", now.Add(-2 * time.Hour)},
		{"admin", "device_wake_failed", "device", "d1", now.Add(-90 * time.Minute)},
		{"admin", "device_sleep", "device", "d2", now.Add(-60 * time.Minute)},
		{"system", "schedule_run", "schedule", "s1", now.Add(-30 * time.Minute)},
		{"admin", "device_check", "device", "d1", now.Add(-10 * time.Minute)},
	}
	for _, r := range rows {
		if _, err := l.db.Exec(
			`INSERT INTO audit_logs (id, actor, action, target_type, target_id, detail, ip_address, created_at)
			 VALUES (?, ?, ?, ?, ?, '', '', ?)`,
			"seed-"+r.action+"-"+r.targetID, r.actor, r.action, r.targetType, r.targetID, r.at); err != nil {
			t.Fatalf("seed %s: %v", r.action, err)
		}
	}
}

func TestListFiltered(t *testing.T) {
	l := setupTestLogger(t)
	seedLogs(t, l)
	now := time.Now().UTC()

	// 无筛选：全部 5 条，total 正确。
	logs, total, err := l.ListFiltered(Filter{}, 1, 50)
	if err != nil || total != 5 || len(logs) != 5 {
		t.Fatalf("无筛选应返回 5 条，total=%d len=%d err=%v", total, len(logs), err)
	}
	// result 派生：失败事件的 result 为 failed。
	for _, e := range logs {
		if e.Action == "device_wake_failed" && e.Result != "failed" {
			t.Fatalf("device_wake_failed 的 result 应为 failed，实际 %q", e.Result)
		}
		if e.Action == "device_wake" && e.Result != "success" {
			t.Fatalf("device_wake 的 result 应为 success，实际 %q", e.Result)
		}
	}
	// action 单值 + 多值。
	if _, total, _ := l.ListFiltered(Filter{Actions: []string{"device_wake"}}, 1, 50); total != 1 {
		t.Fatalf("action=device_wake 应为 1 条，实际 %d", total)
	}
	if _, total, _ := l.ListFiltered(Filter{Actions: []string{"device_wake", "device_sleep"}}, 1, 50); total != 2 {
		t.Fatalf("action 多值应为 2 条，实际 %d", total)
	}
	// actor / target 筛选。
	if _, total, _ := l.ListFiltered(Filter{Actor: "system"}, 1, 50); total != 1 {
		t.Fatalf("actor=system 应为 1 条，实际 %d", total)
	}
	if _, total, _ := l.ListFiltered(Filter{TargetType: "device", TargetID: "d1"}, 1, 50); total != 3 {
		t.Fatalf("target device/d1 应为 3 条，实际 %d", total)
	}
	// result 筛选。
	if _, total, _ := l.ListFiltered(Filter{Result: "failed"}, 1, 50); total != 1 {
		t.Fatalf("result=failed 应为 1 条，实际 %d", total)
	}
	if _, total, _ := l.ListFiltered(Filter{Result: "success"}, 1, 50); total != 4 {
		t.Fatalf("result=success 应为 4 条，实际 %d", total)
	}
	// 时间范围（含端点）。
	from := now.Add(-100 * time.Minute)
	to := now.Add(-20 * time.Minute)
	if _, total, _ := l.ListFiltered(Filter{From: &from, To: &to}, 1, 50); total != 3 {
		t.Fatalf("时间范围应为 3 条，实际 %d", total)
	}
	// 分页：每页 2 条，第二页 2 条，total 仍为 5。
	page2, total, err := l.ListFiltered(Filter{}, 2, 2)
	if err != nil || total != 5 || len(page2) != 2 {
		t.Fatalf("分页 page=2 应返回 2 条 total=5，实际 len=%d total=%d err=%v", len(page2), total, err)
	}
	// pageSize 超限钳制为 200（不报错）。
	if _, _, err := l.ListFiltered(Filter{}, 1, 500); err != nil {
		t.Fatalf("pageSize 超限应钳制而非报错: %v", err)
	}
}

func TestCleanupOlderThan(t *testing.T) {
	l := setupTestLogger(t)
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -100)
	for i, at := range []time.Time{old, old.Add(time.Hour), now.Add(-time.Hour), now} {
		if _, err := l.db.Exec(
			`INSERT INTO audit_logs (id, actor, action, target_type, target_id, detail, ip_address, created_at)
			 VALUES (?, 'admin', 'device_wake', 'device', 'd1', '', '', ?)`,
			"cleanup-"+string(rune('a'+i)), at); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	n, err := l.CleanupOlderThan(90)
	if err != nil || n != 2 {
		t.Fatalf("清理 90 天前应删 2 条，实际 %d（%v）", n, err)
	}
	var total int
	if err := l.db.QueryRow(`SELECT COUNT(*) FROM audit_logs`).Scan(&total); err != nil || total != 2 {
		t.Fatalf("清理后应剩 2 条，实际 %d（%v）", total, err)
	}
}

func TestStartCleanupWritesAuditCleanup(t *testing.T) {
	l := setupTestLogger(t)
	old := time.Now().UTC().AddDate(0, 0, -100)
	if _, err := l.db.Exec(
		`INSERT INTO audit_logs (id, actor, action, target_type, target_id, detail, ip_address, created_at)
		 VALUES ('old-1', 'admin', 'device_wake', 'device', 'd1', '', '', ?)`, old); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		l.StartCleanup(ctx, time.Hour, 90)
		close(done)
	}()
	// 启动时立即清理一次：等待 audit_cleanup 出现（最多 5s）。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		_ = l.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='audit_cleanup'`).Scan(&n)
		if n == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	var n int
	if err := l.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='audit_cleanup'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("清理应写一条 audit_cleanup 审计，实际 %d（%v）", n, err)
	}
	var actor string
	if err := l.db.QueryRow(`SELECT actor FROM audit_logs WHERE action='audit_cleanup'`).Scan(&actor); err != nil || actor != "system" {
		t.Fatalf("audit_cleanup 的 actor 应为 system，实际 %q（%v）", actor, err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("StartCleanup 在 ctx 取消后应退出")
	}
}
