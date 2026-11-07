package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/robfig/cron/v3"
	"github.com/wakehub/wakehub/internal/agent"
	"github.com/wakehub/wakehub/internal/audit"
	"github.com/wakehub/wakehub/internal/auth"
	"github.com/wakehub/wakehub/internal/device"
	"github.com/wakehub/wakehub/internal/secret"
	"github.com/wakehub/wakehub/internal/sleep"
)

// 任务持久化说明（M5）：任务的全部 DB 操作已迁回 internal/device
// Repository（ScheduleItem 含 RunOnce；运行记录类型见 device.TaskRun）。
// 本包不再直接碰 repo.DB()。

// 默认执行参数（契约 §8.3）：单设备 dispatch 超时 10s、单次执行总超时
// 60s、WOL 重发次数默认 3（可由 WAKEHUB_WOL_RETRIES 覆盖，见 D-N2）。
const (
	defaultTaskTimeout = 10 * time.Second
	defaultRunTimeout  = 60 * time.Second
	defaultWOLRetries  = 3
	retryBackoff       = 500 * time.Millisecond
	maxScheduleRetries = 5
	maxScheduleNameLen = 64
)

// cronParser 是全包共用的 Cron 解析器（5 字段 + 描述符）：与 NewEngine
// 的注册解析器同一配置，保证 CRON_TZ 与 @daily 等行为一致。
func cronParser() cron.Parser {
	return cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
}

// hubAgentResolver 把 Hub/Store 适配为 device.AgentResolver（与 api 包
// 的同名适配器相同实现；scheduler 不能 import api，避免循环引用）。
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

// Engine 是定时任务调度器：Cron 注册 + 执行 + 执行历史。
type Engine struct {
	repo *device.Repository
	hub  *agent.Hub
	cron *cron.Cron
	mu   sync.Mutex

	// taskTimeout 是单设备 dispatch 超时；defaultRetries 是 WOL 重发次数
	//（WAKEHUB_WOL_RETRIES，D-N2：不再写死 Count: 3）。
	taskTimeout    time.Duration
	runTimeout     time.Duration
	defaultRetries int

	// running 是正在执行的任务 ID 集合（防重叠执行，契约 §8.3）。
	runMu   sync.Mutex
	running map[string]bool

	// auditLogger 为空时不写 schedule_run 审计（测试构造的 Engine
	// 不传 logger；生产由 main.go 经 SetAuditLogger 注入）。
	auditLogger *audit.Logger

	// secretKey 是 SSH 密码解密密钥（main.go 经 SetSecretKey 注入，
	// 与 api.Server 的 cfg.SecretKey 同源）；未注入时 sleep 任务的
	// SSH 密码解密失败，相关设备记 skipped，不静默失败。
	secretKey string
}

func NewEngine(repo *device.Repository, hub *agent.Hub) *Engine {
	return &Engine{
		repo:           repo,
		hub:            hub,
		cron:           cron.New(cron.WithParser(cronParser())),
		taskTimeout:    defaultTaskTimeout,
		runTimeout:     defaultRunTimeout,
		defaultRetries: defaultWOLRetries,
		running:        make(map[string]bool),
	}
}

// Configure 注入配置的执行参数（由 api.NewServer 在创建 Engine 后调用）。
// 非正值表示未配置，保持默认值——测试构造的零值 cfg 不会把超时压成 0。
func (e *Engine) Configure(taskTimeout time.Duration, defaultRetries int) {
	if taskTimeout > 0 {
		e.taskTimeout = taskTimeout
	}
	if defaultRetries > 0 {
		e.defaultRetries = defaultRetries
	}
}

// SetAuditLogger 注入审计日志器（M5，契约 §1.6 schedule_run）：
// 每次定时执行完成（成功/失败/跳过均记）写一条审计，actor 为 system。
// 未注入时不写审计，调度本身不受影响。
func (e *Engine) SetAuditLogger(l *audit.Logger) {
	e.auditLogger = l
}

// SetSecretKey 注入 SSH 密码解密密钥（与 SetAuditLogger 同一模式）：
// 定时 sleep 任务须解密设备存的 SSH 密码，与手动关机同一密钥。
// 未注入时解密失败，sleep 设备记 skipped。
func (e *Engine) SetSecretKey(key string) {
	e.secretKey = key
}

