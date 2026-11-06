package api

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/wakehub/wakehub/internal/agent"
	"github.com/wakehub/wakehub/internal/audit"
	"github.com/wakehub/wakehub/internal/auth"
	"github.com/wakehub/wakehub/internal/config"
	"github.com/wakehub/wakehub/internal/device"
	"github.com/wakehub/wakehub/internal/scanner"
	"github.com/wakehub/wakehub/internal/scheduler"
	"github.com/wakehub/wakehub/internal/secret"
	"github.com/wakehub/wakehub/internal/sleep"
	"github.com/wakehub/wakehub/internal/status"
	"github.com/wakehub/wakehub/internal/wol"
)

type Server struct {
	cfg       config.ServerConfig
	repo      *device.Repository
	authSvc   *auth.Service
	hub       *agent.Hub
	sched     *scheduler.Engine
	auditLog  *audit.Logger
	wsUpgrade websocket.Upgrader
	// scanTasks 是扫描任务的内存注册表（契约 §7.3）：扫描是一次性运维
	// 动作，不落库；服务重启后任务丢失。M4 负责扫描执行与进度更新，
	// M3 先落地导入接口所需的任务查询。
	scanTasks *scanTaskStore
	// startedAt 是进程启动时间（Dashboard uptime_seconds 用）。
	startedAt time.Time
	// sessionRateLimit 是通用 API 限流（契约 §1.5）：每个已登录会话
	// 每分钟最多 sessionRateLimit 次请求，超限 429。
	sessionRateLimit int
	// rateBuckets 是会话限流的计数桶（key 为会话 jti，见 allowSessionRequest）。
	rateMu      sync.Mutex
	rateBuckets map[string]*rateBucket
}

// rateBucket 是一个会话在一分钟窗口内的请求计数。
type rateBucket struct {
	count       int
	windowStart time.Time
}

// serverVersion 是服务端版本（Dashboard 与健康检查用，契约 §10/§12.2）。
const serverVersion = "0.2.11"

// defaultSessionRateLimit 是契约 §1.5 的通用 API 限流值（600 次/分钟/会话）。
const defaultSessionRateLimit = 600

type apiResponse struct {
	Code     int               `json:"code"`
	Message  string            `json:"message"`
	Data     any               `json:"data,omitempty"`
	Warnings []auth.FieldError `json:"warnings,omitempty"`
}

// scanTaskStatus 是扫描任务状态（契约 §7.1）。
type scanTaskStatus string

const (
	scanStatusRunning   scanTaskStatus = "running"
	scanStatusCompleted scanTaskStatus = "completed"
	scanStatusFailed    scanTaskStatus = "failed"
	scanStatusCancelled scanTaskStatus = "cancelled"
)

// scanProgress 是扫描进度（契约 §7.3）：total 不含网络地址 .0 与广播地址 .255。
type scanProgress struct {
	Scanned int `json:"scanned"`
	Total   int `json:"total"`
	Found   int `json:"found"`
	Failed  int `json:"failed"`
}

// discoveredDevice 是 v0.2.0 的扫描结果对象（契约 §7.4）。
type discoveredDevice struct {
	IP               string    `json:"ip"`
	MAC              string    `json:"mac"`
	Vendor           string    `json:"vendor"`
	FirstSeenAt      time.Time `json:"first_seen_at"`
	Exists           bool      `json:"exists"`
	ExistingDeviceID *string   `json:"existing_device_id"`
	AgentID          string    `json:"agent_id"`
	Subnet           string    `json:"subnet"`
}

// scanTask 是内存中的一次扫描任务（契约 §7.3）。
// 并发规则：创建后 CIDR/AgentID/ScanID/cancel 不再变化；其余字段只
// 能经 scanTaskStore 的方法改（store.mu 统一保护），对外一律经
// snapshot 取快照，禁止直接读写字段。
type scanTask struct {
	ScanID     string             `json:"scan_id"`
	AgentID    string             `json:"agent_id"`
	CIDR       string             `json:"cidr"`
	Status     scanTaskStatus     `json:"status"`
	Progress   scanProgress       `json:"progress"`
	Results    []discoveredDevice `json:"results"`
	StartedAt  time.Time          `json:"started_at"`
	FinishedAt *time.Time         `json:"finished_at,omitempty"`
	DurationMs int64              `json:"duration_ms"`
	Error      string             `json:"error"`
	cancel     context.CancelFunc `json:"-"`
}

// scanTaskStore 是扫描任务的内存注册表：保留最近 20 个已结束任务
// （completed/failed/cancelled），进行中的任务不限。线程安全。
type scanTaskStore struct {
	mu    sync.Mutex
	tasks map[string]*scanTask
	order []string // 按创建顺序排列的 scan_id，用于淘汰。
}

func newScanTaskStore() *scanTaskStore {
	return &scanTaskStore{tasks: make(map[string]*scanTask)}
}

// put 登记一个新任务（M4 创建扫描时调用）。
func (s *scanTaskStore) put(t *scanTask) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.Results == nil {
		t.Results = []discoveredDevice{}
	}
	s.tasks[t.ScanID] = t
	s.order = append(s.order, t.ScanID)
	s.trimLocked()
}

// trimLocked 淘汰多余的已结束任务，只保留最近 20 个。
// 运行中的任务永不淘汰；order 按时间顺序，删最旧的已结束任务。
func (s *scanTaskStore) trimLocked() {
	finished := 0
	for _, id := range s.order {
		if t := s.tasks[id]; t != nil && t.Status != scanStatusRunning {
			finished++
		}
	}
	for finished > 20 {
		idx := -1
		for i, id := range s.order {
			if t := s.tasks[id]; t != nil && t.Status != scanStatusRunning {
				idx = i
				break
			}
		}
		if idx < 0 {
			return
		}
		delete(s.tasks, s.order[idx])
		s.order = append(s.order[:idx], s.order[idx+1:]...)
		finished--
	}
}

// snapshot 返回任务的一份深拷贝快照（results 切片与 FinishedAt 均复制），
// 供 GET 接口序列化；runner 写入期间调用也不会读到半成品。
func (s *scanTaskStore) snapshot(id string) (scanTask, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return scanTask{}, false
	}
	cp := *t
	cp.Results = append([]discoveredDevice{}, t.Results...)
	if t.FinishedAt != nil {
		fin := *t.FinishedAt
		cp.FinishedAt = &fin
	}
	// 运行中的任务不暴露结果（契约 §7.3），失败任务结果为空。
	if cp.Status == scanStatusRunning || cp.Status == scanStatusFailed {
		cp.Results = []discoveredDevice{}
	}
	// 空结果必须返回 [] 而非 null（契约 §1.1：空列表永不返回 null）。
	// append(nil 切片) 在零元素时会返回 nil，上面三处都可能留 nil。
	if cp.Results == nil {
		cp.Results = []discoveredDevice{}
	}
	return cp, true
}

// finish 尝试把任务从 running 转为指定结束状态（先赢者协议）：
// runner 与取消接口只能一人成功；已结束的任务返回 false，调用方
// 不得再改状态。成功时填 FinishedAt 与 DurationMs 并触发淘汰。
func (s *scanTaskStore) finish(id string, status scanTaskStatus, errMsg string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return false
	}
	if t.Status != scanStatusRunning {
		return false
	}
	now := time.Now().UTC()
	t.Status = status
	t.Error = errMsg
	t.FinishedAt = &now
	t.DurationMs = now.Sub(t.StartedAt).Milliseconds()
	s.trimLocked()
	return true
}

// setProgress 更新进度（total 固定后只更新 scanned/found/failed）。
func (s *scanTaskStore) setProgress(id string, scanned, found, failed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return
	}
	t.Progress.Scanned = scanned
	t.Progress.Found = found
	t.Progress.Failed = failed
}

// setTotal 设定进度总数（runner 启动时调用一次）。
func (s *scanTaskStore) setTotal(id string, total int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return
	}
	t.Progress.Total = total
}

// addFound 追加一条已发现设备，按 MAC 去重（契约 §7.3）。
func (s *scanTaskStore) addFound(id string, d discoveredDevice) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return
	}
	for _, r := range t.Results {
		if strings.EqualFold(r.MAC, d.MAC) {
			return
		}
	}
	t.Results = append(t.Results, d)
}

// runningFor 返回某 Agent 当前进行中的扫描任务 ID（无则返回 ""）。
// 每 Agent 同时仅 1 个进行中扫描（契约 §7.3）。
func (s *scanTaskStore) runningFor(agentID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.order {
		t := s.tasks[id]
		if t == nil || t.AgentID != agentID {
			continue
		}
		if t.Status == scanStatusRunning {
			return id, true
		}
	}
	return "", false
}

// scanTaskSummary 是扫描任务的列表摘要（契约 §7.3：列表不含 results；
// 之前复用 scanTask 并把 Results 置 nil 会序列化出 "results":null，
// 违反 §1.1“空列表永不返回 null”，故单独立 struct）。
type scanTaskSummary struct {
	ScanID     string         `json:"scan_id"`
	AgentID    string         `json:"agent_id"`
	CIDR       string         `json:"cidr"`
	Status     scanTaskStatus `json:"status"`
	Progress   scanProgress   `json:"progress"`
	StartedAt  time.Time      `json:"started_at"`
	FinishedAt *time.Time     `json:"finished_at,omitempty"`
	DurationMs int64          `json:"duration_ms"`
	Error      string         `json:"error"`
}

// listSummaries 返回全部任务的摘要（按 started_at DESC，不含 results），
// 供 GET /api/v1/scan 列表用；分页由调用方做。
func (s *scanTaskStore) listSummaries() []scanTaskSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]scanTaskSummary, 0, len(s.order))
	// order 是创建升序，倒序遍历即 started_at DESC。
	for i := len(s.order) - 1; i >= 0; i-- {
		t := s.tasks[s.order[i]]
		if t == nil {
			continue
		}
		cp := scanTaskSummary{
			ScanID:     t.ScanID,
			AgentID:    t.AgentID,
			CIDR:       t.CIDR,
			Status:     t.Status,
			Progress:   t.Progress,
			StartedAt:  t.StartedAt,
			DurationMs: t.DurationMs,
			Error:      t.Error,
		}
		if t.FinishedAt != nil {
			fin := *t.FinishedAt
			cp.FinishedAt = &fin
		}
		out = append(out, cp)
	}
	return out
}

func NewServer(
	cfg config.ServerConfig,
	repo *device.Repository,
	authSvc *auth.Service,
	hub *agent.Hub,
	sched *scheduler.Engine,
	auditLog *audit.Logger,
) *Server {
	// M4：把配置的执行参数注入调度器（单设备 dispatch 超时、WOL 重发次数
	// WAKEHUB_WOL_RETRIES，D-N2）。Configure 忽略非正值，零值 cfg 时
	// 保持调度器默认值。
	if sched != nil {
		sched.Configure(cfg.TaskTimeout, cfg.DefaultRetries)
	}
	return &Server{
		cfg:              cfg,
		repo:             repo,
		authSvc:          authSvc,
		hub:              hub,
		sched:            sched,
		auditLog:         auditLog,
		scanTasks:        newScanTaskStore(),
		startedAt:        time.Now(),
		sessionRateLimit: defaultSessionRateLimit,
		rateBuckets:      make(map[string]*rateBucket),
		// Origin 策略（契约 §3.5）：Origin 为空时放行（非浏览器客户端，
		// 如 Agent）；Origin 非空时仅当与请求 Host 同源，或在
		// WAKEHUB_WS_ALLOWED_ORIGINS 白名单内时放行。Origin 校验不是
		// 身份认证，通过后仍须持有效 Agent token（M2 握手）。
		wsUpgrade: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				if origin == "" {
					return true
				}
				u, err := url.Parse(origin)
				if err != nil {
					return false
				}
				if strings.EqualFold(u.Host, r.Host) {
					return true
				}
				for _, allowed := range cfg.WSAllowedOrigins {
					if origin == strings.TrimSuffix(strings.TrimSpace(allowed), "/") {
						return true
					}
				}
				return false
			},
		},
	}
}

// recordAudit 写一条审计日志；写入失败时记系统日志，不得静默丢弃
// （契约 §9.1）。各 handler 一律经此 helper，不直接调 auditLog.Record。
func (s *Server) recordAudit(actor, action, targetType, targetID, detail, ip string) {
	if err := s.auditLog.Record(actor, action, targetType, targetID, detail, ip); err != nil {
		log.Printf("[Audit] 写入审计日志失败 %s: %v", action, err)
	}
}

// allowSessionRequest 是通用 API 限流（契约 §1.5）：每个已登录会话
// 每分钟最多 sessionRateLimit 次请求。限流键为会话 jti（M1 的 token
// payload 自带，同一用户多会话互不干扰；不要用源端口之类随机值做键——
// 那样限流永不触发，是 v0.1.0 登录限流的旧坑）。
func (s *Server) allowSessionRequest(jti string) bool {
	now := time.Now()
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	b, ok := s.rateBuckets[jti]
	if !ok || now.Sub(b.windowStart) >= time.Minute {
		s.rateBuckets[jti] = &rateBucket{count: 1, windowStart: now}
		s.pruneRateBucketsLocked(now)
		return true
	}
	b.count++
	return b.count <= s.sessionRateLimit
}

// pruneRateBucketsLocked 淘汰过期桶，防止 map 无限增长（桶数上限 10000，
// 超限时删最旧的过期桶；无过期桶时删任意一个最旧桶）。
func (s *Server) pruneRateBucketsLocked(now time.Time) {
	if len(s.rateBuckets) <= 10000 {
		return
	}
	for key, b := range s.rateBuckets {
		if now.Sub(b.windowStart) >= time.Minute {
			delete(s.rateBuckets, key)
		}
	}
	if len(s.rateBuckets) <= 10000 {
		return
	}
	for key := range s.rateBuckets {
		delete(s.rateBuckets, key)
		break
	}
}

// 请求 Body 上限 1 MiB（契约 §1.3），超限返回 413。
const maxRequestBodyBytes = 1 << 20

// 会话 Cookie 名（契约 §2.2）。
const sessionCookieName = "wakehub_session"

