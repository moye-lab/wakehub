package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wakehub/wakehub/internal/agent"
	"github.com/wakehub/wakehub/internal/audit"
	"github.com/wakehub/wakehub/internal/auth"
	"github.com/wakehub/wakehub/internal/config"
	"github.com/wakehub/wakehub/internal/device"
	"github.com/wakehub/wakehub/internal/scheduler"
	_ "modernc.org/sqlite"
)

// setupM4Server 建一个应用了 001+002+003+004 迁移的测试服务端
// （与 setupM3Server 同构，仅多应用 004_runs.sql）。
func setupM4Server(t *testing.T) (*Server, http.Handler, *httptest.Server) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	for _, f := range []string{
		"../../migrations/001_init.sql", "../../migrations/002_auth.sql",
		"../../migrations/003_agent.sql", "../../migrations/004_runs.sql",
		"../../migrations/005_indexes.sql", "../../migrations/006_ssh_sleep.sql",
		"../../migrations/007_ssh_windows.sql", "../../migrations/008_network_identity.sql", "../../migrations/009_rename_seeded_networks.sql", "../../migrations/010_ssh_use_sudo.sql", "../../migrations/011_network_cidr_unique.sql", "../../migrations/012_task_runs_keep_history.sql",
	} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := db.Exec(string(b)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	t.Cleanup(func() { _ = db.Close() })

	cfg := config.ServerConfig{
		ListenAddr:       ":8080",
		SecretKey:        testSecret,
		EnrollToken:      testEnrollToken,
		WSAllowedOrigins: []string{"https://allowed.example.com"},
		TaskTimeout:      10 * time.Second,
		DefaultRetries:   3,
	}
	authSvc := auth.NewService(db, cfg.SecretKey, nil)
	if err := authSvc.EnsureAdmin("admin", testAdminPass); err != nil {
		t.Fatalf("EnsureAdmin: %v", err)
	}
	repo := device.NewRepository(db)
	hub := agent.NewHub(repo)
	sched := scheduler.NewEngine(repo, hub)
	srv := NewServer(cfg, repo, authSvc, hub, sched, audit.NewLogger(db))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, srv.Handler(), ts
}

// decodeEnvelope 解析标准响应信封，返回 data 的原始 JSON。
func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) (int, string, json.RawMessage) {
	t.Helper()
	var env struct {
		Code int             `json:"code"`
		Msg  string          `json:"message"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应不是标准信封: %v (%s)", err, rec.Body.String())
	}
	return env.Code, env.Msg, env.Data
}

// fieldCodeOf 提取 data.errors 里指定字段的 code。
func fieldCodeOf(t *testing.T, data json.RawMessage, field string) string {
	t.Helper()
	var d struct {
		Errors []auth.FieldError `json:"errors"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatalf("解析 errors 失败: %v (%s)", err, string(data))
	}
	for _, e := range d.Errors {
		if e.Field == field {
			return e.Code
		}
	}
	return ""
}

