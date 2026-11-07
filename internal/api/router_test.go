package api

import (
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

const (
	testSecret      = "test-secret-key-for-m1-tests-0123456789"
	testAdminPass   = "admin-pass-123"
	testEnrollToken = "test-enroll-token-123"
)

func setupTestServer(t *testing.T) (http.Handler, *auth.Service) {
	t.Helper()
	cfg := config.ServerConfig{
		ListenAddr:       ":8080",
		SecretKey:        testSecret,
		EnrollToken:      testEnrollToken,
		WSAllowedOrigins: []string{"https://allowed.example.com"},
	}
	return setupTestServerWithConfig(t, cfg, nil)
}

// setupTestServerWithConfig 是 setupTestServer 的可配版：proxies 非空时
// authSvc 与 Server 同时配可信代理（生产 main.go 里两者取同一份 cfg）。
func setupTestServerWithConfig(t *testing.T, cfg config.ServerConfig, proxies []*net.IPNet) (http.Handler, *auth.Service) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	for _, f := range []string{"../../migrations/001_init.sql", "../../migrations/002_auth.sql", "../../migrations/003_agent.sql", "../../migrations/004_runs.sql", "../../migrations/005_indexes.sql", "../../migrations/006_ssh_sleep.sql", "../../migrations/007_ssh_windows.sql", "../../migrations/008_network_identity.sql", "../../migrations/009_rename_seeded_networks.sql", "../../migrations/010_ssh_use_sudo.sql", "../../migrations/011_network_cidr_unique.sql", "../../migrations/012_task_runs_keep_history.sql"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := db.Exec(string(b)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	t.Cleanup(func() { db.Close() })

	cfg.TrustedProxies = proxies
	authSvc := auth.NewService(db, cfg.SecretKey, proxies)
	if err := authSvc.EnsureAdmin("admin", testAdminPass); err != nil {
		t.Fatalf("EnsureAdmin: %v", err)
	}
	repo := device.NewRepository(db)
	hub := agent.NewHub(repo)
	sched := scheduler.NewEngine(repo, hub)
	srv := NewServer(cfg, repo, authSvc, hub, sched, audit.NewLogger(db))
	return srv.Handler(), authSvc
}

func doLogin(t *testing.T, handler http.Handler, username, password string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"username":` + jsonString(username) + `,"password":` + jsonString(password) + `}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	req.RemoteAddr = "192.168.1.51:64692"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func loginCookie(t *testing.T, handler http.Handler) *http.Cookie {
	t.Helper()
	rec := doLogin(t, handler, "admin", testAdminPass)
	if rec.Code != http.StatusOK {
		t.Fatalf("登录应成功，got %d: %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	t.Fatalf("登录响应未下发 %s Cookie：%v", sessionCookieName, rec.Result().Header.Values("Set-Cookie"))
	return nil
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应 JSON 失败：%v，body=%s", err, rec.Body.String())
	}
	return body
}

func TestLoginSuccessSetsCookie(t *testing.T) {
	handler, _ := setupTestServer(t)
	rec := doLogin(t, handler, "admin", testAdminPass)
	if rec.Code != http.StatusOK {
		t.Fatalf("登录应返回 200，got %d: %s", rec.Code, rec.Body.String())
	}
	setCookie := rec.Header().Get("Set-Cookie")
	for _, want := range []string{"wakehub_session=", "HttpOnly", "SameSite=Lax", "Max-Age=86400"} {
		if !strings.Contains(setCookie, want) {
			t.Errorf("Set-Cookie 应包含 %q，got %q", want, setCookie)
		}
	}
	body := decodeBody(t, rec)
	if _, hasToken := body["data"].(map[string]any)["token"]; hasToken {
		t.Error("登录响应不得再返回 token 字段（破坏性变更）")
	}
	data := body["data"].(map[string]any)
	if data["username"] != "admin" || data["expires_at"] == nil || data["expires_at"] == "" {
		t.Errorf("登录响应 data 应含 username 与 expires_at，got %v", data)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	handler, _ := setupTestServer(t)
	rec := doLogin(t, handler, "admin", "wrong-password")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("密码错误应返回 401，got %d", rec.Code)
	}
	if body := decodeBody(t, rec); body["message"] != "用户名或密码错误" {
		t.Errorf("文案应为统一的“用户名或密码错误”，got %v", body["message"])
	}
}

func TestLoginRateLimited(t *testing.T) {
	handler, _ := setupTestServer(t)
	var rec *httptest.ResponseRecorder
	for i := 0; i < 9; i++ {
		rec = doLogin(t, handler, "admin", "wrong-password")
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("第 9 次失败登录应返回 429，got %d: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["message"] != "登录失败次数过多，请稍后再试" {
		t.Errorf("限流文案不正确，got %v", body["message"])
	}
	if data, ok := body["data"].(map[string]any); !ok || data["code"] != "rate_limited" {
		t.Errorf("限流响应 data 应含 code=rate_limited，got %v", body["data"])
	}
}

func TestWithAuthRequiresCookie(t *testing.T) {
	handler, _ := setupTestServer(t)
	// 无 Cookie → 401 固定文案。
	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无 Cookie 应返回 401，got %d", rec.Code)
	}
	if body := decodeBody(t, rec); body["message"] != authFailedMessage {
		t.Errorf("401 文案应为固定值 %q，got %v", authFailedMessage, body["message"])
	}
	// Authorization Bearer 头已被废止：只带 Bearer、无 Cookie 仍 401。
	cookie := loginCookie(t, handler)
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	req2.Header.Set("Authorization", "Bearer "+cookie.Value)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("Bearer 头应被忽略、无 Cookie 应 401，got %d", rec2.Code)
	}
	// 带 Cookie → 200。
	req3 := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	req3.AddCookie(cookie)
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Errorf("带有效 Cookie 应返回 200，got %d: %s", rec3.Code, rec3.Body.String())
	}
}

func TestWithAuthDoesNotLeakErrors(t *testing.T) {
	handler, _ := setupTestServer(t)
	// 篡改过的 Cookie：401 文案必须是固定值，不得外泄 base64/签名等内部错误。
	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "!!!not-base64!!!.bad-sig"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("非法 Cookie 应返回 401，got %d", rec.Code)
	}
	body := rec.Body.String()
	// data 字段为 nil 时按 omitempty 省略（全接口一致的既有行为）。
	if body != `{"code":401,"message":"`+authFailedMessage+`"}`+"\n" {
		t.Errorf("401 响应应为固定文案信封，got %s", body)
	}
	for _, leak := range []string{"base64", "signature", "hmac", "illegal", "expired", "revoked"} {
		if strings.Contains(strings.ToLower(body), leak) {
			t.Errorf("401 响应不得外泄内部错误原文（含 %q）：%s", leak, body)
		}
	}
}

