package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/wakehub/wakehub/internal/device"
	"github.com/wakehub/wakehub/internal/scanner"
	"github.com/wakehub/wakehub/internal/sleep"
	"github.com/wakehub/wakehub/internal/status"
	"github.com/wakehub/wakehub/internal/wol"
)

// WS 协议常量（契约 §3.5）。
const (
	// wsReadLimit 是双向消息上限；超限服务端以 1009 关闭连接。
	wsReadLimit = 1 << 20
	// defaultHeartbeatTimeout 是服务端判定僵死连接的阈值：45s 未收到
	// 应用层心跳即关闭连接、置 offline。
	defaultHeartbeatTimeout = 45 * time.Second
	// defaultPingInterval 是服务端 WS Ping 间隔。
	defaultPingInterval = 30 * time.Second
	// writeDeadline 是单次 WS 写的截止时间。
	writeDeadline = 5 * time.Second
)

type Message struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"` // heartbeat, wake, check, scan, sleep, result, registration_*, cancel
	Payload json.RawMessage `json:"payload"`
}

type WakePayload struct {
	MAC              string `json:"mac"`
	BroadcastAddress string `json:"broadcast_address"`
	Port             int    `json:"port"`
	Count            int    `json:"count"`
}

type CheckPayload struct {
	IP     string `json:"ip"`
	MAC    string `json:"mac"`
	Method string `json:"method"`
	Port   int    `json:"port"`
}

type ScanPayload struct {
	CIDR string `json:"cidr"`
}

type SleepPayload struct {
	AdapterType string       `json:"adapter_type"`
	Endpoint    string       `json:"endpoint"`
	Action      sleep.Action `json:"action"`
	// SSH 适配器字段（契约 §6.3，仅 adapter_type == "ssh" 时使用）：
	// SSHHost 为设备 IP（服务端取设备行，不接受客户端任意主机名）；
	// SSHKnownHostKey 为 TOFU 已信任指纹，为空表示首次连接学习；
	// SSHTargetOS 为目标系统 linux/windows（空视为 linux）。
	SSHHost         string `json:"ssh_host,omitempty"`
	SSHPort         int    `json:"ssh_port,omitempty"`
	SSHUser         string `json:"ssh_user,omitempty"`
	SSHPassword     string `json:"ssh_password,omitempty"`
	SSHKnownHostKey string `json:"ssh_known_host_key,omitempty"`
	SSHTargetOS     string `json:"ssh_target_os,omitempty"`
	// SSHUseSudo 为“使用 sudo 关机”（v0.2.12，Linux 专用）：Agent 以单次
	// 会话 sudo -S 执行关机命令，密码经 stdin 一次喂给 sudo，校验与断电
	// 在同一个 sudo 调用里完成，不依赖跨会话的时间戳；目标机无需预先写
	// 规则；Windows 目标忽略。
	SSHUseSudo bool `json:"ssh_use_sudo,omitempty"`
}

type ResultPayload struct {
	Success bool            `json:"success"`
	Error   string          `json:"error,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// RegistrationPendingPayload 是 pending 模式建立后服务端发给 Agent 的首条消息。
type RegistrationPendingPayload struct {
	AgentID string `json:"agent_id"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// RegistrationApprovedPayload 在审批通过时推送；Agent 须持久化 token 后重连。
type RegistrationApprovedPayload struct {
	AgentID string `json:"agent_id"`
	Token   string `json:"token"`
}

// RegistrationRejectedPayload 在驳回时推送，随后服务端以 4403 关闭 WS。
type RegistrationRejectedPayload struct {
	AgentID string `json:"agent_id"`
	Message string `json:"message"`
}

// CancelPayload 取消在途任务（契约 §3.5）。
type CancelPayload struct {
	TaskID string `json:"task_id"`
}

type connectedSession struct {
	conn *websocket.Conn
	mu   sync.Mutex
	// approved 标记该会话是否已获批。pending 模式的会话只能收心跳与
	// 注册消息，Dispatch 拒绝向其下发任务。
	approved bool
	// lastHeartbeat 是最近一次收到该 Agent 任意消息的时间（Unix 纳秒），
	// 由监控协程用于僵死判定。
	lastHeartbeat atomic.Int64
	ctx           context.Context
	cancel        context.CancelFunc
}

func (s *connectedSession) writeJSON(v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
	return s.conn.WriteJSON(v)
}

func (s *connectedSession) ping() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeDeadline))
}