// withAuth 固定 401 文案（契约 §2.2，修复 B-N15）：不得外泄内部错误原文。
const authFailedMessage = "未登录或登录已过期"

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// 公开认证与 Agent WebSocket 注册接口
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("GET /api/v1/ws/agent", s.handleAgentWS)
	// 健康检查（契约 §12.2，M7）：公开、无需登录、只读、轻量，
	// 供 Docker HEALTHCHECK 与 compose depends_on 调用。
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)

	// 认证管理接口（须登录）
	mux.HandleFunc("POST /api/v1/auth/logout", s.withAuth(s.handleLogout))
	mux.HandleFunc("POST /api/v1/auth/change-password", s.withAuth(s.handleChangePassword))
	mux.HandleFunc("GET /api/v1/auth/me", s.withAuth(s.handleMe))

	// 受保护的 API v1 接口
	mux.HandleFunc("GET /api/v1/dashboard", s.withAuth(s.handleDashboard))
	mux.HandleFunc("GET /api/v1/devices", s.withAuth(s.handleListDevices))
	mux.HandleFunc("POST /api/v1/devices", s.withAuth(s.handleCreateDevice))
	mux.HandleFunc("POST /api/v1/devices/batch-wake", s.withAuth(s.handleBatchWake))
	mux.HandleFunc("GET /api/v1/devices/{id}", s.withAuth(s.handleGetDevice))
	mux.HandleFunc("PUT /api/v1/devices/{id}", s.withAuth(s.handleUpdateDevice))
	mux.HandleFunc("DELETE /api/v1/devices/{id}", s.withAuth(s.handleDeleteDevice))
	mux.HandleFunc("POST /api/v1/devices/{id}/wake", s.withAuth(s.handleWakeDevice))
	mux.HandleFunc("POST /api/v1/devices/{id}/check", s.withAuth(s.handleCheckDevice))
	mux.HandleFunc("POST /api/v1/devices/{id}/sleep", s.withAuth(s.handleSleepDevice))

	mux.HandleFunc("GET /api/v1/groups", s.withAuth(s.handleListGroups))
	mux.HandleFunc("POST /api/v1/groups", s.withAuth(s.handleCreateGroup))

	// 网络管理（v0.2.9 问题 2）。
	mux.HandleFunc("GET /api/v1/networks", s.withAuth(s.handleListNetworks))
	mux.HandleFunc("POST /api/v1/networks", s.withAuth(s.handleCreateNetwork))
	mux.HandleFunc("GET /api/v1/networks/{id}", s.withAuth(s.handleGetNetwork))
	mux.HandleFunc("PUT /api/v1/networks/{id}", s.withAuth(s.handleUpdateNetwork))
	mux.HandleFunc("DELETE /api/v1/networks/{id}", s.withAuth(s.handleDeleteNetwork))
	// Agent 管理路由（契约 §3.4，M2）：列表/预签发/详情/审批/驳回/
	// 吊销/重新签发/禁用/启用/删除/白名单管理。
	mux.HandleFunc("GET /api/v1/agents", s.withAuth(s.handleListAgents))
	mux.HandleFunc("POST /api/v1/agents", s.withAuth(s.handleCreateAgent))
	mux.HandleFunc("GET /api/v1/agents/{id}", s.withAuth(s.handleGetAgent))
	mux.HandleFunc("POST /api/v1/agents/{id}/approve", s.withAuth(s.handleApproveAgent))
	mux.HandleFunc("POST /api/v1/agents/{id}/reject", s.withAuth(s.handleRejectAgent))
	mux.HandleFunc("POST /api/v1/agents/{id}/revoke-token", s.withAuth(s.handleRevokeAgentToken))
	mux.HandleFunc("POST /api/v1/agents/{id}/reissue-token", s.withAuth(s.handleReissueAgentToken))
	mux.HandleFunc("POST /api/v1/agents/{id}/disable", s.withAuth(s.handleDisableAgent))
	mux.HandleFunc("POST /api/v1/agents/{id}/enable", s.withAuth(s.handleEnableAgent))
	mux.HandleFunc("DELETE /api/v1/agents/{id}", s.withAuth(s.handleDeleteAgent))
	mux.HandleFunc("POST /api/v1/agents/{id}/subnets", s.withAuth(s.handleAddAgentSubnet))
	mux.HandleFunc("DELETE /api/v1/agents/{id}/subnets", s.withAuth(s.handleRemoveAgentSubnet))
	mux.HandleFunc("GET /api/v1/agents/{id}/networks", s.withAuth(s.handleListAgentNetworks))
	mux.HandleFunc("PUT /api/v1/agents/{id}/networks", s.withAuth(s.handleSetAgentNetworks))
	mux.HandleFunc("POST /api/v1/scan", s.withAuth(s.handleNetworkScan))
	mux.HandleFunc("GET /api/v1/scan", s.withAuth(s.handleListScans))
	mux.HandleFunc("GET /api/v1/scan/{scan_id}", s.withAuth(s.handleGetScan))
	mux.HandleFunc("DELETE /api/v1/scan/{scan_id}", s.withAuth(s.handleCancelScan))
	mux.HandleFunc("POST /api/v1/scan/import", s.withAuth(s.handleScanImport))
	mux.HandleFunc("GET /api/v1/tasks", s.withAuth(s.handleListTasks))
	mux.HandleFunc("POST /api/v1/tasks", s.withAuth(s.handleCreateTask))
	mux.HandleFunc("PUT /api/v1/tasks/{id}", s.withAuth(s.handleUpdateTask))
	mux.HandleFunc("POST /api/v1/tasks/{id}/pause", s.withAuth(s.handlePauseTask))
	mux.HandleFunc("POST /api/v1/tasks/{id}/resume", s.withAuth(s.handleResumeTask))
	mux.HandleFunc("GET /api/v1/tasks/{id}/runs", s.withAuth(s.handleListTaskRuns))
	mux.HandleFunc("DELETE /api/v1/tasks/{id}", s.withAuth(s.handleDeleteTask))
	mux.HandleFunc("GET /api/v1/logs", s.withAuth(s.handleListLogs))

	// 前端静态文件托管
	mux.HandleFunc("/", s.handleSPA)
	// 全局请求 Body 上限（契约 §1.3）：超限的读取在解码时以
	// *http.MaxBytesError 失败，各 handler 据此返回 413。
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		mux.ServeHTTP(w, r)
	})
}

// checkStateChangingOrigin 是契约 §2.6 的 CSRF 防护：状态变更请求
// （POST/PUT/DELETE/PATCH）校验 Origin / Referer。Origin 与 Referer
// 均为空（非浏览器客户端）或与请求 Host 同源时放行，否则拒绝。
// GET 等读请求不受此限。
func (s *Server) checkStateChangingOrigin(r *http.Request) bool {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch:
	default:
		return true
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return strings.EqualFold(u.Host, r.Host)
	}
	if referer := r.Header.Get("Referer"); referer != "" {
		u, err := url.Parse(referer)
		if err != nil {
			return false
		}
		return strings.EqualFold(u.Host, r.Host)
	}
	return true
}

func (s *Server) withAuth(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.checkStateChangingOrigin(r) {
			writeJSON(w, http.StatusForbidden, 403, "跨站请求被拒绝", nil)
			return
		}
		// 只读 HttpOnly Cookie，不再读 Authorization 头（契约 §2.2，
		// 破坏性变更：Bearer 已废止）。
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			writeJSON(w, http.StatusUnauthorized, 401, authFailedMessage, nil)
			return
		}
		sess, err := s.authSvc.VerifyToken(cookie.Value)
		if err != nil {
			// 固定文案，不得外泄内部错误原文（B-N15）。
			writeJSON(w, http.StatusUnauthorized, 401, authFailedMessage, nil)
			return
		}
		// 通用 API 限流（契约 §1.5）：每会话每分钟最多 600 次，超限 429。
		if !s.allowSessionRequest(sess.JTI) {
			w.Header().Set("Retry-After", "60")
			writeJSON(w, http.StatusTooManyRequests, 429, "请求过于频繁，请稍后再试", map[string]string{"code": "rate_limited"})
			return
		}
		next(w, r.WithContext(auth.WithSession(r.Context(), sess)), sess.Username)
	}
}

// cookieSecure 决定会话 Cookie 是否附加 Secure（v0.2.9 问题 4，
// 两处下发点共用本函数）：请求本身走 HTTPS（含可信代理后的
// X-Forwarded-Proto=https）时附加；WAKEHUB_COOKIE_SECURE=1 强制附加
// （纯 HTTPS 部署的显式开关）；纯 HTTP 局域网请求不附加，否则浏览器
// 不接受 Cookie，局域网登录直接断掉。
func (s *Server) cookieSecure(r *http.Request) bool {
	return s.authSvc.RequestIsSecure(r) || s.cfg.CookieSecure
}

// setSessionCookie 下发会话 Cookie（契约 §2.2）：HttpOnly +
// SameSite=Lax；Secure 由 cookieSecure 决定。
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	cookie := &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400,
		Expires:  expires,
	}
	if s.cookieSecure(r) {
		cookie.Secure = true
	}
	http.SetCookie(w, cookie)
}

// clearSessionCookie 下发清除 Cookie（logout 与改密后）。
// Go 里 MaxAge < 0 才会写出 Max-Age=0（契约 §2.4）。
func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	cookie := &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0).UTC(),
	}
	if s.cookieSecure(r) {
		cookie.Secure = true
	}
	http.SetCookie(w, cookie)
}

// isBodyTooLarge 判断解码错误是否为 Body 超限（MaxBytesReader）。
func isBodyTooLarge(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}

// hubAgentResolver 把 Hub/Store 适配为 device.AgentResolver，供统一
// 操作校验链（契约 §4.4）与设备验证层（§4.2）查询 Agent 状态。
// Agent 行不存在时映射为 device.ErrAgentNotFound。
type hubAgentResolver struct {
	hub *agent.Hub
}

func (r hubAgentResolver) LookupAgent(id string) (string, bool, error) {
	a, err := r.hub.Store().GetAgent(id)
	if err != nil {
		if errors.Is(err, agent.ErrAgentNotFound) {
			return "", false, device.ErrAgentNotFound
		}
		return "", false, err
	}
	return a.ApprovalStatus, r.hub.IsConnected(id), nil
}

func (s *Server) agentResolver() device.AgentResolver {
	return hubAgentResolver{hub: s.hub}
}

// agentScopeCIDRs 取执行 Agent 的管理网段（v0.2.9 问题 7）：
// Issue 2 的管理网络（有 CIDR 的）与 Agent 的 subnet 字段取并集；
// 两者都没有时返回空——调用方只做基线校验，不拦截。
func (s *Server) agentScopeCIDRs(agentID string) []*net.IPNet {
	var out []*net.IPNet
	if networks, err := s.repo.ListAgentNetworks(agentID); err == nil {
		for _, n := range networks {
			if strings.TrimSpace(n.CIDR) == "" {
				continue
			}
			if _, ipnet, err := net.ParseCIDR(strings.TrimSpace(n.CIDR)); err == nil {
				out = append(out, ipnet)
			}
		}
	}
	if a, err := s.hub.Store().GetAgent(agentID); err == nil && strings.TrimSpace(a.Subnet) != "" {
		if _, ipnet, err := net.ParseCIDR(strings.TrimSpace(a.Subnet)); err == nil {
			out = append(out, ipnet)
		}
	}
	return out
}

// decodeOneJSON 从 r.Body 解码恰好一个 JSON 值；尾部还有第二个 JSON 值
// 或垃圾内容时拒绝请求（v0.2.9 问题 10），防止 `{"a":1} {"b":2}` 这类
// 拼接体被静默接受。allowEmpty 为 true 时空体视为成功（不写 dst）。
// 失败时已写好错误响应，调用方直接 return。
func (s *Server) decodeOneJSON(w http.ResponseWriter, r *http.Request, dst any, allowEmpty bool) bool {
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		if err == io.EOF && allowEmpty {
			return true
		}
		if isBodyTooLarge(err) {
			writeJSON(w, http.StatusRequestEntityTooLarge, 413, "请求体过大", nil)
			return false
		}
		writeJSON(w, http.StatusBadRequest, 400, "请求格式错误", nil)
		return false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			writeJSON(w, http.StatusBadRequest, 400, "请求体只能包含一个 JSON 对象", nil)
			return false
		}
		writeJSON(w, http.StatusBadRequest, 400, "请求体尾部存在非法内容", nil)
		return false
	}
	return true
}

// decodeJSONBody 解码请求体；超限返回 413，格式错误返回 400，成功返回 true。
// 空体（EOF）同样视为格式错误——调用方不得再默认执行任何操作（修复 B-N1）。
func (s *Server) decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	return s.decodeOneJSON(w, r, dst, false)
}

// writeJSONWithWarnings 在信封顶层附加 warnings 数组（契约 §4.3，
// 如 Agent 离线警告）；无警告时与 writeJSON 完全一致。
func writeJSONWithWarnings(w http.ResponseWriter, status int, code int, msg string, data any, warnings []auth.FieldError) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiResponse{
		Code:     code,
		Message:  msg,
		Data:     data,
		Warnings: warnings,
	})
}

// parsePagination 解析分页参数（契约 §1.4）：page 默认 1，page_size 默认 50、最大 200。
func parsePagination(r *http.Request) (page, pageSize int) {
	page = 1
	pageSize = 50
	if v := strings.TrimSpace(r.URL.Query().Get("page")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			page = n
		}
	}
	if v := strings.TrimSpace(r.URL.Query().Get("page_size")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			pageSize = n
			if pageSize > 200 {
				pageSize = 200
			}
		}
	}
	return page, pageSize
}

// verifyBoot 在发包成功后轮询检测设备是否上线（契约 §5.1/§5.2：
// sent ≠ booted）。每 2s 用设备的 status_method 回检一次，上限
// timeout；返回 boot_status（online/timeout/unknown）与回检时间。
// 客户端断开（ctx 取消）即停止。
func (s *Server) verifyBoot(ctx context.Context, d *device.Device, timeout time.Duration) (string, *time.Time) {
	deadline := time.Now().Add(timeout)
	lastState := ""
	for {
		checkCtx, cancel := context.WithTimeout(ctx, s.cfg.TaskTimeout)
		res, err := s.hub.Dispatch(checkCtx, d.AgentID, "check", agent.CheckPayload{
			IP:     d.IP,
			MAC:    d.MAC,
			Method: d.StatusMethod,
			Port:   d.StatusPort,
		})
		cancel()
		if err == nil {
			var st status.Result
			if json.Unmarshal(res.Data, &st) == nil {
				lastState = string(st.State)
				if st.State == status.StateOnline {
					now := time.Now().UTC()
					return "online", &now
				}
			} else {
				lastState = "unknown"
			}
		} else {
			lastState = "unknown"
		}
		now := time.Now()
		if now.After(deadline) || now.Equal(deadline) {
			break
		}
		// 等 2s 再下一轮；等待中响应 ctx 取消。
		wait := 2 * time.Second
		if remain := time.Until(deadline); remain < wait {
			wait = remain
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			if lastState == "unknown" {
				return "unknown", nil
			}
			return "timeout", nil
		case <-timer.C:
		}
	}
	if lastState == "unknown" {
		return "unknown", nil
	}
	return "timeout", nil
}

