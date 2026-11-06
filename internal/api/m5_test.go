package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wakehub/wakehub/internal/agent"
)

// TestLogsFilterAndPagination：GET /api/v1/logs 的筛选（action 多值、
// actor、target、时间范围、result）与分页信封（契约 §9.1）。
func TestLogsFilterAndPagination(t *testing.T) {
	srv, handler, _ := setupM3Server(t)
	cookie := loginCookie(t, handler)
	createApprovedAgent(t, srv, "agent-home-lan")
	// 一次成功写操作 + 一次失败登录，产生两类审计。
	createDeviceViaAPI(t, handler, cookie, `{"name":"LAN-29","mac":"AA:BB:CC:DD:EE:01","ip":"192.168.1.29","agent_id":"agent-home-lan"}`)
	rec := doLogin(t, handler, "admin", "wrong-password")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("错误密码应 401，got %d", rec.Code)
	}

	getLogs := func(path string) (int, []any) {
		t.Helper()
		rec := doAPI(t, handler, cookie, http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s 应 200，got %d: %s", path, rec.Code, rec.Body.String())
		}
		data := dataOf(t, rec)
		items, _ := data["items"].([]any)
		total, _ := data["total"].(float64)
		return int(total), items
	}

	// action 单值。
	if total, _ := getLogs("/api/v1/logs?action=device_create"); total != 1 {
		t.Fatalf("action=device_create 应为 1 条，实际 %d", total)
	}
	// action 多值。
	if total, _ := getLogs("/api/v1/logs?action=device_create,login_failed"); total != 2 {
		t.Fatalf("action 多值应为 2 条，实际 %d", total)
	}
	// result=failed 只含失败事件；result 字段派生正确。
	if total, items := getLogs("/api/v1/logs?result=failed"); total != 1 {
		t.Fatalf("result=failed 应为 1 条，实际 %d", total)
	} else if items[0].(map[string]any)["result"] != "failed" {
		t.Fatalf("失败事件的 result 应为 failed，实际 %v", items[0])
	}
	if total, _ := getLogs("/api/v1/logs?result=success"); total < 1 {
		t.Fatalf("result=success 应至少 1 条，实际 %d", total)
	}
	// actor 筛选。
	if total, _ := getLogs("/api/v1/logs?actor=admin"); total < 2 {
		t.Fatalf("actor=admin 应至少 2 条，实际 %d", total)
	}
	// 时间范围：未来 1 小时内应含全部；过去 1 小时前应为 0。
	now := time.Now().UTC()
	from := now.Add(-time.Hour).Format(time.RFC3339)
	to := now.Add(time.Hour).Format(time.RFC3339)
	if total, _ := getLogs("/api/v1/logs?from=" + from + "&to=" + to); total < 2 {
		t.Fatalf("时间范围内应至少 2 条，实际 %d", total)
	}
	if total, _ := getLogs("/api/v1/logs?to=" + now.Add(-time.Hour).Format(time.RFC3339)); total != 0 {
		t.Fatalf("过去时间范围应为 0 条，实际 %d", total)
	}
	// 分页：page_size=1 时 1 条，total 不变。
	if total, items := getLogs("/api/v1/logs?page=1&page_size=1"); total < 2 || len(items) != 1 {
		t.Fatalf("分页应 total>=2 且本页 1 条，实际 total=%d len=%d", total, len(items))
	}
	// 非法 from → 400。
	rec2 := doAPI(t, handler, cookie, http.MethodGet, "/api/v1/logs?from=not-a-time", "")
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("非法 from 应 400，got %d", rec2.Code)
	}
	// 非法 result → 400。
	rec3 := doAPI(t, handler, cookie, http.MethodGet, "/api/v1/logs?result=bogus", "")
	if rec3.Code != http.StatusBadRequest {
		t.Fatalf("非法 result 应 400，got %d", rec3.Code)
	}
}

