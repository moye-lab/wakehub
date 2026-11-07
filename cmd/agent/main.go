package main

import (
	"context"
	"log"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/wakehub/wakehub/internal/agent"
	"github.com/wakehub/wakehub/internal/config"
	dbpkg "github.com/wakehub/wakehub/internal/db"
)

func main() {
	cfg := config.LoadAgentConfig()
	// v0.3.0 单容器：网段未配置时自动推导本机首个非回环 IPv4 网段
	// （host 网络下看到的就是宿主机网卡）；推导失败时留空，由
	// Validate() 报错，提示显式配置 WAKEHUB_AGENT_SUBNET。
	if cfg.Subnet == "" {
		if subnet := config.DetectPrimarySubnet(); subnet != "" {
			cfg.Subnet = subnet
			log.Printf("[WakeHub Agent] 未配置 WAKEHUB_AGENT_SUBNET，自动检测到网段 %s（不合用时显式配置覆盖）", subnet)
		}
	}
	// 契约 §12.1：ID/名/网段未配置即拒绝启动（不再用代码默认值静默运行）。
	if err := cfg.Validate(); err != nil {
		log.Fatalf("[WakeHub Agent] %v", err)
	}
	// v0.2.9 问题 8：配置网段须与本机网卡一致（防 192.168.1.0/24 默认值
	// 用在 192.168.1.0/24 局域网这类错配）；读不到网卡时只警告不拦截；
	// 显式 WAKEHUB_AGENT_SKIP_SUBNET_CHECK=1 可跳过（特殊路由组网时用）。
	if !cfg.SkipSubnetCheck {
		if networks, err := config.LocalNetworks(); err != nil {
			log.Printf("[WakeHub Agent] 警告：无法读取本机网卡（%v），跳过网段校验", err)
		} else if err := config.ValidateSubnetOnNetworks(cfg.Subnet, networks); err != nil {
			log.Fatalf("[WakeHub Agent] %v", err)
		}
	}
	// v0.2.9 问题 3：显式放行明文 ws 时打一条启动警告（开关与
	// TLS_SKIP_VERIFY 相互独立，各管各的）。
	if cfg.AllowInsecureWS {
		if u, err := url.Parse(cfg.ServerWSURL); err == nil && u.Scheme == "ws" {
			log.Printf("[WakeHub Agent] 警告：WAKEHUB_AGENT_ALLOW_INSECURE_WS=1 已启用，正在用明文 ws:// 连接 %s，token 与任务内容以明文传输；公网部署请改用 wss://", u.Host)
		}
	}
	// v0.2.11：每次启动都收紧 token 所在目录（0700）。只在入网时收紧不够：
	// 迁移脚本建出的卷根是 root 属主 0755，Agent 以 wakehub 身份改不了它，
	// 且重启时不重跑入网逻辑，目录会一直留在 0755。收紧失败只警告不拦截
	// （目录 0755 不影响连接；属主不对时靠部署侧一次性 chown 修复）。
	if cfg.TokenFile != "" {
		if err := dbpkg.EnsurePrivateDir(filepath.Dir(cfg.TokenFile)); err != nil {
			log.Printf("[WakeHub Agent] 警告：token 目录 %s 收紧为 0700 失败（%v），请检查属主是否为 wakehub(10001)", filepath.Dir(cfg.TokenFile), err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := agent.NewClient(cfg)
	done := make(chan struct{})
	go func() {
		defer close(done)
		client.Run(ctx)
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-stop:
		log.Println("[WakeHub Agent] 收到退出信号，正在停止...")
		cancel()
		<-done
	case <-done:
		// 客户端自行停止（如注册被管理员驳回）：直接退出，
		// 由部署侧（systemd / Docker restart policy）决定是否重启。
		log.Println("[WakeHub Agent] 客户端已停止，正在退出...")
	}
}
