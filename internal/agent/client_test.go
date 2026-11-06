package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wakehub/wakehub/internal/config"
)

func TestNextBackoff(t *testing.T) {
	cases := []struct{ in, want time.Duration }{
		{time.Second, 2 * time.Second},
		{2 * time.Second, 4 * time.Second},
		{32 * time.Second, 60 * time.Second},
		{60 * time.Second, 60 * time.Second},
		{120 * time.Second, 60 * time.Second},
	}
	for _, c := range cases {
		if got := nextBackoff(c.in); got != c.want {
			t.Fatalf("nextBackoff(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestJitteredBackoffRange(t *testing.T) {
	for i := 0; i < 200; i++ {
		got := jitteredBackoff(10 * time.Second)
		if got < 7500*time.Millisecond || got > 12500*time.Millisecond {
			t.Fatalf("jitteredBackoff(10s) = %v，超出 ±25%% 抖动范围", got)
		}
	}
}

func TestDedupCache(t *testing.T) {
	c := NewClient(config.AgentConfig{})
	res := ResultPayload{Success: true}
	if _, ok := c.lookupSeen("task-1"); ok {
		t.Fatalf("未记录的任务不应命中去重缓存")
	}
	c.rememberSeen("task-1", res)
	got, ok := c.lookupSeen("task-1")
	if !ok || !got.Success {
		t.Fatalf("已记录的任务应命中缓存：%+v %v", got, ok)
	}
	// 同一 id 重复记录不膨胀缓存。
	c.rememberSeen("task-1", ResultPayload{Success: false, Error: "x"})
	if len(c.seen) != 1 || len(c.seenOrder) != 1 {
		t.Fatalf("重复记录同一 id 不应膨胀缓存：seen=%d order=%d", len(c.seen), len(c.seenOrder))
	}
}

// slowSleepServer 返回一个固定延迟后 200 的 Sleep-on-LAN 端点。
// 延迟期间若请求上下文被取消（客户端断开），立即返回，避免
// httptest Close 被挂起的连接拖住。
func slowSleepServer(t *testing.T, delay time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sleepTaskPayload(endpoint string) json.RawMessage {
	raw, _ := json.Marshal(SleepPayload{AdapterType: "http_sol", Endpoint: endpoint, Action: "sleep"})
	return raw
}

func TestHandleTaskDedup(t *testing.T) {
	c := NewClient(config.AgentConfig{})
	srv := slowSleepServer(t, time.Second)
	sendChan := make(chan Message, 16)
	ctx := context.Background()

	msg := Message{ID: "task-dedup-1", Type: "sleep", Payload: sleepTaskPayload(srv.URL)}
	start := time.Now()
	c.handleTask(ctx, msg, sendChan)
	select {
	case res := <-sendChan:
		if res.Type != "result" {
			t.Fatalf("应收到 result，实际 %q", res.Type)
		}
		var p ResultPayload
		if err := json.Unmarshal(res.Payload, &p); err != nil || !p.Success {
			t.Fatalf("慢任务首次执行应成功：%+v err=%v", p, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("首次任务结果超时")
	}
	firstElapsed := time.Since(start)
	if firstElapsed < 800*time.Millisecond {
		t.Fatalf("首次执行应真实耗时 ~1s，实际 %v", firstElapsed)
	}

	// 同一 id 再次下发：直接回放缓存结果，不重复执行。
	start = time.Now()
	c.handleTask(ctx, msg, sendChan)
	select {
	case res := <-sendChan:
		var p ResultPayload
		if err := json.Unmarshal(res.Payload, &p); err != nil || !p.Success {
			t.Fatalf("去重回放应成功：%+v err=%v", p, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("去重回放结果超时")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("去重回放应立即返回，实际耗时 %v", elapsed)
	}
	if len(c.seen) != 1 {
		t.Fatalf("去重缓存应只有 1 条，实际 %d", len(c.seen))
	}
}

func TestCancelCancelsInflight(t *testing.T) {
	c := NewClient(config.AgentConfig{})
	srv := slowSleepServer(t, 30*time.Second)
	sendChan := make(chan Message, 16)
	ctx := context.Background()

	msg := Message{ID: "task-cancel-1", Type: "sleep", Payload: sleepTaskPayload(srv.URL)}
	c.handleTask(ctx, msg, sendChan)
	// 等任务确实开始执行（在途注册完成），再发 cancel。
	deadline := time.Now().Add(3 * time.Second)
	for {
		c.inflightMu.Lock()
		_, ok := c.inflight["task-cancel-1"]
		c.inflightMu.Unlock()
		if ok || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.cancelTask("task-cancel-1")
	select {
	case res := <-sendChan:
		var p ResultPayload
		if err := json.Unmarshal(res.Payload, &p); err != nil {
			t.Fatalf("解析结果失败：%v", err)
		}
		if p.Success {
			t.Fatalf("取消的任务应失败，实际成功")
		}
		if !strings.Contains(strings.ToLower(p.Error), "cancel") {
			t.Fatalf("取消的任务错误应提及 cancel，实际 %q", p.Error)
		}
	case <-time.After(4 * time.Second):
		t.Fatalf("取消后 4s 内应收到任务结果（否则 sendChan 永久阻塞）")
	}
}

func TestSendResultDoesNotBlockOnDeadConn(t *testing.T) {
	c := NewClient(config.AgentConfig{})
	// 无缓冲、无接收者的 channel + 已取消的 ctx：发送必须立即返回。
	sendChan := make(chan Message)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.sendResult(ctx, sendChan, "task-x", ResultPayload{Success: true})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("连接已断时 sendResult 不应永久阻塞")
	}
}

func TestTokenResolution(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "agent-token")
	if err := os.WriteFile(tokenFile, []byte("wha_filetoken123\n"), 0o600); err != nil {
		t.Fatalf("写 token 文件：%v", err)
	}
	// 入网 token（无 wha_ 前缀）+ 文件里有 per-agent token → 文件优先。
	c := NewClient(config.AgentConfig{AgentToken: "enroll-xxx", TokenFile: tokenFile})
	if got := c.currentToken(); got != "wha_filetoken123" {
		t.Fatalf("应优先用文件里的 per-agent token，实际 %q", got)
	}
	// 环境变量里已有 per-agent token → 环境变量优先（管理员重新签发后更新 env 即生效）。
	c2 := NewClient(config.AgentConfig{AgentToken: "wha_envtoken456", TokenFile: tokenFile})
	if got := c2.currentToken(); got != "wha_envtoken456" {
		t.Fatalf("环境变量里的 per-agent token 应优先，实际 %q", got)
	}
}

func TestOnRegistrationApprovedPersists(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "sub", "agent-token")
	c := NewClient(config.AgentConfig{AgentToken: "enroll-xxx", TokenFile: tokenFile})
	c.onRegistrationApproved("wha_newtoken789")
	if got := c.currentToken(); got != "wha_newtoken789" {
		t.Fatalf("获批后内存 token 应更新，实际 %q", got)
	}
	b, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatalf("token 文件应被持久化：%v", err)
	}
	if strings.TrimSpace(string(b)) != "wha_newtoken789" {
		t.Fatalf("token 文件内容错误：%q", string(b))
	}
	info, err := os.Stat(tokenFile)
	if err != nil {
		t.Fatalf("stat token 文件：%v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token 文件权限应为 0600，实际 %o", info.Mode().Perm())
	}
}

// TestClientTaskTimeout 覆盖 v0.2.9 问题 6：Agent 任务执行上限取配置，
// 零值配置回落 10s 默认（避免 WithTimeout(0) 立即过期）。
func TestClientTaskTimeout(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.AgentConfig
		want time.Duration
	}{
		{"zero config falls back", config.AgentConfig{}, 10 * time.Second},
		{"explicit 5s", config.AgentConfig{TaskTimeout: 5 * time.Second}, 5 * time.Second},
		{"explicit 30s", config.AgentConfig{TaskTimeout: 30 * time.Second}, 30 * time.Second},
		{"negative falls back", config.AgentConfig{TaskTimeout: -time.Second}, 10 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient(tc.cfg)
			if got := c.taskTimeout(); got != tc.want {
				t.Errorf("taskTimeout() = %v, want %v", got, tc.want)
			}
		})
	}
}