func TestLogoutRevokesSession(t *testing.T) {
	handler, _ := setupTestServer(t)
	cookie := loginCookie(t, handler)
	// 退出。
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("退出应返回 200，got %d: %s", rec.Code, rec.Body.String())
	}
	if sc := rec.Header().Get("Set-Cookie"); !strings.Contains(sc, "Max-Age=0") {
		t.Errorf("退出应下发清除 Cookie（Max-Age=0），got %q", sc)
	}
	// 旧 Cookie 再次请求 → 401。
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	req2.AddCookie(cookie)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("退出后旧 Cookie 应失效，got %d", rec2.Code)
	}
	// 无 Cookie 退出 → 401。
	req3 := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusUnauthorized {
		t.Errorf("未登录退出应返回 401，got %d", rec3.Code)
	}
}

func TestChangePasswordFlow(t *testing.T) {
	handler, _ := setupTestServer(t)
	cookie := loginCookie(t, handler)
	// 等到下一秒边界再改密：token iat 与 password_changed_at 均为秒精度，
	// 旧会话须在改密前至少一个整秒签发，失效断言才与时间无关、稳定成立。
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second).Add(10 * time.Millisecond)))
	post := func(body string, c *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/change-password", strings.NewReader(body))
		if c != nil {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	// 当前密码错误。
	rec := post(`{"current_password":"wrong","new_password":"new-password-456"}`, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("当前密码错误应返回 400，got %d", rec.Code)
	}
	if errs, ok := decodeBody(t, rec)["data"].(map[string]any)["errors"].([]any); !ok || len(errs) == 0 ||
		errs[0].(map[string]any)["code"] != "wrong_password" {
		t.Errorf("应返回 wrong_password 字段错误，got %s", rec.Body.String())
	}
	// 新密码太弱。
	rec = post(`{"current_password":"admin-pass-123","new_password":"short"}`, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("弱密码应返回 400，got %d", rec.Code)
	}
	if errs, ok := decodeBody(t, rec)["data"].(map[string]any)["errors"].([]any); !ok || len(errs) == 0 ||
		errs[0].(map[string]any)["code"] != "weak_password" {
		t.Errorf("应返回 weak_password 字段错误，got %s", rec.Body.String())
	}
	// 正确改密。
	rec = post(`{"current_password":"admin-pass-123","new_password":"new-password-456"}`, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("改密应返回 200，got %d: %s", rec.Code, rec.Body.String())
	}
	if body := decodeBody(t, rec); body["message"] != "密码已修改，请重新登录" {
		t.Errorf("改密成功文案不正确，got %v", body["message"])
	}
	// 旧 Cookie 立即失效。
	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	req.AddCookie(cookie)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("改密后旧 Cookie 应失效，got %d", rec2.Code)
	}
	// 旧密码登录失败，新密码登录成功。
	if rec := doLogin(t, handler, "admin", testAdminPass); rec.Code != http.StatusUnauthorized {
		t.Errorf("旧密码应登录失败，got %d", rec.Code)
	}
	if rec := doLogin(t, handler, "admin", "new-password-456"); rec.Code != http.StatusOK {
		t.Errorf("新密码应登录成功，got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMe(t *testing.T) {
	handler, _ := setupTestServer(t)
	// 未登录 → 401。
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未登录 /me 应返回 401，got %d", rec.Code)
	}
	// 已登录 → 200。
	cookie := loginCookie(t, handler)
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req2.AddCookie(cookie)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("/me 应返回 200，got %d: %s", rec2.Code, rec2.Body.String())
	}
	data := decodeBody(t, rec2)["data"].(map[string]any)
	if data["username"] != "admin" || data["role"] != "admin" ||
		data["issued_at"] == nil || data["expires_at"] == nil {
		t.Errorf("/me 返回字段不完整，got %v", data)
	}
}