// close 发一个 Close 控制帧后关闭连接。多次调用安全。
func (s *connectedSession) close(code int, reason string) {
	s.mu.Lock()
	_ = s.conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason),
		time.Now().Add(writeDeadline),
	)
	s.mu.Unlock()
	_ = s.conn.Close()
}

// TaskStatus 是 Hub 内一次下发任务的统一状态（v0.2.9 问题 5）：
// pending → dispatched → completed/failed；等待中被取消时按 ctx.Err()
// 区分 cancelled（调用方主动取消）与 timeout（截止时间到）。
// 状态机由 Dispatch 唯一推进。
type TaskStatus string

const (
	TaskStatusPending    TaskStatus = "pending"
	TaskStatusDispatched TaskStatus = "dispatched"
	TaskStatusCompleted  TaskStatus = "completed"
	TaskStatusFailed     TaskStatus = "failed"
	TaskStatusCancelled  TaskStatus = "cancelled"
	TaskStatusTimeout    TaskStatus = "timeout"
)

// TaskInfo 是 Hub 内一次下发任务的可观测状态快照。
type TaskInfo struct {
	ID         string
	AgentID    string
	Type       string
	Status     TaskStatus
	StartedAt  time.Time
	FinishedAt time.Time
	Error      string
}

// maxTrackedTasks 是 Hub 保留的任务状态记录上限（最近 100 条，
// 供排障观测；下发路径本身不依赖它）。
const maxTrackedTasks = 100

type Hub struct {
	store *Store
	mu    sync.RWMutex
	// sessions 是当前 live WS 会话，按 agent_id 索引。
	sessions map[string]*connectedSession
	pending  map[string]chan ResultPayload
	// tasks 是下发任务的统一状态记录（v0.2.9 问题 5），按 msgID 索引；
	// taskOrder 是插入顺序，用于超限时淘汰最旧记录。
	tasks     map[string]*TaskInfo
	taskOrder []string

	// HeartbeatTimeout 是判定僵死连接的阈值（默认 45s）；
	// PingInterval 是服务端 Ping 间隔（默认 30s）。
	// 零值时取默认值；测试可调小以加速验证。
	HeartbeatTimeout time.Duration
	PingInterval     time.Duration
}

func NewHub(repo *device.Repository) *Hub {
	return NewHubWithStore(NewStore(repo.DB()))
}

// NewHubWithStore 供测试与调用方直接注入 Store。
func NewHubWithStore(store *Store) *Hub {
	return &Hub{
		store:    store,
		sessions: make(map[string]*connectedSession),
		pending:  make(map[string]chan ResultPayload),
	}
}

// Store 暴露 Agent 数据层，供 API 层（Agent 管理路由、WS 握手）使用。
func (h *Hub) Store() *Store {
	return h.store
}

func (h *Hub) heartbeatTimeout() time.Duration {
	if h.HeartbeatTimeout > 0 {
		return h.HeartbeatTimeout
	}
	return defaultHeartbeatTimeout
}

func (h *Hub) pingInterval() time.Duration {
	if h.PingInterval > 0 {
		return h.PingInterval
	}
	return defaultPingInterval
}

// AgentAuthError 是 WS 握手失败（契约 §3.5）：HTTP 状态 + 中文文案 +
// 机器可读 code。调用方（router）据此写 JSON 信封，不得用纯文本 http.Error。
type AgentAuthError struct {
	Status  int
	Message string
	Code    string
}

func (e *AgentAuthError) Error() string {
	return e.Message
}

// AgentAuth 是握手认证成功的结果。
type AgentAuth struct {
	AgentID string
	// Approved 为 true 表示已获批，可执行任务；Pending 为 true 表示
	// 待审批模式（只许心跳，不下发任务）。
	Approved   bool
	Pending    bool
	Created    bool // 本次新建了 pending 行（自助注册）
	ReEnrolled bool // 已获批 Agent 持入网 token 重入网，回退 pending
}

