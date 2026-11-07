package statuspoller

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wakehub/wakehub/internal/agent"
	"github.com/wakehub/wakehub/internal/device"
	"github.com/wakehub/wakehub/internal/status"
)

func boolPtr(b bool) *bool { return &b }

// fakeStore 记录 UpdateDeviceStatus 调用，供断言"状态动没动"。
type fakeStore struct {
	mu      sync.Mutex
	devices []device.Device
	updated map[string]string // deviceID -> status
	err     error
}

func (f *fakeStore) ListDevices(groupID, search string) ([]device.Device, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.devices, nil
}

func (f *fakeStore) UpdateDeviceStatus(id, statusStr, errReason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updated == nil {
		f.updated = map[string]string{}
	}
	f.updated[id] = statusStr
	return nil
}

// fakeHub 按 agentID 返回预设结果或错误，并统计最大并发。
type fakeHub struct {
	connected map[string]bool
	results   map[string]status.Result
	errs      map[string]error
	inFlight  atomic.Int32
	maxFlight atomic.Int32
	delay     time.Duration
}

func (f *fakeHub) IsConnected(agentID string) bool { return f.connected[agentID] }

func (f *fakeHub) Dispatch(ctx context.Context, agentID, taskType string, payload any) (agent.ResultPayload, error) {
	cur := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		old := f.maxFlight.Load()
		if cur <= old || f.maxFlight.CompareAndSwap(old, cur) {
			break
		}
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return agent.ResultPayload{}, ctx.Err()
		}
	}
	if err := f.errs[agentID]; err != nil {
		return agent.ResultPayload{}, err
	}
	data, _ := json.Marshal(f.results[agentID])
	return agent.ResultPayload{Success: true, Data: data}, nil
}

func newPoller(store *fakeStore, hub *fakeHub) *Poller {
	return New(store, hub, 5*time.Minute, 10*time.Second, func(string, ...any) {})
}

func TestCheckAllOnlyEnabledDevices(t *testing.T) {
	store := &fakeStore{devices: []device.Device{
		{ID: "on", AgentID: "a1", Enabled: boolPtr(true), StatusMethod: "ping"},
		{ID: "off", AgentID: "a1", Enabled: boolPtr(false), StatusMethod: "ping"},
		{ID: "nil-means-enabled", AgentID: "a1", StatusMethod: "ping"},
	}}
	hub := &fakeHub{
		connected: map[string]bool{"a1": true},
		results:   map[string]status.Result{"a1": {State: status.StateOnline, Method: "ping"}},
	}
	sum := newPoller(store, hub).CheckAll(context.Background())
	if sum.Checked != 2 || sum.Online != 2 {
		t.Fatalf("Checked/Online = %d/%d，想 2/2（禁用的 off 应跳过）", sum.Checked, sum.Online)
	}
	if sum.Skipped != 1 {
		t.Fatalf("Skipped = %d，想 1", sum.Skipped)
	}
	if _, ok := store.updated["off"]; ok {
		t.Fatalf("禁用的设备不该被更新状态")
	}
}

func TestCheckAllSkipsOfflineAgent(t *testing.T) {
	store := &fakeStore{devices: []device.Device{
		{ID: "d1", AgentID: "gone", Enabled: boolPtr(true), StatusMethod: "ping"},
		{ID: "d2", AgentID: "", Enabled: boolPtr(true), StatusMethod: "ping"},
	}}
	hub := &fakeHub{connected: map[string]bool{}}
	sum := newPoller(store, hub).CheckAll(context.Background())
	if sum.Checked != 0 || sum.Skipped != 2 {
		t.Fatalf("Checked/Skipped = %d/%d，想 0/2", sum.Checked, sum.Skipped)
	}
	if len(store.updated) != 0 {
		t.Fatalf("Agent 离线时不该写库，updated = %v", store.updated)
	}
}

func TestCheckAllDispatchErrorKeepsOldStatus(t *testing.T) {
	store := &fakeStore{devices: []device.Device{
		{ID: "d1", AgentID: "a1", Enabled: boolPtr(true), StatusMethod: "ping"},
	}}
	hub := &fakeHub{
		connected: map[string]bool{"a1": true},
		errs:      map[string]error{"a1": errors.New("目标节点 a1 当前离线")},
	}
	sum := newPoller(store, hub).CheckAll(context.Background())
	if sum.Checked != 0 || sum.Skipped != 1 {
		t.Fatalf("Checked/Skipped = %d/%d，想 0/1", sum.Checked, sum.Skipped)
	}
	if len(store.updated) != 0 {
		t.Fatalf("下发失败时不该写库，updated = %v", store.updated)
	}
}