func TestCSRFOriginCheck(t *testing.T) {
	handler, _ := setupTestServer(t)
	cookie := loginCookie(t, handler)
	// 跨站 Origin 的状态变更请求 → 403。
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/batch-wake", strings.NewReader(`{"device_ids":[]}`))
	req.AddCookie(cookie)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("跨站 Origin 的 POST 应返回 403，got %d", rec.Code)
	}
	// 同源 Origin 放行（进入 handler，空列表返回 400 而非 403）。
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/devices/batch-wake", strings.NewReader(`{"device_ids":[]}`))
	req2.AddCookie(cookie)
	req2.Header.Set("Origin", "http://127.0.0.1:8080")
	req2.Host = "127.0.0.1:8080"
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code == http.StatusForbidden {
		t.Errorf("同源 Origin 的 POST 不应被 CSRF 拦截，got %d", rec2.Code)
	}
	// GET 不受 Origin 校验限制。
	req3 := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	req3.AddCookie(cookie)
	req3.Header.Set("Origin", "https://evil.example")
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Errorf("GET 不应被 CSRF 拦截，got %d", rec3.Code)
	}
}

func TestBodyTooLarge(t *testing.T) {
	handler, _ := setupTestServer(t)
	// 超过 1 MiB 的登录请求体 → 413。
	big := `{"username":"admin","password":"` + strings.Repeat("a", (1<<20)+1024) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(big))
	req.RemoteAddr = "192.168.1.51:64692"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("超限 Body 应返回 413，got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCheckOrigin(t *testing.T) {
	handler, _ := setupTestServer(t)
	ts := httptest.NewServer(handler)
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/ws/agent"

	dial := func(origin string, token string) (int, error) {
		header := http.Header{}
		if origin != "" {
			header.Set("Origin", origin)
		}
		if token != "" {
			header.Set("X-Agent-Token", token)
			header.Set("X-Agent-ID", "agent-test-01")
		}
		conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
		if err != nil {
			if resp != nil {
				return resp.StatusCode, err
			}
			return 0, err
		}
		conn.Close()
		return http.StatusSwitchingProtocols, nil
	}

	// 无 Origin、无 token：缺凭证 → 400（契约 §3.5；M2 起全局 token 已废止，
	// 握手凭证为 per-agent token 或入网 token）。
	if code, _ := dial("", ""); code != http.StatusBadRequest {
		t.Errorf("无 Origin 无 token 应返回 400，got %d", code)
	}
	// 跨站 Origin + 有效 token → 403（CheckOrigin 拦截）。
	if code, _ := dial("https://evil.example", testEnrollToken); code != http.StatusForbidden {
		t.Errorf("跨站 Origin 应返回 403，got %d", code)
	}
	// 白名单 Origin + 有效 token → 101。
	if code, _ := dial("https://allowed.example.com", testEnrollToken); code != http.StatusSwitchingProtocols {
		t.Errorf("白名单 Origin 应升级成功（101），got %d", code)
	}
	// 同源 Origin + 有效 token → 101。
	if code, _ := dial(ts.URL, testEnrollToken); code != http.StatusSwitchingProtocols {
		t.Errorf("同源 Origin 应升级成功（101），got %d", code)
	}
	// 无 Origin + 有效 token（Agent 场景）→ 101。
	if code, _ := dial("", testEnrollToken); code != http.StatusSwitchingProtocols {
		t.Errorf("无 Origin 的 Agent 连接应升级成功（101），got %d", code)
	}
}

