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
//   - 关机命令前台执行（v0.2.12 起不再 nohup 后台）：退出码 0 表示
//     关机已发起；非零退出码带目标 stderr 首行返回，失败可见——
//     旧版后台执行时目标机权限不足会静默失败、界面却显示“已发送”
//     （飞牛 polkit 案）；连接在断电瞬间被掐断时，若已超过脚本内
//     sleep 1 延迟，视为目标正在断电（成功），否则视为连接故障；
//   - UseSudo（v0.2.12，Linux 专用）：单次会话 sudo -S：密码经 stdin
//     一次喂给 sudo，校验与断电在同一个 sudo 调用里完成，不依赖跨会话
//     的 sudo 时间戳——两段式（先 sudo -S -v 再另开会话 sudo -n）在
//     飞牛上实测失败：无 TTY 时时间戳不跨会话，-v 通过后 -n 仍认不到
//     （2026-11-06）；目标机无需预先写 polkit 规则或改 sudoers
//     （只要该用户本身有 sudo 权限），装即用；
//   - 动作只来自白名单枚举，命令字符串由本文件固定写死，不拼接任何外部输入。
package sleep

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHOptions 是 SSH 关机一次执行所需的全部输入。Password 由服务端
// 解密后经 Agent 通道下发（与 http_sol 的端点一样只在内存中流转，
// 不落盘、不进日志）。TargetOS 为 "linux" 或 "windows"（"" 视为 linux）。
// UseSudo 仅对 Linux 目标有效（v0.2.12）：勾选后关机命令改走 sudo -S，
// 密码经 stdin 喂给 sudo，不进命令行；Windows 目标忽略该字段。
type SSHOptions struct {
	Host               string
	Port               int
	User               string
	Password           string
	KnownHostKeySHA256 string // 为空表示首次连接（TOFU 学习）
	TargetOS           string
	Action             Action
	UseSudo            bool
}

// poweroffDropGrace 是“连接被掐断视为正在断电”的时间阈值，与 Linux
// 脚本内的 sleep 1 对齐：断电命令在 sleep 1 之后才启动，连接若在
// 1 秒内就断，说明断电尚未开始，是连接故障而非关机成功。
const poweroffDropGrace = time.Second

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
// ssh_os。useSudo 仅对 Linux 有效（v0.2.12）：整条脚本包进
// sudo -S sh -c '...'，密码由调用方经 stdin 一次喂给 sudo，校验与断电
// 在同一个 sudo 调用里完成，不依赖跨会话的时间戳。
// Windows OpenSSH 默认 shell 是 cmd.exe，脚本按 cmd 语法写：
// shutdown /s /t 1 本身立即返回、1 秒后关机；sleep/hibernate 用 start /b
// 后台起、不等它完成就返回，避免会话在断电瞬间被掐断导致成功与失败
// 无法区分。脚本内不含任何外部输入。
//
// Linux 脚本前台执行（v0.2.12 起不再 nohup 后台）：先做存在性检查
// （缺 systemctl/poweroff 即 exit 3），再 sleep 1 后执行断电命令；
// 断电命令的 stderr 不再丢弃（v0.2.12 起由调用方捕获并回显，失败可见）。
// 关机一旦发起，systemctl/poweroff 即返回 0；若全部断电方式都被拒，
// 最后一条命令的非零退出码即为本次执行的结果。
func sshPowerScript(action Action, targetOS string, useSudo bool) (string, error) {
	if targetOS == "" {
		targetOS = "linux"
	}
	switch targetOS {
	case "linux":
		var inner string
		switch action {
		case ActionShutdown:
			inner = `command -v systemctl >/dev/null 2>&1 || command -v poweroff >/dev/null 2>&1 || command -v shutdown >/dev/null 2>&1 || exit 3; sleep 1; systemctl poweroff || poweroff || shutdown -h now`
		case ActionSleep:
			inner = `command -v systemctl >/dev/null 2>&1 || exit 3; sleep 1; systemctl suspend`
		case ActionHibernate:
			inner = `command -v systemctl >/dev/null 2>&1 || exit 3; sleep 1; systemctl hibernate`
		default:
			return "", fmt.Errorf("未授权的电源操作类型: %s", string(action))
		}
		if useSudo {
			return `sudo -S sh -c '` + inner + `'`, nil
		}
		return inner, nil
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
// v0.2.12 起成功含义收紧：退出码 0（断电已发起）或连接在断电开始后
// 被掐断均算成功；工具缺失、权限被拒、sudo 校验失败等均带真实原因
// 返回，不再出现“界面显示已发送、目标实际没动”的静默失败。
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
	// sudo 只对 Linux 目标有意义；Windows 目标忽略该选项
	//（后端与前端同样会把非 Linux 的 sudo 标记归零）。
	targetOS := opts.TargetOS
	if targetOS == "" {
		targetOS = "linux"
	}
	useSudo := opts.UseSudo && targetOS == "linux"
	script, err := sshPowerScript(opts.Action, targetOS, useSudo)
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

	// sudo 模式（Linux）：单次会话 sudo -S。密码经 stdin 一次喂给 sudo，
	// 校验与断电在同一个 sudo 调用里完成，不依赖跨会话的 sudo 时间戳
	// （两段式在飞牛上实测失败：sudo -S -v 通过后，另开会话的 sudo -n
	// 认不到时间戳——无 TTY 时时间戳不跨会话；也可能是 timestamp_timeout=0。
	// 无论哪种，单次会话都不受影响）。密码不进命令行、不进日志。
	var stdin io.Reader
	if useSudo {
		stdin = strings.NewReader(password + "\n")
	}
	if err := runPowerScript(ctx, client, script, stdin, useSudo); err != nil {
		return "", err
	}
	return observed, nil
}

// runSessionCmd 在已建立的 SSH 连接上新开一个会话执行一条命令，
// stdin 为可选输入（nil 表示不喂输入），stdout/stderr 分别捕获后合并返回
// （stderr 在前：调用方只在失败时用首行做回显，错误文本都在 stderr）。
// 注意：ssh 库用两个协程分别拷 stdout 与 stderr，同一 bytes.Buffer
// 会被并发写（race 实测命中），故必须分开两个 Buffer。
// Run 不接受 ctx：放协程里跑，ctx 超时即关会话打断。
func runSessionCmd(ctx context.Context, client *ssh.Client, stdin io.Reader, cmd string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("建立 SSH 会话失败: %w", err)
	}
	defer session.Close()
	var outBuf, errBuf bytes.Buffer
	if stdin != nil {
		session.Stdin = stdin
	}
	session.Stdout = &outBuf
	session.Stderr = &errBuf
	// 注意：脚本直接作为 exec 请求发给目标 shell，不再包一层引号——
	// 脚本本身含单引号（sudo -S sh -c '...'），再包引号会提前闭合导致语法错误。
	runErr := make(chan error, 1)
	go func() { runErr <- session.Run(cmd) }()
	combined := func() string { return errBuf.String() + outBuf.String() }
	select {
	case err := <-runErr:
		return combined(), err
	case <-ctx.Done():
		_ = session.Close()
		return combined(), fmt.Errorf("执行命令超时: %w", ctx.Err())
	}
}

