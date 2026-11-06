package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// setupApprovedAgent 建一个已获批 Agent 并签发 token，返回 token。
func setupApprovedAgent(t *testing.T, hub *Hub, id string) string {
	t.Helper()
	if _, err := hub.Store().CreateAgentApproved(id, id, "192.168.1.0/24"); err != nil {
		t.Fatalf("CreateAgentApproved %s: %v", id, err)
	}
	token, err := hub.Store().IssueToken(id)
	if err != nil {
		t.Fatalf("IssueToken %s: %v", id, err)
	}
	return token
}

func TestConcurrentReconnectKeepsOnlyLiveSession(t *testing.T) {
	hub := setupTestHub(t)
	srv := newTestServer(t, hub)
	token := setupApprovedAgent(t, hub, "agent-race")

	const n = 8
	var wg sync.WaitGroup
	conns := make([]*testAgentConn, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			conns[i] = connectTestAgent(t, wsURL(srv), token, "agent-race", 500*time.Millisecond, true)
		}(i)
	}
	wg.Wait()
	t.Cleanup(func() {
		for _, c := range conns {
			if c != nil {
				c.Close()
			}
		}
	})
	// 等待注册稳定：只剩一个 live 会话，其余旧连接均被关闭。
	waitFor(t, 5*time.Second, "只剩一个 live 会话", func() bool {
		return hub.SessionCount() == 1
	})
	waitFor(t, 5*time.Second, "全部旧连接被关闭", func() bool {
		closed := 0
		for _, c := range conns {
			select {
			case <-c.done:
				closed++
			default:
			}
		}
		return closed == n-1
	})
	if !hub.IsConnected("agent-race") {
		t.Fatalf("重连后 Agent 应仍在线")
	}
	// 唯一的 live 会话必须能正常下发任务。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := hub.Dispatch(ctx, "agent-race", "wake", WakePayload{MAC: "AA:BB:CC:DD:EE:FF", BroadcastAddress: "255.255.255.255", Port: 9, Count: 1})
	if err != nil || !res.Success {
		t.Fatalf("Dispatch 到重连后的会话应成功：res=%+v err=%v", res, err)
	}
}

func TestOldConnDeferDoesNotDeleteNew(t *testing.T) {
	hub := setupTestHub(t)
	srv := newTestServer(t, hub)
	token := setupApprovedAgent(t, hub, "agent-def")

	old := connectTestAgent(t, wsURL(srv), token, "agent-def", 500*time.Millisecond, true)
	waitFor(t, 5*time.Second, "旧连接上线", func() bool { return hub.IsConnected("agent-def") })

	// 新连接替换旧连接：旧连接的 defer 不得误删新会话。
	neu := connectTestAgent(t, wsURL(srv), token, "agent-def", 500*time.Millisecond, true)
	t.Cleanup(func() { neu.Close() })
	waitFor(t, 5*time.Second, "旧连接被关闭", func() bool {
		select {
		case <-old.done:
			return true
		default:
			return false
		}
	})
	if !hub.IsConnected("agent-def") {
		t.Fatalf("旧连接关闭后新会话应仍在线（defer 误删）")
	}
	a, err := hub.Store().GetAgent("agent-def")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if a.Status != "online" {
		t.Fatalf("旧连接 defer 不应把 Agent 置 offline，实际 status=%q", a.Status)
	}
	// 新会话能正常下发任务。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := hub.Dispatch(ctx, "agent-def", "wake", WakePayload{MAC: "AA:BB:CC:DD:EE:FF", BroadcastAddress: "255.255.255.255", Port: 9, Count: 1}); err != nil {
		t.Fatalf("Dispatch 到新会话应成功：%v", err)
	}
	old.Close()
}