func TestScanValidationErrors(t *testing.T) {
	srv, handler, _ := setupM4Server(t)
	cookie := loginCookie(t, handler)
	token := createApprovedAgent(t, srv, "agent-scan-1")
	_ = token // 获批 Agent 已建，白名单为 192.168.1.0/24
	// 另一台获批但离线的 Agent（不连接）。
	createApprovedAgent(t, srv, "agent-offline-1")
	// 待审批与已禁用 Agent。
	pendingID := "agent-pending-1"
	if _, _, err := srv.hub.Store().CreateAgentApprovedWithToken(pendingID, "待审批", "192.168.1.0/24"); err != nil {
		t.Fatalf("create pending base: %v", err)
	}
	if err := srv.hub.Store().SetApprovalStatus(pendingID, "pending"); err != nil {
		t.Fatalf("set pending: %v", err)
	}
	disabledID := "agent-disabled-1"
	if _, _, err := srv.hub.Store().CreateAgentApprovedWithToken(disabledID, "已禁用", "192.168.1.0/24"); err != nil {
		t.Fatalf("create disabled base: %v", err)
	}
	if err := srv.hub.Store().SetApprovalStatus(disabledID, "disabled"); err != nil {
		t.Fatalf("set disabled: %v", err)
	}

	cases := []struct {
		name       string
		body       string
		wantHTTP   int
		wantField  string
		wantCode   string
		wantMsgSub string
	}{
		{"缺 agent_id", `{"cidr":"192.168.1.0/24"}`, 400, "agent_id", "required", ""},
		{"缺 cidr", `{"agent_id":"agent-scan-1"}`, 400, "cidr", "required", ""},
		{"Agent 不存在", `{"agent_id":"no-such-agent","cidr":"192.168.1.0/24"}`, 404, "", "", "Agent 不存在"},
		{"公网网段", `{"agent_id":"agent-scan-1","cidr":"8.8.8.0/24"}`, 400, "cidr", "invalid_cidr", ""},
		{"掩码过小", `{"agent_id":"agent-scan-1","cidr":"192.168.1.0/16"}`, 400, "cidr", "invalid_cidr", ""},
		{"白名单外网段", `{"agent_id":"agent-scan-1","cidr":"10.1.0.0/24"}`, 400, "cidr", "cidr_not_allowed", ""},
		{"待审批 Agent", `{"agent_id":"agent-pending-1","cidr":"192.168.1.0/24"}`, 400, "agent_id", "not_authorized", ""},
		{"已禁用 Agent", `{"agent_id":"agent-disabled-1","cidr":"192.168.1.0/24"}`, 400, "agent_id", "agent_disabled", ""},
		{"离线 Agent", `{"agent_id":"agent-offline-1","cidr":"192.168.1.0/24"}`, 502, "", "", "Agent 已离线"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := doAPI(t, handler, cookie, http.MethodPost, "/api/v1/scan", c.body)
			if rec.Code != c.wantHTTP {
				t.Fatalf("期望 HTTP %d，实际 %d: %s", c.wantHTTP, rec.Code, rec.Body.String())
			}
			_, msg, data := decodeEnvelope(t, rec)
			if c.wantField != "" && fieldCodeOf(t, data, c.wantField) != c.wantCode {
				t.Errorf("期望 %s=%s，实际 body: %s", c.wantField, c.wantCode, rec.Body.String())
			}
			if c.wantMsgSub != "" && !strings.Contains(msg, c.wantMsgSub) {
				t.Errorf("期望消息含 %q，实际 %q", c.wantMsgSub, msg)
			}
		})
	}
}

