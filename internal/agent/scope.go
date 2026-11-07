package agent

import (
	"fmt"
	"net"
	"strings"

	"github.com/wakehub/wakehub/internal/device"
)

// AgentScopeCIDRs 返回某 Agent 的管理网段（v0.2.9 问题 7，v0.3.3
// 问题 9 去重后唯一实现，供 api 与 scheduler 共用）：管理网络
// （有 CIDR 的）与 Agent 的 subnet 字段取并集。
//
// 与旧实现的关键区别：任一查询失败都返回 error，不再静默退化为空
// 作用域——调用方须 fail-closed（拒绝本次关机操作），只有 Agent
// 确实未声明任何网络时才返回空 slice（调用方只做基线校验，即已有
// 的合法回退，见 sleep.ValidateEndpointInScope）。部分结果同样不
// 接受：管理网络读失败时不只用 subnet 凑合。
func AgentScopeCIDRs(repo *device.Repository, store *Store, agentID string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	networks, err := repo.ListAgentNetworks(agentID)
	if err != nil {
		return nil, fmt.Errorf("查询 Agent 管理网络失败: %w", err)
	}
	for _, n := range networks {
		if strings.TrimSpace(n.CIDR) == "" {
			continue
		}
		if _, ipnet, err := net.ParseCIDR(strings.TrimSpace(n.CIDR)); err == nil {
			out = append(out, ipnet)
		}
	}
	a, err := store.GetAgent(agentID)
	if err != nil {
		return nil, fmt.Errorf("查询 Agent 失败: %w", err)
	}
	if strings.TrimSpace(a.Subnet) != "" {
		if _, ipnet, err := net.ParseCIDR(strings.TrimSpace(a.Subnet)); err == nil {
			out = append(out, ipnet)
		}
	}
	return out, nil
}