func (e *Engine) Start() error {
	if err := e.Reload(); err != nil {
		return err
	}
	e.cron.Start()
	return nil
}

func (e *Engine) Stop() {
	e.cron.Stop()
}

func (e *Engine) resolver() device.AgentResolver {
	return hubAgentResolver{hub: e.hub}
}

// agentScopeCIDRs 取执行 Agent 的管理网段（与 api.Server.agentScopeCIDRs
// 同一逻辑，v0.2.9 问题 7）：Issue 2 的管理网络（有 CIDR 的）与 Agent 的
// subnet 字段取并集；两者都没有时返回空——调用方只做基线校验，不拦截。
func (e *Engine) agentScopeCIDRs(agentID string) []*net.IPNet {
	var out []*net.IPNet
	if networks, err := e.repo.ListAgentNetworks(agentID); err == nil {
		for _, n := range networks {
			if strings.TrimSpace(n.CIDR) == "" {
				continue
			}
			if _, ipnet, err := net.ParseCIDR(strings.TrimSpace(n.CIDR)); err == nil {
				out = append(out, ipnet)
			}
		}
	}
	if a, err := e.hub.Store().GetAgent(agentID); err == nil && strings.TrimSpace(a.Subnet) != "" {
		if _, ipnet, err := net.ParseCIDR(strings.TrimSpace(a.Subnet)); err == nil {
			out = append(out, ipnet)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// 任务持久化（M5：全部经 device.Repository；本包不再直接碰 repo.DB()）
// ---------------------------------------------------------------------------

// CreateSchedule 创建任务（调用方先做 ValidateSchedule 校验）。
// retry_count 按传入值原样存储（0 = 不重试）；run_once 同步写入。
func (e *Engine) CreateSchedule(s *device.ScheduleItem, runOnce bool) error {
	s.RunOnce = runOnce
	return e.repo.CreateSchedule(s)
}

// UpdateSchedule 全量更新任务的可变字段（调用方先做合并与校验）。
func (e *Engine) UpdateSchedule(s *device.ScheduleItem, runOnce bool) error {
	s.RunOnce = runOnce
	return e.repo.UpdateSchedule(s)
}

// SetScheduleEnabled 开关任务（pause/resume 用）。不存在返回 ErrScheduleNotFound。
func (e *Engine) SetScheduleEnabled(id string, enabled bool) error {
	return e.repo.SetScheduleEnabled(id, enabled)
}

// ---------------------------------------------------------------------------
// 校验与 DTO（契约 §8.1）
// ---------------------------------------------------------------------------

// ValidateSchedule 校验任务创建/更新请求（契约 §8.1）：
// 名称 → Cron → 时区 → 目标类型 → 目标存在 → Action 白名单 → 重试上限。
// 通过时返回 nil；同时把字段规整化写回 s（去空白、时区默认 UTC、
// action 为空默认 wake）。目标存在性通过仓库查询。
func ValidateSchedule(r *device.Repository, s *device.ScheduleItem) []auth.FieldError {
	var errs []auth.FieldError
	addErr := func(field, code, msg string) {
		errs = append(errs, auth.FieldError{Field: field, Code: code, Message: msg})
	}

	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" {
		addErr("name", "required", "任务名称不能为空")
	} else if utf8.RuneCountInString(s.Name) > maxScheduleNameLen {
		addErr("name", "too_long", "任务名称不得超过 64 个字符")
	}

	s.CronExpr = strings.TrimSpace(s.CronExpr)
	if s.CronExpr == "" {
		addErr("cron_expr", "required", "Cron 表达式不能为空")
	} else {
		spec := s.CronExpr
		tz := strings.TrimSpace(s.Timezone)
		if tz != "" && tz != "UTC" {
			spec = "CRON_TZ=" + tz + " " + s.CronExpr
		}
		if _, err := cronParser().Parse(spec); err != nil {
			addErr("cron_expr", "invalid_format", "Cron 表达式格式错误")
		}
	}

	s.Timezone = strings.TrimSpace(s.Timezone)
	if s.Timezone == "" {
		s.Timezone = "UTC"
	} else if _, err := time.LoadLocation(s.Timezone); err != nil {
		addErr("timezone", "invalid_format", "时区无效")
	}

	s.TargetType = strings.TrimSpace(s.TargetType)
	if s.TargetType != "device" && s.TargetType != "group" {
		addErr("target_type", "invalid_value", "目标类型须为 device 或 group")
	}

	s.TargetID = strings.TrimSpace(s.TargetID)
	if s.TargetID == "" {
		addErr("target_id", "required", "目标不能为空")
	} else if s.TargetType == "device" {
		if _, err := r.GetDevice(s.TargetID); err != nil {
			if errors.Is(err, device.ErrDeviceNotFound) {
				addErr("target_id", "not_found", "目标设备不存在")
			} else {
				addErr("target_id", "invalid_value", "目标设备查询失败："+err.Error())
			}
		}
	} else if s.TargetType == "group" {
		exists, err := r.GroupExists(s.TargetID)
		if err != nil {
			addErr("target_id", "invalid_value", "目标分组查询失败："+err.Error())
		} else if !exists {
			addErr("target_id", "not_found", "目标设备组不存在")
		} else if devices, err := r.ListDevices(s.TargetID, ""); err != nil {
			addErr("target_id", "invalid_value", "目标分组查询失败："+err.Error())
		} else if len(devices) == 0 {
			addErr("target_id", "empty_target", "目标设备组为空")
		}
	}

	s.Action = strings.TrimSpace(s.Action)
	if s.Action == "" {
		s.Action = "wake"
	}
	// v0.3.2 起开放 sleep（定时关机，按设备关机适配器执行 shutdown）；
	// 其余 Action 拒绝创建，不静默忽略。
	if s.Action != "wake" && s.Action != "sleep" {
		addErr("action", "unsupported_action", "暂不支持该操作，仅支持 wake 和 sleep")
	}

	if s.RetryCount < 0 || s.RetryCount > maxScheduleRetries {
		addErr("retry_count", "out_of_range", "重试次数须在 0-5 之间")
	}
	return errs
}

// NextRunAt 计算下一次执行时间（契约 §8.1）：按任务时区解析 Cron，
// 返回 UTC 时间。Cron 或时区非法时返回错误。
func NextRunAt(cronExpr, timezone string) (*time.Time, error) {
	spec := strings.TrimSpace(cronExpr)
	if timezone != "" && timezone != "UTC" {
		spec = "CRON_TZ=" + strings.TrimSpace(timezone) + " " + spec
	}
	sched, err := cronParser().Parse(spec)
	if err != nil {
		return nil, err
	}
	next := sched.Next(time.Now()).UTC()
	return &next, nil
}

// ScheduleView 是 API 返回的任务 DTO（契约 §8.1）：在 ScheduleItem
// 基础上补 target_name / last_run_status / next_run_at；run_once 由
// 内嵌的 ScheduleItem 提供。last_run_status 从最新 task_runs 行派生，
// 不新增列。
type ScheduleView struct {
	device.ScheduleItem
	TargetName    *string    `json:"target_name"`
	LastRunStatus *string    `json:"last_run_status"`
	NextRunAt     *time.Time `json:"next_run_at"`
}

func (e *Engine) enrichSchedule(item device.ScheduleItem) ScheduleView {
	v := ScheduleView{ScheduleItem: item}
	// 目标名：设备取设备名，分组取分组名；目标已删则留空。
	if item.TargetType == "device" {
		if d, err := e.repo.GetDevice(item.TargetID); err == nil {
			name := d.Name
			v.TargetName = &name
		}
	} else if item.TargetType == "group" {
		if groups, err := e.repo.ListGroups(); err == nil {
			for _, g := range groups {
				if g.ID == item.TargetID {
					name := g.Name
					v.TargetName = &name
					break
				}
			}
		}
	}
	// 最近执行状态：最新 task_runs 行派生。
	if status, ok, err := e.repo.LastTaskRunStatus(item.ID); err == nil && ok {
		v.LastRunStatus = &status
	}
	// 下次执行：仅启用中的任务计算；Cron 非法时留空（创建时已校验，
	// 此处为防御性处理）。
	if item.Enabled {
		if next, err := NextRunAt(item.CronExpr, item.Timezone); err == nil {
			v.NextRunAt = next
		}
	}
	return v
}

// ListScheduleViews 返回全部任务的 API DTO（按 created_at ASC；
// 分页由调用方做）。
func (e *Engine) ListScheduleViews() ([]ScheduleView, error) {
	items, err := e.repo.ListSchedules()
	if err != nil {
		return nil, err
	}
	// 仓库层按 created_at DESC；API 保持 M4 的 ASC 顺序。
	sort.SliceStable(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	views := make([]ScheduleView, 0, len(items))
	for _, item := range items {
		views = append(views, e.enrichSchedule(item))
	}
	return views, nil
}

// GetScheduleView 返回单个任务的 API DTO；不存在返回 ErrScheduleNotFound。
func (e *Engine) GetScheduleView(id string) (*ScheduleView, error) {
	item, err := e.repo.GetSchedule(id)
	if err != nil {
		return nil, err
	}
	v := e.enrichSchedule(*item)
	return &v, nil
}

// ---------------------------------------------------------------------------
// 执行历史（契约 §8.3，迁移 004）
// ---------------------------------------------------------------------------

// RunDeviceResult / RunRecord 是仓库层类型的别名（M5：执行历史的
// 存储类型收敛到 device.TaskRunDevice / device.TaskRun，两边字段一致）。
type RunDeviceResult = device.TaskRunDevice
type RunRecord = device.TaskRun

// ListRuns 返回某任务的执行历史（按开始时间 DESC），含每设备结果。
// 004 未应用的老库上 started_at/finished_at 为空时回退到 executed_at。
func (e *Engine) ListRuns(scheduleID string, page, pageSize int) ([]RunRecord, int, error) {
	return e.repo.ListTaskRuns(scheduleID, page, pageSize)
}

// ---------------------------------------------------------------------------
// Cron 注册与执行
// ---------------------------------------------------------------------------

func (e *Engine) Reload() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, entry := range e.cron.Entries() {
		e.cron.Remove(entry.ID)
	}

	items, err := e.repo.ListSchedules()
	if err != nil {
		return err
	}

	for _, item := range items {
		if !item.Enabled {
			continue
		}
		sched := item
		// 时区非法时记日志并跳过该任务，不注册、不崩溃。
		if sched.Timezone != "" && sched.Timezone != "UTC" {
			if _, err := time.LoadLocation(sched.Timezone); err != nil {
				log.Printf("[Scheduler] 跳过计划任务 %s：非法时区 %q (%v)", sched.Name, sched.Timezone, err)
				continue
			}
		}
		spec := sched.CronExpr
		if sched.Timezone != "" && sched.Timezone != "UTC" {
			spec = "CRON_TZ=" + sched.Timezone + " " + sched.CronExpr
		}
		_, err := e.cron.AddFunc(spec, func() {
			e.executeSchedule(sched, sched.RunOnce)
		})
		if err != nil {
			log.Printf("[Scheduler] 无法注册计划任务 %s (%s): %v", sched.Name, spec, err)
		}
	}
	return nil
}

// executeSchedule 执行一次任务（契约 §8.2/§8.3）：
//   - 防重叠：同一任务已有运行在进行时，记一条 skipped run 后返回；
//   - 目标解析：device 取单设备，group 取组内全部设备；
//   - 空目标/全禁用记 skipped，不再假成功；
//   - 每设备走 M3 统一校验链（ValidateOperation），校验失败按其
//     BatchStatus 记 skipped/failed，不参与 dispatch；
//   - 每设备独立 dispatch + 重试，结果独立记录；
//   - 运行状态 success/partial/failed/skipped 由每设备结果派生；
//   - run_once 任务首次执行后自动禁用并重载。
func (e *Engine) executeSchedule(s device.ScheduleItem, runOnce bool) {
	startedAt := time.Now().UTC()

	e.runMu.Lock()
	if e.running[s.ID] {
		e.runMu.Unlock()
		e.recordRun(s, "skipped", "上一次执行尚未结束", nil, startedAt)
		return
	}
	e.running[s.ID] = true
	e.runMu.Unlock()
	defer func() {
		e.runMu.Lock()
		delete(e.running, s.ID)
		e.runMu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), e.runTimeout)
	defer cancel()

	var targets []device.Device
	if s.TargetType == "group" {
		list, _ := e.repo.ListDevices(s.TargetID, "")
		targets = list
	} else {
		if d, err := e.repo.GetDevice(s.TargetID); err == nil {
			targets = append(targets, *d)
		}
	}

	if len(targets) == 0 {
		e.recordRun(s, "skipped", "目标为空，未执行任何操作", nil, startedAt)
		e.finishRunOnce(s, runOnce)
		return
	}

	results := make([]RunDeviceResult, 0, len(targets))
	var nSuccess, nFailed, nSkipped int
	for _, dev := range targets {
		// M3 统一校验链：设备禁用 → skipped；Agent 未获批/禁用/离线 → failed。
		if _, opErr := device.ValidateOperation(e.repo, e.resolver(), dev.ID); opErr != nil {
			if opErr.BatchStatus == "skipped" {
				nSkipped++
			} else {
				nFailed++
			}
			results = append(results, RunDeviceResult{
				DeviceID: dev.ID, Name: dev.Name,
				Status: opErr.BatchStatus, Reason: opErr.Message,
			})
			continue
		}
		if s.Action == "sleep" {
			payload, cfgErr := e.buildSleepPayload(dev)
			if cfgErr != nil {
				// 配置问题不重试、不 dispatch，记 skipped 并写明原因。
				nSkipped++
				results = append(results, RunDeviceResult{
					DeviceID: dev.ID, Name: dev.Name,
					Status: "skipped", Reason: cfgErr.Error(),
				})
				continue
			}
			if err := e.sleepDeviceWithRetry(ctx, dev, payload, s.RetryCount); err != nil {
				nFailed++
				results = append(results, RunDeviceResult{
					DeviceID: dev.ID, Name: dev.Name,
					Status: "failed", Reason: err.Error(),
				})
				e.recordDeviceSleepAudit(dev, payload.Action, false, err.Error())
				continue
			}
			nSuccess++
			now := time.Now().UTC()
			results = append(results, RunDeviceResult{
				DeviceID: dev.ID, Name: dev.Name,
				Status: "success", SentAt: &now,
			})
			e.recordDeviceSleepAudit(dev, payload.Action, true, "")
			continue
		}
		if err := e.wakeDeviceWithRetry(ctx, dev, s.RetryCount); err != nil {
			nFailed++
			results = append(results, RunDeviceResult{
				DeviceID: dev.ID, Name: dev.Name,
				Status: "failed", Reason: err.Error(),
			})
			e.repo.RecordWakeTask(dev.ID, dev.AgentID, "schedule", "failed", err.Error())
			continue
		}
		nSuccess++
		now := time.Now().UTC()
		bootStatus := "sent"
		results = append(results, RunDeviceResult{
			DeviceID: dev.ID, Name: dev.Name,
			Status: "success", SentAt: &now, BootStatus: &bootStatus,
		})
		e.repo.RecordWakeTask(dev.ID, dev.AgentID, "schedule", "success", "")
	}

	status := "success"
	switch {
	case nSuccess == 0 && nFailed == 0:
		status = "skipped"
	case nFailed > 0 && nSuccess > 0:
		status = "partial"
	case nFailed > 0:
		status = "failed"
	}
	output := fmt.Sprintf("成功 %d / 失败 %d / 跳过 %d", nSuccess, nFailed, nSkipped)
	e.recordRun(s, status, output, results, startedAt)
	e.finishRunOnce(s, runOnce)
}

// wakeDeviceWithRetry 对一台设备发 WOL：失败后按 retry_count 重试
// （尝试次数 = retry_count+1），重试间隔 500ms 且响应 ctx 取消；
// 单次 dispatch 超时为 taskTimeout。WOL 重发次数用配置值
// （WAKEHUB_WOL_RETRIES，D-N2），不再写死。
func (e *Engine) wakeDeviceWithRetry(ctx context.Context, dev device.Device, retryCount int) error {
	attempts := retryCount + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(retryBackoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				if lastErr != nil {
					return lastErr
				}
				return ctx.Err()
			case <-timer.C:
			}
		}
		dctx, cancel := context.WithTimeout(ctx, e.taskTimeout)
		_, err := e.hub.Dispatch(dctx, dev.AgentID, "wake", agent.WakePayload{
			MAC:              dev.MAC,
			BroadcastAddress: dev.BroadcastAddress,
			Port:             dev.WOLPort,
			Count:            e.defaultRetries,
		})
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
	}
	return lastErr
}

