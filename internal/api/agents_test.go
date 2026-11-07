package api

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/wakehub/wakehub/internal/config"
)

// TestAgentManagementAPI 覆盖契约 §3.4 的 Agent 管理接口全流程：
// 预签发 → 列表/详情 → 连接 → 重新签发 → 吊销 → 禁用/启用 → 白名单 →
// 删除，以及自助注册 pending → 审批（WS 推送 token）→ 驳回。
func TestAgentManagementAPI(t *testing.T) {
	// v0.3.0：回环自动批准只看 ClientIP。测试服务器本身在回环地址上，
	// 故把 127.0.0.1 配为可信代理、拨号时带 X-Forwarded-For: 192.168.1.65，
	// 让第 8 步的自助注册走非回环 pending 路径（真实远端 Agent 的情形）；
	// per-agent token 路径不看 IP，不受影响。
	_, loopbackNet, _ := net.ParseCIDR("127.0.0.1/32")
	handler, _ := setupTestServerWithConfig(t, config.ServerConfig{
		ListenAddr:       ":8080",
		SecretKey:        testSecret,
		EnrollToken:      testEnrollToken,
		WSAllowedOrigins: []string{"https://allowed.example.com"},
	}, []*net.IPNet{loopbackNet})
	cookie := loginCookie(t, handler)
	ts := httptest.NewServer(handler)
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/ws/agent"

	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	mustStatus := func(rec *httptest.ResponseRecorder, want int, what string) map[string]any {
		t.Helper()
		if rec.Code != want {
			t.Fatalf("%s 应返回 %d，got %d: %s", what, want, rec.Code, rec.Body.String())
		}
		return decodeBody(t, rec)
	}
	dialWS := func(token, agentID string) (*websocket.Conn, *http.Response, error) {
		h := http.Header{}
		h.Set("X-Agent-Token", token)
		h.Set("X-Agent-ID", agentID)
		// 见本测试开头的注释：让 WS 连接的 ClientIP 为非回环，
		// 自助注册走 pending 路径。
		h.Set("X-Forwarded-For", "192.168.1.65")
		return websocket.DefaultDialer.Dial(wsURL, h)
	}
	dialMustFail := func(token, agentID string, wantStatus int, what string) {
		t.Helper()
		conn, resp, err := dialWS(token, agentID)
		if err == nil {
			_ = conn.Close()
			t.Fatalf("%s：握手应失败", what)
		}
		if resp == nil || resp.StatusCode != wantStatus {
			t.Fatalf("%s：应返回 HTTP %d，实际 resp=%v err=%v", what, wantStatus, resp, err)
		}
	}
	dialMustSucceed := func(token, agentID string, what string) *websocket.Conn {
		t.Helper()
		conn, resp, err := dialWS(token, agentID)
		if err != nil {
			status := 0
			if resp != nil {
				status = resp.StatusCode
			}
			t.Fatalf("%s：握手应成功（101），got HTTP %d err=%v", what, status, err)
		}
		return conn
	}
	// waitForAgentStatus 轮询详情接口，直到合并状态变为 want（5s 超时）。
	waitForAgentStatus := func(t *testing.T, handler http.Handler, cookie *http.Cookie, id, want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/"+id, nil)
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code == http.StatusOK {
				if body := decodeBody(t, rec); body["data"].(map[string]any)["status"] == want {
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("Agent %s 状态应在 5s 内变为 %q", id, want)
	}

	// 1) 预签发：创建即 approved，一次性返回 token。
	rec := call(http.MethodPost, "/api/v1/agents", `{"agent_id":"agent-mgmt-a","name":"A","subnet":"192.168.1.0/24"}`)
	body := mustStatus(rec, http.StatusCreated, "预签发创建")
	data := body["data"].(map[string]any)
	tokenA, _ := data["token"].(string)
	if !strings.HasPrefix(tokenA, "wha_") {
		t.Fatalf("预签发应返回 wha_ token，实际 %q", tokenA)
	}
	agentA := data["agent"].(map[string]any)
	if agentA["status"] != "approved" {
		t.Fatalf("预签发应直接 approved，实际 %v", agentA["status"])
	}
	if subs, _ := agentA["allowed_subnets"].([]any); len(subs) != 1 || subs[0] != "192.168.1.0/24" {
		t.Fatalf("注册网段应自动进白名单：%v", subs)
	}

	// 1b) 非法 ID 与重复 ID。
	mustStatus(call(http.MethodPost, "/api/v1/agents", `{"agent_id":"local","name":"L"}`), http.StatusBadRequest, "local 保留 ID 应被拒")
	mustStatus(call(http.MethodPost, "/api/v1/agents", `{"agent_id":"agent-mgmt-a","name":"A2"}`), http.StatusConflict, "重复 ID 应 409")

	// 2) 列表与详情。
	body = mustStatus(call(http.MethodGet, "/api/v1/agents", ""), http.StatusOK, "列表")
	items := body["data"].([]any)
	if len(items) != 1 {
		t.Fatalf("列表应有 1 个 Agent（种子 local 已删），实际 %d", len(items))
	}
	mustStatus(call(http.MethodGet, "/api/v1/agents/agent-nope", ""), http.StatusNotFound, "不存在的 Agent 应 404")

	// 3) 持 token 连接 → connected（握手返回 101 后 RegisterConn 才
	// 注册会话，轮询等待状态翻转为 connected，避免时序抖动）。
	connA := dialMustSucceed(tokenA, "agent-mgmt-a", "持预签发 token 连接")
	defer connA.Close()
	waitForAgentStatus(t, handler, cookie, "agent-mgmt-a", "connected")

	// 4) 重新签发：旧 token 立即失效，新 token 可用。
	body = mustStatus(call(http.MethodPost, "/api/v1/agents/agent-mgmt-a/reissue-token", ""), http.StatusCreated, "重新签发")
	tokenA2, _ := body["data"].(map[string]any)["token"].(string)
	if tokenA2 == tokenA || !strings.HasPrefix(tokenA2, "wha_") {
		t.Fatalf("重新签发应生成新 token，实际 %q", tokenA2)
	}
	dialMustFail(tokenA, "agent-mgmt-a", http.StatusUnauthorized, "旧 token 轮换后应失效")
	connA2 := dialMustSucceed(tokenA2, "agent-mgmt-a", "持新 token 连接")
	defer connA2.Close()

	// 5) 吊销：token 失效，会话断开。会话清理是异步的（吊销后状态仍
	// connected 是时序抖动），轮询等待状态回落，避免偶发失败。
	mustStatus(call(http.MethodPost, "/api/v1/agents/agent-mgmt-a/revoke-token", ""), http.StatusOK, "吊销 token")
	dialMustFail(tokenA2, "agent-mgmt-a", http.StatusForbidden, "吊销后连接应 403")
	waitForAgentStatus(t, handler, cookie, "agent-mgmt-a", "approved")

	// 6) 禁用 / 启用。
	mustStatus(call(http.MethodPost, "/api/v1/agents/agent-mgmt-a/disable", ""), http.StatusOK, "禁用")
	body = mustStatus(call(http.MethodGet, "/api/v1/agents/agent-mgmt-a", ""), http.StatusOK, "禁用后详情")
	if body["data"].(map[string]any)["status"] != "disabled" {
		t.Fatalf("禁用后状态应为 disabled，实际 %v", body["data"])
	}
	mustStatus(call(http.MethodPost, "/api/v1/agents/agent-mgmt-a/disable", ""), http.StatusBadRequest, "重复禁用应 400")
	mustStatus(call(http.MethodPost, "/api/v1/agents/agent-mgmt-a/enable", ""), http.StatusOK, "启用")
	body = mustStatus(call(http.MethodGet, "/api/v1/agents/agent-mgmt-a", ""), http.StatusOK, "启用后详情")
	if body["data"].(map[string]any)["status"] != "approved" {
		t.Fatalf("启用后状态应回 approved，实际 %v", body["data"])
	}

	// 7) 白名单管理。
	body = mustStatus(call(http.MethodPost, "/api/v1/agents/agent-mgmt-a/subnets", `{"cidr":"10.0.1.0/24"}`), http.StatusOK, "加白名单")
	if subs, _ := body["data"].(map[string]any)["allowed_subnets"].([]any); len(subs) != 2 {
		t.Fatalf("加白名单后应有 2 条，实际 %v", subs)
	}
	mustStatus(call(http.MethodPost, "/api/v1/agents/agent-mgmt-a/subnets", `{"cidr":"10.0.1.0/24"}`), http.StatusConflict, "重复加白名单应 409")
	mustStatus(call(http.MethodPost, "/api/v1/agents/agent-mgmt-a/subnets", `{"cidr":"8.8.8.0/24"}`), http.StatusBadRequest, "公网网段应被拒")
	mustStatus(call(http.MethodPost, "/api/v1/agents/agent-mgmt-a/subnets", `{"cidr":"192.168.1.0/16"}`), http.StatusBadRequest, "掩码小于 /24 应被拒")
	body = mustStatus(call(http.MethodDelete, "/api/v1/agents/agent-mgmt-a/subnets", `{"cidr":"10.0.1.0/24"}`), http.StatusOK, "删白名单")
	if subs, _ := body["data"].(map[string]any)["allowed_subnets"].([]any); len(subs) != 1 {
		t.Fatalf("删白名单后应剩 1 条，实际 %v", subs)
	}
	mustStatus(call(http.MethodDelete, "/api/v1/agents/agent-mgmt-a/subnets", `{"cidr":"10.0.1.0/24"}`), http.StatusNotFound, "删不存在的网段应 404")

	// 8) 自助注册 pending → 审批（WS 推送 token）。
	pendConn := dialMustSucceed(testEnrollToken, "agent-pend-1", "自助注册连接")
	defer pendConn.Close()
	_ = pendConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var first map[string]any
	if err := pendConn.ReadJSON(&first); err != nil {
		t.Fatalf("pending 模式应先收到 registration_pending：%v", err)
	}
	if first["type"] != "registration_pending" {
		t.Fatalf("首条消息应为 registration_pending，实际 %v", first["type"])
	}
	body = mustStatus(call(http.MethodGet, "/api/v1/agents/agent-pend-1", ""), http.StatusOK, "pending 详情")
	if body["data"].(map[string]any)["status"] != "pending" {
		t.Fatalf("自助注册后状态应为 pending，实际 %v", body["data"])
	}
	body = mustStatus(call(http.MethodPost, "/api/v1/agents/agent-pend-1/approve", ""), http.StatusCreated, "审批通过")
	approvedToken, _ := body["data"].(map[string]any)["token"].(string)
	if !strings.HasPrefix(approvedToken, "wha_") {
		t.Fatalf("审批应签发 token，实际 %q", approvedToken)
	}
	if body["data"].(map[string]any)["delivered_via"] != "websocket+http" {
		t.Fatalf("在线 pending Agent 审批应经 WS+HTTP 下发，实际 %v", body["data"].(map[string]any)["delivered_via"])
	}
	// WS 侧应收到同样的 token。
	_ = pendConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var pushed map[string]any
	if err := pendConn.ReadJSON(&pushed); err != nil {
		t.Fatalf("审批后应收到 registration_approved 推送：%v", err)
	}
	if pushed["type"] != "registration_approved" {
		t.Fatalf("推送类型应为 registration_approved，实际 %v", pushed["type"])
	}
	pushedPayload, _ := pushed["payload"].(map[string]any)
	if pushedPayload["token"] != approvedToken {
		t.Fatalf("WS 推送的 token 应与 HTTP 返回一致")
	}
	// 持审批签发的 token 可连接。
	approvedConn := dialMustSucceed(approvedToken, "agent-pend-1", "持审批 token 连接")
	defer approvedConn.Close()

	// 9) 驳回：删行。
	rejectConn := dialMustSucceed(testEnrollToken, "agent-pend-2", "自助注册待驳回")
	defer rejectConn.Close()
	mustStatus(call(http.MethodPost, "/api/v1/agents/agent-pend-2/reject", ""), http.StatusOK, "驳回")
	mustStatus(call(http.MethodGet, "/api/v1/agents/agent-pend-2", ""), http.StatusNotFound, "驳回后应查无此 Agent")

	// 10) 删除。
	mustStatus(call(http.MethodDelete, "/api/v1/agents/agent-mgmt-a", ""), http.StatusOK, "删除 Agent")
	mustStatus(call(http.MethodGet, "/api/v1/agents/agent-mgmt-a", ""), http.StatusNotFound, "删除后应 404")
	mustStatus(call(http.MethodDelete, "/api/v1/agents/agent-mgmt-a", ""), http.StatusNotFound, "重复删除应 404")
}