func TestCheckAllValidStatesUpdate(t *testing.T) {
	store := &fakeStore{devices: []device.Device{
		{ID: "d1", AgentID: "a1", Enabled: boolPtr(true), StatusMethod: "ping"},
		{ID: "d2", AgentID: "a2", Enabled: boolPtr(true), StatusMethod: "tcp"},
		{ID: "d3", AgentID: "a3", Enabled: boolPtr(true), StatusMethod: "arp"},
		{ID: "d4", AgentID: "a4", Enabled: boolPtr(true), StatusMethod: "ping"},
	}}
	hub := &fakeHub{
		connected: map[string]bool{"a1": true, "a2": true, "a3": true, "a4": true},
		results: map[string]status.Result{
			"a1": {State: status.StateOnline, Method: "ping"},
			"a2": {State: status.StateOffline, Method: "tcp"},
			"a3": {State: status.StateUnknown, Method: "arp", ErrorReason: "ARP 缓存中未发现该设备"},
			"a4": {State: "checking", Method: "ping"}, // 瞬时状态，非法，不得入库
		},
	}
	sum := newPoller(store, hub).CheckAll(context.Background())
	if sum.Checked != 3 || sum.Online != 1 || sum.Offline != 1 || sum.Unknown != 1 || sum.Skipped != 1 {
		t.Fatalf("汇总 = %+v，想 Checked 3 / Online 1 / Offline 1 / Unknown 1 / Skipped 1", sum)
	}
	if store.updated["d1"] != "online" || store.updated["d2"] != "offline" || store.updated["d3"] != "unknown" {
		t.Fatalf("updated = %v", store.updated)
	}
	if _, ok := store.updated["d4"]; ok {
		t.Fatalf("非法状态 checking 不得入库")
	}
}

func TestCheckAllListErrorReturnsEmpty(t *testing.T) {
	store := &fakeStore{err: errors.New("db down")}
	hub := &fakeHub{}
	sum := newPoller(store, hub).CheckAll(context.Background())
	if sum != (Summary{}) {
		t.Fatalf("列出设备失败时汇总应为空，实际 %+v", sum)
	}
}

func TestCheckAllConcurrencyBound(t *testing.T) {
	var devs []device.Device
	for i := 0; i < 12; i++ {
		devs = append(devs, device.Device{ID: string(rune('a' + i)), AgentID: "a1", Enabled: boolPtr(true), StatusMethod: "ping"})
	}
	store := &fakeStore{devices: devs}
	hub := &fakeHub{
		connected: map[string]bool{"a1": true},
		results:   map[string]status.Result{"a1": {State: status.StateOnline, Method: "ping"}},
		delay:     20 * time.Millisecond,
	}
	sum := newPoller(store, hub).CheckAll(context.Background())
	if sum.Checked != 12 {
		t.Fatalf("Checked = %d，想 12", sum.Checked)
	}
	if max := hub.maxFlight.Load(); max > DefaultConcurrency {
		t.Fatalf("最大并发 %d，超过上限 %d", max, DefaultConcurrency)
	}
	if max := hub.maxFlight.Load(); max < 2 {
		t.Fatalf("最大并发 %d，并发根本没跑起来", max)
	}
}

func TestStartDisabledReturnsImmediately(t *testing.T) {
	store := &fakeStore{devices: []device.Device{
		{ID: "d1", AgentID: "a1", Enabled: boolPtr(true)},
	}}
	hub := &fakeHub{connected: map[string]bool{"a1": true}}
	p := New(store, hub, 0, 10*time.Second, func(string, ...any) {})
	done := make(chan struct{})
	go func() {
		p.Start(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("interval<=0 时 Start 应立即返回")
	}
	if len(store.updated) != 0 {
		t.Fatalf("禁用时不应检测，updated = %v", store.updated)
	}
}

func TestStartRunsCycleAndStopsOnCancel(t *testing.T) {
	store := &fakeStore{devices: []device.Device{
		{ID: "d1", AgentID: "a1", Enabled: boolPtr(true), StatusMethod: "ping"},
	}}
	hub := &fakeHub{
		connected: map[string]bool{"a1": true},
		results:   map[string]status.Result{"a1": {State: status.StateOnline, Method: "ping"}},
	}
	p := New(store, hub, 20*time.Millisecond, 10*time.Second, func(string, ...any) {})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Start(ctx)
		close(done)
	}()
	// 等一轮以上跑完（启动即跑一轮 + 至少一个 tick）。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		n := len(store.updated)
		store.mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后 Start 应退出")
	}
}