// runPowerScript 前台执行关机脚本并解读结果：
//   - 退出码 0：断电已发起，成功；
//   - 非零退出码：
//   - sudo 模式且输出含 sudo 鉴权失败特征（"Sorry, try again"、
//     "a password is required"）：密码错或该用户无 sudo 权限，
//     断电未发起；
//   - 其余：断电命令失败（工具缺失、权限被拒等），错误带上目标
//     stderr 首个有效行，失败可见；
//   - 连接在执行中被掐断：若已超过脚本内的 sleep 1 延迟
//     （poweroffDropGrace），视为目标正在断电（成功）；
//     否则视为连接故障（失败）。
//
// Windows 脚本本身立即返回（shutdown /s /t 1 只排队、不等断电），
// 正常走退出码 0 分支，不会触发掐断分支。
func runPowerScript(ctx context.Context, client *ssh.Client, script string, stdin io.Reader, useSudo bool) error {
	start := time.Now()
	out, err := runSessionCmd(ctx, client, stdin, script)
	if err == nil {
		return nil
	}
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		if useSudo && isSudoAuthFailure(out) {
			return fmt.Errorf("sudo 校验失败（密码错误或该用户无 sudo 权限）: %s", firstLine(out))
		}
		return fmt.Errorf("关机命令执行失败（退出码 %d）: %s", exitErr.ExitStatus(), firstLine(out))
	}
	if time.Since(start) >= poweroffDropGrace {
		// 目标正在断电，sshd 随系统下线把连接掐断，是预期结果。
		return nil
	}
	return fmt.Errorf("SSH 连接在关机命令执行前中断: %w", err)
}

// isSudoAuthFailure 判断输出是否含 sudo 鉴权失败的特征：密码错时
// sudo 连打 "Sorry, try again."；stdin 读不到密码时报
// "a password is required"。命中即断电未发起。
func isSudoAuthFailure(out string) bool {
	return strings.Contains(out, "Sorry, try again") ||
		strings.Contains(out, "a password is required")
}

// firstLine 取输出的首个有效行（去首尾空白），超长截断；
// 无输出时返回“（无输出）”，供错误回显用。
// 有效行跳过 sshd 的会话噪音：用户无家目录时 sshd 先打
// "Could not chdir to home directory /home/<user>: No such file
// or directory"（飞牛 sg 即如此），它不是命令本身的错误，
// 留着会盖住真正的错误行（2026-11-06 飞牛实测：sudo 时间戳失败
// 的真因被这行盖住，只看到警告）。
func firstLine(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "Could not chdir to home directory") {
			continue
		}
		if len(line) > 200 {
			line = line[:200] + "…"
		}
		return line
	}
	return "（无输出）"
}