// wakeResult 是单设备/批量唤醒的每设备结果（契约 §5.2/§5.3）。
type wakeResult struct {
	DeviceID      string     `json:"device_id"`
	Name          string     `json:"name"`
	MAC           string     `json:"mac"`
	Status        string     `json:"status"` // sent | failed | skipped
	Reason        string     `json:"reason"`
	SentAt        *time.Time `json:"sent_at"`
	BootStatus    *string    `json:"boot_status"` // pending | online | timeout | unknown | null
	BootCheckedAt *time.Time `json:"boot_checked_at"`
}

func strPtr(s string) *string { return &s }

// dispatchWakeOnce 下发一次 WOL 魔术包；成功返回发送时间。
func (s *Server) dispatchWakeOnce(ctx context.Context, d *device.Device) (time.Time, error) {
	wakeCtx, cancel := context.WithTimeout(ctx, s.cfg.TaskTimeout)
	defer cancel()
	_, err := s.hub.Dispatch(wakeCtx, d.AgentID, "wake", agent.WakePayload{
		MAC:              d.MAC,
		BroadcastAddress: d.BroadcastAddress,
		Port:             d.WOLPort,
		Count:            s.cfg.DefaultRetries,
	})
	if err != nil {
		return time.Time{}, err
	}
	return time.Now().UTC(), nil
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.checkStateChangingOrigin(r) {
		writeJSON(w, http.StatusForbidden, 403, "跨站请求被拒绝", nil)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !s.decodeOneJSON(w, r, &req, false) {
		return
	}
	clientIP := s.authSvc.ClientIP(r)
	token, sess, err := s.authSvc.Authenticate(req.Username, req.Password, clientIP)
	if err != nil {
		if errors.Is(err, auth.ErrRateLimited) {
			s.recordAudit(req.Username, "login_failed", "user", req.Username, "登录失败次数过多，请稍后再试", clientIP)
			writeJSON(w, http.StatusTooManyRequests, 429, "登录失败次数过多，请稍后再试", map[string]string{"code": "rate_limited"})
			return
		}
		s.recordAudit(req.Username, "login_failed", "user", req.Username, "用户名或密码错误", clientIP)
		writeJSON(w, http.StatusUnauthorized, 401, "用户名或密码错误", nil)
		return
	}
	// 只发 HttpOnly Cookie，不再返回 token（契约 §2.1/§2.4，破坏性变更）。
	s.setSessionCookie(w, r, token, sess.ExpiresAt)
	s.recordAudit(req.Username, "login_success", "user", req.Username, "管理员登录成功", clientIP)
	writeJSON(w, http.StatusOK, 0, "ok", map[string]string{
		"username":   sess.Username,
		"expires_at": sess.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// handleLogout 把当前会话的 jti 写入撤销表并清除 Cookie（契约 §2.4）。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, _ string) {
	sess, ok := auth.SessionFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, 401, authFailedMessage, nil)
		return
	}
	_ = s.authSvc.RevokeSession(sess.JTI, sess.Username, sess.ExpiresAt)
	s.clearSessionCookie(w, r)
	s.recordAudit(sess.Username, "auth_logout", "user", sess.Username, "退出登录", s.authSvc.ClientIP(r))
	writeJSON(w, http.StatusOK, 0, "ok", nil)
}

// handleChangePassword 修改管理员密码（契约 §2.4）：成功后全部会话
// 立即失效（含当前会话），前端须跳登录页重新登录。
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request, _ string) {
	sess, ok := auth.SessionFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, 401, authFailedMessage, nil)
		return
	}
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !s.decodeOneJSON(w, r, &req, false) {
		return
	}
	if err := s.authSvc.ChangePassword(sess.Username, req.CurrentPassword, req.NewPassword); err != nil {
		var ve *auth.ValidationError
		if errors.As(err, &ve) {
			writeJSON(w, http.StatusBadRequest, 400, "密码修改失败", map[string]any{"errors": ve.Errors})
			return
		}
		if errors.Is(err, auth.ErrUserNotFound) {
			writeJSON(w, http.StatusBadRequest, 400, "用户不存在", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "密码修改失败", nil)
		return
	}
	s.clearSessionCookie(w, r)
	s.recordAudit(sess.Username, "auth_password_change", "user", sess.Username, "修改密码", s.authSvc.ClientIP(r))
	writeJSON(w, http.StatusOK, 0, "密码已修改，请重新登录", nil)
}

// handleMe 返回当前会话信息（契约 §2.4）。
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, _ string) {
	sess, ok := auth.SessionFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, 401, authFailedMessage, nil)
		return
	}
	writeJSON(w, http.StatusOK, 0, "ok", map[string]string{
		"username":   sess.Username,
		"role":       "admin",
		"issued_at":  sess.IssuedAt.UTC().Format(time.RFC3339),
		"expires_at": sess.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// handleAgentWS 是 Agent WS 端点（契约 §3.5，M2 重写）。
// 全局 Agent Token 已废止（契约 §0.1）：身份由每 Agent 独立 token
// 或入网 token 决定，X-Agent-ID 不再是唯一身份依据；`local` 保留，
// 远程冒认直接 403（修复 B-N8）。握手失败返回 JSON 信封，不用纯文本
// http.Error；Origin 校验由 Upgrader 的 CheckOrigin 负责（M1）。
func (s *Server) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	ip := s.authSvc.ClientIP(r)
	authRes, err := s.hub.AuthenticateAgent(
		r.Header.Get("X-Agent-Token"),
		r.Header.Get("X-Agent-ID"),
		r.Header.Get("X-Agent-Name"),
		r.Header.Get("X-Agent-Subnet"),
		r.Header.Get("X-Agent-Version"),
		r.Header.Get("X-Agent-OS"),
		r.Header.Get("X-Agent-Arch"),
		ip,
		s.cfg.EnrollToken,
	)
	if err != nil {
		var ae *agent.AgentAuthError
		if errors.As(err, &ae) {
			writeJSON(w, ae.Status, ae.Status, ae.Message, map[string]string{"code": ae.Code})
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "服务端错误", nil)
		return
	}
	if authRes.Created {
		s.recordAudit(authRes.AgentID, "agent_register", "agent", authRes.AgentID, "Agent 自助注册，等待审批", ip)
	} else if authRes.ReEnrolled {
		s.recordAudit(authRes.AgentID, "agent_register", "agent", authRes.AgentID, "Agent 重新入网，等待审批", ip)
	}
	conn, err := s.wsUpgrade.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.recordAudit(authRes.AgentID, "agent_connect", "agent", authRes.AgentID, "Agent 上线", ip)
	// RegisterConn 阻塞到连接断开。旧连接被新连接替换时它也会返回，
	// 此时 IsConnected 为 true，不记掉线审计。
	s.hub.RegisterConn(authRes.AgentID, conn, authRes.Approved)
	if !s.hub.IsConnected(authRes.AgentID) {
		s.recordAudit(authRes.AgentID, "agent_disconnect", "agent", authRes.AgentID, "Agent 掉线", ip)
	}
}

// handleHealth 是健康检查端点（契约 §12.2，M7）：公开、无需登录、
// 只读、轻量。db 为 ok/error（SELECT 1 探测，经 repo.Ping）。
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	dbStatus := "ok"
	if err := s.repo.Ping(); err != nil {
		dbStatus = "error"
		log.Printf("[API] 健康检查数据库探测失败: %v", err)
	}
	writeJSON(w, http.StatusOK, 0, "ok", map[string]any{
		"status":         "ok",
		"version":        serverVersion,
		"db":             dbStatus,
		"uptime_seconds": int64(time.Since(s.startedAt).Seconds()),
	})
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request, _ string) {
	stats, err := s.repo.GetDashboardStats()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	stats.ServerStatus = "ok"
	if err := s.repo.Ping(); err != nil {
		stats.DBStatus = "error"
		log.Printf("[API] Dashboard 数据库探测失败: %v", err)
	} else {
		stats.DBStatus = "ok"
	}
	stats.UptimeSeconds = int64(time.Since(s.startedAt).Seconds())
	stats.Version = serverVersion
	// 即将执行的任务真实计算（契约 §10，修复 v0.1.0 死数据）：
	// 已启用任务中 next_run_at 最近的 5 条。
	stats.UpcomingTasks = s.upcomingTasks()
	writeJSON(w, http.StatusOK, 0, "ok", stats)
}

// upcomingTasks 计算即将执行的任务：已启用任务按 Cron 算下次执行时间，
// 取最近的 5 条；目标名取设备名/分组名，目标已删则留空。
func (s *Server) upcomingTasks() []device.UpcomingTask {
	if s.sched == nil {
		return []device.UpcomingTask{}
	}
	items, err := s.repo.ListEnabledSchedules()
	if err != nil {
		log.Printf("[API] 查询已启用任务失败: %v", err)
		return []device.UpcomingTask{}
	}
	out := []device.UpcomingTask{}
	for _, item := range items {
		next, err := scheduler.NextRunAt(item.CronExpr, item.Timezone)
		if err != nil {
			continue
		}
		ut := device.UpcomingTask{
			ScheduleID: item.ID,
			Name:       item.Name,
			NextRunAt:  next,
		}
		if item.TargetType == "device" {
			if d, err := s.repo.GetDevice(item.TargetID); err == nil {
				ut.TargetName = d.Name
			}
		} else if item.TargetType == "group" {
			if groups, err := s.repo.ListGroups(); err == nil {
				for _, g := range groups {
					if g.ID == item.TargetID {
						ut.TargetName = g.Name
						break
					}
				}
			}
		}
		out = append(out, ut)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].NextRunAt.Before(*out[j].NextRunAt)
	})
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request, _ string) {
	q := r.URL.Query()
	page, pageSize := parsePagination(r)
	list, total, err := s.repo.ListDevicesPage(
		q.Get("group_id"), q.Get("q"), q.Get("status"), q.Get("agent_id"), q.Get("network_id"), page, pageSize,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, "查询设备列表失败", nil)
		return
	}
	writeJSON(w, http.StatusOK, 0, "ok", map[string]any{
		"items":     list,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// encryptSSHPassword 把 Device 上的 SSH 明文密码加密进密文列并清空明文。
// SleepType != "ssh" 时清空凭据（换适配器即丢密码）。调用方须在
// ValidateDevice 之后调用（校验需要看到明文或已存密文）；更新时
// 未给明文即保持原密文不动。
func (s *Server) encryptSSHPassword(d *device.Device) error {
	if d.SleepType != "ssh" {
		d.SSHPasswordEnc = ""
		d.SSHPassword = ""
		return nil
	}
	if d.SSHPassword == "" {
		return nil
	}
	enc, err := secret.Encrypt(s.cfg.SecretKey, d.SSHPassword)
	if err != nil {
		return err
	}
	d.SSHPasswordEnc = enc
	d.SSHPassword = ""
	return nil
}

func (s *Server) handleCreateDevice(w http.ResponseWriter, r *http.Request, actor string) {
	var d device.Device
	if !s.decodeJSONBody(w, r, &d) {
		return
	}
	res := device.ValidateDevice(s.repo, s.agentResolver(), &d, "")
	if res.HasErrors() {
		// MAC 冲突是 409（契约 §4.2），不是 400：校验层先发现时同样映射。
		if conflictErr, ok := macConflictError(res.Errors); ok {
			writeMACConflict(w, conflictErr)
			return
		}
		writeValidationError(w, "设备信息校验失败", res.Errors)
		return
	}
	// SSH 密码：校验通过后加密存入密文列，明文不落库、不进响应。
	if err := s.encryptSSHPassword(&d); err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, "SSH 密码加密失败", nil)
		return
	}
	if err := s.repo.CreateDevice(&d); err != nil {
		if errors.Is(err, device.ErrMACConflict) {
			writeMACConflict(w, auth.FieldError{Field: "mac", Code: "conflict", Message: "MAC 地址已被其他设备占用"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "创建设备失败", nil)
		return
	}
	d.SSHPasswordSet = d.SSHPasswordEnc != ""
	s.recordAudit(actor, "device_create", "device", d.ID, d.Name+" ("+d.MAC+")", r.RemoteAddr)
	writeJSONWithWarnings(w, http.StatusCreated, 0, "created", d, res.Warnings)
}

func (s *Server) handleGetDevice(w http.ResponseWriter, r *http.Request, _ string) {
	d, err := s.repo.GetDevice(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, 404, "设备不存在", nil)
		return
	}
	writeJSON(w, http.StatusOK, 0, "ok", d)
}

// handleUpdateDevice 是部分更新（契约 §4.3）：只更新请求体中出现的字段，
// 未出现的字段保持原值；成功返回重查后的数据库行（修复 B-N4），
// 不存在的设备返回 404（修复 B-N5）。
func (s *Server) handleUpdateDevice(w http.ResponseWriter, r *http.Request, actor string) {
	id := r.PathValue("id")
	orig, err := s.repo.GetDevice(id)
	if err != nil {
		if errors.Is(err, device.ErrDeviceNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "设备不存在", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "查询设备失败", nil)
		return
	}
	var raw map[string]json.RawMessage
	if !s.decodeJSONBody(w, r, &raw) {
		return
	}
	merged := *orig
	// 按 JSON key 存在性合并：显式传空字符串表示清空该字段；
	// 未出现的 key 保持原值。
	var strVal string
	var intVal int
	var boolVal bool
	for key, val := range raw {
		switch key {
		case "name", "mac", "ip", "agent_id", "broadcast_address",
			"device_type", "status_method", "group_id", "network_id",
			"sleep_type", "sleep_endpoint", "ssh_user", "ssh_password", "ssh_os", "remark":
			if err := json.Unmarshal(val, &strVal); err != nil {
				writeJSON(w, http.StatusBadRequest, 400, "字段 "+key+" 格式错误", nil)
				return
			}
			switch key {
			case "name":
				merged.Name = strVal
			case "mac":
				merged.MAC = strVal
			case "ip":
				merged.IP = strVal
			case "agent_id":
				merged.AgentID = strVal
			case "broadcast_address":
				merged.BroadcastAddress = strVal
			case "device_type":
				merged.DeviceType = strVal
			case "status_method":
				merged.StatusMethod = strVal
			case "group_id":
				merged.GroupID = strVal
			case "network_id":
				merged.NetworkID = strVal
			case "sleep_type":
				merged.SleepType = strVal
			case "sleep_endpoint":
				merged.SleepEndpoint = strVal
			case "ssh_user":
				merged.SSHUser = strVal
			case "ssh_os":
				merged.SSHOS = strVal
			case "ssh_password":
				// 非空才替换（同时清空主机指纹，改密码即重新信任主机）；
				// 空字符串表示保持原密码，不动密文列。
				if strVal != "" {
					merged.SSHPassword = strVal
					merged.SSHHostKeySHA256 = ""
				}
			case "remark":
				merged.Remark = strVal
			}
		case "wol_port", "status_port", "ssh_port":
			if err := json.Unmarshal(val, &intVal); err != nil {
				writeJSON(w, http.StatusBadRequest, 400, "字段 "+key+" 格式错误", nil)
				return
			}
			switch key {
			case "wol_port":
				merged.WOLPort = intVal
			case "status_port":
				merged.StatusPort = intVal
			case "ssh_port":
				merged.SSHPort = intVal
			}
		case "enabled":
			if err := json.Unmarshal(val, &boolVal); err != nil {
				writeJSON(w, http.StatusBadRequest, 400, "字段 enabled 格式错误", nil)
				return
			}
			merged.Enabled = &boolVal
		default:
			// 未知字段忽略（id/status/created_at 等服务端字段不接受客户端写入）。
		}
	}
	// 离开 SSH 适配器即清空已存凭据与主机信任（只保留用户名/端口，
	// 下次切回只需重填密码）。
	if merged.SleepType != "ssh" {
		merged.SSHPasswordEnc = ""
		merged.SSHHostKeySHA256 = ""
		merged.SSHPassword = ""
	}
	res := device.ValidateDevice(s.repo, s.agentResolver(), &merged, id)
	if res.HasErrors() {
		// MAC 冲突是 409（契约 §4.2），不是 400：校验层先发现时同样映射。
		if conflictErr, ok := macConflictError(res.Errors); ok {
			writeMACConflict(w, conflictErr)
			return
		}
		writeValidationError(w, "设备信息校验失败", res.Errors)
		return
	}
	// SSH 密码：校验通过后加密存入密文列，明文不落库、不进响应。
	if err := s.encryptSSHPassword(&merged); err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, "SSH 密码加密失败", nil)
		return
	}
	if err := s.repo.UpdateDevice(id, &merged); err != nil {
		if errors.Is(err, device.ErrDeviceNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "设备不存在", nil)
			return
		}
		if errors.Is(err, device.ErrMACConflict) {
			writeMACConflict(w, auth.FieldError{Field: "mac", Code: "conflict", Message: "MAC 地址已被其他设备占用"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "更新设备失败", nil)
		return
	}
	updated, err := s.repo.GetDevice(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, "更新后查询设备失败", nil)
		return
	}
	s.recordAudit(actor, "device_update", "device", id, updated.Name, r.RemoteAddr)
	writeJSONWithWarnings(w, http.StatusOK, 0, "updated", updated, res.Warnings)
}