// sleepConfigError 是睡眠 payload 构造失败（设备配置问题）：
// 这类错误不重试、不 dispatch，执行时记 skipped 并写明原因。
type sleepConfigError struct{ reason string }

func (e sleepConfigError) Error() string { return e.reason }

// buildSleepPayload 按设备配置构造下发给 Agent 的睡眠 payload，
// 与手动关机（api.handleSleepDevice）同一校验链：适配器分支、
// 端点/IP 范围校验、SSH 密码解密。定时 sleep 任务的电源动作固定
// 为 shutdown（与界面“睡眠”按钮一致，见 DevicesView）。
// 返回 sleepConfigError 表示配置问题（不重试、不 dispatch）。
func (e *Engine) buildSleepPayload(dev device.Device) (agent.SleepPayload, error) {
	if strings.TrimSpace(dev.SleepType) == "" || dev.SleepType == "none" {
		return agent.SleepPayload{}, sleepConfigError{"未配置睡眠适配器"}
	}
	switch dev.SleepType {
	case "http_sol":
		if err := sleep.ValidateEndpoint(dev.SleepEndpoint); err != nil {
			return agent.SleepPayload{}, sleepConfigError{"睡眠端点无效：" + err.Error()}
		}
		if err := sleep.ValidateEndpointInScope(dev.SleepEndpoint, e.agentScopeCIDRs(dev.AgentID)); err != nil {
			return agent.SleepPayload{}, sleepConfigError{"睡眠端点不在执行 Agent 的管理网段内：" + err.Error()}
		}
		return agent.SleepPayload{AdapterType: "http_sol", Endpoint: dev.SleepEndpoint, Action: sleep.ActionShutdown}, nil
	case "ssh":
		if strings.TrimSpace(dev.SSHUser) == "" {
			return agent.SleepPayload{}, sleepConfigError{"SSH 用户名未配置"}
		}
		if strings.TrimSpace(dev.IP) == "" {
			return agent.SleepPayload{}, sleepConfigError{"设备未配置 IP，SSH 关机需要目标 IP"}
		}
		if err := sleep.ValidateIPInScope(dev.IP, e.agentScopeCIDRs(dev.AgentID)); err != nil {
			return agent.SleepPayload{}, sleepConfigError{"SSH 目标不在执行 Agent 的管理网段内：" + err.Error()}
		}
		password, err := secret.Decrypt(e.secretKey, dev.SSHPasswordEnc)
		if err != nil || password == "" {
			return agent.SleepPayload{}, sleepConfigError{"SSH 密码无法解密，请重新填写并保存"}
		}
		port := dev.SSHPort
		if port == 0 {
			port = 22
		}
		return agent.SleepPayload{
			AdapterType:     "ssh",
			SSHHost:         dev.IP,
			SSHPort:         port,
			SSHUser:         dev.SSHUser,
			SSHPassword:     password,
			SSHKnownHostKey: dev.SSHHostKeySHA256,
			SSHTargetOS:     dev.SSHOS,
			SSHUseSudo:      dev.SSHUseSudo,
			Action:          sleep.ActionShutdown,
		}, nil
	default:
		return agent.SleepPayload{}, sleepConfigError{"睡眠适配器类型无效"}
	}
}