func TestDispatchToPendingAgentRefused(t *testing.T) {
	hub := setupTestHub(t)
	srv := newTestServer(t, hub)
	// 持入网 token 连接：进 pending 模式。
	pending := connectTestAgent(t, wsURL(srv), testEnrollToken, "agent-pend", 500*time.Millisecond, true)
	defer pending.Close()
	waitFor(t, 5*time.Second, "pending 会话建立", func() bool { return hub.IsConnected("agent-pend") })
	a, err := hub.Store().GetAgent("agent-pend")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if a.ApprovalStatus != "pending" {
		t.Fatalf("入网 token 连接应建 pending 行，实际 %q", a.ApprovalStatus)
	}
	// 未授权 Agent 不允许执行任何设备操作。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = hub.Dispatch(ctx, "agent-pend", "wake", WakePayload{MAC: "AA:BB:CC:DD:EE:FF", BroadcastAddress: "255.255.255.255", Port: 9, Count: 1})
	if err == nil || !strings.Contains(err.Error(), "尚未获批") {
		t.Fatalf("向 pending Agent 下发应被拒，实际 err=%v", err)
	}
}

func TestLocalImpersonationRejected(t *testing.T) {
	hub := setupTestHub(t)
	// 单元层：任何远程 Agent 不得以 X-Agent-ID: local 注册。
	_, err := hub.AuthenticateAgent(testEnrollToken, "local", "Fake", "", "", "", "", "127.0.0.1", testEnrollToken)
	var ae *AgentAuthError
	if !errors.As(err, &ae) || ae.Status != 403 || ae.Code != "agent_id_mismatch" {
		t.Fatalf("冒认 local 应返回 403 agent_id_mismatch，实际 %v", err)
	}
	// 握手层：持有效 per-agent token 也冒认不了 local。
	token := setupApprovedAgent(t, hub, "agent-real")
	_, err = hub.AuthenticateAgent(token, "local", "Fake", "", "", "", "", "127.0.0.1", testEnrollToken)
	if !errors.As(err, &ae) || ae.Status != 403 || ae.Code != "agent_id_mismatch" {
		t.Fatalf("持真 token 冒认 local 也应被拒，实际 %v", err)
	}
	// HTTP 层：Dial 直接被拒。
	srv := newTestServer(t, hub)
	conn, resp, err := dialRaw(t, wsURL(srv), testEnrollToken, "local")
	if err == nil {
		_ = conn.Close()
		t.Fatalf("冒认 local 的 WS 握手应失败")
	}
	if resp == nil || resp.StatusCode != 403 {
		t.Fatalf("冒认 local 应返回 HTTP 403，实际 resp=%v", resp)
	}
}