// TestLoginCookieSecureBehindTrustedProxy 覆盖 v0.2.9 问题 4 的端到端
// 行为：可信代理后 X-Forwarded-Proto=https 时登录 Cookie 带 Secure；
// 其余情况（http / 无头 / 不可信方谎报）一律不带，局域网 HTTP 不受影响。
func TestLoginCookieSecureBehindTrustedProxy(t *testing.T) {
	_, proxyNet, err := net.ParseCIDR("192.168.1.0/24")
	if err != nil {
		t.Fatalf("parse CIDR: %v", err)
	}
	proxies := []*net.IPNet{proxyNet}
	cfg := config.ServerConfig{
		ListenAddr:       ":8080",
		SecretKey:        testSecret,
		EnrollToken:      testEnrollToken,
		WSAllowedOrigins: []string{"https://allowed.example.com"},
	}
	handler, _ := setupTestServerWithConfig(t, cfg, proxies)
	cases := []struct {
		name       string
		remoteAddr string
		xfp        string
		wantSecure bool
	}{
		{"可信代理 + https", "192.168.1.5:1234", "https", true},
		{"可信代理 + http", "192.168.1.5:1234", "http", false},
		{"可信代理无 XFP", "192.168.1.5:1234", "", false},
		{"不可信直连方谎报 https", "203.0.113.9:1234", "https", false},
		{"直连 HTTP", "192.168.1.9:1234", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"username":"admin","password":"` + testAdminPass + `"}`
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
			req.RemoteAddr = tc.remoteAddr
			if tc.xfp != "" {
				req.Header.Set("X-Forwarded-Proto", tc.xfp)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("登录应成功，got %d: %s", rec.Code, rec.Body.String())
			}
			setCookie := rec.Header().Get("Set-Cookie")
			hasSecure := strings.Contains(setCookie, "Secure")
			if hasSecure != tc.wantSecure {
				t.Errorf("Set-Cookie Secure = %v, want %v (got %q)", hasSecure, tc.wantSecure, setCookie)
			}
		})
	}
}
