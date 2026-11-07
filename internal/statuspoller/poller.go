// Package statuspoller 是服务端周期状态检测（V-N2）：默认每 5 分钟对全部
// 已启用设备做一次状态检测，让设备列表与仪表盘计数在无人点"检测"时
// 也能自己变新鲜；页面刷新时前端还会立即触发一次（见 web 设备 store
// 的 refreshAll），两者互补。
//
// 设计要点：
//   - 只检测已启用设备；Agent 离线或未分配时跳过，不写脏状态。
//   - 检测失败（下发失败、结果解析失败、非法状态）不改库：保留上次
//     已知状态，不把"没测到"谎报成离线（契约 §4.5 同款语义）。
//   - 周期检测不写审计（device_check 只记手动检测与唤醒回检）：
//     每 5 分钟 × N 台全写审计会把日志刷爆，状态本身已入库可查。
//   - 并发上限 5，避免一次周期打爆 Agent 与服务端。
package statuspoller

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/wakehub/wakehub/internal/agent"
	"github.com/wakehub/wakehub/internal/device"
	"github.com/wakehub/wakehub/internal/status"
)

// deviceStore 是 Poller 需要的设备仓储子集；*device.Repository 满足它。
type deviceStore interface {
	ListDevices(groupID, search string) ([]device.Device, error)
	UpdateDeviceStatus(id, statusStr, errReason string) error
}

// dispatcher 是 Poller 需要的 Agent 下发子集；*agent.Hub 满足它。
type dispatcher interface {
	IsConnected(agentID string) bool
	Dispatch(ctx context.Context, agentID, taskType string, payload any) (agent.ResultPayload, error)
}

// Summary 是一次检测周期的汇总（供日志与测试）。
type Summary struct {
	Total   int // 列出的设备总数
	Checked int // 实际下发并得到合法结果的台数
	Online  int
	Offline int
	Unknown int
	Skipped int // 未启用 / 无 Agent / Agent 离线 / 下发失败 / 结果非法
}

// DefaultConcurrency 是单周期内并发检测上限。
const DefaultConcurrency = 5

// Poller 按固定间隔对全部已启用设备做状态检测。
type Poller struct {
	store       deviceStore
	hub         dispatcher
	interval    time.Duration
	taskTimeout time.Duration
	concurrency int
	logf        func(format string, args ...any)
}

// New 构造 Poller。interval <= 0 表示禁用（Start 直接返回）。
// logf 为 nil 时用标准 log.Printf。
func New(store deviceStore, hub dispatcher, interval, taskTimeout time.Duration, logf func(format string, args ...any)) *Poller {
	if logf == nil {
		logf = log.Printf
	}
	return &Poller{
		store:       store,
		hub:         hub,
		interval:    interval,
		taskTimeout: taskTimeout,
		concurrency: DefaultConcurrency,
		logf:        logf,
	}
}

// Start 运行周期检测直到 ctx 取消。interval <= 0 时只记一行日志即返回。
// 启动后立即跑一轮（重启后状态立刻变新鲜），之后每 interval 跑一轮；
// 上一轮未跑完时下一轮跳过（ticker 丢弃堆积 tick，不叠加）。
func (p *Poller) Start(ctx context.Context) {
	if p.interval <= 0 {
		p.logf("[WakeHub] 周期状态检测已禁用（WAKEHUB_STATUS_CHECK_INTERVAL=0）")
		return
	}
	p.logf("[WakeHub] 周期状态检测已启用，间隔 %s", p.interval)
	p.CheckAll(ctx)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.CheckAll(ctx)
		}
	}
}

// CheckAll 对全部已启用设备做一轮检测，返回汇总。
func (p *Poller) CheckAll(ctx context.Context) Summary {
	var sum Summary
	devices, err := p.store.ListDevices("", "")
	if err != nil {
		p.logf("[WakeHub] 周期状态检测：列出设备失败: %v", err)
		return sum
	}
	sum.Total = len(devices)

	sem := make(chan struct{}, p.concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, d := range devices {
		d := d
		if !d.IsEnabled() {
			mu.Lock()
			sum.Skipped++
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				mu.Lock()
				sum.Skipped++
				mu.Unlock()
				return
			}
			p.checkOne(ctx, &d, &sum, &mu)
		}()
	}
	wg.Wait()
	p.logf("[WakeHub] 周期状态检测完成：共 %d 台，检测 %d（在线 %d / 离线 %d / 未知 %d），跳过 %d",
		sum.Total, sum.Checked, sum.Online, sum.Offline, sum.Unknown, sum.Skipped)
	return sum
}

// checkOne 检测单台设备：Agent 离线/下发失败/结果非法都只记跳过，
// 不动库里原有状态。
func (p *Poller) checkOne(ctx context.Context, d *device.Device, sum *Summary, mu *sync.Mutex) {
	skip := func() {
		mu.Lock()
		sum.Skipped++
		mu.Unlock()
	}
	if d.AgentID == "" || !p.hub.IsConnected(d.AgentID) {
		skip()
		return
	}
	checkCtx, cancel := context.WithTimeout(ctx, p.taskTimeout)
	defer cancel()
	res, err := p.hub.Dispatch(checkCtx, d.AgentID, "check", agent.CheckPayload{
		IP:     d.IP,
		MAC:    d.MAC,
		Method: d.StatusMethod,
		Port:   d.StatusPort,
	})
	if err != nil {
		skip()
		return
	}
	var st status.Result
	if err := json.Unmarshal(res.Data, &st); err != nil {
		skip()
		return
	}
	switch st.State {
	case status.StateOnline, status.StateOffline, status.StateUnknown:
	default:
		skip()
		return
	}
	if err := p.store.UpdateDeviceStatus(d.ID, string(st.State), st.ErrorReason); err != nil {
		skip()
		return
	}
	mu.Lock()
	sum.Checked++
	switch st.State {
	case status.StateOnline:
		sum.Online++
	case status.StateOffline:
		sum.Offline++
	default:
		sum.Unknown++
	}
	mu.Unlock()
}