func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request, actor string) {
	id := r.PathValue("id")
	if err := s.repo.DeleteDevice(id); err != nil {
		if errors.Is(err, device.ErrDeviceNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "设备不存在", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "删除设备失败", nil)
		return
	}
	s.recordAudit(actor, "device_delete", "device", id, "删除设备", r.RemoteAddr)
	writeJSON(w, http.StatusOK, 0, "deleted", nil)
}

// handleWakeDevice 是单设备唤醒（契约 §5.2）：校验链同 §4.4；
// sent ≠ booted——默认只证明魔术包已发出，verify=true 时再回检上线。
func (s *Server) handleWakeDevice(w http.ResponseWriter, r *http.Request, actor string) {
	id := r.PathValue("id")
	var req struct {
		Verify               bool `json:"verify"`
		VerifyTimeoutSeconds int  `json:"verify_timeout_seconds"`
	}
	// verify 参数可选：空体视为 verify=false。Body 格式错误时 400，
	// 但空体（EOF）在此处允许，因为 verify 本就是可选参数。
	// 注意：不用 r.ContentLength 判断空体——chunked 请求的
	// ContentLength 为 -1，会误判。
	if !s.decodeOneJSON(w, r, &req, true) {
		return
	}
	d, chkErr := device.ValidateOperation(s.repo, s.agentResolver(), id)
	if chkErr != nil {
		writeJSON(w, chkErr.HTTPStatus, chkErr.HTTPStatus, chkErr.Message, map[string]string{"code": chkErr.Code})
		return
	}
	verifyTimeout := 30
	if req.VerifyTimeoutSeconds > 0 {
		verifyTimeout = req.VerifyTimeoutSeconds
		if verifyTimeout < 1 {
			verifyTimeout = 1
		}
		if verifyTimeout > 120 {
			verifyTimeout = 120
		}
	}
	sentAt, err := s.dispatchWakeOnce(r.Context(), d)
	if err != nil {
		s.repo.RecordWakeTask(d.ID, d.AgentID, "manual", "failed", err.Error())
		s.recordAudit(actor, "device_wake_failed", "device", d.ID, err.Error(), r.RemoteAddr)
		writeJSON(w, http.StatusBadGateway, 502, "唤醒失败: "+dispatchErrMessage(err), nil)
		return
	}
	s.repo.RecordWakeTask(d.ID, d.AgentID, "manual", "success", "")
	s.recordAudit(actor, "device_wake", "device", d.ID, "发送 WOL 魔术包至 "+d.MAC, r.RemoteAddr)
	bootStatus := "pending"
	var bootCheckedAt *time.Time
	message := "WOL 魔术包已发送"
	if req.Verify {
		bs, checkedAt := s.verifyBoot(r.Context(), d, time.Duration(verifyTimeout)*time.Second)
		bootStatus = bs
		bootCheckedAt = checkedAt
		switch bs {
		case "online":
			message = "WOL 魔术包已发送，设备已上线"
		case "timeout":
			message = "魔术包已发送，但限时内未检测到设备上线"
		case "unknown":
			message = "魔术包已发送，但回检失败，无法确认设备是否上线"
		}
	}
	writeJSON(w, http.StatusOK, 0, message, map[string]any{
		"device_id":       d.ID,
		"mac":             d.MAC,
		"send_status":     "sent",
		"sent_at":         sentAt.UTC().Format(time.RFC3339),
		"boot_status":     bootStatus,
		"boot_checked_at": bootCheckedAt,
	})
}

// handleBatchWake 是批量唤醒（契约 §5.3）：per-device 结果、成功/失败/
// 跳过分别统计、批量任务 ID、耗时；并发上限 10，整体上限 120s；
// 客户端断开即取消剩余任务。
func (s *Server) handleBatchWake(w http.ResponseWriter, r *http.Request, actor string) {
	var req struct {
		DeviceIDs            []string `json:"device_ids"`
		Verify               bool     `json:"verify"`
		VerifyTimeoutSeconds int      `json:"verify_timeout_seconds"`
	}
	if !s.decodeJSONBody(w, r, &req) {
		return
	}
	if len(req.DeviceIDs) == 0 {
		writeValidationError(w, "请提供要唤醒的设备 ID 列表", []auth.FieldError{
			{Field: "device_ids", Code: "required", Message: "设备 ID 列表不能为空"},
		})
		return
	}
	if len(req.DeviceIDs) > 100 {
		writeValidationError(w, "批量唤醒设备数量超限", []auth.FieldError{
			{Field: "device_ids", Code: "out_of_range", Message: "单次批量唤醒最多 100 台设备"},
		})
		return
	}
	verifyTimeout := 30
	if req.VerifyTimeoutSeconds > 0 {
		verifyTimeout = req.VerifyTimeoutSeconds
		if verifyTimeout < 1 {
			verifyTimeout = 1
		}
		if verifyTimeout > 120 {
			verifyTimeout = 120
		}
	}
	batchID := uuid.NewString()
	startedAt := time.Now().UTC()
	// 整体上限 120s；客户端断开同样取消（ctx 传播）。
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	results := make([]wakeResult, len(req.DeviceIDs))
	seen := make(map[string]bool)
	sem := make(chan struct{}, 10)
	var wg sync.WaitGroup
	for i, id := range req.DeviceIDs {
		if seen[id] {
			results[i] = wakeResult{DeviceID: id, Status: "skipped", Reason: "duplicate"}
			continue
		}
		seen[id] = true
		wg.Add(1)
		go func(idx int, deviceID string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[idx] = wakeResult{DeviceID: deviceID, Status: "failed", Reason: "timeout"}
				return
			}
			defer func() { <-sem }()
			results[idx] = s.wakeOneInBatch(ctx, deviceID, req.Verify, verifyTimeout)
		}(i, id)
	}
	wg.Wait()
	finishedAt := time.Now().UTC()

	success, failed, skipped := 0, 0, 0
	for _, res := range results {
		switch res.Status {
		case "sent":
			success++
		case "failed":
			failed++
		case "skipped":
			skipped++
		}
	}
	s.recordAudit(actor, "device_batch_wake", "device", batchID,
		"批量唤醒：成功 "+itoa(success)+"，失败 "+itoa(failed)+"，跳过 "+itoa(skipped), r.RemoteAddr)
	writeJSON(w, http.StatusOK, 0, "批量唤醒执行完毕", map[string]any{
		"batch_id":    batchID,
		"total":       len(req.DeviceIDs),
		"success":     success,
		"failed":      failed,
		"skipped":     skipped,
		"results":     results,
		"started_at":  startedAt.Format(time.RFC3339),
		"finished_at": finishedAt.Format(time.RFC3339),
		"duration_ms": finishedAt.Sub(startedAt).Milliseconds(),
	})
}

func itoa(n int) string { return strconv.Itoa(n) }

// wakeOneInBatch 执行批量中的一台设备：统一校验链 → 发包 → 可选回检。
// 返回的 wakeResult 中 BootStatus 为 nil 表示“未回检”（skipped/failed），
// verify=false 且已发送时为 "pending"。
func (s *Server) wakeOneInBatch(ctx context.Context, deviceID string, verify bool, verifyTimeout int) wakeResult {
	d, chkErr := device.ValidateOperation(s.repo, s.agentResolver(), deviceID)
	if chkErr != nil {
		// 设备不存在/禁用 → skipped；Agent 问题 → failed（契约 §4.4）。
		return wakeResult{
			DeviceID: deviceID,
			Status:   chkErr.BatchStatus,
			Reason:   chkErr.Code,
		}
	}
	res := wakeResult{DeviceID: d.ID, Name: d.Name, MAC: d.MAC}
	sentAt, err := s.dispatchWakeOnce(ctx, d)
	if err != nil {
		s.repo.RecordWakeTask(d.ID, d.AgentID, "batch", "failed", err.Error())
		res.Status = "failed"
		res.Reason = err.Error()
		return res
	}
	s.repo.RecordWakeTask(d.ID, d.AgentID, "batch", "success", "")
	res.Status = "sent"
	res.SentAt = &sentAt
	if verify {
		bs, checkedAt := s.verifyBoot(ctx, d, time.Duration(verifyTimeout)*time.Second)
		res.BootStatus = strPtr(bs)
		res.BootCheckedAt = checkedAt
	} else {
		res.BootStatus = strPtr("pending")
	}
	return res
}

// handleCheckDevice 是设备状态检测（契约 §4.5）：校验链同 §4.4；
// 检测是同步完成的；检测异常（dispatch 失败、结果解析失败）返回 502
// 且不写脏状态入库（修复 B-N2），恢复检测前的原状态。
// 检测开始前先把设备行置为 checking（StateChecking 的真实用途），
// 成功或失败后都会被最终状态覆盖。
// 自动检测（周期检测器、页面刷新触发）通过可选 body {"auto": true}
// 标识：检测逻辑与手动完全一致，但不写 device_check /
// device_check_failed 审计——自动检测每轮每台都写审计会把日志刷爆，
// 状态本身已入库可查；手动点检测照旧写审计。
func (s *Server) handleCheckDevice(w http.ResponseWriter, r *http.Request, actor string) {
	id := r.PathValue("id")
	d, chkErr := device.ValidateOperation(s.repo, s.agentResolver(), id)
	if chkErr != nil {
		writeJSON(w, chkErr.HTTPStatus, chkErr.HTTPStatus, chkErr.Message, map[string]string{"code": chkErr.Code})
		return
	}
	auto := false
	if r.Body != nil {
		var req struct {
			Auto bool `json:"auto"`
		}
		// 宽容解析：无 body/非法 body 都按手动检测处理，不因此 400。
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			auto = req.Auto
		}
	}
	audit := func(event, detail string) {
		if !auto {
			s.recordAudit(actor, event, "device", d.ID, detail, r.RemoteAddr)
		}
	}
	prevStatus, prevErrReason := d.Status, d.LastStatusError
	_ = s.repo.UpdateDeviceStatus(d.ID, string(status.StateChecking), "")
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.TaskTimeout)
	defer cancel()

	res, err := s.hub.Dispatch(ctx, d.AgentID, "check", agent.CheckPayload{
		IP:     d.IP,
		MAC:    d.MAC,
		Method: d.StatusMethod,
		Port:   d.StatusPort,
	})
	if err != nil {
		// 检测异常不写脏状态：恢复检测前状态。
		_ = s.repo.UpdateDeviceStatus(d.ID, prevStatus, prevErrReason)
		audit("device_check_failed", "状态检测失败："+dispatchErrMessage(err))
		writeJSON(w, http.StatusBadGateway, 502, dispatchErrMessage(err), nil)
		return
	}
	var st status.Result
	if err := json.Unmarshal(res.Data, &st); err != nil {
		_ = s.repo.UpdateDeviceStatus(d.ID, prevStatus, prevErrReason)
		audit("device_check_failed", "检测结果解析失败")
		writeJSON(w, http.StatusBadGateway, 502, "检测结果解析失败", nil)
		return
	}
	switch st.State {
	case status.StateOnline, status.StateOffline, status.StateUnknown:
		// 合法状态才入库。注意：checking 是检测中的瞬时状态，不应
		// 出现在同步检测结果里（契约 §4.5），返回它即视为非法。
	default:
		_ = s.repo.UpdateDeviceStatus(d.ID, prevStatus, prevErrReason)
		audit("device_check_failed", "检测结果状态非法")
		writeJSON(w, http.StatusBadGateway, 502, "检测结果状态非法", nil)
		return
	}
	_ = s.repo.UpdateDeviceStatus(d.ID, string(st.State), st.ErrorReason)
	// 状态检测写 device_check 审计（契约 §4.5/§1.6，M5 补 v0.1.0 缺失项）。
	audit("device_check", "状态检测："+string(st.State)+"（"+st.Method+"）")
	writeJSON(w, http.StatusOK, 0, "ok", st)
}