// AuthenticateAgent 做 WS 握手前的身份认证与注册分流（契约 §3.5）。
// 不做 HTTP Upgrade，不写审计（调用方按 Created/ReEnrolled 写 agent_register）。
//
//  1. per-agent token：验哈希（恒定时间）→ 未吊销 → 审批状态 approved
//     → token 绑定的 agent_id 与 X-Agent-ID 一致 → Approved。
//  2. 入网 token：验入网密钥（恒定时间）→ 新 ID 建 pending 行；
//     已存在且 approved 的 ID 回退 pending 并作废旧 token（重入网）；
//     已存在且 disabled 的 ID 拒绝。
//  3. `local` 是保留 ID，任何远程 Agent 不得以此注册（修复 B-N8）。
func (h *Hub) AuthenticateAgent(token, agentID, name, subnet, version, osName, arch, ip, enrollToken string) (*AgentAuth, error) {
	if token == "" || agentID == "" {
		return nil, &AgentAuthError{Status: 400, Message: "缺少 Agent 身份凭证", Code: "invalid_token"}
	}
	if ReservedAgentID(agentID) {
		return nil, &AgentAuthError{Status: 403, Message: "Agent 身份与凭证不匹配", Code: "agent_id_mismatch"}
	}

	// 路径 1：per-agent token。
	if boundID, revoked, err := h.store.VerifyTokenHash(token); err == nil {
		if revoked {
			return nil, &AgentAuthError{Status: 403, Message: "Agent 凭证已被吊销", Code: "token_revoked"}
		}
		if boundID != agentID {
			return nil, &AgentAuthError{Status: 403, Message: "Agent 身份与凭证不匹配", Code: "agent_id_mismatch"}
		}
		a, err := h.store.GetAgent(agentID)
		if err != nil {
			// token 存在但 Agent 行已删（孤儿 token）：按凭证无效处理。
			return nil, &AgentAuthError{Status: 401, Message: "Agent 凭证无效", Code: "invalid_token"}
		}
		switch a.ApprovalStatus {
		case "disabled":
			return nil, &AgentAuthError{Status: 403, Message: "Agent 已被禁用", Code: "agent_disabled"}
		case "pending":
			return nil, &AgentAuthError{Status: 403, Message: "Agent 尚未获批", Code: "agent_not_approved"}
		case "approved":
			// 已获批：更新握手上报信息与 token 最近使用时间。
			_ = h.store.UpdateHandshake(agentID, name, subnet, version, osName, arch, ip)
			h.store.TouchTokenLastUsed(agentID)
			return &AgentAuth{AgentID: agentID, Approved: true}, nil
		default:
			return nil, &AgentAuthError{Status: 403, Message: "Agent 尚未获批", Code: "agent_not_approved"}
		}
	} else if err != nil && !errors.Is(err, ErrTokenNotFound) {
		return nil, err
	}

	// 路径 2：入网 token（自助注册 / 重入网）。
	if enrollToken != "" && tokensEqual(token, enrollToken) {
		a, err := h.store.GetAgent(agentID)
		if err != nil {
			if errors.Is(err, ErrAgentNotFound) {
				if _, err := h.store.CreateAgentPending(agentID, name, subnet, version, osName, arch, ip); err != nil {
					return nil, err
				}
				return &AgentAuth{AgentID: agentID, Pending: true, Created: true}, nil
			}
			return nil, err
		}
		switch a.ApprovalStatus {
		case "disabled":
			return nil, &AgentAuthError{Status: 403, Message: "Agent 已被禁用", Code: "agent_disabled"}
		case "pending":
			_ = h.store.UpdateHandshake(agentID, name, subnet, version, osName, arch, ip)
			return &AgentAuth{AgentID: agentID, Pending: true}, nil
		case "approved":
			// 重入网：旧 per-agent token 作废，回到 pending，须重新审批。
			// token 不存在时 RevokeToken 返回 ErrTokenNotFound，忽略。
			_ = h.store.RevokeToken(agentID)
			if err := h.store.SetApprovalStatus(agentID, "pending"); err != nil {
				return nil, err
			}
			_ = h.store.UpdateHandshake(agentID, name, subnet, version, osName, arch, ip)
			return &AgentAuth{AgentID: agentID, Pending: true, ReEnrolled: true}, nil
		default:
			return nil, &AgentAuthError{Status: 403, Message: "Agent 尚未获批", Code: "agent_not_approved"}
		}
	}

	return nil, &AgentAuthError{Status: 401, Message: "Agent 凭证无效", Code: "invalid_token"}
}