// sleepDeviceWithRetry 对一台设备发关机：失败后按 retry_count 重试
// （尝试次数 = retry_count+1），重试间隔 500ms 且响应 ctx 取消；
// 单次 dispatch 超时为 taskTimeout。与 wakeDeviceWithRetry 同一
// 重试模型；配置错误在 buildSleepPayload 已拦截，不进重试。
// 成功后做 TOFU 主机密钥学习（与手动关机一致）。
func (e *Engine) sleepDeviceWithRetry(ctx context.Context, dev device.Device, payload agent.SleepPayload, retryCount int) error {
	attempts := retryCount + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(retryBackoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				if lastErr != nil {
					return lastErr
				}
				return ctx.Err()
			case <-timer.C:
			}
		}
		dctx, cancel := context.WithTimeout(ctx, e.taskTimeout)
		res, err := e.hub.Dispatch(dctx, dev.AgentID, "sleep", payload)
		cancel()
		if err == nil {
			// TOFU：SSH 首次成功后保存 Agent 回传的主机密钥指纹；
			// 保存失败不影响主流程（与手动关机一致）。
			if payload.AdapterType == "ssh" && dev.SSHHostKeySHA256 == "" && res.Data != nil {
				var learned struct {
					HostKeySHA256 string `json:"host_key_sha256"`
				}
				if json.Unmarshal(res.Data, &learned) == nil && learned.HostKeySHA256 != "" {
					_ = e.repo.SetSSHHostKeyFingerprint(dev.ID, learned.HostKeySHA256)
				}
			}
			return nil
		}
		lastErr = err
	}
	return lastErr
}