// handleSleepDevice 是远程关机/睡眠（契约 §6）：校验链同 §4.4；
// Body 解码失败必须返回 400，禁止默认执行（修复 B-N1）；
// Agent 执行失败返回 502（v0.1.0 此处误用 400）。
func (s *Server) handleSleepDevice(w http.ResponseWriter, r *http.Request, actor string) {
	id := r.PathValue("id")
	var req struct {
		Action string `json:"action"`
	}
	if !s.decodeJSONBody(w, r, &req) {
		return
	}
	// 校验顺序（契约 §6）：先校验请求本身与设备自身配置（动作枚举、
	// sleep_type、端点），再走 §4.4 操作校验链（启用/Agent 状态）。
	// 请求/配置错误是 400，不应被 Agent 离线等状态错误掩盖。
	act, err := sleep.ValidateAction(req.Action)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	d, err := s.repo.GetDevice(id)
	if err != nil {
		if errors.Is(err, device.ErrDeviceNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "设备不存在", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "查询设备失败", nil)
		return
	}
	if d.SleepType == "none" {
		writeJSON(w, http.StatusBadRequest, 400, "设备未配置受控远程关机适配器", map[string]any{"errors": []auth.FieldError{
			{Field: "sleep_type", Code: "invalid_value", Message: "设备未配置受控远程关机适配器"},
		}})
		return
	}
	// 按适配器分支构造下发 payload：http_sol 沿用端点校验，
	// ssh 校验用户名/IP/已存密码（解密失败提示重新填写保存）。
	// v0.2.9 问题 7：两种适配器的目标都须落在执行 Agent 的管理网段内
	//（设备级目标即设备存的端点/IP，只认这一个，不接受调用方另传目标）。
	var payload agent.SleepPayload
	switch d.SleepType {
	case "http_sol":
		if err := sleep.ValidateEndpoint(d.SleepEndpoint); err != nil {
			writeJSON(w, http.StatusBadRequest, 400, "睡眠端点无效："+err.Error(), map[string]any{"errors": []auth.FieldError{
				{Field: "sleep_endpoint", Code: "invalid_value", Message: err.Error()},
			}})
			return
		}
		if err := sleep.ValidateEndpointInScope(d.SleepEndpoint, s.agentScopeCIDRs(d.AgentID)); err != nil {
			writeJSON(w, http.StatusBadRequest, 400, "睡眠端点不在执行 Agent 的管理网段内："+err.Error(), map[string]any{"errors": []auth.FieldError{
				{Field: "sleep_endpoint", Code: "invalid_value", Message: err.Error()},
			}})
			return
		}
		payload = agent.SleepPayload{AdapterType: "http_sol", Endpoint: d.SleepEndpoint, Action: act}
	case "ssh":
		if strings.TrimSpace(d.SSHUser) == "" {
			writeJSON(w, http.StatusBadRequest, 400, "SSH 用户名未配置", map[string]any{"errors": []auth.FieldError{
				{Field: "ssh_user", Code: "required", Message: "使用 SSH 适配器时必须提供 SSH 用户名"},
			}})
			return
		}
		if strings.TrimSpace(d.IP) == "" {
			writeJSON(w, http.StatusBadRequest, 400, "设备未配置 IP，SSH 关机需要目标 IP", map[string]any{"errors": []auth.FieldError{
				{Field: "ip", Code: "required", Message: "使用 SSH 适配器时必须提供设备 IP"},
			}})
			return
		}
		if err := sleep.ValidateIPInScope(d.IP, s.agentScopeCIDRs(d.AgentID)); err != nil {
			writeJSON(w, http.StatusBadRequest, 400, "SSH 目标不在执行 Agent 的管理网段内："+err.Error(), map[string]any{"errors": []auth.FieldError{
				{Field: "ip", Code: "invalid_value", Message: err.Error()},
			}})
			return
		}
		password, err := secret.Decrypt(s.cfg.SecretKey, d.SSHPasswordEnc)
		if err != nil || password == "" {
			writeJSON(w, http.StatusBadRequest, 400, "SSH 密码无法解密，请重新填写并保存", map[string]any{"errors": []auth.FieldError{
				{Field: "ssh_password", Code: "invalid_value", Message: "SSH 密码无法解密，请重新填写并保存"},
			}})
			return
		}
		port := d.SSHPort
		if port == 0 {
			port = 22
		}
		payload = agent.SleepPayload{
			AdapterType:     "ssh",
			SSHHost:         d.IP,
			SSHPort:         port,
			SSHUser:         d.SSHUser,
			SSHPassword:     password,
			SSHKnownHostKey: d.SSHHostKeySHA256,
			SSHTargetOS:     d.SSHOS,
			Action:          act,
		}
	default:
		writeJSON(w, http.StatusBadRequest, 400, "睡眠适配器类型无效", map[string]any{"errors": []auth.FieldError{
			{Field: "sleep_type", Code: "invalid_value", Message: "睡眠适配器类型无效"},
		}})
		return
	}
	if _, chkErr := device.ValidateOperation(s.repo, s.agentResolver(), id); chkErr != nil {
		writeJSON(w, chkErr.HTTPStatus, chkErr.HTTPStatus, chkErr.Message, map[string]string{"code": chkErr.Code})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.TaskTimeout)
	defer cancel()
	res, err := s.hub.Dispatch(ctx, d.AgentID, "sleep", payload)
	if err != nil {
		s.recordAudit(actor, "device_sleep_failed", "device", d.ID, err.Error(), r.RemoteAddr)
		writeJSON(w, http.StatusBadGateway, 502, dispatchErrMessage(err), nil)
		return
	}
	// TOFU：首次成功后保存 Agent 回传的主机密钥指纹；保存失败不影响主流程。
	if d.SleepType == "ssh" && d.SSHHostKeySHA256 == "" && res.Data != nil {
		var learned struct {
			HostKeySHA256 string `json:"host_key_sha256"`
		}
		if json.Unmarshal(res.Data, &learned) == nil && learned.HostKeySHA256 != "" {
			_ = s.repo.SetSSHHostKeyFingerprint(d.ID, learned.HostKeySHA256)
		}
	}
	s.recordAudit(actor, "device_sleep", "device", d.ID, "执行电源操作: "+string(act), r.RemoteAddr)
	writeJSON(w, http.StatusOK, 0, "远程关机/睡眠指令已执行", nil)
}

func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request, _ string) {
	groups, err := s.repo.ListGroups()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	// 分组数少，内存分页（契约 §11、§1.4；与 handleListTasks 同一模式）。
	page, pageSize := parsePagination(r)
	total := len(groups)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	writeJSON(w, http.StatusOK, 0, "ok", map[string]any{
		"items":     groups[start:end],
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request, actor string) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !s.decodeOneJSON(w, r, &req, false) {
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, 400, "分组名称不能为空", nil)
		return
	}
	g, err := s.repo.CreateGroup(req.Name, req.Description)
	if err != nil {
		if errors.Is(err, device.ErrGroupNameConflict) {
			writeJSON(w, http.StatusConflict, 409, "分组名称已存在", nil)
			return
		}
		writeJSON(w, http.StatusBadRequest, 400, "创建分组失败", nil)
		return
	}
	s.recordAudit(actor, "group_create", "group", g.ID, g.Name, r.RemoteAddr)
	writeJSON(w, http.StatusCreated, 0, "created", g)
}

// ---------------------------------------------------------------------------
// 网络管理（v0.2.9 问题 2）：networks + agent_networks
// ---------------------------------------------------------------------------

// resolveScanNetwork 按扫描网段解析设备归属网络：存在同 CIDR 的网络行
// 即用它，否则归属默认网络。
func (s *Server) resolveScanNetwork(cidr string) string {
	if n, err := s.repo.GetNetworkByCIDR(cidr); err == nil && n != nil {
		return n.ID
	}
	return device.DefaultNetworkID
}

func (s *Server) handleListNetworks(w http.ResponseWriter, r *http.Request, _ string) {
	networks, err := s.repo.ListNetworks()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, "查询网络列表失败", nil)
		return
	}
	// 网络数少，内存分页（与 handleListGroups 同一模式）。
	page, pageSize := parsePagination(r)
	total := len(networks)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	writeJSON(w, http.StatusOK, 0, "ok", map[string]any{
		"items":     networks[start:end],
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (s *Server) handleCreateNetwork(w http.ResponseWriter, r *http.Request, actor string) {
	var req struct {
		Name        string `json:"name"`
		CIDR        string `json:"cidr"`
		Description string `json:"description"`
	}
	if !s.decodeJSONBody(w, r, &req) {
		return
	}
	n := &device.Network{Name: req.Name, CIDR: req.CIDR, Description: req.Description}
	if err := s.repo.CreateNetwork(n); err != nil {
		if errors.Is(err, device.ErrNetworkCIDRConflict) {
			writeJSON(w, http.StatusConflict, 409, "该网段已被另一网络占用", nil)
			return
		}
		if errors.Is(err, device.ErrNetworkNameRequired) {
			writeJSON(w, http.StatusBadRequest, 400, "网络名称不能为空", nil)
			return
		}
		if errors.Is(err, device.ErrNetworkCIDRInvalid) {
			writeJSON(w, http.StatusBadRequest, 400, "网段格式错误，应为合法 CIDR", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "创建网络失败", nil)
		return
	}
	s.recordAudit(actor, "network_create", "network", n.ID, n.Name, r.RemoteAddr)
	writeJSON(w, http.StatusCreated, 0, "created", n)
}

func (s *Server) handleGetNetwork(w http.ResponseWriter, r *http.Request, _ string) {
	n, err := s.repo.GetNetwork(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, device.ErrNetworkNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "网络不存在", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "查询网络失败", nil)
		return
	}
	writeJSON(w, http.StatusOK, 0, "ok", n)
}

func (s *Server) handleUpdateNetwork(w http.ResponseWriter, r *http.Request, actor string) {
	id := r.PathValue("id")
	var req struct {
		Name        string `json:"name"`
		CIDR        string `json:"cidr"`
		Description string `json:"description"`
	}
	if !s.decodeJSONBody(w, r, &req) {
		return
	}
	n := &device.Network{Name: req.Name, CIDR: req.CIDR, Description: req.Description}
	if err := s.repo.UpdateNetwork(id, n); err != nil {
		if errors.Is(err, device.ErrNetworkNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "网络不存在", nil)
			return
		}
		if errors.Is(err, device.ErrNetworkCIDRConflict) {
			writeJSON(w, http.StatusConflict, 409, "该网段已被另一网络占用", nil)
			return
		}
		if errors.Is(err, device.ErrNetworkNameRequired) {
			writeJSON(w, http.StatusBadRequest, 400, "网络名称不能为空", nil)
			return
		}
		if errors.Is(err, device.ErrNetworkCIDRInvalid) {
			writeJSON(w, http.StatusBadRequest, 400, "网段格式错误，应为合法 CIDR", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "更新网络失败", nil)
		return
	}
	updated, err := s.repo.GetNetwork(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, "更新后查询网络失败", nil)
		return
	}
	s.recordAudit(actor, "network_update", "network", id, updated.Name, r.RemoteAddr)
	writeJSON(w, http.StatusOK, 0, "updated", updated)
}

func (s *Server) handleDeleteNetwork(w http.ResponseWriter, r *http.Request, actor string) {
	id := r.PathValue("id")
	if err := s.repo.DeleteNetwork(id); err != nil {
		if errors.Is(err, device.ErrNetworkNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "网络不存在", nil)
			return
		}
		if errors.Is(err, device.ErrNetworkDeleteDefault) {
			writeJSON(w, http.StatusBadRequest, 400, "默认网络不可删除", nil)
			return
		}
		if errors.Is(err, device.ErrNetworkInUse) {
			writeJSON(w, http.StatusConflict, 409, "网络仍被设备或 Agent 引用，请先移出设备并解除关联", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "删除网络失败", nil)
		return
	}
	s.recordAudit(actor, "network_delete", "network", id, "删除网络", r.RemoteAddr)
	writeJSON(w, http.StatusOK, 0, "deleted", nil)
}

func (s *Server) handleListAgentNetworks(w http.ResponseWriter, r *http.Request, _ string) {
	id := r.PathValue("id")
	if _, err := s.hub.Store().GetAgent(id); err != nil {
		if errors.Is(err, agent.ErrAgentNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "Agent 不存在", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "查询 Agent 失败", nil)
		return
	}
	networks, err := s.repo.ListAgentNetworks(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, "查询 Agent 网络失败", nil)
		return
	}
	writeJSON(w, http.StatusOK, 0, "ok", map[string]any{"agent_id": id, "networks": networks})
}

func (s *Server) handleSetAgentNetworks(w http.ResponseWriter, r *http.Request, actor string) {
	id := r.PathValue("id")
	if _, err := s.hub.Store().GetAgent(id); err != nil {
		if errors.Is(err, agent.ErrAgentNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "Agent 不存在", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "查询 Agent 失败", nil)
		return
	}
	var req struct {
		NetworkIDs []string `json:"network_ids"`
	}
	if !s.decodeJSONBody(w, r, &req) {
		return
	}
	if req.NetworkIDs == nil {
		req.NetworkIDs = []string{}
	}
	if err := s.repo.SetAgentNetworks(id, req.NetworkIDs); err != nil {
		if errors.Is(err, device.ErrNetworkNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "网络不存在", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "设置 Agent 网络失败", nil)
		return
	}
	networks, _ := s.repo.ListAgentNetworks(id)
	s.recordAudit(actor, "agent_update", "agent", id, "设置管理网络", r.RemoteAddr)
	writeJSON(w, http.StatusOK, 0, "ok", map[string]any{"agent_id": id, "networks": networks})
}