// RegisterConn 接管一条已完成握手认证的 WS 连接，直到连接断开才返回。
// approved=false 为 pending 待审批模式：只处理心跳，不下发任务。
//
// 竞态修复（审计第 1 项）：
//   - 新连接替换旧连接时，先在锁内交换 map 引用，解锁后再关闭旧连接
//     （关闭旧连接不持有 hub 锁）；
//   - 旧连接的 defer 在删除前比对 sessions[agentID] == sess，只有匹配
//     才删并置 offline，绝不误删新连接。
func (h *Hub) RegisterConn(agentID string, conn *websocket.Conn, approved bool) {
	ctx, cancel := context.WithCancel(context.Background())
	sess := &connectedSession{conn: conn, approved: approved, ctx: ctx, cancel: cancel}
	sess.lastHeartbeat.Store(time.Now().UnixNano())

	h.mu.Lock()
	old, hadOld := h.sessions[agentID]
	h.sessions[agentID] = sess
	h.mu.Unlock()
	if hadOld {
		old.close(websocket.CloseNormalClosure, "replaced by new connection")
	}
	_ = h.store.SetAgentStatus(agentID, "online")
	if !approved {
		// pending 模式：建会话后立即发首条待审批消息。
		raw, _ := json.Marshal(RegistrationPendingPayload{
			AgentID: agentID, Status: "pending", Message: "等待管理员审批",
		})
		_ = sess.writeJSON(Message{ID: uuid.NewString(), Type: "registration_pending", Payload: raw})
	}

	defer func() {
		cancel()
		h.mu.Lock()
		current := h.sessions[agentID] == sess
		if current {
			delete(h.sessions, agentID)
		}
		h.mu.Unlock()
		if current {
			_ = h.store.SetAgentStatus(agentID, "offline")
		}
		_ = conn.Close()
	}()

	conn.SetReadLimit(wsReadLimit)
	_ = conn.SetReadDeadline(time.Now().Add(h.heartbeatTimeout()))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(h.heartbeatTimeout()))
		return nil
	})

	// 监控协程：服务端 Ping + 心跳超时清理僵死连接。
	go h.monitorSession(agentID, sess)

	for {
		var msg Message
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}
		// 任意消息都证明 Agent 存活：刷新读超时与心跳时间戳。
		_ = conn.SetReadDeadline(time.Now().Add(h.heartbeatTimeout()))
		sess.lastHeartbeat.Store(time.Now().UnixNano())
		switch msg.Type {
		case "heartbeat":
			_ = h.store.SetAgentHeartbeat(agentID)
			_ = sess.writeJSON(Message{ID: msg.ID, Type: "heartbeat_ack"})
		case "result":
			var res ResultPayload
			if err := json.Unmarshal(msg.Payload, &res); err != nil {
				// 回包无法解析是协议错误：不要静默丢弃（否则 Dispatch
				// 一直等到 ctx 超时），直接给等待者一个明确的失败结果。
				res = ResultPayload{Success: false, Error: "Agent 返回了无法解析的结果"}
			} else if !res.Success && res.Error == "" {
				// json.Unmarshal("null") 成功且得到零值；空错误会一路
				// 变成空 message，在此补一个明确文案。
				res.Error = "Agent 任务执行失败（未返回错误信息）"
			}
			h.mu.Lock()
			ch, ok := h.pending[msg.ID]
			h.mu.Unlock()
			if ok {
				select {
				case ch <- res:
				default:
				}
			}
		}
	}
}

// monitorSession 是单会话的监控协程：每 PingInterval 发一次 Ping，
// 定期检查心跳新鲜度；超 heartbeatTimeout 未收到消息即判定
// 僵死，关闭连接（读循环随之退出并清理会话）。
func (h *Hub) monitorSession(agentID string, sess *connectedSession) {
	pingTicker := time.NewTicker(h.pingInterval())
	defer pingTicker.Stop()
	// 检查间隔：默认 5s；超时阈值很小时（如测试）按阈值的一半检查，
	// 避免僵死判定被检查粒度拖慢。
	checkEvery := 5 * time.Second
	if ht := h.heartbeatTimeout(); ht < 10*time.Second {
		checkEvery = ht / 2
	}
	checkTicker := time.NewTicker(checkEvery)
	defer checkTicker.Stop()
	for {
		select {
		case <-sess.ctx.Done():
			return
		case <-pingTicker.C:
			if err := sess.ping(); err != nil {
				sess.close(websocket.CloseNormalClosure, "ping failed")
				return
			}
		case <-checkTicker.C:
			last := time.Unix(0, sess.lastHeartbeat.Load())
			if time.Since(last) > h.heartbeatTimeout() {
				sess.close(websocket.CloseNormalClosure, "heartbeat timeout")
				return
			}
		}
	}
}

func (h *Hub) session(agentID string) (*connectedSession, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	sess, ok := h.sessions[agentID]
	return sess, ok
}