// recordDeviceSleepAudit 写单设备定时关机的审计（与手动关机同一
// device_sleep / device_sleep_failed 动作，actor 为 system）：
// 定时任务的可观测性与手动关机对齐。未注入 auditLogger 时跳过。
func (e *Engine) recordDeviceSleepAudit(dev device.Device, action sleep.Action, success bool, errMsg string) {
	if e.auditLogger == nil {
		return
	}
	if success {
		if err := e.auditLogger.Record("system", "device_sleep", "device", dev.ID,
			"定时任务：执行电源操作: "+string(action), ""); err != nil {
			log.Printf("[Scheduler] 写入 device_sleep 审计失败 %s: %v", dev.Name, err)
		}
	} else {
		if err := e.auditLogger.Record("system", "device_sleep_failed", "device", dev.ID,
			errMsg, ""); err != nil {
			log.Printf("[Scheduler] 写入 device_sleep_failed 审计失败 %s: %v", dev.Name, err)
		}
	}
}

// finishRunOnce：单次任务首次执行后自动禁用并重载 Cron（契约 §8.2）。
// 无论执行结果如何都禁用——“执行一次”即完成使命。
func (e *Engine) finishRunOnce(s device.ScheduleItem, runOnce bool) {
	if !runOnce {
		return
	}
	if err := e.repo.SetScheduleEnabled(s.ID, false); err != nil {
		log.Printf("[Scheduler] 单次任务 %s 执行后禁用失败: %v", s.Name, err)
		return
	}
	if err := e.Reload(); err != nil {
		log.Printf("[Scheduler] 单次任务 %s 执行后重载失败: %v", s.Name, err)
	}
}