// agentResponse 是 Agent 管理接口的对外形态（契约 §3.4）。
// status 是审批状态与连接状态的合并值（§3.2）：
// disabled → pending → connected（approved 且在线）→ approved（approved 且离线）。
type agentResponse struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Subnet          string     `json:"subnet"`
	AllowedSubnets  []string   `json:"allowed_subnets"`
	Status          string     `json:"status"`
	Authorized      bool       `json:"authorized"`
	IP              string     `json:"ip"`
	OS              string     `json:"os"`
	Arch            string     `json:"arch"`
	Version         string     `json:"version"`
	LastSeenAt      *time.Time `json:"last_seen_at"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

// toAgentResponse 把存储行转为对外形态。authorized 是派生值
// （approval_status=approved 时为 true），标记废弃，仅供过渡期读取。
func (s *Server) toAgentResponse(a agent.Agent) agentResponse {
	var merged string
	switch a.ApprovalStatus {
	case "disabled":
		merged = "disabled"
	case "pending":
		merged = "pending"
	case "approved":
		if s.hub.IsConnected(a.ID) {
			merged = "connected"
		} else {
			merged = "approved"
		}
	default:
		merged = a.ApprovalStatus
	}
	if a.AllowedSubnets == nil {
		a.AllowedSubnets = []string{}
	}
	return agentResponse{
		ID:              a.ID,
		Name:            a.Name,
		Subnet:          a.Subnet,
		AllowedSubnets:  a.AllowedSubnets,
		Status:          merged,
		Authorized:      a.ApprovalStatus == "approved",
		IP:              a.IP,
		OS:              a.OS,
		Arch:            a.Arch,
		Version:         a.Version,
		LastSeenAt:      a.LastSeenAt,
		LastHeartbeatAt: a.LastHeartbeatAt,
		CreatedAt:       a.CreatedAt,
	}
}

// writeValidationError 写契约 §1.2 的结构化校验错误。
func writeValidationError(w http.ResponseWriter, message string, errs []auth.FieldError) {
	writeJSON(w, http.StatusBadRequest, 400, message, map[string]any{"errors": errs})
}

// macConflictError 从校验错误里找出 mac/conflict（契约 §4.2：重复 MAC → 409）。
func macConflictError(errs []auth.FieldError) (auth.FieldError, bool) {
	for _, e := range errs {
		if e.Field == "mac" && e.Code == "conflict" {
			return e, true
		}
	}
	return auth.FieldError{}, false
}

// writeMACConflict 写 409 MAC 冲突（契约 §4.2）。
func writeMACConflict(w http.ResponseWriter, conflictErr auth.FieldError) {
	writeJSON(w, http.StatusConflict, 409, conflictErr.Message, map[string]any{"errors": []auth.FieldError{conflictErr}})
}

// dispatchErrMessage 把 Agent 派发错误转成面向客户端的消息：
// 某些异常路径（如 Agent 回包为空）会产生空错误文本，绝不能原样
// 输出空 message。
func dispatchErrMessage(err error) string {
	if err == nil {
		return "Agent 任务执行失败"
	}
	if msg := strings.TrimSpace(err.Error()); msg != "" {
		return msg
	}
	return "Agent 任务执行失败"
}

// generateAgentID 生成服务端默认 Agent ID：`agent-` + 8 位随机小写字母数字。
func generateAgentID() (string, error) {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString("agent-")
	for _, v := range b {
		sb.WriteByte(letters[int(v)%len(letters)])
	}
	return sb.String(), nil
}

// validAllowedSubnet 校验扫描白名单网段（契约 §3.4/§7.2）：
// 须为私有 IPv4 CIDR 且掩码不小于 /24。返回规范化后的 CIDR。
func validAllowedSubnet(cidr string) (string, bool) {
	ip, ipnet, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil || ip.To4() == nil {
		return "", false
	}
	if !ip.IsPrivate() {
		return "", false
	}
	ones, _ := ipnet.Mask.Size()
	if ones < 24 {
		return "", false
	}
	return ipnet.String(), true
}

// decodeAgentBody 解码 Agent 管理接口的请求体；超限返回 413，格式错误返回 400。
func (s *Server) decodeAgentBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	return s.decodeOneJSON(w, r, dst, false)
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request, _ string) {
	agents, err := s.hub.Store().ListAgents()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, "查询 Agent 列表失败", nil)
		return
	}
	out := make([]agentResponse, 0, len(agents))
	for _, a := range agents {
		out = append(out, s.toAgentResponse(a))
	}
	writeJSON(w, http.StatusOK, 0, "ok", out)
}

// handleCreateAgent 是路径 A（管理员预签发，契约 §3.4）：创建即 approved，
// 并一次性返回明文 token（服务端永不再次返回该 token 明文）。
func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request, actor string) {
	var req struct {
		AgentID string `json:"agent_id"`
		Name    string `json:"name"`
		Subnet  string `json:"subnet"`
	}
	if !s.decodeAgentBody(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	var errs []auth.FieldError
	if req.Name == "" {
		errs = append(errs, auth.FieldError{Field: "name", Code: "required", Message: "Agent 名称不能为空"})
	} else if utf8.RuneCountInString(req.Name) > 64 {
		errs = append(errs, auth.FieldError{Field: "name", Code: "out_of_range", Message: "Agent 名称最长 64 个字符"})
	}
	id := strings.TrimSpace(req.AgentID)
	if id == "" {
		gen, err := generateAgentID()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, 500, "生成 Agent ID 失败", nil)
			return
		}
		id = gen
	} else if agent.ReservedAgentID(id) || !agent.ValidAgentID(id) {
		errs = append(errs, auth.FieldError{Field: "agent_id", Code: "invalid_value", Message: "Agent ID 格式错误或为保留值"})
	}
	subnet := ""
	if strings.TrimSpace(req.Subnet) != "" {
		_, ipnet, err := net.ParseCIDR(strings.TrimSpace(req.Subnet))
		if err != nil {
			errs = append(errs, auth.FieldError{Field: "subnet", Code: "invalid_format", Message: "网段 CIDR 格式错误"})
		} else {
			subnet = ipnet.String()
		}
	}
	if len(errs) > 0 {
		writeValidationError(w, "Agent 信息校验失败", errs)
		return
	}
	if _, err := s.hub.Store().GetAgent(id); err == nil {
		writeJSON(w, http.StatusConflict, 409, "Agent ID 已存在", map[string]any{"errors": []auth.FieldError{
			{Field: "agent_id", Code: "conflict", Message: "Agent ID 已存在"},
		}})
		return
	}
	a, token, err := s.hub.Store().CreateAgentApprovedWithToken(id, req.Name, subnet)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, "创建 Agent 失败", nil)
		return
	}
	s.recordAudit(actor, "agent_register", "agent", id, "管理员预签发 Agent "+req.Name, s.authSvc.ClientIP(r))
	writeJSON(w, http.StatusCreated, 0, "created", map[string]any{
		"agent": s.toAgentResponse(*a),
		"token": token,
	})
}

func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request, _ string) {
	a, err := s.hub.Store().GetAgent(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, agent.ErrAgentNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "Agent 不存在", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "查询 Agent 失败", nil)
		return
	}
	writeJSON(w, http.StatusOK, 0, "ok", s.toAgentResponse(*a))
}

// handleApproveAgent 审批通过（仅 pending 可审批）：签发新 token；
// 若该 Agent 正以 pending 模式连接，同时经 WS 推送 registration_approved。
func (s *Server) handleApproveAgent(w http.ResponseWriter, r *http.Request, actor string) {
	id := r.PathValue("id")
	if _, err := s.hub.Store().GetAgent(id); err != nil {
		if errors.Is(err, agent.ErrAgentNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "Agent 不存在", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "查询 Agent 失败", nil)
		return
	}
	token, err := s.hub.Store().ApproveAgent(id)
	if err != nil {
		if errors.Is(err, agent.ErrAgentNotPending) {
			a, _ := s.hub.Store().GetAgent(id)
			cur := ""
			if a != nil {
				cur = a.ApprovalStatus
			}
			writeJSON(w, http.StatusBadRequest, 400, "当前状态为 "+cur+"，无法审批", map[string]any{"errors": []auth.FieldError{
				{Field: "approval_status", Code: "invalid_value", Message: "仅待审批的 Agent 可以审批通过"},
			}})
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "审批 Agent 失败", nil)
		return
	}
	deliveredVia := "http"
	if err := s.hub.NotifyApproved(id, token); err == nil {
		deliveredVia = "websocket+http"
	}
	a, _ := s.hub.Store().GetAgent(id)
	s.recordAudit(actor, "agent_approve", "agent", id, "审批通过 Agent", s.authSvc.ClientIP(r))
	writeJSON(w, http.StatusCreated, 0, "ok", map[string]any{
		"agent":         s.toAgentResponse(*a),
		"token":         token,
		"delivered_via": deliveredVia,
	})
}

// handleRejectAgent 驳回（仅 pending 可驳回）：删除 Agent 行；
// 若正以 pending 模式连接，先发 registration_rejected 再以 4403 关闭 WS。
func (s *Server) handleRejectAgent(w http.ResponseWriter, r *http.Request, actor string) {
	id := r.PathValue("id")
	a, err := s.hub.Store().GetAgent(id)
	if err != nil {
		if errors.Is(err, agent.ErrAgentNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "Agent 不存在", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "查询 Agent 失败", nil)
		return
	}
	if a.ApprovalStatus != "pending" {
		writeJSON(w, http.StatusBadRequest, 400, "当前状态为 "+a.ApprovalStatus+"，无法驳回", map[string]any{"errors": []auth.FieldError{
			{Field: "approval_status", Code: "invalid_value", Message: "仅待审批的 Agent 可以驳回"},
		}})
		return
	}
	_ = s.hub.NotifyRejected(id)
	s.hub.CloseSession(id, 4403, "registration rejected")
	if err := s.hub.Store().DeleteAgent(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, "驳回 Agent 失败", nil)
		return
	}
	s.recordAudit(actor, "agent_reject", "agent", id, "驳回 Agent 注册", s.authSvc.ClientIP(r))
	writeJSON(w, http.StatusOK, 0, "deleted", nil)
}

// handleRevokeAgentToken 吊销当前 per-agent token（契约 §3.4）：
// 只吊销 token，不改审批状态；在线会话立即以 4403 断开。
// 吊销只影响该 Agent，其他 Agent 不受影响（M2 验收标准）。
func (s *Server) handleRevokeAgentToken(w http.ResponseWriter, r *http.Request, actor string) {
	id := r.PathValue("id")
	if _, err := s.hub.Store().GetAgent(id); err != nil {
		if errors.Is(err, agent.ErrAgentNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "Agent 不存在", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "查询 Agent 失败", nil)
		return
	}
	if err := s.hub.Store().RevokeToken(id); err != nil {
		if errors.Is(err, agent.ErrTokenNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "该 Agent 尚无可吊销的 token", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "吊销 token 失败", nil)
		return
	}
	s.hub.CloseSession(id, 4403, "token revoked")
	s.recordAudit(actor, "agent_revoke", "agent", id, "吊销 Agent token", s.authSvc.ClientIP(r))
	writeJSON(w, http.StatusOK, 0, "ok", map[string]any{"agent_id": id, "revoked": true})
}

// handleReissueAgentToken 重新签发 token（即轮换，契约 §3.3）：
// 原子替换旧哈希，旧 token 立即失效，无宽限期；在线会话随即关闭，
// Agent 用新 token 重连。
func (s *Server) handleReissueAgentToken(w http.ResponseWriter, r *http.Request, actor string) {
	id := r.PathValue("id")
	if _, err := s.hub.Store().GetAgent(id); err != nil {
		if errors.Is(err, agent.ErrAgentNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "Agent 不存在", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "查询 Agent 失败", nil)
		return
	}
	token, err := s.hub.Store().IssueToken(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, "重新签发 token 失败", nil)
		return
	}
	s.hub.CloseSession(id, 4403, "token reissued")
	s.recordAudit(actor, "agent_reissue", "agent", id, "重新签发 Agent token", s.authSvc.ClientIP(r))
	writeJSON(w, http.StatusCreated, 0, "ok", map[string]any{"agent_id": id, "token": token})
}

// handleDisableAgent 禁用（仅 approved 可禁用）：在线会话以 4403 立即断开；
// 其设备仍保留关联，唤醒等操作返回 502 agent_disabled（M3 校验链）。
func (s *Server) handleDisableAgent(w http.ResponseWriter, r *http.Request, actor string) {
	id := r.PathValue("id")
	a, err := s.hub.Store().GetAgent(id)
	if err != nil {
		if errors.Is(err, agent.ErrAgentNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "Agent 不存在", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "查询 Agent 失败", nil)
		return
	}
	if a.ApprovalStatus != "approved" {
		writeJSON(w, http.StatusBadRequest, 400, "当前状态为 "+a.ApprovalStatus+"，无法禁用", map[string]any{"errors": []auth.FieldError{
			{Field: "approval_status", Code: "invalid_value", Message: "仅已获批的 Agent 可以禁用"},
		}})
		return
	}
	if err := s.hub.Store().SetApprovalStatus(id, "disabled"); err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, "禁用 Agent 失败", nil)
		return
	}
	s.hub.CloseSession(id, 4403, "agent disabled")
	a, _ = s.hub.Store().GetAgent(id)
	s.recordAudit(actor, "agent_disable", "agent", id, "禁用 Agent", s.authSvc.ClientIP(r))
	writeJSON(w, http.StatusOK, 0, "ok", s.toAgentResponse(*a))
}

// handleEnableAgent 启用（仅 disabled 可启用）：恢复为 approved，token 不变；
// 若 token 已被吊销，须另行重新签发。
func (s *Server) handleEnableAgent(w http.ResponseWriter, r *http.Request, actor string) {
	id := r.PathValue("id")
	a, err := s.hub.Store().GetAgent(id)
	if err != nil {
		if errors.Is(err, agent.ErrAgentNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "Agent 不存在", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "查询 Agent 失败", nil)
		return
	}
	if a.ApprovalStatus != "disabled" {
		writeJSON(w, http.StatusBadRequest, 400, "当前状态为 "+a.ApprovalStatus+"，无法启用", map[string]any{"errors": []auth.FieldError{
			{Field: "approval_status", Code: "invalid_value", Message: "仅已禁用的 Agent 可以启用"},
		}})
		return
	}
	if err := s.hub.Store().SetApprovalStatus(id, "approved"); err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, "启用 Agent 失败", nil)
		return
	}
	a, _ = s.hub.Store().GetAgent(id)
	s.recordAudit(actor, "agent_enable", "agent", id, "启用 Agent", s.authSvc.ClientIP(r))
	writeJSON(w, http.StatusOK, 0, "ok", s.toAgentResponse(*a))
}

// handleDeleteAgent 删除 Agent 行、token、白名单（FK 级联）。
// 其设备不删除，agent_id 原样保留；后续操作返回 502 agent_not_found（M3）。
func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request, actor string) {
	id := r.PathValue("id")
	if _, err := s.hub.Store().GetAgent(id); err != nil {
		if errors.Is(err, agent.ErrAgentNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "Agent 不存在", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "查询 Agent 失败", nil)
		return
	}
	s.hub.CloseSession(id, websocket.CloseNormalClosure, "agent deleted")
	if err := s.hub.Store().DeleteAgent(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, "删除 Agent 失败", nil)
		return
	}
	s.recordAudit(actor, "agent_delete", "agent", id, "删除 Agent", s.authSvc.ClientIP(r))
	writeJSON(w, http.StatusOK, 0, "deleted", nil)
}

// handleAddAgentSubnet 向扫描白名单加一个网段（契约 §3.4/§7.2）。
func (s *Server) handleAddAgentSubnet(w http.ResponseWriter, r *http.Request, actor string) {
	id := r.PathValue("id")
	if _, err := s.hub.Store().GetAgent(id); err != nil {
		if errors.Is(err, agent.ErrAgentNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "Agent 不存在", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "查询 Agent 失败", nil)
		return
	}
	var req struct {
		CIDR string `json:"cidr"`
	}
	if !s.decodeAgentBody(w, r, &req) {
		return
	}
	normalized, ok := validAllowedSubnet(req.CIDR)
	if !ok {
		writeValidationError(w, "网段校验失败", []auth.FieldError{
			{Field: "cidr", Code: "invalid_cidr", Message: "网段须为私有 IPv4 CIDR 且掩码不小于 /24"},
		})
		return
	}
	if err := s.hub.Store().AddAllowedSubnet(id, normalized); err != nil {
		if errors.Is(err, agent.ErrSubnetExists) {
			writeJSON(w, http.StatusConflict, 409, "网段已在白名单内", map[string]any{"errors": []auth.FieldError{
				{Field: "cidr", Code: "conflict", Message: "网段已在白名单内"},
			}})
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "添加网段失败", nil)
		return
	}
	subnets, _ := s.hub.Store().ListAllowedSubnets(id)
	s.recordAudit(actor, "agent_update", "agent", id, "添加扫描白名单 "+normalized, s.authSvc.ClientIP(r))
	writeJSON(w, http.StatusOK, 0, "ok", map[string]any{"allowed_subnets": subnets})
}

// handleRemoveAgentSubnet 从扫描白名单删一个网段。
func (s *Server) handleRemoveAgentSubnet(w http.ResponseWriter, r *http.Request, actor string) {
	id := r.PathValue("id")
	if _, err := s.hub.Store().GetAgent(id); err != nil {
		if errors.Is(err, agent.ErrAgentNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "Agent 不存在", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "查询 Agent 失败", nil)
		return
	}
	var req struct {
		CIDR string `json:"cidr"`
	}
	if !s.decodeAgentBody(w, r, &req) {
		return
	}
	normalized, ok := validAllowedSubnet(req.CIDR)
	if !ok {
		writeValidationError(w, "网段校验失败", []auth.FieldError{
			{Field: "cidr", Code: "invalid_cidr", Message: "网段须为私有 IPv4 CIDR 且掩码不小于 /24"},
		})
		return
	}
	removed, err := s.hub.Store().RemoveAllowedSubnet(id, normalized)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, "删除网段失败", nil)
		return
	}
	if !removed {
		writeJSON(w, http.StatusNotFound, 404, "网段不在白名单内", nil)
		return
	}
	subnets, _ := s.hub.Store().ListAllowedSubnets(id)
	s.recordAudit(actor, "agent_update", "agent", id, "删除扫描白名单 "+normalized, s.authSvc.ClientIP(r))
	writeJSON(w, http.StatusOK, 0, "ok", map[string]any{"allowed_subnets": subnets})
}

// 扫描执行参数（契约 §7.3）。
const (
	// scanOverallTimeout 是单次扫描的整体上限（含全部主机的探测与重试）。
	scanOverallTimeout = 120 * time.Second
	// scanWorkers 是服务端逐台探测的并发度：低于 Agent 单连接任务
	// 上限 8，留 2 路给并发的唤醒等其他任务。
	scanWorkers = 6
	// scanProbeRetries 是 Agent 忙时单台主机的最大重试次数（不含首次）。
	scanProbeRetries = 2
	// scanProbeBackoff 是 Agent 忙重试的等待间隔（响应 ctx 取消）。
	scanProbeBackoff = 500 * time.Millisecond
)

// handleNetworkScan 创建一次异步扫描（契约 §7.2/§7.3）：
// 校验顺序：目标网段 → Agent 存在/审批 → 白名单 → Agent 在线 →
// 每 Agent 仅 1 个进行中扫描。全部通过后立即返回 202 + scan_id，
// 实际探测在后台进行，进度由 GET /api/v1/scan/{scan_id} 轮询。
func (s *Server) handleNetworkScan(w http.ResponseWriter, r *http.Request, actor string) {
	var req struct {
		AgentID string `json:"agent_id"`
		CIDR    string `json:"cidr"`
	}
	if !s.decodeJSONBody(w, r, &req) {
		return
	}
	agentID := strings.TrimSpace(req.AgentID)
	if agentID == "" {
		writeValidationError(w, "请指定执行扫描的 Agent", []auth.FieldError{
			{Field: "agent_id", Code: "required", Message: "必须选择执行扫描的 Agent"},
		})
		return
	}
	cidr := strings.TrimSpace(req.CIDR)
	if cidr == "" {
		writeValidationError(w, "请指定要扫描的受控局域网段 CIDR", []auth.FieldError{
			{Field: "cidr", Code: "required", Message: "必须指定要扫描的网段 CIDR"},
		})
		return
	}
	// 1. 目标网段：私有 IPv4、掩码 >= /24（公网、过大网段拒绝）。
	normalized, err := scanner.ValidateScanCIDR(cidr)
	if err != nil {
		writeValidationError(w, "网段校验失败", []auth.FieldError{
			{Field: "cidr", Code: "invalid_cidr", Message: err.Error()},
		})
		return
	}
	// 2. Agent 存在性与审批状态。
	ag, err := s.hub.Store().GetAgent(agentID)
	if err != nil {
		if errors.Is(err, agent.ErrAgentNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "Agent 不存在", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "Agent 查询失败", nil)
		return
	}
	switch ag.ApprovalStatus {
	case "pending":
		writeJSON(w, http.StatusBadRequest, 400, "Agent 尚未获批", map[string]any{"errors": []auth.FieldError{
			{Field: "agent_id", Code: "not_authorized", Message: "Agent 尚未获批，无法执行扫描"},
		}})
		return
	case "disabled":
		writeJSON(w, http.StatusBadRequest, 400, "Agent 已禁用", map[string]any{"errors": []auth.FieldError{
			{Field: "agent_id", Code: "agent_disabled", Message: "Agent 已禁用，无法执行扫描"},
		}})
		return
	case "approved":
	default:
		writeJSON(w, http.StatusBadRequest, 400, "Agent 状态异常", map[string]any{"errors": []auth.FieldError{
			{Field: "agent_id", Code: "not_authorized", Message: "Agent 状态异常：" + ag.ApprovalStatus},
		}})
		return
	}
	// 3. 白名单：请求网段须等于白名单某项或是其子网。
	allowed, err := s.hub.Store().ListAllowedSubnets(agentID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, "白名单查询失败", nil)
		return
	}
	if !scanner.CIDRWithinAllowed(normalized, allowed) {
		writeValidationError(w, "网段不在白名单内", []auth.FieldError{
			{Field: "cidr", Code: "cidr_not_allowed", Message: "目标网段不在该 Agent 的扫描白名单内"},
		})
		return
	}
	// 4. Agent 在线。
	if !s.hub.IsConnected(agentID) {
		writeJSON(w, http.StatusBadGateway, 502, "Agent 已离线", nil)
		return
	}
	// 5. 每 Agent 同时仅 1 个进行中扫描。
	if runningID, ok := s.scanTasks.runningFor(agentID); ok {
		writeJSON(w, http.StatusConflict, 409, "该 Agent 已有进行中的扫描", map[string]any{"scan_id": runningID})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), scanOverallTimeout)
	task := &scanTask{
		ScanID:    uuid.NewString(),
		AgentID:   agentID,
		CIDR:      normalized,
		Status:    scanStatusRunning,
		Results:   []discoveredDevice{},
		StartedAt: time.Now().UTC(),
		cancel:    cancel,
	}
	s.scanTasks.put(task)
	s.recordAudit(actor, "network_scan", "subnet", normalized, "扫描网段 "+normalized, r.RemoteAddr)
	go s.runScanTask(ctx, task)
	writeJSON(w, http.StatusAccepted, 0, "扫描已开始", map[string]any{
		"scan_id":    task.ScanID,
		"agent_id":   task.AgentID,
		"cidr":       task.CIDR,
		"status":     task.Status,
		"started_at": task.StartedAt,
	})
}

// runScanTask 是扫描任务的后台执行体（契约 §7.3）：服务端按主机逐个
// 发 /32 探测（复用现有 Dispatch("scan") 与 Agent 侧 ScanSubnet，
// 不改 Agent 协议），工作池并发 scanWorkers；每完成一台更新进度。
// failed 只计探测任务本身失败（dispatch 错误、Agent 忙耗尽重试等），
// 不计单台主机无响应/端口关闭（那是正常阴性结果）——与契约 §7.3
// 示例（found 6、failed 0）一致；字面“超时/拒绝均计入”与示例冲突，
// 此处裁决见 M4 报告，留待父代理/用户确认。
func (s *Server) runScanTask(ctx context.Context, task *scanTask) {
	hosts, err := scanner.HostsInCIDR(task.CIDR)
	if err != nil {
		// 创建时已校验，此处只做防御：不应发生。
		s.scanTasks.finish(task.ScanID, scanStatusFailed, err.Error())
		return
	}
	s.scanTasks.setTotal(task.ScanID, len(hosts))

	jobs := make(chan net.IP)
	var wg sync.WaitGroup
	var mu sync.Mutex
	scanned, found, failed := 0, 0, 0
	for w := 0; w < scanWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case host, ok := <-jobs:
					if !ok {
						return
					}
					n, f := s.probeHost(ctx, task, host)
					mu.Lock()
					scanned++
					found += n
					failed += f
					s.scanTasks.setProgress(task.ScanID, scanned, found, failed)
					mu.Unlock()
				}
			}
		}()
	}
feed:
	for _, host := range hosts {
		select {
		case <-ctx.Done():
			break feed
		case jobs <- host:
		}
	}
	close(jobs)
	wg.Wait()

	// 收尾：先赢者协议——若取消接口已把任务置为 cancelled，这里不再覆盖。
	if ctx.Err() == context.Canceled {
		s.scanTasks.finish(task.ScanID, scanStatusCancelled, "扫描已取消")
		return
	}
	if ctx.Err() == context.DeadlineExceeded {
		s.scanTasks.finish(task.ScanID, scanStatusFailed, "扫描超时")
		return
	}
	s.scanTasks.finish(task.ScanID, scanStatusCompleted, "")
}

// probeHost 探测一台主机（/32）：dispatch 成功时解析 Agent 返回的
// 设备并富化（first_seen_at、已存在识别、关联执行 Agent），按 MAC
// 去重后计入结果；dispatch 失败时仅当 Agent 忙才重试（最多
// scanProbeRetries 次，间隔响应 ctx 取消），其他失败直接计 failed。
// 返回 (发现设备数, 失败数)。
func (s *Server) probeHost(ctx context.Context, task *scanTask, host net.IP) (int, int) {
	var res agent.ResultPayload
	var err error
	// 单台探测超时：配置未设置时默认 10s（与 config 默认值一致），
	// 避免零值 cfg 下 WithTimeout(0) 立即过期导致全部探测失败。
	probeTimeout := s.cfg.TaskTimeout
	if probeTimeout <= 0 {
		probeTimeout = 10 * time.Second
	}
	for attempt := 0; attempt <= scanProbeRetries; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(scanProbeBackoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return 0, 0 // 扫描已取消/超时：不计入失败。
			case <-timer.C:
			}
		}
		pctx, cancel := context.WithTimeout(ctx, probeTimeout)
		res, err = s.hub.Dispatch(pctx, task.AgentID, "scan", agent.ScanPayload{CIDR: host.String() + "/32"})
		cancel()
		if err == nil {
			break
		}
		// 只有 Agent 忙才重试；其他错误（离线、超时等）直接失败。
		if !strings.Contains(err.Error(), "agent busy") {
			break
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return 0, 0 // 扫描已取消/超时：不计入失败。
		}
		return 0, 1
	}
	var discovered []scanner.DiscoveredDevice
	if err := json.Unmarshal(res.Data, &discovered); err != nil {
		return 0, 1
	}
	found := 0
	for _, d := range discovered {
		enriched := discoveredDevice{
			IP:          d.IP,
			MAC:         d.MAC,
			Vendor:      d.Vendor,
			FirstSeenAt: time.Now().UTC(),
			AgentID:     task.AgentID,
			Subnet:      task.CIDR,
		}
		// 已存在标记按扫描网段所在网络判断（v0.2.9 问题 2）：同一 MAC 在
		// 其他网络已存在时，本次扫描仍视为新设备。
		if existing, err := s.repo.GetDeviceByMACInNetwork(d.MAC, s.resolveScanNetwork(task.CIDR)); err == nil && existing != nil {
			enriched.Exists = true
			id := existing.ID
			enriched.ExistingDeviceID = &id
		}
		s.scanTasks.addFound(task.ScanID, enriched)
		found++
	}
	return found, 0
}

// handleListScans 列出扫描任务（契约 §1.4/§7.3）：按 started_at DESC，
// 分页信封，不含 results。
func (s *Server) handleListScans(w http.ResponseWriter, r *http.Request, _ string) {
	page, pageSize := parsePagination(r)
	all := s.scanTasks.listSummaries()
	total := len(all)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	writeJSON(w, http.StatusOK, 0, "ok", map[string]any{
		"items":     all[start:end],
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// handleGetScan 查询单个扫描任务（契约 §7.3）：运行中 results 为 []，
// failed 时 error 有原因且 results 为 []，cancelled 时保留已发现结果。
func (s *Server) handleGetScan(w http.ResponseWriter, r *http.Request, _ string) {
	scanID := strings.TrimSpace(r.PathValue("scan_id"))
	snap, ok := s.scanTasks.snapshot(scanID)
	if !ok {
		writeJSON(w, http.StatusNotFound, 404, "扫描任务不存在", nil)
		return
	}
	writeJSON(w, http.StatusOK, 0, "ok", snap)
}

// handleCancelScan 取消进行中的扫描（契约 §7.3）：先赢者协议——
// 已结束的任务报 400；取消后保留已发现结果，写 scan_cancel 审计。
func (s *Server) handleCancelScan(w http.ResponseWriter, r *http.Request, actor string) {
	scanID := strings.TrimSpace(r.PathValue("scan_id"))
	snap, ok := s.scanTasks.snapshot(scanID)
	if !ok {
		writeJSON(w, http.StatusNotFound, 404, "扫描任务不存在", nil)
		return
	}
	if snap.Status != scanStatusRunning {
		writeJSON(w, http.StatusBadRequest, 400, "任务已结束，无法取消", map[string]any{"errors": []auth.FieldError{
			{Field: "scan_id", Code: "invalid_value", Message: "任务已结束，无法取消"},
		}})
		return
	}
	if snap.cancel != nil {
		snap.cancel()
	}
	// finish 与 runner 收尾是先赢者协议：此处成功则状态为 cancelled；
	// 若 runner 已先完成，finish 返回 false，返回 runner 的最终快照。
	cancelled := s.scanTasks.finish(scanID, scanStatusCancelled, "扫描已取消")
	snap, _ = s.scanTasks.snapshot(scanID)
	if cancelled {
		s.recordAudit(actor, "scan_cancel", "scan", scanID, "取消扫描任务", r.RemoteAddr)
		writeJSON(w, http.StatusOK, 0, "扫描已取消", snap)
		return
	}
	writeJSON(w, http.StatusOK, 0, "ok", snap)
}

// handleScanImport 是扫描导入接口（契约 §7.3）：逐条 best-effort，按
// 规范化 MAC 去重；导入设备的 agent_id 强制为该扫描任务的执行 Agent，
// 禁止客户端指定其他 Agent。扫描任务进度/取消由 M4 补。
func (s *Server) handleScanImport(w http.ResponseWriter, r *http.Request, actor string) {
	var req struct {
		ScanID  string `json:"scan_id"`
		Devices []struct {
			IP   string `json:"ip"`
			MAC  string `json:"mac"`
			Name string `json:"name"`
		} `json:"devices"`
	}
	if !s.decodeJSONBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.ScanID) == "" {
		writeValidationError(w, "请指定扫描任务", []auth.FieldError{
			{Field: "scan_id", Code: "required", Message: "扫描任务 ID 不能为空"},
		})
		return
	}
	if len(req.Devices) == 0 {
		writeValidationError(w, "请提供要导入的设备", []auth.FieldError{
			{Field: "devices", Code: "required", Message: "导入设备列表不能为空"},
		})
		return
	}
	if len(req.Devices) > 100 {
		writeValidationError(w, "导入设备数量超限", []auth.FieldError{
			{Field: "devices", Code: "out_of_range", Message: "单次导入最多 100 台设备"},
		})
		return
	}
	task, ok := s.scanTasks.snapshot(strings.TrimSpace(req.ScanID))
	if !ok {
		writeJSON(w, http.StatusNotFound, 404, "扫描任务不存在", nil)
		return
	}
	if task.Status == scanStatusRunning {
		writeJSON(w, http.StatusBadRequest, 400, "扫描尚未完成", map[string]any{"errors": []auth.FieldError{
			{Field: "scan_id", Code: "invalid_value", Message: "扫描尚未完成，无法导入"},
		}})
		return
	}

	// 导入归属网络：扫描网段对应的网络行，否则默认网络（v0.2.9 问题 2）。
	networkID := s.resolveScanNetwork(task.CIDR)

	type importResult struct {
		IP       string  `json:"ip"`
		MAC      string  `json:"mac"`
		DeviceID *string `json:"device_id"`
		Status   string  `json:"status"` // created | skipped
		Reason   string  `json:"reason"`
	}
	results := make([]importResult, 0, len(req.Devices))
	imported, skipped := 0, 0
	for _, item := range req.Devices {
		mac, err := wol.ValidateMAC(item.MAC)
		if err != nil {
			results = append(results, importResult{IP: item.IP, MAC: item.MAC, Status: "skipped", Reason: "invalid_mac"})
			skipped++
			continue
		}
		ip := net.ParseIP(strings.TrimSpace(item.IP))
		if ip == nil || ip.To4() == nil {
			results = append(results, importResult{IP: item.IP, MAC: mac, Status: "skipped", Reason: "invalid_ip"})
			skipped++
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			// 默认命名 LAN-<IP 末段>（与 v0.1.0 实测一致）。
			parts := strings.Split(ip.String(), ".")
			name = "LAN-" + parts[len(parts)-1]
		} else if utf8.RuneCountInString(name) > 64 {
			results = append(results, importResult{IP: item.IP, MAC: mac, Status: "skipped", Reason: "invalid_name"})
			skipped++
			continue
		}
		if existing, err := s.repo.GetDeviceByMACInNetwork(mac, networkID); err == nil && existing != nil {
			results = append(results, importResult{IP: item.IP, MAC: mac, DeviceID: &existing.ID, Status: "skipped", Reason: "already_exists"})
			skipped++
			continue
		}
		d := &device.Device{
			Name: name, MAC: mac, IP: ip.String(),
			AgentID: task.AgentID, NetworkID: networkID,
		}
		if err := s.repo.CreateDevice(d); err != nil {
			if errors.Is(err, device.ErrMACConflict) {
				existing, _ := s.repo.GetDeviceByMACInNetwork(mac, networkID)
				var existingID *string
				if existing != nil {
					existingID = &existing.ID
				}
				results = append(results, importResult{IP: item.IP, MAC: mac, DeviceID: existingID, Status: "skipped", Reason: "already_exists"})
				skipped++
				continue
			}
			writeJSON(w, http.StatusInternalServerError, 500, "导入设备失败", nil)
			return
		}
		id := d.ID
		results = append(results, importResult{IP: item.IP, MAC: mac, DeviceID: &id, Status: "created", Reason: ""})
		imported++
	}
	s.recordAudit(actor, "scan_import", "scan", task.ScanID,
		"扫描导入：导入 "+itoa(imported)+"，跳过 "+itoa(skipped), r.RemoteAddr)
	writeJSON(w, http.StatusOK, 0, "导入完成", map[string]any{
		"scan_id":  task.ScanID,
		"agent_id": task.AgentID,
		"imported": imported,
		"skipped":  skipped,
		"results":  results,
	})
}

// scheduleRequest 是任务创建/更新的请求体：RetryCount、Enabled、RunOnce
// 用指针区分“未传”（创建时用默认值 / 更新时保留原值）与“显式传值”
// （如 retry_count=0 表示不重试，enabled=false 表示创建即暂停）。
type scheduleRequest struct {
	Name       string `json:"name"`
	CronExpr   string `json:"cron_expr"`
	Timezone   string `json:"timezone"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	Action     string `json:"action"`
	RetryCount *int   `json:"retry_count"`
	Enabled    *bool  `json:"enabled"`
	RunOnce    *bool  `json:"run_once"`
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request, _ string) {
	page, pageSize := parsePagination(r)
	views, err := s.sched.ListScheduleViews()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, "任务列表查询失败", nil)
		return
	}
	total := len(views)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	writeJSON(w, http.StatusOK, 0, "ok", map[string]any{
		"items":     views[start:end],
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request, actor string) {
	var req scheduleRequest
	if !s.decodeJSONBody(w, r, &req) {
		return
	}
	item := device.ScheduleItem{
		Name: req.Name, CronExpr: req.CronExpr, Timezone: req.Timezone,
		TargetType: req.TargetType, TargetID: req.TargetID, Action: req.Action,
		Enabled: true,
	}
	// retry_count 未传时默认 1（契约 §8.1）；显式传 0 表示不重试。
	if req.RetryCount != nil {
		item.RetryCount = *req.RetryCount
	} else {
		item.RetryCount = 1
	}
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	if errs := scheduler.ValidateSchedule(s.repo, &item); len(errs) > 0 {
		writeValidationError(w, "任务校验失败", errs)
		return
	}
	runOnce := req.RunOnce != nil && *req.RunOnce
	if err := s.sched.CreateSchedule(&item, runOnce); err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, "创建任务失败", nil)
		return
	}
	_ = s.sched.Reload()
	s.recordAudit(actor, "schedule_create", "schedule", item.ID, item.Name+" ("+item.CronExpr+")", r.RemoteAddr)
	view, err := s.sched.GetScheduleView(item.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, "创建任务失败", nil)
		return
	}
	writeJSON(w, http.StatusCreated, 0, "created", view)
}