// TestScanStoreProtocol 覆盖扫描任务内存注册表的核心协议：去重、
// 快照语义、先赢者 finish、最近 20 个淘汰。
func TestScanStoreProtocol(t *testing.T) {
	store := newScanTaskStore()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	task := &scanTask{
		ScanID: "scan-1", AgentID: "agent-1", CIDR: "192.168.1.0/24",
		Status: scanStatusRunning, Results: []discoveredDevice{},
		StartedAt: time.Now().UTC(), cancel: cancel,
	}
	store.put(task)

	// 去重：同一 MAC 两次只保留一条。
	d := discoveredDevice{IP: "192.168.1.29", MAC: "AA:BB:CC:DD:EE:01", Vendor: "Realtek"}
	store.addFound("scan-1", d)
	store.addFound("scan-1", discoveredDevice{IP: "192.168.1.30", MAC: "aa:bb:cc:dd:ee:01", Vendor: "Realtek"})
	snap, ok := store.snapshot("scan-1")
	if !ok {
		t.Fatal("任务应存在")
	}
	if len(snap.Results) != 0 {
		// 运行中快照不暴露结果；用 finish 后再验证去重。
		t.Errorf("运行中快照 results 应为空，实际 %d 条", len(snap.Results))
	}
	if snap.Progress.Total != 0 {
		t.Errorf("初始 total 应为 0，实际 %d", snap.Progress.Total)
	}
	store.setTotal("scan-1", 254)
	store.setProgress("scan-1", 10, 1, 0)
	snap, _ = store.snapshot("scan-1")
	if snap.Progress.Total != 254 || snap.Progress.Scanned != 10 || snap.Progress.Found != 1 {
		t.Errorf("进度异常: %+v", snap.Progress)
	}

	// 先赢者 finish：第一次成功，第二次返回 false。
	if !store.finish("scan-1", scanStatusCompleted, "") {
		t.Error("首次 finish 应成功")
	}
	if store.finish("scan-1", scanStatusCancelled, "x") {
		t.Error("已结束的任务 finish 应返回 false")
	}
	snap, _ = store.snapshot("scan-1")
	if snap.Status != scanStatusCompleted || len(snap.Results) != 1 {
		t.Errorf("完成后快照异常: status=%s results=%d", snap.Status, len(snap.Results))
	}
	if snap.Results[0].MAC != "AA:BB:CC:DD:EE:01" {
		t.Errorf("去重后应只剩第一条，实际 %+v", snap.Results)
	}
	if snap.FinishedAt == nil || snap.DurationMs < 0 {
		t.Errorf("完成时间/耗时异常: %+v", snap)
	}

	// runningFor：进行中的任务可查，已结束的查不到。
	if _, ok := store.runningFor("agent-1"); ok {
		t.Error("已结束的任务不应被 runningFor 命中")
	}
	task2 := &scanTask{
		ScanID: "scan-2", AgentID: "agent-1", CIDR: "192.168.1.0/24",
		Status: scanStatusRunning, Results: []discoveredDevice{},
		StartedAt: time.Now().UTC(),
	}
	store.put(task2)
	if id, ok := store.runningFor("agent-1"); !ok || id != "scan-2" {
		t.Errorf("runningFor 应返回 scan-2，实际 %q %v", id, ok)
	}

	// 淘汰：再放 25 个已结束任务，只保留最近 20 个。
	for i := 0; i < 25; i++ {
		id := "old-" + string(rune('a'+i)) + "-" + string(rune('0'+i%10))
		st := &scanTask{
			ScanID: id, AgentID: "agent-x", CIDR: "192.168.1.0/24",
			Status: scanStatusCompleted, Results: []discoveredDevice{},
			StartedAt: time.Now().UTC(),
		}
		store.put(st)
	}
	summaries := store.listSummaries()
	finished := 0
	for _, s := range summaries {
		if s.Status != scanStatusRunning {
			finished++
		}
	}
	if finished != 20 {
		t.Errorf("已结束任务应只保留最近 20 个，实际 %d 个（总数 %d）", finished, len(summaries))
	}
	// 运行中的 scan-2 永不淘汰。
	if _, ok := store.snapshot("scan-2"); !ok {
		t.Error("运行中的任务不应被淘汰")
	}
	// 列表按 started_at DESC：最新的在前。
	if len(summaries) < 2 || !summaries[0].StartedAt.After(summaries[len(summaries)-1].StartedAt) {
		t.Error("列表应按 started_at DESC 排列")
	}
}