func TestAuthenticateAgentPaths(t *testing.T) {
	hub := setupTestHub(t)
	ip := "192.168.1.65"

	// 入网 token 建 pending。
	auth, err := hub.AuthenticateAgent(testEnrollToken, "agent-x", "X", "192.168.1.0/24", "0.2.0", "linux", "amd64", ip, testEnrollToken)
	if err != nil {
		t.Fatalf("自助注册应成功建 pending：%v", err)
	}
	if !auth.Pending || !auth.Created {
		t.Fatalf("首次自助注册应为 Pending+Created：%+v", auth)
	}
	// pending 行持 per-agent token 连接应被拒（尚未获批）。
	if _, err := hub.AuthenticateAgent("wha_nonexistent", "agent-x", "", "", "", "", "", ip, testEnrollToken); err == nil {
		t.Fatalf("未知 token 应被拒")
	}
	// 审批后用签发的 token 连接。
	token, err := hub.Store().ApproveAgent("agent-x")
	if err != nil {
		t.Fatalf("ApproveAgent: %v", err)
	}
	auth, err = hub.AuthenticateAgent(token, "agent-x", "X", "", "0.2.0", "linux", "amd64", ip, testEnrollToken)
	if err != nil || !auth.Approved {
		t.Fatalf("获批后持 token 连接应 Approved：auth=%+v err=%v", auth, err)
	}
	// token 与 X-Agent-ID 不绑定。
	if _, err := hub.AuthenticateAgent(token, "agent-y", "", "", "", "", "", ip, testEnrollToken); !isAuthError(err, 403, "agent_id_mismatch") {
		t.Fatalf("token 与 ID 不绑定应返回 403 agent_id_mismatch，实际 %v", err)
	}
	// 无效 token。
	if _, err := hub.AuthenticateAgent("bad-token", "agent-x", "", "", "", "", "", ip, testEnrollToken); !isAuthError(err, 401, "invalid_token") {
		t.Fatalf("无效 token 应返回 401 invalid_token，实际 %v", err)
	}
	// 缺凭证。
	if _, err := hub.AuthenticateAgent("", "agent-x", "", "", "", "", "", ip, testEnrollToken); !isAuthError(err, 400, "invalid_token") {
		t.Fatalf("缺 token 应返回 400，实际 %v", err)
	}
	// 吊销后被拒，其他 Agent 不受影响（隔离性在 store 层另测，此处测握手）。
	if err := hub.Store().RevokeToken("agent-x"); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	if _, err := hub.AuthenticateAgent(token, "agent-x", "", "", "", "", "", ip, testEnrollToken); !isAuthError(err, 403, "token_revoked") {
		t.Fatalf("吊销后连接应返回 403 token_revoked，实际 %v", err)
	}
	// 禁用后被拒。
	if err := hub.Store().SetApprovalStatus("agent-x", "disabled"); err != nil {
		t.Fatalf("SetApprovalStatus: %v", err)
	}
	token2, err := hub.Store().IssueToken("agent-x")
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if _, err := hub.AuthenticateAgent(token2, "agent-x", "", "", "", "", "", ip, testEnrollToken); !isAuthError(err, 403, "agent_disabled") {
		t.Fatalf("禁用后连接应返回 403 agent_disabled，实际 %v", err)
	}
	// 重入网：已获批 Agent 持入网 token 再次连接 → 回 pending，旧 token 作废。
	if err := hub.Store().SetApprovalStatus("agent-x", "approved"); err != nil {
		t.Fatalf("SetApprovalStatus approved: %v", err)
	}
	auth, err = hub.AuthenticateAgent(testEnrollToken, "agent-x", "X", "", "0.2.0", "linux", "amd64", ip, testEnrollToken)
	if err != nil || !auth.Pending || !auth.ReEnrolled {
		t.Fatalf("重入网应回 pending：auth=%+v err=%v", auth, err)
	}
	// 旧 token 已吊销作废：再用它连接应返回 403 token_revoked。
	if _, revoked, err := hub.Store().VerifyTokenHash(token2); err != nil || !revoked {
		t.Fatalf("重入网后旧 token 应被吊销作废：revoked=%v err=%v", revoked, err)
	}
	if _, err := hub.AuthenticateAgent(token2, "agent-x", "", "", "", "", "", ip, testEnrollToken); !isAuthError(err, 403, "token_revoked") {
		t.Fatalf("重入网后旧 token 连接应返回 403 token_revoked，实际 %v", err)
	}
}

func isAuthError(err error, status int, code string) bool {
	var ae *AgentAuthError
	return errors.As(err, &ae) && ae.Status == status && ae.Code == code
}

