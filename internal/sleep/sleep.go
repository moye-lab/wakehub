package sleep

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Action string

const (
	ActionShutdown  Action = "shutdown"
	ActionSleep     Action = "sleep"
	ActionHibernate Action = "hibernate"
)

// ValidateAction 严格限制只允许预定义操作，杜绝任意命令注入
func ValidateAction(act string) (Action, error) {
	switch Action(strings.ToLower(strings.TrimSpace(act))) {
	case ActionShutdown, ActionSleep, ActionHibernate:
		return Action(strings.ToLower(strings.TrimSpace(act))), nil
	default:
		return "", fmt.Errorf("未授权的电源操作类型: %s", act)
	}
}

var (
	rfc1918_10   = &net.IPNet{IP: net.ParseIP("10.0.0.0"), Mask: net.CIDRMask(8, 32)}
	rfc1918_172  = &net.IPNet{IP: net.ParseIP("172.16.0.0"), Mask: net.CIDRMask(12, 32)}
	rfc1918_192  = &net.IPNet{IP: net.ParseIP("192.168.0.0"), Mask: net.CIDRMask(16, 32)}
	linkLocal169 = &net.IPNet{IP: net.ParseIP("169.254.0.0"), Mask: net.CIDRMask(16, 32)}
	loopback127  = &net.IPNet{IP: net.ParseIP("127.0.0.0"), Mask: net.CIDRMask(8, 32)}
)

// ValidateEndpoint 按契约 §6.1 严格校验 Sleep Endpoint：
//   - 仅 http/https；
//   - 主机必须为 RFC1918 私有 IPv4（10/8、172.16/12、192.168/16）或
//     本机回环 127/8；禁止公网地址、主机名与 169.254.0.0/16 云元数据段；
//   - 禁止 URL 中带 userinfo；
//   - 端口 1–65535（未显式写端口时 http 默认 80、https 默认 443）。
//
// 注意：回环 127/8 是对契约 §6.1 字面枚举的有意扩展（见 M3 报告）：
// Sleep-on-LAN 服务可能就跑在 Agent 本机上（本机回环监听）；回环地址
// 不可路由出本机，攻击面窄于已允许的整个局域网段，且 M2 的 Agent 侧
// 测试用 httptest 回环端点验证任务去重/取消也依赖此路径。公网与
// 元数据地址仍被禁止。
func ValidateEndpoint(endpoint string) error {
	endpoint = strings.TrimSpace(endpoint)
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("睡眠端点 URL 解析失败: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("受控端点必须为有效的 HTTP/HTTPS 地址")
	}
	if u.User != nil {
		return errors.New("睡眠端点 URL 不得包含用户信息")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil {
		return errors.New("睡眠端点主机必须为私有 IPv4 地址，不支持主机名")
	}
	ip = ip.To4()
	if linkLocal169.Contains(ip) {
		return errors.New("睡眠端点不得指向 169.254.0.0/16 元数据地址段")
	}
	if !rfc1918_10.Contains(ip) && !rfc1918_172.Contains(ip) && !rfc1918_192.Contains(ip) && !loopback127.Contains(ip) {
		return errors.New("睡眠端点主机必须为私有 IPv4 地址（10/8、172.16/12、192.168/16）或本机回环地址")
	}
	if portStr := u.Port(); portStr != "" {
		p, err := strconv.Atoi(portStr)
		if err != nil || p < 1 || p > 65535 {
			return fmt.Errorf("睡眠端点端口超出范围 1-65535: %s", portStr)
		}
	}
	return nil
}

// ValidateEndpointInScope 在 ValidateEndpoint 基线之上，再要求端点 IP
// 落在执行 Agent 的管理网段内（v0.2.9 问题 7，最小一致版）：
//   - cidrs 为空表示 Agent 未声明管理网段，此时只做基线校验（不拦截老 Agent）；
//   - 回环 127/8 始终放行（它是 Agent 本机的地址，见 ValidateEndpoint 注释）；
//   - 其余 IP 须落在任一给定 CIDR 内，否则拒绝。
func ValidateEndpointInScope(endpoint string, cidrs []*net.IPNet) error {
	if err := ValidateEndpoint(endpoint); err != nil {
		return err
	}
	if len(cidrs) == 0 {
		return nil
	}
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return fmt.Errorf("睡眠端点 URL 解析失败: %v", err)
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil {
		return errors.New("睡眠端点主机必须为私有 IPv4 地址，不支持主机名")
	}
	return validateIPInScope(ip, cidrs, "睡眠端点")
}

// ValidateIPInScope 校验裸 IP 是否在管理网段内（SSH 适配器用，
// 目标 IP 即设备的 IP 字段）。cidrs 为空时只做格式校验，不做范围拦截。
func ValidateIPInScope(ipStr string, cidrs []*net.IPNet) error {
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil {
		return fmt.Errorf("目标 IP %q 不是合法 IP 地址", ipStr)
	}
	return validateIPInScope(ip, cidrs, "目标 IP")
}

// validateIPInScope 是两处范围校验的公共部分：未声明网段时不拦截，
// 回环放行，其余须命中 CIDR。
func validateIPInScope(ip net.IP, cidrs []*net.IPNet, label string) error {
	if len(cidrs) == 0 {
		return nil
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return fmt.Errorf("%s %q 不是 IPv4 地址", label, ip.String())
	}
	if loopback127.Contains(ip4) {
		return nil
	}
	for _, n := range cidrs {
		if n.Contains(ip4) {
			return nil
		}
	}
	return fmt.Errorf("%s %s 不在执行 Agent 的管理网段内", label, ip4.String())
}

// 安全约束（契约 §6.1）：端点须通过 ValidateEndpoint；禁止跟随重定向
// （3xx 即视为失败）；请求超时 5s。签名认证 v0.2.0 明确不做（契约 §6.2），
// 本函数不发送任何签名头。
func Execute(ctx context.Context, adapterType, endpoint string, action Action) error {
	if _, err := ValidateAction(string(action)); err != nil {
		return err
	}
	switch strings.ToLower(adapterType) {
	case "http_sol":
		if err := ValidateEndpoint(endpoint); err != nil {
			return err
		}
		targetURL := fmt.Sprintf("%s/%s", strings.TrimRight(endpoint, "/"), string(action))
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, nil)
		if err != nil {
			return err
		}
		client := &http.Client{
			Timeout: 5 * time.Second,
			// 禁止跟随重定向：3xx 由调用方按失败处理（修复默认跟随 10 次的问题）。
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("请求受控执行端失败: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			// 3xx 意味着端点要求重定向到另一地址；本客户端禁止跟随
			// （SSRF 防护），直接按失败处理，并明确指出原因。
			return fmt.Errorf("受控执行端要求重定向（状态码 %d），已拒绝跟随", resp.StatusCode)
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("受控执行端返回状态码 %d", resp.StatusCode)
		}
		return nil
	default:
		return fmt.Errorf("设备未启用或未配置受控远程关机适配器 (当前类型: %s)", adapterType)
	}
}
