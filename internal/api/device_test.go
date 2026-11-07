package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/wakehub/wakehub/internal/agent"
	"github.com/wakehub/wakehub/internal/audit"
	"github.com/wakehub/wakehub/internal/auth"
	"github.com/wakehub/wakehub/internal/config"
	"github.com/wakehub/wakehub/internal/device"
	"github.com/wakehub/wakehub/internal/scheduler"
	_ "modernc.org/sqlite"
)

// setupM3Server 建一个带真实 Server 的测试服务端（与 setupTestServer
// 同构，但同时返回 *Server，M3 测试需直接操作 hub 与 scanTasks）。
func setupM3Server(t *testing.T) (*Server, http.Handler, *httptest.Server) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	for _, f := range []string{"../../migrations/001_init.sql", "../../migrations/002_auth.sql", "../../migrations/003_agent.sql", "../../migrations/004_runs.sql", "../../migrations/005_indexes.sql", "../../migrations/006_ssh_sleep.sql", "../../migrations/007_ssh_windows.sql", "../../migrations/008_network_identity.sql", "../../migrations/009_rename_seeded_networks.sql", "../../migrations/010_ssh_use_sudo.sql"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := db.Exec(string(b)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	t.Cleanup(func() { db.Close() })

	cfg := config.ServerConfig{
		ListenAddr:       ":8080",
		SecretKey:        testSecret,
		EnrollToken:      testEnrollToken,
		WSAllowedOrigins: []string{"https://allowed.example.com"},
		// 与 config 默认一致（10s）：dispatchWakeOnce/handleCheckDevice
		// 用 TaskTimeout 做派发超时；零值会让 ctx 立即超时。
		TaskTimeout: 10 * time.Second,
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

// createApprovedAgent 在库里建一个已获批 Agent，返回其 per-agent token。
func createApprovedAgent(t *testing.T, srv *Server, id string) string {
	t.Helper()
	_, token, err := srv.hub.Store().CreateAgentApprovedWithToken(id, "测试 Agent", "192.168.1.0/24")
	if err != nil {
		t.Fatalf("创建获批 Agent 失败：%v", err)
	}
	return token
}

// fakeAgent 是测试用 Agent 端：连真实 WS，按 agent.ExecuteRawTask
// 真实执行任务并回包；reply 为空时走真实执行，否则用自定义回包
// （用于模拟 Agent 返回畸形结果）。
type fakeAgent struct {
	t       *testing.T
	conn    *websocket.Conn
	writeMu sync.Mutex
	reply   func(agent.Message) agent.ResultPayload
	done    chan struct{}
}

func connectFakeAgent(t *testing.T, wsURL, token, agentID string, reply func(agent.Message) agent.ResultPayload) *fakeAgent {
	t.Helper()
	header := http.Header{}
	header.Set("X-Agent-Token", token)
	header.Set("X-Agent-ID", agentID)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("dial agent %s: %v", agentID, err)
	}
	fa := &fakeAgent{t: t, conn: conn, reply: reply, done: make(chan struct{})}
	// 心跳保活（hub 45s 无消息即判僵死）。
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-fa.done:
				return
			case <-ticker.C:
				_ = fa.writeJSON(agent.Message{Type: "heartbeat"})
			}
		}
	}()
	go fa.run()
	return fa
}

func (fa *fakeAgent) writeJSON(v any) error {
	fa.writeMu.Lock()
	defer fa.writeMu.Unlock()
	_ = fa.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return fa.conn.WriteJSON(v)
}

func (fa *fakeAgent) run() {
	defer close(fa.done)
	for {
		var msg agent.Message
		if err := fa.conn.ReadJSON(&msg); err != nil {
			return
		}
		switch msg.Type {
		case "wake", "check", "scan", "sleep":
			var res agent.ResultPayload
			if fa.reply != nil {
				res = fa.reply(msg)
			} else {
				res = agent.ExecuteRawTask(context.Background(), msg.Type, msg.Payload)
			}
			raw, _ := json.Marshal(res)
			_ = fa.writeJSON(agent.Message{ID: msg.ID, Type: "result", Payload: raw})
		}
	}
}

func (fa *fakeAgent) close() {
	_ = fa.conn.Close()
}

// doAPI 发一个带登录 Cookie 的请求。
func doAPI(t *testing.T, handler http.Handler, cookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// dataOf 取响应信封的 data 字段。
func dataOf(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	body := decodeBody(t, rec)
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("响应应含 data 对象，got %s", rec.Body.String())
	}
	return data
}

func errorsOf(t *testing.T, rec *httptest.ResponseRecorder) []any {
	t.Helper()
	// 结构化校验错误放在信封的 data.errors 里（writeValidationError）。
	data := dataOf(t, rec)
	errs, _ := data["errors"].([]any)
	return errs
}

func createDeviceViaAPI(t *testing.T, handler http.Handler, cookie *http.Cookie, body string) string {
	t.Helper()
	rec := doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("创建设备应 201，got %d: %s", rec.Code, rec.Body.String())
	}
	data := dataOf(t, rec)
	id, _ := data["id"].(string)
	if id == "" {
		t.Fatalf("创建响应应含设备 id：%s", rec.Body.String())
	}
	return id
}

