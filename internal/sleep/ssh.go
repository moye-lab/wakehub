// SSH 关机适配器（契约 §6.3，v0.2.5 新增；Windows 目标支持 v0.2.6 加入）。
//
// 与 http_sol 不同：目标上不用装任何监听服务，Agent 用 SSH 直连目标
// 执行关机命令。适用 Linux 目标（NAS、迷你主机、虚拟机、路由器等）；
// Windows 目标不在本适配器范围内（命令是 Linux 的）。
//
// 安全要点：
//   - 目标主机必须是私有 IPv4 或回环地址（与 §6.1 同款 allowlist），
//     禁止主机名与公网地址；端口 1–65535；
//   - 主机密钥用 TOFU：首次连接记录指纹（由服务端持久化），之后每次
//     校验，不符即拒绝（防局域网中间人偷密码）。改密码即重新信任主机；
//   - 关机命令先在前台做存在性检查（缺 systemctl/poweroff 即刻报错），
//     再以后台方式延迟 1 秒执行——Agent 收到命令退出的 0 返回码后，
//     目标才真正断电；否则 SSH 会话会在断电瞬间被掐断，成功与失败无法区分；
//   - 动作只来自白名单枚举，命令字符串由本文件固定写死，不拼接任何外部输入。
package sleep

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHOptions 是 SSH 关机一次执行所需的全部输入。Password 由服务端
// 解密后经 Agent 通道下发（与 http_sol 的端点一样只在内存中流转，
// 不落盘、不进日志）。TargetOS 为 "linux" 或 "windows"（"" 视为 linux）。
type SSHOptions struct {
	Host               string
	Port               int
	User               string
	Password           string
	KnownHostKeySHA256 string // 为空表示首次连接（TOFU 学习）
	TargetOS           string
	Action             Action
}

var (
	sshRFC1918_10   = &net.IPNet{IP: net.ParseIP("10.0.0.0"), Mask: net.CIDRMask(8, 32)}
	sshRFC1918_172  = &net.IPNet{IP: net.ParseIP("172.16.0.0"), Mask: net.CIDRMask(12, 32)}
	sshRFC1918_192  = &net.IPNet{IP: net.ParseIP("192.168.0.0"), Mask: net.CIDRMask(16, 32)}
	sshLoopback127  = &net.IPNet{IP: net.ParseIP("127.0.0.0"), Mask: net.CIDRMask(8, 32)}
	sshLinkLocal169 = &net.IPNet{IP: net.ParseIP("169.254.0.0"), Mask: net.CIDRMask(16, 32)}
)

// ValidateSSHHost 校验 SSH 目标主机：必须是私有 IPv4 或回环地址，
// 禁止主机名、公网地址与 169.254.0.0/16。
func ValidateSSHHost(host string) error {
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil || ip.To4() == nil {
		return errors.New("SSH 目标主机必须为 IPv4 地址，不支持主机名")
	}
	ip = ip.To4()
	if sshLinkLocal169.Contains(ip) {
		return errors.New("SSH 目标不得指向 169.254.0.0/16 元数据地址段")
	}
	if !sshRFC1918_10.Contains(ip) && !sshRFC1918_172.Contains(ip) && !sshRFC1918_192.Contains(ip) && !sshLoopback127.Contains(ip) {
		return errors.New("SSH 目标主机必须为私有 IPv4 地址（10/8、172.16/12、192.168/16）或回环地址")
	}
	return nil
}

