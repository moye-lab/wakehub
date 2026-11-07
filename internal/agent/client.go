package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/wakehub/wakehub/internal/config"
	dbpkg "github.com/wakehub/wakehub/internal/db"
)

// agentVersion 是本 Agent 上报的版本号（握手头 X-Agent-Version）。
const agentVersion = "0.3.2"

// 重连与心跳常量（契约 §3.5）。
const (
	// maxReconnectBackoff 是指数退避上限。
	maxReconnectBackoff = 60 * time.Second
	// serverSilenceTimeout 是 Agent 判定连接已死的阈值：45s 未收到
	// 服务端任何消息（ack/任务/Ping）即主动重连。
	serverSilenceTimeout = 45 * time.Second
	// defaultAgentTaskTimeout 是单个任务的执行上限默认值（v0.2.9 问题 6）：
	// 配置 WAKEHUB_AGENT_TASK_TIMEOUT，默认 10s；服务端等待上限默认
	// 15s，比它多 5s，给回包留余量。零值配置回落本默认值，避免
	// WithTimeout(0) 立即过期。
	defaultAgentTaskTimeout = 10 * time.Second
	// maxConcurrentTasks 是单连接任务并发上限；超限直接回 agent busy。
	maxConcurrentTasks = 8
	// dedupWindow 是任务去重窗口；dedupCap 是缓存上限（超限淘汰最旧）。
	dedupWindow = 5 * time.Minute
	dedupCap    = 1000
)

var (
	// errRegistrationApproved 表示收到审批通过推送：已持久化新 token，须立即重连。
	errRegistrationApproved = errors.New("registration approved, reconnecting with new token")
	// errRegistrationRejected 表示注册被驳回：停止重连。
	errRegistrationRejected = errors.New("registration rejected")
)

type seenResult struct {
	res ResultPayload
	at  time.Time
}

type Client struct {
	cfg config.AgentConfig
	// writeMu 串行化对同一 WebSocket 连接的所有写操作：
	// gorilla/websocket 不允许并发 WriteJSON，心跳协程与任务回包协程
	// 必须经由同一把锁。
	writeMu sync.Mutex
	dialer  *websocket.Dialer

	mu sync.Mutex
	// tokenOverride 是 registration_approved 下发的新 token（内存态，
	// 同时持久化到 TokenFile）；优先于配置中的 token 使用。
	tokenOverride string
	// rejected 为 true 表示注册已被驳回，Run 停止重连。
	rejected bool

	// backoff 是当前重连退避（仅 Run 协程访问）：首次 1s，翻倍至
	// 上限 60s；连接成功后复位。
	backoff time.Duration

	// taskSem 限制单连接任务并发数。
	taskSem chan struct{}
	// seen 是最近处理过的任务 id → 结果缓存（防重复执行，契约 §3.5）。
	seenMu    sync.Mutex
	seen      map[string]seenResult
	seenOrder []string
	// inflight 是在途任务 id → 取消函数（服务端 cancel 消息用）。
	inflightMu sync.Mutex
	inflight   map[string]context.CancelFunc
}

func NewClient(cfg config.AgentConfig) *Client {
	// TLS：默认验证服务端证书，禁止默认跳过（契约 §3.5）；
	// 仅当显式配置 WAKEHUB_AGENT_TLS_SKIP_VERIFY=1 时才跳过。
	dialer := *websocket.DefaultDialer
	if cfg.TLSSkipVerify {
		dialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	// token 解析优先级：环境变量里的 per-agent token（wha_ 前缀）>
	// token 文件（审批下发的已持久化凭证）> 环境变量里的入网 token。
	// 入网 token 只用于首次注册；已获批的 Agent 重启后应走文件里的
	// per-agent token，否则会触发重入网、旧 token 作废。
	token := cfg.AgentToken
	if !strings.HasPrefix(token, "wha_") && cfg.TokenFile != "" {
		if b, err := os.ReadFile(cfg.TokenFile); err == nil {
			if t := strings.TrimSpace(string(b)); t != "" {
				token = t
			}
		}
	}
	cfg.AgentToken = token
	return &Client{
		cfg:      cfg,
		dialer:   &dialer,
		backoff:  time.Second,
		taskSem:  make(chan struct{}, maxConcurrentTasks),
		seen:     make(map[string]seenResult),
		inflight: make(map[string]context.CancelFunc),
	}
}

// writeJSON 是本客户端唯一的 WebSocket 写出口。
func (c *Client) writeJSON(conn *websocket.Conn, v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(writeDeadline))
	return conn.WriteJSON(v)
}

func (c *Client) isRejected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rejected
}

func (c *Client) setRejected(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rejected = v
}

// taskTimeout 返回单个任务的执行上限（v0.2.9 问题 6）：配置
// WAKEHUB_AGENT_TASK_TIMEOUT，默认 10s；零值配置回落默认。
func (c *Client) taskTimeout() time.Duration {
	if c.cfg.TaskTimeout > 0 {
		return c.cfg.TaskTimeout
	}
	return defaultAgentTaskTimeout
}

func (c *Client) currentToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tokenOverride != "" {
		return c.tokenOverride
	}
	return c.cfg.AgentToken
}