func TestTokenRevokeIsolation(t *testing.T) {
	hub := setupTestHub(t)
	srv := newTestServer(t, hub)
	tokenA := setupApprovedAgent(t, hub, "agent-a")
	tokenB := setupApprovedAgent(t, hub, "agent-b")

	a := connectTestAgent(t, wsURL(srv), tokenA, "agent-a", 500*time.Millisecond, true)
	defer a.Close()
	b := connectTestAgent(t, wsURL(srv), tokenB, "agent-b", 500*time.Millisecond, true)
	defer b.Close()
	waitFor(t, 5*time.Second, "A、B 均上线", func() bool {
		return hub.IsConnected("agent-a") && hub.IsConnected("agent-b")
	})

	// 吊销 A：关会话 + 吊销 token。
	if err := hub.Store().RevokeToken("agent-a"); err != nil {
		t.Fatalf("RevokeToken A: %v", err)
	}
	hub.CloseSession("agent-a", 4403, "token revoked")
	waitFor(t, 5*time.Second, "A 掉线", func() bool { return !hub.IsConnected("agent-a") })

	// B 不受影响：仍在线、任务可下发。
	if !hub.IsConnected("agent-b") {
		t.Fatalf("吊销 A 不应影响 B 的连接")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := hub.Dispatch(ctx, "agent-b", "wake", WakePayload{MAC: "AA:BB:CC:DD:EE:FF", BroadcastAddress: "255.255.255.255", Port: 9, Count: 1}); err != nil {
		t.Fatalf("吊销 A 后 B 的任务应仍可下发：%v", err)
	}
	// A 已离线，下发失败。
	if _, err := hub.Dispatch(ctx, "agent-a", "wake", WakePayload{MAC: "AA:BB:CC:DD:EE:FF", BroadcastAddress: "255.255.255.255", Port: 9, Count: 1}); err == nil {
		t.Fatalf("吊销后 A 的任务应失败")
	}
}

func TestHeartbeatTimeoutCleansZombie(t *testing.T) {
	hub := setupTestHub(t)
	hub.HeartbeatTimeout = time.Second
	hub.PingInterval = 500 * time.Millisecond
	srv := newTestServer(t, hub)
	token := setupApprovedAgent(t, hub, "agent-zombie")

	// 连接后不发任何心跳（只读不写）：服务端应判定僵死并清理。
	zombie := connectTestAgent(t, wsURL(srv), token, "agent-zombie", 0, false)
	defer zombie.Close()
	waitFor(t, 5*time.Second, "僵尸连接上线", func() bool { return hub.IsConnected("agent-zombie") })
	waitFor(t, 10*time.Second, "僵尸连接被心跳超时清理", func() bool {
		return !hub.IsConnected("agent-zombie")
	})
	a, err := hub.Store().GetAgent("agent-zombie")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if a.Status != "offline" {
		t.Fatalf("僵尸清理后 Agent 应置 offline，实际 %q", a.Status)
	}
	if hub.SessionCount() != 0 {
		t.Fatalf("僵尸清理后会话数应为 0，实际 %d", hub.SessionCount())
	}
}

func TestDispatchCancelPropagates(t *testing.T) {
	hub := setupTestHub(t)
	srv := newTestServer(t, hub)
	token := setupApprovedAgent(t, hub, "agent-cancel")

	// autoReply=false：Agent 收到任务不回包，Dispatch 会超时取消。
	ta := connectTestAgent(t, wsURL(srv), token, "agent-cancel", 500*time.Millisecond, false)
	defer ta.Close()
	waitFor(t, 5*time.Second, "Agent 上线", func() bool { return hub.IsConnected("agent-cancel") })

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err := hub.Dispatch(ctx, "agent-cancel", "wake", WakePayload{MAC: "AA:BB:CC:DD:EE:FF", BroadcastAddress: "255.255.255.255", Port: 9, Count: 1})
	if err == nil {
		t.Fatalf("不回包的任务应超时失败")
	}
	// Agent 应收到 cancel 消息，且 task_id 与原任务 id 一致。
	deadline := time.Now().Add(5 * time.Second)
	var taskID string
	var cancelSeen bool
	for time.Now().Before(deadline) {
		select {
		case msg := <-ta.received:
			switch msg.Type {
			case "wake":
				taskID = msg.ID
			case "cancel":
				var p CancelPayload
				if err := json.Unmarshal(msg.Payload, &p); err == nil && p.TaskID != "" {
					if taskID != "" && p.TaskID == taskID {
						cancelSeen = true
					}
				}
			}
		case <-time.After(200 * time.Millisecond):
		}
		if cancelSeen {
			break
		}
	}
	if !cancelSeen {
		t.Fatalf("服务端取消应向 Agent 传播 cancel 消息（task_id=%q）", taskID)
	}
}

func TestDispatchNullResultPayloadReturnsError(t *testing.T) {
	hub := setupTestHub(t)
	srv := newTestServer(t, hub)
	token := setupApprovedAgent(t, hub, "agent-null")
	// autoReply=false：由测试手动回包。
	ta := connectTestAgent(t, wsURL(srv), token, "agent-null", 500*time.Millisecond, false)
	t.Cleanup(func() { ta.Close() })
	waitFor(t, 5*time.Second, "Agent 上线", func() bool { return hub.IsConnected("agent-null") })

	errCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := hub.Dispatch(ctx, "agent-null", "wake", WakePayload{MAC: "AA:BB:CC:DD:EE:FF", BroadcastAddress: "255.255.255.255", Port: 9, Count: 1})
		errCh <- err
	}()

	// 等下发的 wake 任务（received 里还可能混有 heartbeat_ack，跳过）。
	var task Message
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case m := <-ta.received:
			if m.Type == "wake" {
				task = m
				goto gotTask
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("未收到下发的 wake 任务")
gotTask:
	// 回包 payload 为 null：json.Unmarshal("null") 成功得到零值，
	// 旧代码会产生空错误消息（API 曾吐出 {"code":400,"message":""}）。
	if err := ta.writeJSON(Message{ID: task.ID, Type: "result", Payload: json.RawMessage("null")}); err != nil {
		t.Fatalf("回包失败: %v", err)
	}
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("空回包应返回错误")
		}
		if err.Error() == "" {
			t.Fatal("错误消息不得为空")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Dispatch 未返回")
	}
}