// sshPowerScript 生成目标上执行的 shell 脚本。targetOS 为 "linux" 或
// "windows"（"" 视为 linux，供旧 payload 兼容）；生产调用方传设备的
// ssh_os。Windows OpenSSH 默认 shell 是 cmd.exe，脚本按 cmd 语法写：
// shutdown /s /t 1 本身立即返回、1 秒后关机；sleep/hibernate 用 start /b
// 后台起、不等它完成就返回，避免会话在断电瞬间被掐断导致成功与失败
// 无法区分。脚本内不含任何外部输入。
func sshPowerScript(action Action, targetOS string) (string, error) {
	if targetOS == "" {
		targetOS = "linux"
	}
	switch targetOS {
	case "linux":
		switch action {
		case ActionShutdown:
			return `command -v systemctl >/dev/null 2>&1 || command -v poweroff >/dev/null 2>&1 || command -v shutdown >/dev/null 2>&1 || exit 3; nohup sh -c 'sleep 1; systemctl poweroff 2>/dev/null || poweroff 2>/dev/null || shutdown -h now' >/dev/null 2>&1 &`, nil
		case ActionSleep:
			return `command -v systemctl >/dev/null 2>&1 || exit 3; nohup sh -c 'sleep 1; systemctl suspend' >/dev/null 2>&1 &`, nil
		case ActionHibernate:
			return `command -v systemctl >/dev/null 2>&1 || exit 3; nohup sh -c 'sleep 1; systemctl hibernate' >/dev/null 2>&1 &`, nil
		default:
			return "", fmt.Errorf("未授权的电源操作类型: %s", string(action))
		}
	case "windows":
		switch action {
		case ActionShutdown:
			return `where shutdown >nul 2>&1 || exit 3 & shutdown /s /t 1`, nil
		case ActionSleep:
			return `where rundll32 >nul 2>&1 || exit 3 & start "" /b rundll32.exe powrprof.dll,SetSuspendState 0,1,0`, nil
		case ActionHibernate:
			return `where shutdown >nul 2>&1 || exit 3 & start "" /b shutdown /h`, nil
		default:
			return "", fmt.Errorf("未授权的电源操作类型: %s", string(action))
		}
	default:
		return "", fmt.Errorf("SSH 关机适配器暂不支持 %s 目标（仅 Linux 与 Windows）", targetOS)
	}
}

// ExecuteSSH 用 SSH 直连目标并发起电源操作。成功时返回观测到的主机密钥
// SHA-256 指纹（小写 hex，供服务端 TOFU 持久化）；失败返回错误。
// 关机一旦发起即无法从本端确认目标是否真的断电——返回成功仅表示
// “指令已发起”（与唤醒的 sent ≠ booted 同理）。
func ExecuteSSH(ctx context.Context, opts SSHOptions) (string, error) {
	if _, err := ValidateAction(string(opts.Action)); err != nil {
		return "", err
	}
	if err := ValidateSSHHost(opts.Host); err != nil {
		return "", err
	}
	port := opts.Port
	if port == 0 {
		port = 22
	}
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("SSH 端口超出范围 1-65535: %d", port)
	}
	if strings.TrimSpace(opts.User) == "" {
		return "", errors.New("SSH 用户名不能为空")
	}
	if opts.Password == "" {
		return "", errors.New("SSH 密码不能为空")
	}
	script, err := sshPowerScript(opts.Action, opts.TargetOS)
	if err != nil {
		return "", err
	}

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var dialer net.Dialer
	netConn, err := dialer.DialContext(dialCtx, "tcp", net.JoinHostPort(opts.Host, strconv.Itoa(port)))
	if err != nil {
		return "", fmt.Errorf("连接 SSH 目标失败: %w", err)
	}
	// TOFU：记录观测指纹；已有信任指纹时不符即拒绝。
	var observed string
	password := opts.Password
	config := &ssh.ClientConfig{
		User: opts.User,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
			ssh.KeyboardInteractive(func(user, instruction string, questions []string, echos []bool) ([]string, error) {
				ans := make([]string, len(questions))
				for i := range ans {
					ans[i] = password
				}
				return ans, nil
			}),
		},
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			sum := sha256.Sum256(key.Marshal())
			observed = hex.EncodeToString(sum[:])
			if opts.KnownHostKeySHA256 != "" && !strings.EqualFold(opts.KnownHostKeySHA256, observed) {
				return fmt.Errorf("SSH 主机密钥已变化（可能遭遇中间人），已拒绝连接；请重新保存设备密码以重新信任主机")
			}
			return nil
		},
		Timeout: 10 * time.Second,
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(netConn, net.JoinHostPort(opts.Host, strconv.Itoa(port)), config)
	if err != nil {
		return "", fmt.Errorf("SSH 握手或认证失败: %w", err)
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("建立 SSH 会话失败: %w", err)
	}
	defer session.Close()
	// Run 不接受 ctx：放协程里跑，ctx 超时即关会话打断。
	// 注意：脚本直接作为 exec 请求发给目标 shell，不再包一层引号——
	// 脚本本身含单引号（nohup sh -c '...'），再包引号会提前闭合导致语法错误。
	runErr := make(chan error, 1)
	go func() { runErr <- session.Run(script) }()
	select {
	case err := <-runErr:
		if err != nil {
			return "", fmt.Errorf("执行关机命令失败: %w", err)
		}
		return observed, nil
	case <-ctx.Done():
		_ = session.Close()
		return "", fmt.Errorf("执行关机命令超时: %w", ctx.Err())
	}
}