// handleUpdateTask 部分更新任务（契约 §8.1）：只覆盖请求里出现的字段，
// 其余保留原值；合并后全量校验，不存在返回 404。
func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request, actor string) {
	id := strings.TrimSpace(r.PathValue("id"))
	cur, err := s.sched.GetScheduleView(id)
	if err != nil {
		if errors.Is(err, device.ErrScheduleNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "定时任务不存在", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "任务查询失败", nil)
		return
	}
	var req scheduleRequest
	if !s.decodeJSONBody(w, r, &req) {
		return
	}
	item := cur.ScheduleItem
	runOnce := cur.RunOnce
	if req.Name != "" {
		item.Name = req.Name
	}
	if req.CronExpr != "" {
		item.CronExpr = req.CronExpr
	}
	if req.Timezone != "" {
		item.Timezone = req.Timezone
	}
	if req.TargetType != "" {
		item.TargetType = req.TargetType
	}
	if req.TargetID != "" {
		item.TargetID = req.TargetID
	}
	if req.Action != "" {
		item.Action = req.Action
	}
	if req.RetryCount != nil {
		item.RetryCount = *req.RetryCount
	}
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	if req.RunOnce != nil {
		runOnce = *req.RunOnce
	}
	if errs := scheduler.ValidateSchedule(s.repo, &item); len(errs) > 0 {
		writeValidationError(w, "任务校验失败", errs)
		return
	}
	if err := s.sched.UpdateSchedule(&item, runOnce); err != nil {
		if errors.Is(err, device.ErrScheduleNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "定时任务不存在", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "更新任务失败", nil)
		return
	}
	_ = s.sched.Reload()
	s.recordAudit(actor, "schedule_update", "schedule", id, "更新定时任务 "+item.Name, r.RemoteAddr)
	view, err := s.sched.GetScheduleView(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, "更新任务失败", nil)
		return
	}
	writeJSON(w, http.StatusOK, 0, "ok", view)
}