// TestSessionRateLimit：通用 API 限流（契约 §1.5）——同一会话每分钟
// 超限返回 429 rate_limited。测试把阈值临时调低，避免真发 600 次请求。
func TestSessionRateLimit(t *testing.T) {
	srv, handler, _ := setupM3Server(t)
	srv.sessionRateLimit = 3
	cookie := loginCookie(t, handler)
	for i := 0; i < 3; i++ {
		rec := doAPI(t, handler, cookie, http.MethodGet, "/api/v1/devices", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("第 %d 次请求应 200，got %d", i+1, rec.Code)
		}
	}
	rec := doAPI(t, handler, cookie, http.MethodGet, "/api/v1/devices", "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("第 4 次请求应 429，got %d: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	data, _ := body["data"].(map[string]any)
	if data["code"] != "rate_limited" {
		t.Fatalf("超限响应的 code 应为 rate_limited，实际 %s", rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("超限响应应带 Retry-After 头")
	}
	// 另一会话（重新登录）不受影响：限流键是会话 jti，不是用户或 IP。
	cookie2 := loginCookie(t, handler)
	rec = doAPI(t, handler, cookie2, http.MethodGet, "/api/v1/devices", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("新会话的请求应 200，got %d", rec.Code)
	}
}

// TestDeviceCheckWritesAudit：状态检测写 device_check 审计（契约 §4.5/§1.6）。
func TestDeviceCheckWritesAudit(t *testing.T) {
	srv, handler, ts := setupM3Server(t)
	cookie := loginCookie(t, handler)
	token := createApprovedAgent(t, srv, "agent-home-lan")
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/ws/agent"
	// 自定义回包：检测直接报 online，不依赖真实 ping。
	fa := connectFakeAgent(t, wsURL, token, "agent-home-lan", func(msg agent.Message) agent.ResultPayload {
		if msg.Type == "check" {
			return agent.ResultPayload{Success: true, Data: json.RawMessage(
				`{"state":"online","method":"ping","checked_at":"2026-11-05T22:45:22Z","latency_ms":12,"error_reason":""}`)}
		}
		return agent.ResultPayload{Success: true}
	})
	defer fa.close()
	time.Sleep(200 * time.Millisecond)

	id := createDeviceViaAPI(t, handler, cookie, `{"name":"LAN-29","mac":"AA:BB:CC:DD:EE:01","ip":"192.168.1.29","agent_id":"agent-home-lan"}`)
	rec := doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices/"+id+"/check", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("检测应 200，got %d: %s", rec.Code, rec.Body.String())
	}
	rec = doAPI(t, handler, cookie, http.MethodGet, "/api/v1/logs?action=device_check", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("查日志应 200，got %d", rec.Code)
	}
	data := dataOf(t, rec)
	if total, _ := data["total"].(float64); total != 1 {
		t.Fatalf("应有 1 条 device_check 审计，实际 %v：%s", total, rec.Body.String())
	}
	items, _ := data["items"].([]any)
	entry, _ := items[0].(map[string]any)
	if entry["target_id"] != id || entry["actor"] != "admin" {
		t.Fatalf("审计行内容异常：%v", entry)
	}
}

// TestDashboardFieldsAndUpcomingTasks：Dashboard 新字段（契约 §10）与
// 即将执行任务的真实计算。
func TestDashboardFieldsAndUpcomingTasks(t *testing.T) {
	srv, handler, _ := setupM3Server(t)
	cookie := loginCookie(t, handler)

	rec := doAPI(t, handler, cookie, http.MethodGet, "/api/v1/dashboard", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("Dashboard 应 200，got %d", rec.Code)
	}
	data := dataOf(t, rec)
	for _, key := range []string{"pending_agents", "server_status", "db_status", "uptime_seconds", "version", "upcoming_tasks"} {
		if _, ok := data[key]; !ok {
			t.Fatalf("Dashboard 缺少字段 %s：%s", key, rec.Body.String())
		}
	}
	if data["server_status"] != "ok" || data["db_status"] != "ok" || data["version"] != "0.2.11" {
		t.Fatalf("Dashboard 字段值异常：%s", rec.Body.String())
	}
	if uptime, _ := data["uptime_seconds"].(float64); uptime < 0 {
		t.Fatalf("uptime_seconds 不应为负：%v", uptime)
	}
	if tasks, _ := data["upcoming_tasks"].([]any); len(tasks) != 0 {
		t.Fatalf("无任务时 upcoming_tasks 应为 []，实际 %v", tasks)
	}

	// 建设备 + 任务：upcoming_tasks 应有 1 条，含真实 next_run_at 与目标名。
	token := createApprovedAgent(t, srv, "agent-home-lan")
	_ = token
	id := createDeviceViaAPI(t, handler, cookie, `{"name":"LAN-29","mac":"AA:BB:CC:DD:EE:01","ip":"192.168.1.29","agent_id":"agent-home-lan"}`)
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/tasks",
		fmt.Sprintf(`{"name":"晨起唤醒","cron_expr":"0 8 * * *","timezone":"Asia/Shanghai","target_type":"device","target_id":"%s"}`, id))
	if rec.Code != http.StatusCreated {
		t.Fatalf("创建任务应 201，got %d: %s", rec.Code, rec.Body.String())
	}
	rec = doAPI(t, handler, cookie, http.MethodGet, "/api/v1/dashboard", "")
	data = dataOf(t, rec)
	tasks, _ := data["upcoming_tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("upcoming_tasks 应为 1 条，实际 %v", tasks)
	}
	task, _ := tasks[0].(map[string]any)
	if task["target_name"] != "LAN-29" {
		t.Fatalf("target_name 应为 LAN-29，实际 %v", task)
	}
	if task["next_run_at"] == nil || task["next_run_at"] == "" {
		t.Fatalf("next_run_at 不应为空：%v", task)
	}
	if _, err := time.Parse(time.RFC3339, task["next_run_at"].(string)); err != nil {
		t.Fatalf("next_run_at 应为 RFC 3339 时间：%v", task["next_run_at"])
	}
}
