package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHealthEndpoint 是健康检查端点测试（契约 §12.2，M7 新增）：
// 公开、无需登录、只读、轻量；db 由 SELECT 1 探测。
func TestHealthEndpoint(t *testing.T) {
	handler, _ := setupTestServer(t)

	// 不带 Cookie 也能访问（公开端点）。
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("健康检查应返回 200，got %d: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if code, _ := body["code"].(float64); code != 0 {
		t.Fatalf("code 应为 0，got %v", body["code"])
	}
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("data 应为对象，got %T", body["data"])
	}
	if data["status"] != "ok" {
		t.Errorf("status 应为 ok，got %v", data["status"])
	}
	if data["version"] != "0.3.1" {
		t.Errorf("version 应为 0.3.1，got %v", data["version"])
	}
	if data["db"] != "ok" {
		t.Errorf("db 应为 ok，got %v", data["db"])
	}
	if _, ok := data["uptime_seconds"].(float64); !ok {
		t.Errorf("uptime_seconds 应为数字，got %v", data["uptime_seconds"])
	}
}

// TestListGroupsPaginationEnvelope 是分组列表分页信封测试（契约 §11，M7）：
// v0.1.0 返回裸数组，v0.2.0 改为 §1.4 分页信封。
func TestListGroupsPaginationEnvelope(t *testing.T) {
	handler, _ := setupTestServer(t)

	// 未登录 → 401。
	req := httptest.NewRequest(http.MethodGet, "/api/v1/groups", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应返回 401，got %d", rec.Code)
	}

	cookie := loginCookie(t, handler)
	createGroup := func(name string) {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"name": name})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/groups", strings.NewReader(string(b)))
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("创建分组 %q 应返回 201，got %d: %s", name, rec.Code, rec.Body.String())
		}
	}
	createGroup("客厅")
	createGroup("书房")
	createGroup("卧室")

	getGroups := func(query string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/groups"+query, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/groups%s 应返回 200，got %d: %s", query, rec.Code, rec.Body.String())
		}
		body := decodeBody(t, rec)
		data, ok := body["data"].(map[string]any)
		if !ok {
			t.Fatalf("data 应为分页信封对象，got %T", body["data"])
		}
		return data
	}

	// 默认分页：001 迁移已种子 1 个默认分组，再加新建的 3 个，共 4 条。
	data := getGroups("")
	if total, _ := data["total"].(float64); total != 4 {
		t.Errorf("total 应为 4，got %v", data["total"])
	}
	if items, _ := data["items"].([]any); len(items) != 4 {
		t.Errorf("默认页应返回 4 条，got %d", len(items))
	}

	// page_size=2：第 1 页 2 条，第 2 页 2 条。
	data = getGroups("?page=1&page_size=2")
	if items, _ := data["items"].([]any); len(items) != 2 {
		t.Errorf("第 1 页应返回 2 条，got %d", len(items))
	}
	if page, _ := data["page"].(float64); page != 1 {
		t.Errorf("page 应为 1，got %v", data["page"])
	}
	data = getGroups("?page=2&page_size=2")
	if items, _ := data["items"].([]any); len(items) != 2 {
		t.Errorf("第 2 页应返回 2 条，got %d", len(items))
	}

	// 超出范围的页返回空数组而非 null。
	data = getGroups("?page=99&page_size=2")
	items, _ := data["items"].([]any)
	if items == nil || len(items) != 0 {
		t.Errorf("超出范围的页应返回 []，got %v", data["items"])
	}
}