// jitteredBackoff 给退避加 ±25% 随机抖动，避免多 Agent 同时重连惊群。
func jitteredBackoff(d time.Duration) time.Duration {
	jitter := 0.75 + rand.Float64()*0.5
	return time.Duration(float64(d) * jitter)
}

// nextBackoff 是指数退避的单步递进：翻倍至上限 60s。
func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > maxReconnectBackoff {
		return maxReconnectBackoff
	}
	return d
}

func (c *Client) Run(ctx context.Context) {
	if c.cfg.TLSSkipVerify {
		log.Printf("[Agent] 警告：已配置跳过 TLS 证书验证（WAKEHUB_AGENT_TLS_SKIP_VERIFY=1），仅限测试环境使用")
	}
	for {
		if c.isRejected() {
			// 注册被驳回：停止重连，等待外部取消。
			<-ctx.Done()
			return
		}
		err := c.connectAndServe(ctx)
		if err == nil {
			return
		}
		if errors.Is(err, errRegistrationRejected) {
			c.setRejected(true)
			log.Printf("[Agent] 注册已被管理员驳回，停止重连")
			continue
		}
		if errors.Is(err, errRegistrationApproved) {
			// 已收到新 token 并持久化：立即重连，退避复位。
			c.backoff = time.Second
			continue
		}
		wait := jitteredBackoff(c.backoff)
		log.Printf("[Agent] 连接断开: %v，%v 后自动重连...", err, wait.Round(time.Second))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		c.backoff = nextBackoff(c.backoff)
	}
}

func (c *Client) connectAndServe(ctx context.Context) error {
	// 每连接独立子 ctx：连接关闭时取消全部在途任务（修复审计第 14 项）。
	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	headers := http.Header{}
	headers.Set("X-Agent-ID", c.cfg.AgentID)
	headers.Set("X-Agent-Name", c.cfg.AgentName)
	headers.Set("X-Agent-Token", c.currentToken())
	headers.Set("X-Agent-Subnet", c.cfg.Subnet)
	headers.Set("X-Agent-Version", agentVersion)
	headers.Set("X-Agent-OS", runtime.GOOS)
	headers.Set("X-Agent-Arch", runtime.GOARCH)

	conn, resp, err := c.dialer.DialContext(ctx, c.cfg.ServerWSURL, headers)
	if err != nil {
		if resp != nil {
			// 握手被拒（401/403 等）：服务端返回 JSON 信封，状态码即原因。
			log.Printf("[Agent] 握手被拒: HTTP %d", resp.StatusCode)
		}
		return err
	}
	defer conn.Close()
	// 连接成功：重连退避复位。
	c.backoff = time.Second
	log.Printf("[Agent] 已成功连接中心端: %s (节点ID: %s)", c.cfg.ServerWSURL, c.cfg.AgentID)

	// lastMsgAt 是最近一次收到服务端消息的时间（Unix 纳秒），
	// 用于 45s 静默判定。Pong 回复同样刷新（gorilla 默认处理 Ping，
	// PongHandler 在此刷新读超时与 lastMsgAt）。
	var lastMsgAt atomic.Int64
	lastMsgAt.Store(time.Now().UnixNano())
	conn.SetReadLimit(wsReadLimit)
	_ = conn.SetReadDeadline(time.Now().Add(serverSilenceTimeout))
	conn.SetPongHandler(func(string) error {
		lastMsgAt.Store(time.Now().UnixNano())
		_ = conn.SetReadDeadline(time.Now().Add(serverSilenceTimeout))
		return nil
	})

	sendChan := make(chan Message, 32)
	errChan := make(chan error, 2)

	// 写协程与定时心跳：所有写操作都走 c.writeJSON（带锁的唯一出口）。
	// 同时每 5s 检查服务端静默：45s 无消息即判定连接已死。
	go func() {
		hbTicker := time.NewTicker(c.cfg.HeartbeatInterval)
		defer hbTicker.Stop()
		silenceTicker := time.NewTicker(5 * time.Second)
		defer silenceTicker.Stop()
		for {
			select {
			case <-connCtx.Done():
				return
			case <-hbTicker.C:
				if err := c.writeJSON(conn, Message{ID: uuid.NewString(), Type: "heartbeat"}); err != nil {
					errChan <- err
					return
				}
			case <-silenceTicker.C:
				if time.Since(time.Unix(0, lastMsgAt.Load())) > serverSilenceTimeout {
					errChan <- errors.New("45s 未收到服务端消息，连接判定已死")
					return
				}
			case msg := <-sendChan:
				if err := c.writeJSON(conn, msg); err != nil {
					errChan <- err
					return
				}
			}
		}
	}()

	// 读协程：分发任务、注册消息与取消消息。
	go func() {
		for {
			var msg Message
			if err := conn.ReadJSON(&msg); err != nil {
				errChan <- err
				return
			}
			lastMsgAt.Store(time.Now().UnixNano())
			_ = conn.SetReadDeadline(time.Now().Add(serverSilenceTimeout))
			switch msg.Type {
			case "heartbeat_ack":
				continue
			case "registration_pending":
				log.Printf("[Agent] 注册待审批：等待管理员审批")
				continue
			case "registration_approved":
				var p RegistrationApprovedPayload
				if err := json.Unmarshal(msg.Payload, &p); err == nil && p.Token != "" {
					c.onRegistrationApproved(p.Token)
					errChan <- errRegistrationApproved
					return
				}
				continue
			case "registration_rejected":
				errChan <- errRegistrationRejected
				return
			case "cancel":
				var p CancelPayload
				if err := json.Unmarshal(msg.Payload, &p); err == nil {
					c.cancelTask(p.TaskID)
				}
				continue
			default:
				c.handleTask(connCtx, msg, sendChan)
			}
		}
	}()

	select {
	case <-ctx.Done():
		return nil
	case <-connCtx.Done():
		return nil
	case err := <-errChan:
		return err
	}
}