func TestScanCancelHandlers(t *testing.T) {
	srv, handler, _ := setupM4Server(t)
	cookie := loginCookie(t, handler)

	// 不存在的任务：GET/DELETE 均为 404。
	rec := doAPI(t, handler, cookie, http.MethodGet, "/api/v1/scan/nope", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET 不存在的任务应 404，实际 %d", rec.Code)
	}
	rec = doAPI(t, handler, cookie, http.MethodDelete, "/api/v1/scan/nope", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("DELETE 不存在的任务应 404，实际 %d", rec.Code)
	}

	// 造一个进行中的任务（含一条已发现结果），取消后结果应保留。
	_, cancel := context.WithCancel(context.Background())
	task := &scanTask{
		ScanID: "scan-cancel-1", AgentID: "agent-1", CIDR: "192.168.1.0/24",
		Status: scanStatusRunning, Results: []discoveredDevice{},
		StartedAt: time.Now().UTC(), cancel: cancel,
	}
	srv.scanTasks.put(task)
	srv.scanTasks.addFound("scan-cancel-1", discoveredDevice{
		IP: "192.168.1.29", MAC: "AA:BB:CC:DD:EE:01", Vendor: "Realtek",
		AgentID: "agent-1", Subnet: "192.168.1.0/24", FirstSeenAt: time.Now().UTC(),
	})
	rec = doAPI(t, handler, cookie, http.MethodDelete, "/api/v1/scan/scan-cancel-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("取消进行中的任务应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	_, msg, data := decodeEnvelope(t, rec)
	if !strings.Contains(msg, "取消") {
		t.Errorf("取消响应消息异常: %q", msg)
	}
	var cancelled scanTask
	if err := json.Unmarshal(data, &cancelled); err != nil {
		t.Fatalf("解析取消响应失败: %v", err)
	}
	if cancelled.Status != scanStatusCancelled {
		t.Errorf("取消后状态应为 cancelled，实际 %s", cancelled.Status)
	}
	if len(cancelled.Results) != 1 {
		t.Errorf("取消后应保留已发现结果，实际 %d 条", len(cancelled.Results))
	}
	// 再次取消已结束的任务：400。
	rec = doAPI(t, handler, cookie, http.MethodDelete, "/api/v1/scan/scan-cancel-1", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("重复取消应 400，实际 %d", rec.Code)
	}
	// GET 确认状态。
	rec = doAPI(t, handler, cookie, http.MethodGet, "/api/v1/scan/scan-cancel-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET 已取消任务应 200，实际 %d", rec.Code)
	}
	_, _, data = decodeEnvelope(t, rec)
	var got scanTask
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("解析 GET 响应失败: %v", err)
	}
	if got.Status != scanStatusCancelled || len(got.Results) != 1 {
		t.Errorf("GET 已取消任务异常: status=%s results=%d", got.Status, len(got.Results))
	}

	// 列表接口：分页信封。
	rec = doAPI(t, handler, cookie, http.MethodGet, "/api/v1/scan?page=1&page_size=50", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /scan 应 200，实际 %d", rec.Code)
	}
	_, _, data = decodeEnvelope(t, rec)
	var list struct {
		Items []scanTask `json:"items"`
		Total int        `json:"total"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("解析列表失败: %v", err)
	}
	if list.Total != 1 || len(list.Items) != 1 {
		t.Errorf("列表应有 1 条任务，实际 total=%d len=%d", list.Total, len(list.Items))
	}
}

func TestTasksCRUD(t *testing.T) {
	srv, handler, _ := setupM4Server(t)
	cookie := loginCookie(t, handler)
	repo := srv.repo

	dev := &device.Device{Name: "PC-01", MAC: "AA:BB:CC:DD:EE:01", AgentID: "agent-1"}
	if err := repo.CreateDevice(dev); err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	emptyGroup, err := repo.CreateGroup("空组", "")
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	// 创建失败用例。
	badCases := []struct {
		name      string
		body      string
		wantField string
		wantCode  string
	}{
		{"Action 非 wake/sleep", `{"name":"x","cron_expr":"0 8 * * *","target_type":"device","target_id":"` + dev.ID + `","action":"check"}`, "action", "unsupported_action"},
		{"目标设备不存在", `{"name":"x","cron_expr":"0 8 * * *","target_type":"device","target_id":"nope"}`, "target_id", "not_found"},
		{"目标分组为空", `{"name":"x","cron_expr":"0 8 * * *","target_type":"group","target_id":"` + emptyGroup.ID + `"}`, "target_id", "empty_target"},
		{"Cron 非法", `{"name":"x","cron_expr":"xxx","target_type":"device","target_id":"` + dev.ID + `"}`, "cron_expr", "invalid_format"},
		{"名称为空", `{"cron_expr":"0 8 * * *","target_type":"device","target_id":"` + dev.ID + `"}`, "name", "required"},
		{"重试越界", `{"name":"x","cron_expr":"0 8 * * *","target_type":"device","target_id":"` + dev.ID + `","retry_count":9}`, "retry_count", "out_of_range"},
	}
	for _, c := range badCases {
		t.Run("创建失败:"+c.name, func(t *testing.T) {
			rec := doAPI(t, handler, cookie, http.MethodPost, "/api/v1/tasks", c.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("期望 400，实际 %d: %s", rec.Code, rec.Body.String())
			}
			_, _, data := decodeEnvelope(t, rec)
			if got := fieldCodeOf(t, data, c.wantField); got != c.wantCode {
				t.Errorf("期望 %s=%s，实际 body: %s", c.wantField, c.wantCode, rec.Body.String())
			}
		})
	}

	// 创建成功：retry_count=0（不重试）+ run_once。
	rec := doAPI(t, handler, cookie, http.MethodPost, "/api/v1/tasks",
		`{"name":"晨起唤醒","cron_expr":"0 8 * * *","timezone":"Asia/Shanghai","target_type":"device","target_id":"`+dev.ID+`","retry_count":0,"run_once":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("创建应 201，实际 %d: %s", rec.Code, rec.Body.String())
	}
	_, _, data := decodeEnvelope(t, rec)
	var created scheduler.ScheduleView
	if err := json.Unmarshal(data, &created); err != nil {
		t.Fatalf("解析创建响应失败: %v", err)
	}
	if created.RetryCount != 0 {
		t.Errorf("retry_count 应为 0，实际 %d", created.RetryCount)
	}
	if !created.RunOnce {
		t.Error("run_once 应为 true")
	}
	if created.TargetName == nil || *created.TargetName != "PC-01" {
		t.Errorf("target_name 应为 PC-01，实际 %+v", created.TargetName)
	}
	if created.NextRunAt == nil {
		t.Error("next_run_at 不应为空")
	}
	if !created.Enabled {
		t.Error("新任务默认应启用")
	}
	taskID := created.ID

	// 创建睡眠任务（v0.3.2 起开放 sleep）：应 201 且 action 为 sleep。
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/tasks",
		`{"name":"午夜关机","cron_expr":"0 0 * * *","timezone":"Asia/Shanghai","target_type":"device","target_id":"`+dev.ID+`","action":"sleep","retry_count":1}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("创建 sleep 任务应 201，实际 %d: %s", rec.Code, rec.Body.String())
	}
	_, _, data = decodeEnvelope(t, rec)
	var createdSleep scheduler.ScheduleView
	if err := json.Unmarshal(data, &createdSleep); err != nil {
		t.Fatalf("解析 sleep 创建响应失败: %v", err)
	}
	if createdSleep.Action != "sleep" {
		t.Errorf("sleep 任务 action 应为 sleep，实际 %q", createdSleep.Action)
	}
	sleepTaskID := createdSleep.ID

	// 列表：分页信封 + DTO。
	rec = doAPI(t, handler, cookie, http.MethodGet, "/api/v1/tasks?page=1&page_size=50", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("列表应 200，实际 %d", rec.Code)
	}
	_, _, data = decodeEnvelope(t, rec)
	var list struct {
		Items []scheduler.ScheduleView `json:"items"`
		Total int                      `json:"total"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("解析列表失败: %v", err)
	}
	if list.Total != 2 || len(list.Items) != 2 {
		t.Errorf("列表应有 2 条任务，实际 total=%d len=%d", list.Total, len(list.Items))
	}
	seen := map[string]bool{}
	for _, it := range list.Items {
		seen[it.ID] = true
	}
	if !seen[taskID] || !seen[sleepTaskID] {
		t.Errorf("列表应含 wake 与 sleep 两任务，实际 %+v", list.Items)
	}

	// 更新：改名 + retry_count 改 5。
	rec = doAPI(t, handler, cookie, http.MethodPut, "/api/v1/tasks/"+taskID,
		`{"name":"晨起唤醒2","retry_count":5}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("更新应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	_, _, data = decodeEnvelope(t, rec)
	var updated scheduler.ScheduleView
	if err := json.Unmarshal(data, &updated); err != nil {
		t.Fatalf("解析更新响应失败: %v", err)
	}
	if updated.Name != "晨起唤醒2" || updated.RetryCount != 5 {
		t.Errorf("更新未生效: %+v", updated)
	}
	if updated.CronExpr != "0 8 * * *" || !updated.RunOnce {
		t.Errorf("未传字段应保留原值: %+v", updated)
	}
	// 更新不存在的任务：404。
	rec = doAPI(t, handler, cookie, http.MethodPut, "/api/v1/tasks/nope", `{"name":"x"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("更新不存在的任务应 404，实际 %d", rec.Code)
	}

	// 暂停/恢复：重复操作报 400。
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/tasks/"+taskID+"/pause", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("暂停应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	_, _, data = decodeEnvelope(t, rec)
	var paused scheduler.ScheduleView
	_ = json.Unmarshal(data, &paused)
	if paused.Enabled {
		t.Error("暂停后 Enabled 应为 false")
	}
	if paused.NextRunAt != nil {
		t.Error("暂停后 next_run_at 应为空")
	}
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/tasks/"+taskID+"/pause", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("重复暂停应 400，实际 %d", rec.Code)
	}
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/tasks/"+taskID+"/resume", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("恢复应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/tasks/"+taskID+"/resume", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("重复恢复应 400，实际 %d", rec.Code)
	}
	// 暂停不存在的任务：404。
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/tasks/nope/pause", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("暂停不存在的任务应 404，实际 %d", rec.Code)
	}

	// 执行历史：空。
	rec = doAPI(t, handler, cookie, http.MethodGet, "/api/v1/tasks/"+taskID+"/runs", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("执行历史应 200，实际 %d", rec.Code)
	}
	_, _, data = decodeEnvelope(t, rec)
	var runs struct {
		Items []scheduler.RunRecord `json:"items"`
		Total int                   `json:"total"`
	}
	if err := json.Unmarshal(data, &runs); err != nil {
		t.Fatalf("解析执行历史失败: %v", err)
	}
	if runs.Total != 0 {
		t.Errorf("未执行过，总数应为 0，实际 %d", runs.Total)
	}
	// 不存在任务的执行历史：404。
	rec = doAPI(t, handler, cookie, http.MethodGet, "/api/v1/tasks/nope/runs", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("不存在任务的执行历史应 404，实际 %d", rec.Code)
	}

	// 删除：成功后再次删除 404。
	rec = doAPI(t, handler, cookie, http.MethodDelete, "/api/v1/tasks/"+taskID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("删除应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	rec = doAPI(t, handler, cookie, http.MethodDelete, "/api/v1/tasks/"+taskID, "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("重复删除应 404，实际 %d", rec.Code)
	}
}