// recordRun 写一条执行记录（迁移 004）：task_runs 一行 +
// task_run_devices 每设备一行 + 更新 schedules.last_run_at。
// last_run_status 不存列，由最新 task_runs 行派生（契约 §8.1）。
// 写库失败只记日志，不让调度器崩溃；成功后按契约 §1.6 写一条
// schedule_run 审计（成功/失败/跳过均记，actor 为 system）。
func (e *Engine) recordRun(s device.ScheduleItem, status, output string, devices []RunDeviceResult, startedAt time.Time) {
	finishedAt := time.Now().UTC()
	runID, err := e.repo.RecordTaskRun(s.ID, status, output, startedAt, finishedAt)
	if err != nil {
		log.Printf("[Scheduler] 记录任务执行失败 %s: %v", s.Name, err)
		return
	}
	for _, d := range devices {
		if err := e.repo.RecordTaskRunDevice(runID, d); err != nil {
			log.Printf("[Scheduler] 记录设备执行结果失败 %s/%s: %v", runID, d.DeviceID, err)
		}
	}
	_ = e.repo.UpdateScheduleLastRun(s.ID, finishedAt)
	if e.auditLogger != nil {
		if err := e.auditLogger.Record("system", "schedule_run", "schedule", s.ID,
			fmt.Sprintf("定时任务 %s 执行完成：%s", s.Name, output), ""); err != nil {
			log.Printf("[Scheduler] 写入 schedule_run 审计失败 %s: %v", s.Name, err)
		}
	}
}