// TestDispatchCancelGoesToRetriedSession 覆盖 v0.2.9 问题 5 的核心回归：
// Dispatch 取到旧会话、写失败、重试到新会话后，超时 cancel 必须发往
// 承载任务的新会话，而不是已断的旧会话。
//
// 复现手法（确定性，无时序竞态）：
//  1. A1 上线后由 A2 同 ID 重连替换；此时锁住 sess1 的写互斥量，
//     A2 的 RegisterConn 会卡在关闭旧连接那一步（map 已换成 sess2）；
//  2. 把 sess1 重新注入 map 并启动 Dispatch——它取到 sess1，
//     在 writeJSON 入口处被写锁冻住；
//  3. 测试把 map 翻回 sess2 再放锁：Dispatch 的写因连接已关而失败，
//     重取会话拿到 sess2，重试成功；
//  4. 超时后 cancel 应出现在 A2 的收信里（旧实现发往 sess1，A2 收不到）。
func TestDispatchCancelGoesToRetriedSession(t *testing.T) {
	hub := setupTestHub(t)
	srv := newTestServer(t, hub)
	token := setupApprovedAgent(t, hub, "agent-retry")

	ta1 := connectTestAgent(t, wsURL(srv), token, "agent-retry", 500*time.Millisecond, false)
	defer ta1.Close()
	waitFor(t, 5*time.Second, "A1 上线", func() bool { return hub.IsConnected("agent-retry") })
	sess1, ok := hub.session("agent-retry")
	if !ok {
		t.Fatal("A1 会话不存在")
	}
	// 先锁住旧会话的写锁：后续 A2 的 RegisterConn 会卡在 old.close()，
	// 但 map 替换已完成；旧会话的清理 defer 也被挡在连接关闭之后，
	// 不会在测试中途误删后面重新注入的 sess1。
	// 注意：这里不用 defer 解锁——翻转后由测试显式放锁（唯一一次解锁）。
	sess1.mu.Lock()

	ta2 := connectTestAgent(t, wsURL(srv), token, "agent-retry", 500*time.Millisecond, false)
	defer ta2.Close()
	waitFor(t, 5*time.Second, "A2 接管会话", func() bool {
		s, ok := hub.session("agent-retry")
		return ok && s != sess1
	})
	sess2, _ := hub.session("agent-retry")

	// 把旧会话重新注入 map，模拟“取会话与写之间被重连抢占”的窗口。
	hub.mu.Lock()
	hub.sessions["agent-retry"] = sess1
	hub.mu.Unlock()

	errCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := hub.Dispatch(ctx, "agent-retry", "wake", WakePayload{MAC: "AA:BB:CC:DD:EE:FF", BroadcastAddress: "255.255.255.255", Port: 9, Count: 1})
		errCh <- err
	}()
	// 等 Dispatch 注册完 pending（已取到 sess1、正要写），再翻 map。
	waitFor(t, 5*time.Second, "Dispatch 注册 pending", func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		return len(hub.pending) == 1
	})
	hub.mu.Lock()
	hub.sessions["agent-retry"] = sess2
	hub.mu.Unlock()
	sess1.mu.Unlock()

	if err := <-errCh; err == nil {
		t.Fatal("不回包的任务应超时失败")
	} else if !strings.Contains(err.Error(), "超时") {
		t.Fatalf("超时错误应含“超时”，got %v", err)
	}

	// A2 应先收到 wake，再收到 cancel，且 task_id 与 wake 的 id 一致。
	deadline := time.Now().Add(5 * time.Second)
	var taskID string
	var cancelSeen bool
	for time.Now().Before(deadline) {
		select {
		case msg := <-ta2.received:
			switch msg.Type {
			case "wake":
				taskID = msg.ID
			case "cancel":
				var p CancelPayload
				if err := json.Unmarshal(msg.Payload, &p); err == nil && p.TaskID != "" {
					if taskID != "" && p.TaskID == taskID {
						cancelSeen = true
					}
				}
			}
		case <-time.After(200 * time.Millisecond):
		}
		if cancelSeen {
			break
		}
	}
	if !cancelSeen {
		t.Fatalf("cancel 应发往重试后的新会话（task_id=%q）", taskID)
	}
	if taskID == "" {
		t.Fatal("A2 未收到 wake 任务")
	}
	info, ok := hub.TaskStatus(taskID)
	if !ok {
		t.Fatalf("任务 %q 应有状态记录", taskID)
	}
	if info.Status != TaskStatusTimeout {
		t.Errorf("任务状态 = %q, want %q", info.Status, TaskStatusTimeout)
	}
	if info.AgentID != "agent-retry" || info.Type != "wake" {
		t.Errorf("任务记录应含 agent/type，got %+v", info)
	}
}