func TestCreateDeviceValidation(t *testing.T) {
	_, handler, _ := setupM3Server(t)
	cookie := loginCookie(t, handler)

	// agent_id 必填：不再静默回落 local。
	rec := doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices",
		`{"name":"LAN-29","mac":"AA:BB:CC:DD:EE:01","ip":"192.168.1.29","device_type":"pc","status_method":"ping"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 agent_id 应 400，got %d: %s", rec.Code, rec.Body.String())
	}
	found := false
	for _, e := range errorsOf(t, rec) {
		m := e.(map[string]any)
		if m["field"] == "agent_id" && m["code"] == "required" {
			found = true
		}
	}
	if !found {
		t.Fatalf("应有 agent_id/required 错误，got %s", rec.Body.String())
	}

	// MAC 非法 → 结构化错误。
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices",
		`{"name":"X","mac":"not-a-mac","ip":"192.168.1.29","agent_id":"agent-home-lan","device_type":"pc","status_method":"ping"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 MAC 应 400，got %d: %s", rec.Code, rec.Body.String())
	}

	// Agent 不存在 → 400 not_found。
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices",
		`{"name":"X","mac":"AA:BB:CC:DD:EE:01","ip":"192.168.1.29","agent_id":"agent-missing","device_type":"pc","status_method":"ping"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("不存在的 Agent 应 400，got %d: %s", rec.Code, rec.Body.String())
	}

	// WOL 端口越界 → 400 out_of_range。
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices",
		`{"name":"X","mac":"AA:BB:CC:DD:EE:01","ip":"192.168.1.29","agent_id":"agent-home-lan","wol_port":70000,"device_type":"pc","status_method":"ping"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("越界端口应 400，got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateDeviceDuplicateMACAndOfflineWarning(t *testing.T) {
	srv, handler, _ := setupM3Server(t)
	cookie := loginCookie(t, handler)
	_ = createApprovedAgent(t, srv, "agent-home-lan")

	body := `{"name":"LAN-29","mac":"AA:BB:CC:DD:EE:01","ip":"192.168.1.29","agent_id":"agent-home-lan","device_type":"pc","status_method":"ping"}`
	// Agent 离线：创建成功，但带 agent_offline 警告。
	rec := doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("创建应 201，got %d: %s", rec.Code, rec.Body.String())
	}
	warns, _ := decodeBody(t, rec)["warnings"].([]any)
	if len(warns) != 1 {
		t.Fatalf("离线 Agent 创建应带 1 条警告，got %s", rec.Body.String())
	}
	// 重复 MAC → 409。
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices",
		`{"name":"LAN-30","mac":"AA:BB:CC:DD:EE:01","ip":"192.168.1.30","agent_id":"agent-home-lan","device_type":"pc","status_method":"ping"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("重复 MAC 应 409，got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUpdateDevicePartialAndNotFound(t *testing.T) {
	srv, handler, _ := setupM3Server(t)
	cookie := loginCookie(t, handler)
	_ = createApprovedAgent(t, srv, "agent-home-lan")
	id := createDeviceViaAPI(t, handler, cookie,
		`{"name":"LAN-29","mac":"AA:BB:CC:DD:EE:01","ip":"192.168.1.29","agent_id":"agent-home-lan","device_type":"pc","status_method":"ping"}`)

	// 只改名称：未传字段保持原值，返回的是重查行。
	rec := doAPI(t, handler, cookie, http.MethodPut, "/api/v1/devices/"+id, `{"name":"RENAMED"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("更新应 200，got %d: %s", rec.Code, rec.Body.String())
	}
	data := dataOf(t, rec)
	if data["name"] != "RENAMED" {
		t.Fatalf("名称应更新为 RENAMED，got %v", data["name"])
	}
	if data["ip"] != "192.168.1.29" || data["mac"] != "AA:BB:CC:DD:EE:01" {
		t.Fatalf("未传字段应保持原值，got %s", rec.Body.String())
	}
	// 显式空字符串表示清空备注。
	rec = doAPI(t, handler, cookie, http.MethodPut, "/api/v1/devices/"+id, `{"remark":""}`)
	if rec.Code != http.StatusOK || dataOf(t, rec)["remark"] != "" {
		t.Fatalf("显式空备注应清空，got %d: %s", rec.Code, rec.Body.String())
	}
	// 更新不存在的设备 → 404（B-N5）。
	rec = doAPI(t, handler, cookie, http.MethodPut, "/api/v1/devices/no-such-id", `{"name":"X"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("更新不存在设备应 404，got %d: %s", rec.Code, rec.Body.String())
	}
	// 更新为非法 MAC → 400。
	rec = doAPI(t, handler, cookie, http.MethodPut, "/api/v1/devices/"+id, `{"mac":"bad"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 MAC 更新应 400，got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteDeviceNotFound(t *testing.T) {
	_, handler, _ := setupM3Server(t)
	cookie := loginCookie(t, handler)
	rec := doAPI(t, handler, cookie, http.MethodDelete, "/api/v1/devices/no-such-id", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("删除不存在设备应 404，got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestListDevicesPagination(t *testing.T) {
	srv, handler, _ := setupM3Server(t)
	cookie := loginCookie(t, handler)
	_ = createApprovedAgent(t, srv, "agent-home-lan")
	for i, ip := range []string{"192.168.1.11", "192.168.1.12", "192.168.1.13"} {
		createDeviceViaAPI(t, handler, cookie, fmt.Sprintf(
			`{"name":"DEV-%s","mac":"00:11:22:33:44:0%d","ip":"%s","agent_id":"agent-home-lan","device_type":"pc","status_method":"ping"}`,
			ip, i+1, ip))
	}
	rec := doAPI(t, handler, cookie, http.MethodGet, "/api/v1/devices?page=1&page_size=2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("列表应 200，got %d: %s", rec.Code, rec.Body.String())
	}
	data := dataOf(t, rec)
	if data["total"] != float64(3) || data["page"] != float64(1) || data["page_size"] != float64(2) {
		t.Fatalf("分页信封错误，got %s", rec.Body.String())
	}
	items, _ := data["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("第一页应 2 条，got %d", len(items))
	}
	// 按 Agent 过滤不存在 → 空列表 []（永不 null）。
	rec = doAPI(t, handler, cookie, http.MethodGet, "/api/v1/devices?agent_id=agent-missing", "")
	data = dataOf(t, rec)
	if data["total"] != float64(0) {
		t.Fatalf("不存在的 Agent 应 total=0，got %s", rec.Body.String())
	}
	if items, _ := data["items"].([]any); items == nil {
		t.Fatalf("空列表应返回 [] 而非 null：%s", rec.Body.String())
	}
}

func TestWakeDeviceOfflineAndDisabled(t *testing.T) {
	srv, handler, _ := setupM3Server(t)
	cookie := loginCookie(t, handler)
	_ = createApprovedAgent(t, srv, "agent-home-lan")
	id := createDeviceViaAPI(t, handler, cookie,
		`{"name":"LAN-29","mac":"AA:BB:CC:DD:EE:01","ip":"192.168.1.29","agent_id":"agent-home-lan","device_type":"pc","status_method":"ping"}`)

	// Agent 离线 → 502 agent_offline。
	rec := doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices/"+id+"/wake", `{}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("离线 Agent 唤醒应 502，got %d: %s", rec.Code, rec.Body.String())
	}
	// 不存在的设备 → 404。
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices/no-such-id/wake", `{}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在设备唤醒应 404，got %d: %s", rec.Code, rec.Body.String())
	}
	// 禁用设备 → 400 device_disabled。
	rec = doAPI(t, handler, cookie, http.MethodPut, "/api/v1/devices/"+id, `{"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("禁用设备应 200，got %d: %s", rec.Code, rec.Body.String())
	}
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices/"+id+"/wake", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("禁用设备唤醒应 400，got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestWakeDeviceSuccessAndVerify(t *testing.T) {
	srv, handler, ts := setupM3Server(t)
	cookie := loginCookie(t, handler)
	token := createApprovedAgent(t, srv, "agent-home-lan")
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/ws/agent"
	fa := connectFakeAgent(t, wsURL, token, "agent-home-lan", nil)
	defer fa.close()
	time.Sleep(200 * time.Millisecond) // 等连接在 hub 注册。

	// TCP 检测端口：本机监听，verify 回检可判 online。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	id := createDeviceViaAPI(t, handler, cookie, fmt.Sprintf(
		`{"name":"LAN-29","mac":"AA:BB:CC:DD:EE:01","ip":"127.0.0.1","agent_id":"agent-home-lan","device_type":"pc","status_method":"tcp","status_port":%d}`,
		port))

	// verify=false：只报 sent，不虚报启动成功。
	rec := doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices/"+id+"/wake", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("唤醒应 200，got %d: %s", rec.Code, rec.Body.String())
	}
	data := dataOf(t, rec)
	if data["send_status"] != "sent" || data["boot_status"] != "pending" {
		t.Fatalf("verify=false 应 sent/pending，got %s", rec.Body.String())
	}
	if data["boot_checked_at"] != nil {
		t.Fatalf("未回检时 boot_checked_at 应为 null，got %s", rec.Body.String())
	}

	// verify=true：回检 TCP 端口开放 → online。
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices/"+id+"/wake",
		`{"verify":true,"verify_timeout_seconds":6}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("带回检唤醒应 200，got %d: %s", rec.Code, rec.Body.String())
	}
	data = dataOf(t, rec)
	if data["send_status"] != "sent" || data["boot_status"] != "online" {
		t.Fatalf("回检应 sent/online，got %s", rec.Body.String())
	}
	if data["boot_checked_at"] == nil {
		t.Fatalf("回检后应有 boot_checked_at，got %s", rec.Body.String())
	}
}

func TestBatchWakeStructure(t *testing.T) {
	srv, handler, ts := setupM3Server(t)
	cookie := loginCookie(t, handler)
	token := createApprovedAgent(t, srv, "agent-home-lan")
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/ws/agent"
	fa := connectFakeAgent(t, wsURL, token, "agent-home-lan", nil)
	defer fa.close()
	time.Sleep(200 * time.Millisecond)

	onlineID := createDeviceViaAPI(t, handler, cookie,
		`{"name":"ONLINE","mac":"00:11:22:33:44:01","ip":"192.168.1.11","agent_id":"agent-home-lan","device_type":"pc","status_method":"ping"}`)
	disabledID := createDeviceViaAPI(t, handler, cookie,
		`{"name":"DISABLED","mac":"00:11:22:33:44:02","ip":"192.168.1.12","agent_id":"agent-home-lan","device_type":"pc","status_method":"ping"}`)
	rec := doAPI(t, handler, cookie, http.MethodPut, "/api/v1/devices/"+disabledID, `{"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("禁用设备应 200，got %d: %s", rec.Code, rec.Body.String())
	}
	// 离线 Agent 的设备：直接入库（跳过 API 校验）。
	_, _, err := srv.hub.Store().CreateAgentApprovedWithToken("agent-offline", "离线 Agent", "192.168.1.0/24")
	if err != nil {
		t.Fatalf("创建离线 Agent 失败：%v", err)
	}
	if err := srv.repo.CreateDevice(&device.Device{
		Name: "OFFLINE", MAC: "00:11:22:33:44:03", IP: "192.168.1.13",
		AgentID: "agent-offline", DeviceType: "pc", StatusMethod: "ping",
	}); err != nil {
		t.Fatalf("直接入库离线设备失败：%v", err)
	}

	// 批量：在线 1 台 + 禁用 1 台 + 不存在 1 台 + 重复 1 次 + 离线 Agent 1 台。
	// 离线设备需用其真实 ID；用 MAC 查库。
	offDev, err := srv.repo.GetDeviceByMAC("00:11:22:33:44:03")
	if err != nil {
		t.Fatalf("查离线设备失败：%v", err)
	}
	body := fmt.Sprintf(`{"device_ids":["%s","%s","no-such-id","%s","%s"]}`,
		onlineID, disabledID, onlineID, offDev.ID)

	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices/batch-wake", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("批量唤醒应 200，got %d: %s", rec.Code, rec.Body.String())
	}
	data := dataOf(t, rec)
	if data["total"] != float64(5) || data["success"] != float64(1) ||
		data["failed"] != float64(1) || data["skipped"] != float64(3) {
		t.Fatalf("计数应 total=5 success=1 failed=1 skipped=3，got %s", rec.Body.String())
	}
	if _, ok := data["batch_id"].(string); !ok || data["batch_id"] == "" {
		t.Fatalf("应含 batch_id，got %s", rec.Body.String())
	}
	if _, ok := data["duration_ms"].(float64); !ok {
		t.Fatalf("应含 duration_ms，got %s", rec.Body.String())
	}
	results, _ := data["results"].([]any)
	if len(results) != 5 {
		t.Fatalf("应返回 5 条 per-device 结果，got %d", len(results))
	}
	// 按提交顺序断言（同一设备出现两次：首次 sent、重复项 skipped，
	// 故不用 device_id 做 map key 去重）。
	want := []struct{ status, reason string }{
		{"sent", ""},                    // 在线设备
		{"skipped", "device_disabled"},  // 禁用设备
		{"skipped", "device_not_found"}, // 不存在
		{"skipped", "duplicate"},        // 重复 ID
		{"failed", "agent_offline"},     // 离线 Agent
	}
	for i, w := range want {
		m := results[i].(map[string]any)
		if m["status"] != w.status || m["reason"] != w.reason {
			t.Fatalf("第 %d 条结果应 %s/%s，got %v", i, w.status, w.reason, m)
		}
	}
	sent := results[0].(map[string]any)
	if sent["device_id"] != onlineID || sent["boot_status"] != "pending" {
		t.Fatalf("在线设备应 sent/pending，got %v", sent)
	}
	// 空 device_ids → 400。
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices/batch-wake", `{"device_ids":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空批量应 400，got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSleepMalformedJSON(t *testing.T) {
	srv, handler, _ := setupM3Server(t)
	cookie := loginCookie(t, handler)
	_ = createApprovedAgent(t, srv, "agent-home-lan")
	id := createDeviceViaAPI(t, handler, cookie,
		`{"name":"LAN-29","mac":"AA:BB:CC:DD:EE:01","ip":"192.168.1.29","agent_id":"agent-home-lan","device_type":"pc","status_method":"ping","sleep_type":"http_sol","sleep_endpoint":"http://192.168.1.29:8765"}`)

	// B-N1：畸形 JSON 必须 400，绝不能默认执行睡眠。
	rec := doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices/"+id+"/sleep", `{invalid json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("畸形 JSON 调睡眠应 400，got %d: %s", rec.Code, rec.Body.String())
	}
	// 非法动作 → 400。
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices/"+id+"/sleep", `{"action":"explode"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法睡眠动作应 400，got %d: %s", rec.Code, rec.Body.String())
	}
	// sleep_type=none 的设备调睡眠 → 400。
	noneID := createDeviceViaAPI(t, handler, cookie,
		`{"name":"NONE","mac":"00:11:22:33:44:09","ip":"192.168.1.19","agent_id":"agent-home-lan","device_type":"pc","status_method":"ping"}`)
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices/"+noneID+"/sleep", `{"action":"sleep"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("sleep_type=none 调睡眠应 400，got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSleepSuccessViaFakeAgent(t *testing.T) {
	srv, handler, ts := setupM3Server(t)
	cookie := loginCookie(t, handler)
	token := createApprovedAgent(t, srv, "agent-home-lan")
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/ws/agent"
	fa := connectFakeAgent(t, wsURL, token, "agent-home-lan", nil)
	defer fa.close()
	time.Sleep(200 * time.Millisecond)

	// 睡眠端点为本机 httptest 服务（127.0.0.1 回环，允许）。
	sol := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer sol.Close()

	id := createDeviceViaAPI(t, handler, cookie, fmt.Sprintf(
		`{"name":"LAN-29","mac":"AA:BB:CC:DD:EE:01","ip":"192.168.1.29","agent_id":"agent-home-lan","device_type":"pc","status_method":"ping","sleep_type":"http_sol","sleep_endpoint":%s}`,
		jsonString(sol.URL)))
	rec := doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices/"+id+"/sleep", `{"action":"sleep"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("睡眠应 200，got %d: %s", rec.Code, rec.Body.String())
	}
}

// B-N2：Agent 返回畸形检测结果时，服务端应 502 且不写脏状态入库。
func TestCheckDeviceBadResultDoesNotWriteDirtyState(t *testing.T) {
	srv, handler, ts := setupM3Server(t)
	cookie := loginCookie(t, handler)
	token := createApprovedAgent(t, srv, "agent-home-lan")
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/ws/agent"
	garbage := func(msg agent.Message) agent.ResultPayload {
		if msg.Type == "check" {
			// 有效 JSON 但不是检测结果对象：走服务端“结果解析失败”分支。
			return agent.ResultPayload{Success: true, Data: json.RawMessage(`"not-a-result-object"`)}
		}
		return agent.ExecuteRawTask(context.Background(), msg.Type, msg.Payload)
	}
	fa := connectFakeAgent(t, wsURL, token, "agent-home-lan", garbage)
	defer fa.close()
	time.Sleep(200 * time.Millisecond)

	id := createDeviceViaAPI(t, handler, cookie,
		`{"name":"LAN-29","mac":"AA:BB:CC:DD:EE:01","ip":"192.168.1.29","agent_id":"agent-home-lan","device_type":"pc","status_method":"ping"}`)
	rec := doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices/"+id+"/check", `{}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("畸形检测结果应 502，got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := srv.repo.GetDevice(id)
	if err != nil {
		t.Fatalf("重查设备失败：%v", err)
	}
	if got.Status != "unknown" {
		t.Fatalf("检测失败不应写脏状态，DB 状态应保持 unknown，got %q", got.Status)
	}
}

// 正常检测：TCP 端口开放 → online 并入库。
func TestCheckDeviceOnline(t *testing.T) {
	srv, handler, ts := setupM3Server(t)
	cookie := loginCookie(t, handler)
	token := createApprovedAgent(t, srv, "agent-home-lan")
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/ws/agent"
	fa := connectFakeAgent(t, wsURL, token, "agent-home-lan", nil)
	defer fa.close()
	time.Sleep(200 * time.Millisecond)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	id := createDeviceViaAPI(t, handler, cookie, fmt.Sprintf(
		`{"name":"LAN-29","mac":"AA:BB:CC:DD:EE:01","ip":"127.0.0.1","agent_id":"agent-home-lan","device_type":"pc","status_method":"tcp","status_port":%d}`,
		port))
	rec := doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices/"+id+"/check", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("检测应 200，got %d: %s", rec.Code, rec.Body.String())
	}
	data := dataOf(t, rec)
	if data["state"] != "online" || data["method"] != "tcp" {
		t.Fatalf("检测结果应 online/tcp，got %s", rec.Body.String())
	}
	got, _ := srv.repo.GetDevice(id)
	if got.Status != "online" {
		t.Fatalf("检测 online 应入库，got %q", got.Status)
	}
}

// B-N3：扫描任务返回畸形结果时应 502，不再返回 ok + null。
// M4 更新：POST /scan 已改为异步（契约 §7.3），创建时只返回 202；
// 畸形结果改为按探测失败计入 failed，任务完成后 failed 应为 254
// （全部探测均解析失败），results 为空——同样不返回 ok + null。
func TestNetworkScanBadResult(t *testing.T) {
	srv, handler, ts := setupM3Server(t)
	cookie := loginCookie(t, handler)
	token := createApprovedAgent(t, srv, "agent-home-lan")
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/ws/agent"
	garbage := func(msg agent.Message) agent.ResultPayload {
		if msg.Type == "scan" {
			// Agent 报告成功却未返回任何结果数据：服务端不得返回 ok + null，
			// 应按结果解析失败处理（M4：计入 failed）。
			return agent.ResultPayload{Success: true}
		}
		return agent.ExecuteRawTask(context.Background(), msg.Type, msg.Payload)
	}
	fa := connectFakeAgent(t, wsURL, token, "agent-home-lan", garbage)
	defer fa.close()
	time.Sleep(200 * time.Millisecond)

	rec := doAPI(t, handler, cookie, http.MethodPost, "/api/v1/scan",
		`{"agent_id":"agent-home-lan","cidr":"192.168.1.0/24"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("创建扫描应 202，got %d: %s", rec.Code, rec.Body.String())
	}
	_, _, data := decodeEnvelope(t, rec)
	var created struct {
		ScanID string `json:"scan_id"`
	}
	if err := json.Unmarshal(data, &created); err != nil || created.ScanID == "" {
		t.Fatalf("创建响应应含 scan_id: %s (%v)", rec.Body.String(), err)
	}
	// 轮询至完成：全部 254 次探测都应因结果解析失败而计入 failed。
	var final scanTask
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		rec = doAPI(t, handler, cookie, http.MethodGet, "/api/v1/scan/"+created.ScanID, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("轮询应 200，got %d: %s", rec.Code, rec.Body.String())
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
	if final.Progress.Failed != 254 {
		t.Errorf("畸形结果应使全部 254 次探测计入 failed，实际 %+v", final.Progress)
	}
	if final.Progress.Found != 0 || len(final.Results) != 0 {
		t.Errorf("畸形结果不应产生任何发现，实际 %+v results=%d", final.Progress, len(final.Results))
	}
	// agent_id 缺失 → 400。
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/scan", `{"cidr":"192.168.1.0/24"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 agent_id 的扫描应 400，got %d: %s", rec.Code, rec.Body.String())
	}
}

// 契约 §7：扫描导入 —— 逐条 best-effort，agent_id 强制为扫描任务的 Agent。
func TestScanImport(t *testing.T) {
	srv, handler, _ := setupM3Server(t)
	cookie := loginCookie(t, handler)
	_ = createApprovedAgent(t, srv, "agent-home-lan")

	// 预置一个已完成的扫描任务（M4 补创建接口，此处直接注注册表）。
	srv.scanTasks.put(&scanTask{
		ScanID:    "scan-1",
		AgentID:   "agent-home-lan",
		CIDR:      "192.168.1.0/24",
		Status:    scanStatusCompleted,
		StartedAt: time.Now(),
	})
	// 已存在的设备：导入时应 skipped already_exists。
	createDeviceViaAPI(t, handler, cookie,
		`{"name":"LAN-29","mac":"AA:BB:CC:DD:EE:01","ip":"192.168.1.29","agent_id":"agent-home-lan","device_type":"pc","status_method":"ping"}`)

	body := `{"scan_id":"scan-1","devices":[
		{"ip":"192.168.1.30","mac":"AA:BB:CC:DD:EE:30"},
		{"ip":"192.168.1.29","mac":"AA:BB:CC:DD:EE:01"},
		{"ip":"192.168.1.31","mac":"bad-mac"},
		{"ip":"999.1.1.1","mac":"AA:BB:CC:DD:EE:31"}
	]}`
	rec := doAPI(t, handler, cookie, http.MethodPost, "/api/v1/scan/import", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("导入应 200，got %d: %s", rec.Code, rec.Body.String())
	}
	data := dataOf(t, rec)
	if data["imported"] != float64(1) || data["skipped"] != float64(3) {
		t.Fatalf("应 imported=1 skipped=3，got %s", rec.Body.String())
	}
	if data["agent_id"] != "agent-home-lan" {
		t.Fatalf("导入 agent_id 应强制为扫描任务的 Agent，got %s", rec.Body.String())
	}
	results, _ := data["results"].([]any)
	if len(results) != 4 {
		t.Fatalf("应返回 4 条逐条结果，got %d", len(results))
	}
	reasons := map[string]string{}
	for _, r := range results {
		m := r.(map[string]any)
		reasons[m["ip"].(string)] = m["reason"].(string)
	}
	if reasons["192.168.1.30"] != "" || reasons["192.168.1.29"] != "already_exists" ||
		reasons["192.168.1.31"] != "invalid_mac" || reasons["999.1.1.1"] != "invalid_ip" {
		t.Fatalf("逐条 reason 不符，got %+v", reasons)
	}
	// 新导入的设备应自动生成名称 LAN-30 且归属扫描 Agent。
	imported, err := srv.repo.GetDeviceByMAC("AA:BB:CC:DD:EE:30")
	if err != nil {
		t.Fatalf("查导入设备失败：%v", err)
	}
	if imported.Name != "LAN-30" || imported.AgentID != "agent-home-lan" || imported.Status != "unknown" {
		t.Fatalf("导入设备字段不符：%+v", imported)
	}
	// 未知 scan_id → 404；运行中的任务 → 400。
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/scan/import",
		`{"scan_id":"scan-missing","devices":[{"ip":"192.168.1.40","mac":"AA:BB:CC:DD:EE:40"}]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知 scan_id 应 404，got %d: %s", rec.Code, rec.Body.String())
	}
	srv.scanTasks.put(&scanTask{
		ScanID: "scan-running", AgentID: "agent-home-lan", CIDR: "192.168.1.0/24",
		Status: scanStatusRunning, StartedAt: time.Now(),
	})
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/scan/import",
		`{"scan_id":"scan-running","devices":[{"ip":"192.168.1.40","mac":"AA:BB:CC:DD:EE:40"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("运行中的扫描导入应 400，got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGetDeviceByID(t *testing.T) {
	srv, handler, _ := setupM3Server(t)
	cookie := loginCookie(t, handler)
	_ = createApprovedAgent(t, srv, "agent-home-lan")
	id := createDeviceViaAPI(t, handler, cookie,
		`{"name":"LAN-29","mac":"AA:BB:CC:DD:EE:01","ip":"192.168.1.29","agent_id":"agent-home-lan","device_type":"pc","status_method":"ping"}`)

	// 存在 → 200，返回设备行。
	rec := doAPI(t, handler, cookie, http.MethodGet, "/api/v1/devices/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("查询应 200，got %d: %s", rec.Code, rec.Body.String())
	}
	data := dataOf(t, rec)
	if data["id"] != id || data["mac"] != "AA:BB:CC:DD:EE:01" || data["name"] != "LAN-29" {
		t.Fatalf("返回的设备行应与创建一致，got %s", rec.Body.String())
	}
	// 不存在 → 404。
	rec = doAPI(t, handler, cookie, http.MethodGet, "/api/v1/devices/no-such-id", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("查询不存在设备应 404，got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestNetworksCRUDAndDeviceNetworkScope(t *testing.T) {
	srv, handler, _ := setupM3Server(t)
	cookie := loginCookie(t, handler)
	_ = createApprovedAgent(t, srv, "agent-home-lan")

	// 默认网络在列表首位。
	rec := doAPI(t, handler, cookie, http.MethodGet, "/api/v1/networks", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("网络列表应 200，got %d: %s", rec.Code, rec.Body.String())
	}
	items, _ := dataOf(t, rec)["items"].([]any)
	if len(items) == 0 || items[0].(map[string]any)["id"] != "default" {
		t.Fatalf("网络列表首位应为默认网络，got %s", rec.Body.String())
	}
	// 创建实验室网络。
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/networks",
		`{"name":"实验室","cidr":"192.168.2.0/24","description":"隔离测试网"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("创建网络应 201，got %d: %s", rec.Code, rec.Body.String())
	}
	labID, _ := dataOf(t, rec)["id"].(string)
	if labID == "" {
		t.Fatalf("创建网络响应应含 id：%s", rec.Body.String())
	}
	// CIDR 重复 → 409；非法 CIDR → 400；空名 → 400。
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/networks", `{"name":"重复","cidr":"192.168.2.0/24"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("重复 CIDR 应 409，got %d: %s", rec.Code, rec.Body.String())
	}
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/networks", `{"name":"坏","cidr":"nope"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 CIDR 应 400，got %d: %s", rec.Code, rec.Body.String())
	}
	// 设备 A（默认网络）与设备 B（实验室网络）用同一 MAC → 都 201。
	devA := createDeviceViaAPI(t, handler, cookie,
		`{"name":"LAN-29","mac":"AA:BB:CC:DD:EE:01","ip":"192.168.1.29","agent_id":"agent-home-lan","device_type":"pc","status_method":"ping"}`)
	devB := createDeviceViaAPI(t, handler, cookie,
		`{"name":"LAB-29","mac":"AA:BB:CC:DD:EE:01","ip":"192.168.2.29","agent_id":"agent-home-lan","network_id":"`+labID+`","device_type":"pc","status_method":"ping"}`)
	// 同网络再建同一 MAC → 409；不存在的网络 → 400。
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices",
		`{"name":"LAB-30","mac":"AA:BB:CC:DD:EE:01","ip":"192.168.2.30","agent_id":"agent-home-lan","network_id":"`+labID+`","device_type":"pc","status_method":"ping"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("同网络重复 MAC 应 409，got %d: %s", rec.Code, rec.Body.String())
	}
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices",
		`{"name":"X","mac":"AA:BB:CC:DD:EE:40","ip":"192.168.1.40","agent_id":"agent-home-lan","network_id":"no-such","device_type":"pc","status_method":"ping"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("不存在的网络应 400，got %d: %s", rec.Code, rec.Body.String())
	}
	// 列表按 network_id 过滤。
	rec = doAPI(t, handler, cookie, http.MethodGet, "/api/v1/devices?network_id="+labID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("按网络过滤应 200，got %d: %s", rec.Code, rec.Body.String())
	}
	if dataOf(t, rec)["total"] != float64(1) {
		t.Fatalf("实验室网络应只有 1 台设备，got %s", rec.Body.String())
	}
	// 把 B 迁回默认网络 → 409（重新检查）。
	rec = doAPI(t, handler, cookie, http.MethodPut, "/api/v1/devices/"+devB, `{"network_id":"default"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("迁入已有同 MAC 的网络应 409，got %d: %s", rec.Code, rec.Body.String())
	}
	// Agent 网络关联：设置 + 查询。
	rec = doAPI(t, handler, cookie, http.MethodPut, "/api/v1/agents/agent-home-lan/networks",
		`{"network_ids":["default","`+labID+`"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("设置 Agent 网络应 200，got %d: %s", rec.Code, rec.Body.String())
	}
	rec = doAPI(t, handler, cookie, http.MethodGet, "/api/v1/agents/agent-home-lan/networks", "")
	if len(dataOf(t, rec)["networks"].([]any)) != 2 {
		t.Fatalf("Agent 应管理 2 个网络，got %s", rec.Body.String())
	}
	// 网络被设备引用 → 409；删设备后仍被 Agent 引用 → 409；解除关联后 → 200。
	rec = doAPI(t, handler, cookie, http.MethodDelete, "/api/v1/networks/"+labID, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("被引用的网络删除应 409，got %d: %s", rec.Code, rec.Body.String())
	}
	rec = doAPI(t, handler, cookie, http.MethodDelete, "/api/v1/devices/"+devB, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("删除设备 B 应 200，got %d: %s", rec.Code, rec.Body.String())
	}
	rec = doAPI(t, handler, cookie, http.MethodDelete, "/api/v1/networks/"+labID, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("被 Agent 引用的网络删除应 409，got %d: %s", rec.Code, rec.Body.String())
	}
	rec = doAPI(t, handler, cookie, http.MethodPut, "/api/v1/agents/agent-home-lan/networks", `{"network_ids":["default"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("解除 Agent 网络关联应 200，got %d: %s", rec.Code, rec.Body.String())
	}
	rec = doAPI(t, handler, cookie, http.MethodDelete, "/api/v1/networks/"+labID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("解除引用后删除网络应 200，got %d: %s", rec.Code, rec.Body.String())
	}
	// 默认网络不可删；不存在的网络 → 404。
	rec = doAPI(t, handler, cookie, http.MethodDelete, "/api/v1/networks/default", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("默认网络删除应 400，got %d: %s", rec.Code, rec.Body.String())
	}
	rec = doAPI(t, handler, cookie, http.MethodGet, "/api/v1/networks/no-such", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在的网络应 404，got %d: %s", rec.Code, rec.Body.String())
	}
	_ = devA
}

func TestDecodeJSONBodyTrailingContent(t *testing.T) {
	srv, handler, _ := setupM3Server(t)
	cookie := loginCookie(t, handler)
	_ = createApprovedAgent(t, srv, "agent-home-lan")
	valid := `{"name":"LAN-29","mac":"AA:BB:CC:DD:EE:01","ip":"192.168.1.29","agent_id":"agent-home-lan","device_type":"pc","status_method":"ping"}`
	// 正常单 JSON → 201。
	rec := doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices", valid)
	if rec.Code != http.StatusCreated {
		t.Fatalf("正常请求应 201，got %d: %s", rec.Code, rec.Body.String())
	}
	// 尾部第二个 JSON → 400。
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices", valid+` {"name":"X"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("尾部第二 JSON 应 400，got %d: %s", rec.Code, rec.Body.String())
	}
	// 尾部垃圾 → 400。
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices", valid+` garbage`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("尾部垃圾应 400，got %d: %s", rec.Code, rec.Body.String())
	}
	// 尾部空白不是第二个值 → 仍 201（换 MAC 避免冲突）。
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices",
		strings.Replace(valid, "AA:BB:CC:DD:EE:01", "AA:BB:CC:DD:EE:31", 1)+"  \n")
	if rec.Code != http.StatusCreated {
		t.Fatalf("尾部空白应仍 201，got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestScanImportNetworkScoped(t *testing.T) {
	srv, handler, _ := setupM3Server(t)
	cookie := loginCookie(t, handler)
	_ = createApprovedAgent(t, srv, "agent-home-lan")
	// 建实验室网络（与扫描网段同 CIDR）。
	rec := doAPI(t, handler, cookie, http.MethodPost, "/api/v1/networks", `{"name":"实验室","cidr":"192.168.2.0/24"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("创建网络应 201，got %d: %s", rec.Code, rec.Body.String())
	}
	labID, _ := dataOf(t, rec)["id"].(string)
	// 默认网络已有同 MAC 设备。
	createDeviceViaAPI(t, handler, cookie,
		`{"name":"LAN-29","mac":"AA:BB:CC:DD:EE:01","ip":"192.168.1.29","agent_id":"agent-home-lan","device_type":"pc","status_method":"ping"}`)
	// 扫描任务：网段 192.168.2.0/24（实验室网络）。
	srv.scanTasks.put(&scanTask{
		ScanID:    "scan-lab",
		AgentID:   "agent-home-lan",
		CIDR:      "192.168.2.0/24",
		Status:    scanStatusCompleted,
		StartedAt: time.Now(),
	})
	body := `{"scan_id":"scan-lab","devices":[{"ip":"192.168.2.29","mac":"AA:BB:CC:DD:EE:01"}]}`
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/scan/import", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("导入应 200，got %d: %s", rec.Code, rec.Body.String())
	}
	data := dataOf(t, rec)
	if data["imported"] != float64(1) || data["skipped"] != float64(0) {
		t.Fatalf("同 MAC 在另一网络应导入，got %s", rec.Body.String())
	}
	imported, err := srv.repo.GetDeviceByMACInNetwork("AA:BB:CC:DD:EE:01", labID)
	if err != nil {
		t.Fatalf("实验室网络应查到导入设备：%v", err)
	}
	if imported.IP != "192.168.2.29" || imported.AgentID != "agent-home-lan" {
		t.Fatalf("导入设备字段不符：%+v", imported)
	}
	// 再导一次同一网段 → skipped already_exists。
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/scan/import", body)
	data = dataOf(t, rec)
	if data["imported"] != float64(0) || data["skipped"] != float64(1) {
		t.Fatalf("重复导入同网络应 skipped，got %s", rec.Body.String())
	}
}

// TestSleepEndpointOutsideAgentScope 覆盖 v0.2.9 问题 7（最小一致版）：
// 睡眠目标须落在执行 Agent 的管理网段内（本测试 Agent 为 192.168.1.0/24）。
func TestSleepEndpointOutsideAgentScope(t *testing.T) {
	srv, handler, ts := setupM3Server(t)
	cookie := loginCookie(t, handler)
	token := createApprovedAgent(t, srv, "agent-home-lan")

	// http_sol 端点在网段外（仍是私有 IP，通过基线校验）→ 400 范围错误。
	outID := createDeviceViaAPI(t, handler, cookie,
		`{"name":"OUT","mac":"00:11:22:33:44:01","ip":"192.168.2.50","agent_id":"agent-home-lan","device_type":"pc","status_method":"ping","sleep_type":"http_sol","sleep_endpoint":"http://192.168.2.50:8009"}`)
	rec := doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices/"+outID+"/sleep", `{"action":"shutdown"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("网段外端点应 400，got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "管理网段") {
		t.Fatalf("范围错误应说明管理网段，got %s", rec.Body.String())
	}

	// SSH 目标 IP 在网段外 → 同样 400（在解密密码之前拦截）。
	sshID := createDeviceViaAPI(t, handler, cookie,
		`{"name":"SSH-OUT","mac":"00:11:22:33:44:02","ip":"192.168.2.50","agent_id":"agent-home-lan","device_type":"pc","status_method":"ping","sleep_type":"ssh","ssh_user":"testuser","ssh_password":"test-pass-123"}`)
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices/"+sshID+"/sleep", `{"action":"shutdown"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("网段外 SSH 目标应 400，got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "管理网段") {
		t.Fatalf("范围错误应说明管理网段，got %s", rec.Body.String())
	}

	// 网段内端点应通过范围校验：接上 fake Agent 后下发会真实执行，目标
	// 192.168.1.29:8765 无人监听 → 502（不是 400），证明范围校验已放行。
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/ws/agent"
	fa := connectFakeAgent(t, wsURL, token, "agent-home-lan", nil)
	defer fa.close()
	time.Sleep(200 * time.Millisecond)
	inID := createDeviceViaAPI(t, handler, cookie,
		`{"name":"IN","mac":"00:11:22:33:44:03","ip":"192.168.1.29","agent_id":"agent-home-lan","device_type":"pc","status_method":"ping","sleep_type":"http_sol","sleep_endpoint":"http://192.168.1.29:8765"}`)
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices/"+inID+"/sleep", `{"action":"shutdown"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("网段内端点应通过范围校验并尝试执行（目标无人监听，应 502），got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "管理网段") {
		t.Fatalf("网段内端点不应被范围校验拦截，got %s", rec.Body.String())
	}
}

// TestSleepSSHPayloadCarriesUseSudo 覆盖 v0.2.12：设备勾选“使用 sudo 关机”
// 时，睡眠下发给 Agent 的 payload 须带 ssh_use_sudo=true；未勾选时为 false。
// fake Agent 用自定义 reply 捕获 payload 并直接回成功（不真连目标 SSH）。
func TestSleepSSHPayloadCarriesUseSudo(t *testing.T) {
	srv, handler, ts := setupM3Server(t)
	cookie := loginCookie(t, handler)
	token := createApprovedAgent(t, srv, "agent-home-lan")

	captured := make(chan agent.SleepPayload, 4)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/ws/agent"
	fa := connectFakeAgent(t, wsURL, token, "agent-home-lan", func(msg agent.Message) agent.ResultPayload {
		if msg.Type == "sleep" {
			var p agent.SleepPayload
			_ = json.Unmarshal(msg.Payload, &p)
			captured <- p
		}
		return agent.ResultPayload{Success: true}
	})
	defer fa.close()
	time.Sleep(200 * time.Millisecond)

	// 勾选 sudo 的 Linux 设备：payload 应带 use_sudo=true。
	sudoID := createDeviceViaAPI(t, handler, cookie,
		`{"name":"NAS","mac":"AA:BB:CC:DD:EE:01","ip":"192.168.1.3","agent_id":"agent-home-lan","device_type":"nas","status_method":"ping","sleep_type":"ssh","ssh_user":"testuser","ssh_password":"test-pass-123","ssh_os":"linux","ssh_use_sudo":true}`)
	rec := doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices/"+sudoID+"/sleep", `{"action":"shutdown"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("sudo 设备睡眠应 200，got %d: %s", rec.Code, rec.Body.String())
	}
	select {
	case p := <-captured:
		if !p.SSHUseSudo {
			t.Fatalf("勾选 sudo 的设备，下发 payload 应含 ssh_use_sudo=true，实际 %+v", p)
		}
		if p.SSHUser != "testuser" || p.SSHTargetOS != "linux" {
			t.Fatalf("payload 字段不符: %+v", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fake Agent 未收到睡眠任务")
	}

	// 未勾选的设备：payload 应为 false。
	plainID := createDeviceViaAPI(t, handler, cookie,
		`{"name":"VM","mac":"AA:BB:CC:DD:EE:02","ip":"192.168.1.4","agent_id":"agent-home-lan","device_type":"server","status_method":"ping","sleep_type":"ssh","ssh_user":"root","ssh_password":"test-pass-123","ssh_os":"linux"}`)
	rec = doAPI(t, handler, cookie, http.MethodPost, "/api/v1/devices/"+plainID+"/sleep", `{"action":"shutdown"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("普通设备睡眠应 200，got %d: %s", rec.Code, rec.Body.String())
	}
	select {
	case p := <-captured:
		if p.SSHUseSudo {
			t.Fatalf("未勾选 sudo 的设备，下发 payload 应为 false，实际 %+v", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fake Agent 未收到睡眠任务")
	}

	// 部分更新：PUT ssh_use_sudo=true 后 GET 应回显 true；再把目标系统
	// 切到 windows，sudo 标记应被仓库层归零（sudo 只对 Linux 有意义）。
	rec = doAPI(t, handler, cookie, http.MethodPut, "/api/v1/devices/"+plainID, `{"ssh_use_sudo":true}`)
	if rec.Code != http.StatusOK || dataOf(t, rec)["ssh_use_sudo"] != true {
		t.Fatalf("PUT ssh_use_sudo=true 应回显 true，got %d: %s", rec.Code, rec.Body.String())
	}
	rec = doAPI(t, handler, cookie, http.MethodPut, "/api/v1/devices/"+plainID, `{"ssh_os":"windows"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("切 windows 应 200，got %d: %s", rec.Code, rec.Body.String())
	}
	if dataOf(t, rec)["ssh_use_sudo"] != false {
		t.Fatalf("切到 windows 后 ssh_use_sudo 应被归零，got %s", rec.Body.String())
	}
}