// IsConnected 报告该 Agent 当前是否有 live WS 会话。
func (h *Hub) IsConnected(agentID string) bool {
	_, ok := h.session(agentID)
	return ok
}

// recordTask 登记一次下发任务的初始状态；超限时淘汰最旧记录。
func (h *Hub) recordTask(task *TaskInfo) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tasks == nil {
		h.tasks = make(map[string]*TaskInfo)
	}
	h.tasks[task.ID] = task
	h.taskOrder = append(h.taskOrder, task.ID)
	for len(h.taskOrder) > maxTrackedTasks {
		oldest := h.taskOrder[0]
		h.taskOrder = h.taskOrder[1:]
		delete(h.tasks, oldest)
	}
}

// TaskStatus 返回一次下发任务的统一状态快照（不存在时 ok=false，
// 例如已被淘汰出最近 100 条记录）。
func (h *Hub) TaskStatus(id string) (TaskInfo, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	task, ok := h.tasks[id]
	if !ok {
		return TaskInfo{}, false
	}
	return *task, true
}

// SessionCount 返回当前 live 会话数（主要供测试与运维观测）。
func (h *Hub) SessionCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.sessions)
}

// CloseSession 立即关闭该 Agent 的 live 会话（吊销/禁用/驳回/重新签发时调用）。
// code 用 WS 私有码段：4403 禁止（契约 §3.5）。
func (h *Hub) CloseSession(agentID string, code int, reason string) {
	if sess, ok := h.session(agentID); ok {
		sess.close(code, reason)
	}
}

// NotifyApproved 在审批通过时向在线的 pending Agent 推送新 token。
func (h *Hub) NotifyApproved(agentID, token string) error {
	sess, ok := h.session(agentID)
	if !ok {
		return ErrAgentNotFound
	}
	raw, _ := json.Marshal(RegistrationApprovedPayload{AgentID: agentID, Token: token})
	return sess.writeJSON(Message{ID: uuid.NewString(), Type: "registration_approved", Payload: raw})
}

// NotifyRejected 在驳回时向在线的 pending Agent 推送驳回消息。
// 调用方随后应以 4403 关闭该会话。
func (h *Hub) NotifyRejected(agentID string) error {
	sess, ok := h.session(agentID)
	if !ok {
		return ErrAgentNotFound
	}
	raw, _ := json.Marshal(RegistrationRejectedPayload{AgentID: agentID, Message: "注册已被驳回"})
	return sess.writeJSON(Message{ID: uuid.NewString(), Type: "registration_rejected", Payload: raw})
}

// Dispatch 向指定 Agent 下发任务。`local` 进程内直发已退役（契约 §3.6）：
// 全部任务必须经注册 Agent 执行，agent_id 为空或无 live 会话即报错。
// 未获批（pending）的会话不允许执行任何设备操作。
//
// TOCTOU 修复（B-N12）：写失败时重新取当前会话引用并重试一次；
// 若会话已被替换，重试发往新连接。
//
// v0.2.9 问题 5：carrier 记录实际成功发送的会话；后续 cancel 只发给
// 承载任务的会话（不再发往已被替换的旧会话）；任务状态由 TaskStatus
// 统一记录（pending → dispatched → completed/failed/cancelled/timeout）。
func (h *Hub) Dispatch(ctx context.Context, agentID, taskType string, payload any) (ResultPayload, error) {
	if agentID == "" {
		return ResultPayload{}, errors.New("未指定 Agent")
	}
	sess, ok := h.session(agentID)
	if !ok {
		return ResultPayload{}, fmt.Errorf("目标节点 %s 当前离线", agentID)
	}
	if !sess.approved {
		return ResultPayload{}, errors.New("Agent 尚未获批")
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return ResultPayload{}, err
	}
	msgID := uuid.NewString()
	ch := make(chan ResultPayload, 1)

	h.mu.Lock()
	h.pending[msgID] = ch
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.pending, msgID)
		h.mu.Unlock()
	}()

	// 统一任务状态：状态机由 Dispatch 唯一推进。
	task := &TaskInfo{ID: msgID, AgentID: agentID, Type: taskType, Status: TaskStatusPending, StartedAt: time.Now()}
	h.recordTask(task)
	setStatus := func(status TaskStatus) {
		h.mu.Lock()
		task.Status = status
		h.mu.Unlock()
	}
	finish := func(status TaskStatus, errMsg string) {
		h.mu.Lock()
		task.Status = status
		task.FinishedAt = time.Now()
		task.Error = errMsg
		h.mu.Unlock()
	}

	msg := Message{ID: msgID, Type: taskType, Payload: raw}
	// carrier 是实际承载任务的会话：首次发送失败且会话已被替换时，
	// 重试发往新会话，后续 cancel 也发往它。
	carrier := sess
	if err := sess.writeJSON(msg); err != nil {
		if sess2, ok2 := h.session(agentID); ok2 && sess2 != sess {
			if err2 := sess2.writeJSON(msg); err2 != nil {
				finish(TaskStatusFailed, err2.Error())
				return ResultPayload{}, fmt.Errorf("发送指令至 Agent 失败: %w", err2)
			}
			carrier = sess2
		} else {
			finish(TaskStatusFailed, err.Error())
			return ResultPayload{}, fmt.Errorf("发送指令至 Agent 失败: %w", err)
		}
	}
	setStatus(TaskStatusDispatched)

	select {
	case <-ctx.Done():
		// 服务端取消向 Agent 传播 cancel（契约 §3.5），只发给承载任务的会话。
		cancelRaw, _ := json.Marshal(CancelPayload{TaskID: msgID})
		_ = carrier.writeJSON(Message{ID: uuid.NewString(), Type: "cancel", Payload: cancelRaw})
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			finish(TaskStatusTimeout, ctx.Err().Error())
			return ResultPayload{}, errors.New("Agent 任务执行超时")
		}
		finish(TaskStatusCancelled, ctx.Err().Error())
		return ResultPayload{}, errors.New("任务已取消")
	case res := <-ch:
		if !res.Success {
			finish(TaskStatusFailed, res.Error)
			return res, errors.New(res.Error)
		}
		finish(TaskStatusCompleted, "")
		return res, nil
	}
}