// TestDispatchSuccessRecordsCompleted 覆盖统一任务状态的完成分支：
// 自动回包的任务成功后应记为 completed。
func TestDispatchSuccessRecordsCompleted(t *testing.T) {
	hub := setupTestHub(t)
	srv := newTestServer(t, hub)
	token := setupApprovedAgent(t, hub, "agent-done")

	ta := connectTestAgent(t, wsURL(srv), token, "agent-done", 500*time.Millisecond, true)
	defer ta.Close()
	waitFor(t, 5*time.Second, "Agent 上线", func() bool { return hub.IsConnected("agent-done") })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := hub.Dispatch(ctx, "agent-done", "wake", WakePayload{MAC: "AA:BB:CC:DD:EE:FF", BroadcastAddress: "255.255.255.255", Port: 9, Count: 1})
	if err != nil || !res.Success {
		t.Fatalf("Dispatch 应成功：res=%+v err=%v", res, err)
	}
	// 从 Agent 收到的 wake 消息里取任务 id，再查状态。
	deadline := time.Now().Add(5 * time.Second)
	var taskID string
	for time.Now().Before(deadline) && taskID == "" {
		select {
		case msg := <-ta.received:
			if msg.Type == "wake" {
				taskID = msg.ID
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
	if taskID == "" {
		t.Fatal("Agent 未收到 wake 任务")
	}
	info, ok := hub.TaskStatus(taskID)
	if !ok {
		t.Fatalf("任务 %q 应有状态记录", taskID)
	}
	if info.Status != TaskStatusCompleted {
		t.Errorf("任务状态 = %q, want %q", info.Status, TaskStatusCompleted)
	}
	if !info.FinishedAt.After(info.StartedAt) && !info.FinishedAt.Equal(info.StartedAt) {
		t.Errorf("完成任务应有结束时间，got %+v", info)
	}
}
