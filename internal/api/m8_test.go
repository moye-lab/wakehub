package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestCreateGroupDuplicateReturns409：分组重名须返回 409 而非 400，
// 且不得把原始 DB 错误文本（UNIQUE constraint）外泄（M8 验收发现）。
func TestCreateGroupDuplicateReturns409(t *testing.T) {
	handler, _ := setupTestServer(t)
	cookie := loginCookie(t, handler)
	post := func(name string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/groups", strings.NewReader(`{"name":"`+name+`"}`))
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := post("m8-group"); rec.Code != http.StatusCreated {
		t.Fatalf("首次创建应 201，got %d: %s", rec.Code, rec.Body.String())
	}
	rec := post("m8-group")
	if rec.Code != http.StatusConflict {
		t.Fatalf("重名应返回 409，got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "UNIQUE constraint") {
		t.Fatalf("不得外泄原始 DB 错误文本：%s", rec.Body.String())
	}
}

// TestScanSnapshotEmptyResultsAreArray：空结果的扫描快照必须序列化为
// [] 而非 null（契约 §1.1），列表摘要不得含 results 字段（契约 §7.3）。
func TestScanSnapshotEmptyResultsAreArray(t *testing.T) {
	store := newScanTaskStore()
	now := time.Now().UTC()
	store.put(&scanTask{ScanID: "s-empty", AgentID: "a1", CIDR: "192.168.1.0/24", Status: scanStatusCompleted, StartedAt: now})
	store.put(&scanTask{ScanID: "s-running", AgentID: "a1", CIDR: "192.168.1.0/24", Status: scanStatusRunning, StartedAt: now})

	snap, ok := store.snapshot("s-empty")
	if !ok {
		t.Fatal("快照应存在")
	}
	b, _ := json.Marshal(snap)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("marshal 快照失败：%v", err)
	}
	res, has := m["results"]
	if !has {
		t.Fatal("详情快照应含 results 字段")
	}
	if res == nil {
		t.Fatalf("空结果应序列化为 [] 而非 null：%s", string(b))
	}
	if arr, ok := res.([]any); !ok || len(arr) != 0 {
		t.Fatalf("空结果应为 []，got %v", res)
	}

	sums := store.listSummaries()
	if len(sums) != 2 {
		t.Fatalf("摘要应有 2 条，got %d", len(sums))
	}
	sb, _ := json.Marshal(sums)
	var sm []map[string]any
	if err := json.Unmarshal(sb, &sm); err != nil {
		t.Fatalf("marshal 摘要失败：%v", err)
	}
	for _, item := range sm {
		if _, has := item["results"]; has {
			t.Fatalf("列表摘要不得含 results 字段：%s", string(sb))
		}
	}
}