// ExecuteRawTask 供远程独立 Agent 统一调用的底层网络任务执行器。
func ExecuteRawTask(ctx context.Context, taskType string, raw json.RawMessage) ResultPayload {
	switch taskType {
	case "wake":
		var p WakePayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return ResultPayload{Success: false, Error: err.Error()}
		}
		err := wol.Send(ctx, wol.SendOptions{
			MAC:              p.MAC,
			BroadcastAddress: p.BroadcastAddress,
			Port:             p.Port,
			Count:            p.Count,
		})
		if err != nil {
			return ResultPayload{Success: false, Error: err.Error()}
		}
		return ResultPayload{Success: true}

	case "check":
		var p CheckPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return ResultPayload{Success: false, Error: err.Error()}
		}
		st := status.Check(ctx, p.IP, p.MAC, p.Method, p.Port)
		data, _ := json.Marshal(st)
		return ResultPayload{Success: true, Data: data}

	case "scan":
		var p ScanPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return ResultPayload{Success: false, Error: err.Error()}
		}
		devs, err := scanner.ScanSubnet(ctx, p.CIDR)
		if err != nil {
			return ResultPayload{Success: false, Error: err.Error()}
		}
		data, _ := json.Marshal(devs)
		return ResultPayload{Success: true, Data: data}

	case "sleep":
		var p SleepPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return ResultPayload{Success: false, Error: err.Error()}
		}
		if p.AdapterType == "ssh" {
			// SSH 适配器（契约 §6.3）：Agent 直连目标执行关机命令。
			observed, err := sleep.ExecuteSSH(ctx, sleep.SSHOptions{
				Host:               p.SSHHost,
				Port:               p.SSHPort,
				User:               p.SSHUser,
				Password:           p.SSHPassword,
				KnownHostKeySHA256: p.SSHKnownHostKey,
				TargetOS:           p.SSHTargetOS,
				Action:             p.Action,
				UseSudo:            p.SSHUseSudo,
			})
			if err != nil {
				return ResultPayload{Success: false, Error: err.Error()}
			}
			// 观测到的主机密钥指纹回传给服务端做 TOFU 持久化
			// （首次学习；已信任时服务端忽略）。
			data, _ := json.Marshal(map[string]string{"host_key_sha256": observed})
			return ResultPayload{Success: true, Data: data}
		}
		if err := sleep.Execute(ctx, p.AdapterType, p.Endpoint, p.Action); err != nil {
			return ResultPayload{Success: false, Error: err.Error()}
		}
		return ResultPayload{Success: true}

	default:
		return ResultPayload{Success: false, Error: "未知的任务类型: " + taskType}
	}
}