// handlePauseTask 暂停任务（契约 §8.2）：已是目标态时报 400。
func (s *Server) handlePauseTask(w http.ResponseWriter, r *http.Request, actor string) {
	s.setTaskEnabled(w, r, actor, false, "schedule_pause", "暂停定时任务")
}

// handleResumeTask 恢复任务（契约 §8.2）：已是目标态时报 400。
func (s *Server) handleResumeTask(w http.ResponseWriter, r *http.Request, actor string) {
	s.setTaskEnabled(w, r, actor, true, "schedule_resume", "恢复定时任务")
}

func (s *Server) setTaskEnabled(w http.ResponseWriter, r *http.Request, actor string, enabled bool, event, verb string) {
	id := strings.TrimSpace(r.PathValue("id"))
	cur, err := s.sched.GetScheduleView(id)
	if err != nil {
		if errors.Is(err, device.ErrScheduleNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "定时任务不存在", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "任务查询失败", nil)
		return
	}
	if cur.Enabled == enabled {
		state := "已暂停"
		if enabled {
			state = "已启用"
		}
		writeJSON(w, http.StatusBadRequest, 400, "任务"+state, map[string]any{"errors": []auth.FieldError{
			{Field: "enabled", Code: "invalid_value", Message: "任务" + state + "，无需重复操作"},
		}})
		return
	}
	if err := s.sched.SetScheduleEnabled(id, enabled); err != nil {
		if errors.Is(err, device.ErrScheduleNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "定时任务不存在", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "更新任务失败", nil)
		return
	}
	_ = s.sched.Reload()
	s.recordAudit(actor, event, "schedule", id, verb+" "+cur.Name, r.RemoteAddr)
	view, err := s.sched.GetScheduleView(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, "更新任务失败", nil)
		return
	}
	writeJSON(w, http.StatusOK, 0, "ok", view)
}

// handleListTaskRuns 查询任务的执行历史（契约 §8.3）：分页信封，
// 每条 run 含每设备独立结果；任务不存在返回 404。
func (s *Server) handleListTaskRuns(w http.ResponseWriter, r *http.Request, _ string) {
	id := strings.TrimSpace(r.PathValue("id"))
	if _, err := s.sched.GetScheduleView(id); err != nil {
		if errors.Is(err, device.ErrScheduleNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "定时任务不存在", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "任务查询失败", nil)
		return
	}
	page, pageSize := parsePagination(r)
	runs, total, err := s.sched.ListRuns(id, page, pageSize)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, "执行历史查询失败", nil)
		return
	}
	writeJSON(w, http.StatusOK, 0, "ok", map[string]any{
		"items":     runs,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request, actor string) {
	id := r.PathValue("id")
	if err := s.repo.DeleteSchedule(id); err != nil {
		if errors.Is(err, device.ErrScheduleNotFound) {
			writeJSON(w, http.StatusNotFound, 404, "定时任务不存在", nil)
			return
		}
		writeJSON(w, http.StatusInternalServerError, 500, "删除任务失败", nil)
		return
	}
	_ = s.sched.Reload()
	s.recordAudit(actor, "schedule_delete", "schedule", id, "删除定时任务", r.RemoteAddr)
	writeJSON(w, http.StatusOK, 0, "deleted", nil)
}

func (s *Server) handleListLogs(w http.ResponseWriter, r *http.Request, _ string) {
	q := r.URL.Query()
	f := audit.Filter{
		TargetType: strings.TrimSpace(q.Get("target_type")),
		TargetID:   strings.TrimSpace(q.Get("target_id")),
		Actor:      strings.TrimSpace(q.Get("actor")),
		Result:     strings.TrimSpace(q.Get("result")),
	}
	if v := strings.TrimSpace(q.Get("from")); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, 400, "from 须为 RFC 3339 时间", nil)
			return
		}
		f.From = &t
	}
	if v := strings.TrimSpace(q.Get("to")); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, 400, "to 须为 RFC 3339 时间", nil)
			return
		}
		f.To = &t
	}
	if v := strings.TrimSpace(q.Get("action")); v != "" {
		for _, a := range strings.Split(v, ",") {
			if a = strings.TrimSpace(a); a != "" {
				f.Actions = append(f.Actions, a)
			}
		}
	}
	if f.Result != "" && f.Result != "success" && f.Result != "failed" {
		writeJSON(w, http.StatusBadRequest, 400, "result 须为 success 或 failed", nil)
		return
	}
	page, pageSize := parsePagination(r)
	logs, total, err := s.auditLog.ListFiltered(f, page, pageSize)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, "查询操作日志失败", nil)
		return
	}
	writeJSON(w, http.StatusOK, 0, "ok", map[string]any{
		"items":     logs,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	fullPath := filepath.Join(s.cfg.WebDistPath, filepath.Clean(r.URL.Path))
	if info, err := os.Stat(fullPath); err == nil && !info.IsDir() {
		http.ServeFile(w, r, fullPath)
		return
	}
	indexFile := filepath.Join(s.cfg.WebDistPath, "index.html")
	if _, err := os.Stat(indexFile); err == nil {
		http.ServeFile(w, r, indexFile)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("WakeHub Server is running. Build frontend inside ./web or use Docker Compose."))
}

func writeJSON(w http.ResponseWriter, status int, code int, msg string, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiResponse{
		Code:    code,
		Message: msg,
		Data:    data,
	})
}
