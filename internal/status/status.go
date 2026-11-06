package status

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type State string

const (
	StateOnline   State = "online"
	StateOffline  State = "offline"
	StateUnknown  State = "unknown"
	StateChecking State = "checking"
)

type Result struct {
	State       State     `json:"state"`
	Method      string    `json:"method"`
	CheckedAt   time.Time `json:"checked_at"`
	LatencyMs   int64     `json:"latency_ms"`
	ErrorReason string    `json:"error_reason,omitempty"`
}

// Check 执行设备在线检测 (支持 tcp / arp / ping)。
// 语义（契约 §4.5）：检测出离线是正常结果；检测异常（权限不足、参数
// 错误、执行失败）返回 unknown，不得简单记为离线。
func Check(ctx context.Context, ip, mac, method string, port int) Result {
	start := time.Now()
	res := Result{
		State:     StateOffline,
		Method:    method,
		CheckedAt: start.UTC(),
	}
	if strings.TrimSpace(ip) == "" {
		res.State = StateUnknown
		res.ErrorReason = "未配置设备 IP 地址"
		return res
	}
	// IP 先行校验：既防 ping 参数注入（B-N10），也保证后续检测
	// 面对的都是合法 IPv4。
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil || parsed.To4() == nil {
		res.State = StateUnknown
		res.ErrorReason = "IP 地址格式错误"
		return res
	}
	ip = parsed.String()

	switch strings.ToLower(method) {
	case "tcp":
		return checkTCP(ctx, ip, port, start, res)
	case "arp":
		return checkARPResult(ctx, ip, mac, start, res)
	default: // 默认 ping 检测
		return checkPing(ctx, ip, start, res)
	}
}

func checkTCP(ctx context.Context, ip string, port int, start time.Time, res Result) Result {
	res.Method = "tcp"
	if port <= 0 {
		port = 22
	}
	if port > 65535 {
		res.State = StateUnknown
		res.ErrorReason = "TCP 检测端口超出范围 1-65535"
		return res
	}
	target := net.JoinHostPort(ip, fmt.Sprintf("%d", port))
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", target)
	if err == nil {
		_ = conn.Close()
		res.State = StateOnline
		res.LatencyMs = time.Since(start).Milliseconds()
		return res
	}
	// 端口关闭 ≠ 设备关机（契约 §4.5）：连接被拒只能证明端口未监听，
	// 不能据此判定设备已关机。
	if errors.Is(err, syscall.ECONNREFUSED) {
		res.State = StateUnknown
		res.ErrorReason = "端口关闭，无法据此判定设备关机"
		return res
	}
	res.ErrorReason = fmt.Sprintf("TCP 端口 %d 不可达: %v", port, err)
	return res
}

func checkPing(ctx context.Context, ip string, start time.Time, res Result) Result {
	res.Method = "ping"
	cmdCtx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "ping", "-c", "1", "-W", "2", ip)
	out, err := cmd.CombinedOutput()
	if err == nil {
		res.State = StateOnline
		res.LatencyMs = time.Since(start).Milliseconds()
		return res
	}
	if cmdCtx.Err() != nil {
		res.State = StateUnknown
		res.ErrorReason = "Ping 检测超时"
		return res
	}
	// 权限不足的判定不依赖英文输出文案（修复 B-N10 的 locale 依赖）：
	// iputils ping 无响应退出码为 1，执行错误（无权建 raw socket、
	// 参数错误等）退出码为 2。IP 已在入口处校验过，退出码 2 只能是
	// 执行侧错误。英文关键词只作补充判断，用于给出更准确的提示。
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if exitErr.ExitCode() == 1 {
			res.ErrorReason = "ICMP Ping 无响应"
			return res
		}
		outStr := strings.ToLower(string(out))
		if strings.Contains(outStr, "operation not permitted") || strings.Contains(outStr, "permission denied") {
			res.State = StateUnknown
			res.ErrorReason = "容器缺少 ICMP 权限，请改用 TCP 检测或赋予 CAP_NET_RAW"
			return res
		}
		res.State = StateUnknown
		res.ErrorReason = fmt.Sprintf("Ping 执行失败（退出码 %d）", exitErr.ExitCode())
		return res
	}
	res.State = StateUnknown
	res.ErrorReason = fmt.Sprintf("Ping 执行失败: %v", err)
	return res
}

// checkARPResult 是缓存型 ARP 检测：它读取系统邻居表，不是主动 ARP
// 请求（真主动 ARP 需 raw socket/特权，见 M4 评估）。能力边界如实
// 标注：缓存未命中只能记 unknown，不得谎称为“设备离线”。
func checkARPResult(ctx context.Context, ip, expectedMAC string, start time.Time, res Result) Result {
	res.Method = "arp"
	online, err := checkARP(ctx, ip, expectedMAC)
	if err != nil {
		res.State = StateUnknown
		res.ErrorReason = err.Error()
		return res
	}
	if online {
		res.State = StateOnline
		res.LatencyMs = time.Since(start).Milliseconds()
		return res
	}
	// 缓存中无条目 ≠ 设备关机：设备可能在线但从未与本机通信。
	res.State = StateUnknown
	res.ErrorReason = "ARP 缓存中未发现该设备（缓存检测，无法确认离线）"
	return res
}

func checkARP(ctx context.Context, ip, expectedMAC string) (bool, error) {
	// 优先用 `ip neigh`：能区分 REACHABLE/STALE/FAILED 等状态，
	// 可识别已过期的缓存条目；无该命令时回落到 /proc/net/arp。
	if ok, err := checkARPWithIPNeigh(ctx, ip, expectedMAC); err == nil {
		return ok, nil
	}
	return checkARPWithProc(ip, expectedMAC)
}

var arpOnlineStates = map[string]bool{
	"REACHABLE": true, "DELAY": true, "PROBE": true, "STALE": true,
}

func checkARPWithIPNeigh(ctx context.Context, ip, expectedMAC string) (bool, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "ip", "neigh", "show", ip)
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	// 行格式：192.168.1.29 dev eth0 lladdr aa:bb:cc:dd:ee:01 REACHABLE
	fields := strings.Fields(string(out))
	if len(fields) < 1 {
		return false, nil
	}
	var mac, state string
	for i, f := range fields {
		switch f {
		case "lladdr":
			if i+1 < len(fields) {
				mac = fields[i+1]
			}
		case "REACHABLE", "STALE", "DELAY", "PROBE", "FAILED", "INCOMPLETE", "PERMANENT", "NOARP":
			state = f
		}
	}
	if state == "" || !arpOnlineStates[state] {
		return false, nil
	}
	if expectedMAC != "" && !strings.EqualFold(mac, expectedMAC) {
		return false, nil
	}
	return true, nil
}

func checkARPWithProc(ip, expectedMAC string) (bool, error) {
	f, err := os.Open("/proc/net/arp")
	if err != nil {
		return false, fmt.Errorf("无法读取 /proc/net/arp: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	_ = scanner.Scan() // 跳过表头
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		arpIP := fields[0]
		arpFlags := fields[2]
		arpMAC := strings.ToUpper(fields[3])
		// flags 0x0 为未完成条目，不计入在线。
		if arpIP == ip && arpFlags != "0x0" && arpMAC != "00:00:00:00:00:00" {
			if expectedMAC == "" || strings.EqualFold(arpMAC, expectedMAC) {
				return true, nil
			}
		}
	}
	return false, nil
}