// onRegistrationApproved 持久化下发的新 token（0600）并切到新凭证。
// 下次连接即用新 token；本连接随即由调用方关闭并重连。
func (c *Client) onRegistrationApproved(token string) {
	c.mu.Lock()
	c.tokenOverride = token
	c.mu.Unlock()
	if c.cfg.TokenFile != "" {
		// v0.2.9 问题 11：token 所在目录 0700（已存在的旧目录也显式收紧）。
		// v0.2.11：收紧失败打警告（之前用 _ 吞掉了，755 目录静默留存）。
		if err := dbpkg.EnsurePrivateDir(filepath.Dir(c.cfg.TokenFile)); err != nil {
			log.Printf("[Agent] token 目录 %s 收紧失败: %v", filepath.Dir(c.cfg.TokenFile), err)
		}
		if err := os.WriteFile(c.cfg.TokenFile, []byte(token+"\n"), 0o600); err != nil {
			log.Printf("[Agent] 保存新凭证到 %s 失败: %v", c.cfg.TokenFile, err)
		}
	}
	log.Printf("[Agent] 注册已获批，新凭证已保存，正在重连...")
}

// handleTask 处理一条下发任务：去重 → 并发上限 → 异步执行 → 回包。
// 回包发送监听连接 ctx，连接断开时丢弃、不永久阻塞（修复审计第 14 项）。
func (c *Client) handleTask(connCtx context.Context, msg Message, sendChan chan<- Message) {
	if res, ok := c.lookupSeen(msg.ID); ok {
		// 5 分钟内已处理过的任务 id：直接回放缓存结果，不重复执行。
		c.sendResult(connCtx, sendChan, msg.ID, res)
		return
	}
	select {
	case c.taskSem <- struct{}{}:
	default:
		c.sendResult(connCtx, sendChan, msg.ID, ResultPayload{Success: false, Error: "agent busy"})
		return
	}
	go func() {
		defer func() { <-c.taskSem }()
		taskCtx, cancel := context.WithTimeout(connCtx, c.taskTimeout())
		c.registerInflight(msg.ID, cancel)
		defer func() {
			cancel()
			c.unregisterInflight(msg.ID)
		}()
		res := ExecuteRawTask(taskCtx, msg.Type, msg.Payload)
		c.rememberSeen(msg.ID, res)
		c.sendResult(connCtx, sendChan, msg.ID, res)
	}()
}

// sendResult 把任务结果发往写协程；连接已断时丢弃，不阻塞。
func (c *Client) sendResult(ctx context.Context, sendChan chan<- Message, id string, res ResultPayload) {
	rawRes, _ := json.Marshal(res)
	msg := Message{ID: id, Type: "result", Payload: rawRes}
	select {
	case sendChan <- msg:
	case <-ctx.Done():
	}
}

func (c *Client) lookupSeen(id string) (ResultPayload, bool) {
	c.seenMu.Lock()
	defer c.seenMu.Unlock()
	if r, ok := c.seen[id]; ok {
		if time.Since(r.at) < dedupWindow {
			return r.res, true
		}
		delete(c.seen, id)
	}
	return ResultPayload{}, false
}

func (c *Client) rememberSeen(id string, res ResultPayload) {
	c.seenMu.Lock()
	defer c.seenMu.Unlock()
	if _, ok := c.seen[id]; !ok {
		if len(c.seen) >= dedupCap {
			oldest := c.seenOrder[0]
			c.seenOrder = c.seenOrder[1:]
			delete(c.seen, oldest)
		}
		c.seenOrder = append(c.seenOrder, id)
	}
	c.seen[id] = seenResult{res: res, at: time.Now()}
}

func (c *Client) registerInflight(id string, cancel context.CancelFunc) {
	c.inflightMu.Lock()
	c.inflight[id] = cancel
	c.inflightMu.Unlock()
}

func (c *Client) unregisterInflight(id string) {
	c.inflightMu.Lock()
	delete(c.inflight, id)
	c.inflightMu.Unlock()
}

// cancelTask 取消一条在途任务（服务端 cancel 消息）。
func (c *Client) cancelTask(id string) {
	c.inflightMu.Lock()
	cancel, ok := c.inflight[id]
	c.inflightMu.Unlock()
	if ok {
		cancel()
	}
}