// TestScanEndToEndWithFakeAgent 是扫描全链路端到端测试：真实
// fakeAgent（走 agent.ExecuteRawTask 真实执行扫描）+ 真实 HTTP。
// 目标网段 10.255.255.0/24 确定无主机，应完整跑完 254 台、
// found 0、failed 0。
func TestScanEndToEndWithFakeAgent(t *testing.T) {
	srv, handler, ts := setupM4Server(t)
	cookie := loginCookie(t, handler)
	token := createApprovedAgent(t, srv, "agent-e2e-1")
	if err := srv.hub.Store().AddAllowedSubnet("agent-e2e-1", "10.255.255.0/24"); err != nil {
		t.Fatalf("AddAllowedSubnet: %v", err)
	}
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/ws/agent"
	fa := connectFakeAgent(t, wsURL, token, "agent-e2e-1", nil)
	t.Cleanup(fa.close)
	// 等 Agent 上线。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if srv.hub.IsConnected("agent-e2e-1") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !srv.hub.IsConnected("agent-e2e-1") {
		t.Fatal("fakeAgent 未在 10s 内上线")
	}

	rec := doAPI(t, handler, cookie, http.MethodPost, "/api/v1/scan",
		`{"agent_id":"agent-e2e-1","cidr":"10.255.255.0/24"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("创建扫描应 202，实际 %d: %s", rec.Code, rec.Body.String())
	}
	_, _, data := decodeEnvelope(t, rec)
	var created struct {
		ScanID string `json:"scan_id"`
		CIDR   string `json:"cidr"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(data, &created); err != nil {
		t.Fatalf("解析创建响应失败: %v", err)
	}
	if created.CIDR != "10.255.255.0/24" {
		t.Errorf("CIDR 应规范化为 10.255.255.0/24，实际 %s", created.CIDR)
	}
	// 同一 Agent 再开扫描：409。
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/scan",
		`{"agent_id":"agent-e2e-1","cidr":"10.255.255.0/24"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("并发扫描应 409，实际 %d: %s", rec.Code, rec.Body.String())
	}

	// 轮询至完成（254 台 / 6 并发 × 250ms ≈ 11s，限 60s）。
	var final scanTask
	deadline = time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		rec = doAPI(t, handler, cookie, http.MethodGet, "/api/v1/scan/"+created.ScanID, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("轮询应 200，实际 %d", rec.Code)
		}
		_, _, data = decodeEnvelope(t, rec)
		if err := json.Unmarshal(data, &final); err != nil {
			t.Fatalf("解析轮询响应失败: %v", err)
		}
		if final.Status != scanStatusRunning {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if final.Status != scanStatusCompleted {
		t.Fatalf("扫描应在 60s 内完成，实际 status=%s progress=%+v", final.Status, final.Progress)
	}
	if final.Progress.Total != 254 || final.Progress.Scanned != 254 {
		t.Errorf("应扫完 254 台，实际 %+v", final.Progress)
	}
	if final.Progress.Found != 0 || final.Progress.Failed != 0 {
		t.Errorf("无主机网段应 found=0 failed=0，实际 %+v", final.Progress)
	}
	if len(final.Results) != 0 {
		t.Errorf("无主机网段结果应为空，实际 %+v", final.Results)
	}
	if final.FinishedAt == nil || final.DurationMs <= 0 {
		t.Errorf("完成时间/耗时异常: %+v", final)
	}
}

// TestScanCancelEndToEnd 是取消的端到端测试：扫描进行中取消，
// 状态为 cancelled 且进度保留。
func TestScanCancelEndToEnd(t *testing.T) {
	srv, handler, ts := setupM4Server(t)
	cookie := loginCookie(t, handler)
	token := createApprovedAgent(t, srv, "agent-e2e-2")
	if err := srv.hub.Store().AddAllowedSubnet("agent-e2e-2", "10.255.255.0/24"); err != nil {
		t.Fatalf("AddAllowedSubnet: %v", err)
	}
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/ws/agent"
	// fakeAgent 串行处理任务：每条探测延迟 50ms，254 台约需 13s，
	// 给取消留出可靠窗口（沙箱内 dial 快速失败，不加延迟扫描
	// 1~2s 就跑完，取消测不到）。
	slowReply := func(msg agent.Message) agent.ResultPayload {
		time.Sleep(50 * time.Millisecond)
		return agent.ExecuteRawTask(context.Background(), msg.Type, msg.Payload)
	}
	fa := connectFakeAgent(t, wsURL, token, "agent-e2e-2", slowReply)
	t.Cleanup(fa.close)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if srv.hub.IsConnected("agent-e2e-2") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !srv.hub.IsConnected("agent-e2e-2") {
		t.Fatal("fakeAgent 未在 10s 内上线")
	}

	rec := doAPI(t, handler, cookie, http.MethodPost, "/api/v1/scan",
		`{"agent_id":"agent-e2e-2","cidr":"10.255.255.0/24"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("创建扫描应 202，实际 %d: %s", rec.Code, rec.Body.String())
	}
	_, _, data := decodeEnvelope(t, rec)
	var created struct {
		ScanID string `json:"scan_id"`
	}
	_ = json.Unmarshal(data, &created)

	// 等进度开始推进（total 已设定且至少扫了几台），再取消。
	var cur scanTask
	deadline = time.Now().Add(15 * time.Second)
	started := false
	for time.Now().Before(deadline) {
		rec = doAPI(t, handler, cookie, http.MethodGet, "/api/v1/scan/"+created.ScanID, "")
		_, _, data = decodeEnvelope(t, rec)
		_ = json.Unmarshal(data, &cur)
		if cur.Status == scanStatusRunning && cur.Progress.Scanned > 0 {
			started = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !started {
		t.Fatalf("扫描未在 15s 内开始推进: status=%s progress=%+v", cur.Status, cur.Progress)
	}
	scannedBefore := cur.Progress.Scanned

	rec = doAPI(t, handler, cookie, http.MethodDelete, "/api/v1/scan/"+created.ScanID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("取消应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	// 取消后状态稳定为 cancelled，进度不低于取消前。
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rec = doAPI(t, handler, cookie, http.MethodGet, "/api/v1/scan/"+created.ScanID, "")
		_, _, data = decodeEnvelope(t, rec)
		_ = json.Unmarshal(data, &cur)
		if cur.Status == scanStatusCancelled {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if cur.Status != scanStatusCancelled {
		t.Fatalf("取消后状态应为 cancelled，实际 %s", cur.Status)
	}
	if cur.Progress.Scanned < scannedBefore {
		t.Errorf("取消后进度不应回退：取消前 %d，取消后 %d", scannedBefore, cur.Progress.Scanned)
	}
}
