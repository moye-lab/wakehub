package sleep

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// testSSHServer 是进程内真实 SSH 协议测试服务器：校验密码、
// 记录收到的 exec 命令、回 exit-status 0。不执行任何命令。
// v0.2.12 扩展：可模拟单次会话 sudo -S（读 stdin 密码，对上才执行
// 内层断电脚本）、断电脚本非零退出（powerExit/powerStderr）、
// 断电瞬间掐断连接（dropAfter，模拟目标关机），以及 sshd 的 chdir
// 警告（用户无家目录时，飞牛 sg 即如此）。
type testSSHServer struct {
	t            *testing.T
	listener     net.Listener
	signer       ssh.Signer
	fingerprint  string
	mu           sync.Mutex
	commands     []string
	authSeen     []string      // "user:password"
	stdinSeen    []string      // 每次 exec 收到的 stdin（sudo -S 喂密码用）
	powerExit    uint32        // 断电脚本的退出码（默认 0）
	powerStderr  string        // 断电脚本的 stderr 文本
	dropAfter    time.Duration // 断电脚本收到后多久掐断连接（0 = 不掐断）
	sudoPassword string        // sudo -S 期望的密码（默认 "testpass"）
	chdirWarning bool          // 为真时每条命令前先打 chdir 警告到 stderr
	powerRan     bool          // 断电脚本是否实际执行过（sudo 密码错时不执行）
}

func newTestSSHServer(t *testing.T) *testSSHServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("生成测试主机密钥失败: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("构造 signer 失败: %v", err)
	}
	sum := sha256.Sum256(signer.PublicKey().Marshal())
	s := &testSSHServer{t: t, signer: signer, fingerprint: hex.EncodeToString(sum[:]), sudoPassword: "testpass"}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	s.listener = ln
	config := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			s.mu.Lock()
			s.authSeen = append(s.authSeen, conn.User()+":"+string(password))
			s.mu.Unlock()
			if conn.User() == "testuser" && string(password) == "testpass" {
				return nil, nil
			}
			return nil, errors.New("认证失败")
		},
	}
	config.AddHostKey(signer)
	go s.serve(config)
	return s
}

func (s *testSSHServer) serve(config *ssh.ServerConfig) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn, config)
	}
}

func (s *testSSHServer) handleConn(netConn net.Conn, config *ssh.ServerConfig) {
	sshConn, chans, reqs, err := ssh.NewServerConn(netConn, config)
	if err != nil {
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(reqs)
	for ch := range chans {
		if ch.ChannelType() != "session" {
			_ = ch.Reject(ssh.UnknownChannelType, "只接受 session")
			continue
		}
		channel, requests, err := ch.Accept()
		if err != nil {
			continue
		}
		go s.handleSession(netConn, channel, requests)
	}
}

// readChannelStdin 读通道上的 stdin 直到 EOF 或超时（sudo -S 喂密码用）。
// 超时返回已读到的内容；客户端不喂输入时立即 EOF，不阻塞。
func readChannelStdin(ch ssh.Channel, timeout time.Duration) string {
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		tmp := make([]byte, 1024)
		for {
			n, err := ch.Read(tmp)
			if n > 0 {
				buf.Write(tmp[:n])
			}
			if err != nil {
				break
			}
		}
		done <- buf.String()
	}()
	select {
	case s := <-done:
		return s
	case <-time.After(timeout):
		return ""
	}
}

// isPowerScript 报告命令是否为断电脚本（Linux systemctl 或 Windows shutdown）。
func isPowerScript(cmd string) bool {
	return strings.Contains(cmd, "systemctl poweroff") ||
		strings.Contains(cmd, "systemctl suspend") ||
		strings.Contains(cmd, "systemctl hibernate") ||
		strings.Contains(cmd, "shutdown /s")
}

func (s *testSSHServer) handleSession(netConn net.Conn, channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()
	for req := range requests {
		switch req.Type {
		case "exec":
			var msg struct{ Command string }
			if err := ssh.Unmarshal(req.Payload, &msg); err != nil {
				_ = req.Reply(false, nil)
				continue
			}
			s.mu.Lock()
			s.commands = append(s.commands, msg.Command)
			s.mu.Unlock()
			_ = req.Reply(true, nil)
			// 飞牛 sg 无家目录时 sshd 在命令执行前先打 chdir 警告
			// （v0.2.12 firstLine 过滤用；警告不影响退出码）。
			if s.chdirWarning {
				_, _ = channel.Stderr().Write([]byte("Could not chdir to home directory /home/testuser: No such file or directory\n"))
			}
			// sudo -S sh -c '...'（单次会话 sudo，v0.2.12）：读 stdin 密码，
			// 对上才执行内层断电脚本，对不上回 "Sorry, try again." + exit 1
			// 且不执行内层命令（与真实 sudo -S 行为一致）。
			// 注意：发完 stderr 与 exit-status 后须先 CloseWrite 再等
			// 一小会儿才关通道——真实 sshd 不会立刻关通道，客户端
			// 须先把已缓冲的 stderr 读走；立刻关通道会竞态丢数据。
			if strings.HasPrefix(msg.Command, "sudo -S sh -c '") {
				stdin := readChannelStdin(channel, 2*time.Second)
				s.mu.Lock()
				s.stdinSeen = append(s.stdinSeen, stdin)
				s.mu.Unlock()
				if stdin != s.sudoPassword+"\n" {
					_, _ = channel.Stderr().Write([]byte("Sorry, try again.\n"))
					_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{1}))
					_ = channel.CloseWrite()
					time.Sleep(100 * time.Millisecond)
					return
				}
				// 密码对上：落到下面的 isPowerScript 分支执行内层脚本。
			}
			// 断电脚本：按配置掐断连接（模拟目标关机）或回退出码。
			if isPowerScript(msg.Command) {
				s.mu.Lock()
				s.powerRan = true
				dropAfter, powerExit, powerStderr := s.dropAfter, s.powerExit, s.powerStderr
				s.mu.Unlock()
				if dropAfter > 0 {
					time.Sleep(dropAfter)
					_ = netConn.Close()
					return
				}
				if powerStderr != "" {
					_, _ = channel.Stderr().Write([]byte(powerStderr))
				}
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{powerExit}))
				if powerStderr != "" {
					_ = channel.CloseWrite()
					time.Sleep(100 * time.Millisecond)
				}
				return
			}
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
			return
		default:
			_ = req.Reply(false, nil)
		}
	}
}

func (s *testSSHServer) addr() (string, int) {
	tcpAddr := s.listener.Addr().(*net.TCPAddr)
	return "127.0.0.1", tcpAddr.Port
}

func (s *testSSHServer) lastCommand() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.commands) == 0 {
		return ""
	}
	return s.commands[len(s.commands)-1]
}

func (s *testSSHServer) commandCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.commands)
}

func (s *testSSHServer) commandAt(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < 0 || i >= len(s.commands) {
		return ""
	}
	return s.commands[i]
}

func (s *testSSHServer) lastStdin() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.stdinSeen) == 0 {
		return ""
	}
	return s.stdinSeen[len(s.stdinSeen)-1]
}

func (s *testSSHServer) didPowerRun() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.powerRan
}

func (s *testSSHServer) sawAuth(userPass string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.authSeen {
		if a == userPass {
			return true
		}
	}
	return false
}

func (s *testSSHServer) close() { _ = s.listener.Close() }

func TestExecuteSSHShutdownCommand(t *testing.T) {
	srv := newTestSSHServer(t)
	defer srv.close()
	host, port := srv.addr()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	observed, err := ExecuteSSH(ctx, SSHOptions{
		Host: host, Port: port, User: "testuser", Password: "testpass", Action: ActionShutdown,
	})
	if err != nil {
		t.Fatalf("ExecuteSSH 失败: %v", err)
	}
	if observed != srv.fingerprint {
		t.Fatalf("观测指纹不符: got %q want %q", observed, srv.fingerprint)
	}
	cmd := srv.lastCommand()
	for _, want := range []string{"systemctl poweroff", "sleep 1"} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("下发命令缺 %q，实际: %q", want, cmd)
		}
	}
	// v0.2.12 起 Linux 脚本前台执行，不再 nohup 后台。
	if strings.Contains(cmd, "nohup") {
		t.Fatalf("Linux 脚本不应含 nohup（前台执行）: %q", cmd)
	}
	if !srv.sawAuth("testuser:testpass") {
		t.Fatalf("服务器未收到正确的账号密码")
	}
}

func TestExecuteSSHTofu(t *testing.T) {
	srv := newTestSSHServer(t)
	defer srv.close()
	host, port := srv.addr()
	call := func(known string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return ExecuteSSH(ctx, SSHOptions{
			Host: host, Port: port, User: "testuser", Password: "testpass",
			KnownHostKeySHA256: known, Action: ActionShutdown,
		})
	}
	// 首次：无信任指纹，学习并成功。
	observed, err := call("")
	if err != nil {
		t.Fatalf("首次连接失败: %v", err)
	}
	// 已信任指纹相符：成功。
	if _, err := call(observed); err != nil {
		t.Fatalf("信任指纹相符时应成功: %v", err)
	}
	// 指纹不符：拒绝。
	if _, err := call(strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "主机密钥已变化") {
		t.Fatalf("指纹不符应拒绝并提示主机密钥变化，实际: %v", err)
	}
}

func TestExecuteSSHAuthAndActions(t *testing.T) {
	srv := newTestSSHServer(t)
	defer srv.close()
	host, port := srv.addr()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// 密码错：失败。
	if _, err := ExecuteSSH(ctx, SSHOptions{Host: host, Port: port, User: "testuser", Password: "wrong", Action: ActionShutdown}); err == nil {
		t.Fatalf("密码错应失败")
	}
	// sleep / hibernate 下发对应命令。
	if _, err := ExecuteSSH(ctx, SSHOptions{Host: host, Port: port, User: "testuser", Password: "testpass", Action: ActionSleep}); err != nil {
		t.Fatalf("sleep 失败: %v", err)
	}
	if !strings.Contains(srv.lastCommand(), "systemctl suspend") {
		t.Fatalf("sleep 应下发 systemctl suspend，实际: %q", srv.lastCommand())
	}
	if _, err := ExecuteSSH(ctx, SSHOptions{Host: host, Port: port, User: "testuser", Password: "testpass", Action: ActionHibernate}); err != nil {
		t.Fatalf("hibernate 失败: %v", err)
	}
	if !strings.Contains(srv.lastCommand(), "systemctl hibernate") {
		t.Fatalf("hibernate 应下发 systemctl hibernate，实际: %q", srv.lastCommand())
	}
	// 目标不可达：失败而非挂死。
	if _, err := ExecuteSSH(ctx, SSHOptions{Host: "10.255.255.1", Port: 22, User: "u", Password: "p", Action: ActionShutdown}); err == nil {
		t.Fatalf("不可达目标应失败")
	}
}

func TestValidateSSHHost(t *testing.T) {
	for _, c := range []struct {
		host string
		ok   bool
	}{
		{"192.168.1.3", true},
		{"192.168.1.1", true},
		{"172.16.0.5", true},
		{"127.0.0.1", true},
		{"8.8.8.8", false},
		{"169.254.1.1", false},
		{"example.com", false},
		{"", false},
		{"::1", false},
	} {
		err := ValidateSSHHost(c.host)
		if c.ok && err != nil {
			t.Errorf("%q 应通过，实际错误: %v", c.host, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%q 应拒绝", c.host)
		}
	}
}

func TestSSHPowerScript(t *testing.T) {
	shutdown, err := sshPowerScript(ActionShutdown, "linux", false)
	if err != nil {
		t.Fatalf("shutdown 脚本生成失败: %v", err)
	}
	for _, want := range []string{"command -v systemctl", "poweroff", "shutdown -h now", "sleep 1"} {
		if !strings.Contains(shutdown, want) {
			t.Fatalf("shutdown 脚本缺 %q: %q", want, shutdown)
		}
	}
	// v0.2.12 起 Linux 脚本前台执行，不再 nohup 后台（失败可见）。
	// 注意：2>&1 里的 & 是重定向语法，不是后台操作符，只禁 nohup 与 " &"。
	if strings.Contains(shutdown, "nohup") || strings.Contains(shutdown, " &") {
		t.Fatalf("Linux 脚本不应后台执行: %q", shutdown)
	}
	sleepScript, _ := sshPowerScript(ActionSleep, "linux", false)
	if !strings.Contains(sleepScript, "systemctl suspend") {
		t.Fatalf("sleep 脚本不符: %q", sleepScript)
	}
	// v0.2.12 sudo 模式：整条脚本包进 sudo -S sh -c '...'（单次会话）。
	sudoShutdown, err := sshPowerScript(ActionShutdown, "linux", true)
	if err != nil {
		t.Fatalf("sudo shutdown 脚本生成失败: %v", err)
	}
	if !strings.HasPrefix(sudoShutdown, "sudo -S sh -c '") || !strings.HasSuffix(sudoShutdown, "'") {
		t.Fatalf("sudo 脚本应包进 sudo -S sh -c '...'，实际: %q", sudoShutdown)
	}
	for _, want := range []string{"systemctl poweroff", "sleep 1"} {
		if !strings.Contains(sudoShutdown, want) {
			t.Fatalf("sudo 脚本缺 %q: %q", want, sudoShutdown)
		}
	}
	sudoSleep, _ := sshPowerScript(ActionSleep, "linux", true)
	if !strings.Contains(sudoSleep, "sudo -S sh -c '") || !strings.Contains(sudoSleep, "systemctl suspend") {
		t.Fatalf("sudo sleep 脚本不符: %q", sudoSleep)
	}
	if _, err := sshPowerScript(ActionShutdown, "windows", false); err != nil {
		t.Fatalf("windows shutdown 脚本生成失败: %v", err)
	}
	// Windows 不吃 sudo：useSudo 应被忽略，脚本与原来一致。
	winSudo, _ := sshPowerScript(ActionShutdown, "windows", true)
	winPlain, _ := sshPowerScript(ActionShutdown, "windows", false)
	if winSudo != winPlain || strings.Contains(winSudo, "sudo") {
		t.Fatalf("windows 应忽略 sudo，实际: %q", winSudo)
	}
	// Windows 分支（cmd 语法，OpenSSH 默认 shell）：shutdown 本身立即
	// 返回、1 秒后关机；sleep/hibernate 用 start /b 后台起、不等完成。
	winShutdown, _ := sshPowerScript(ActionShutdown, "windows", false)
	if !strings.Contains(winShutdown, "shutdown /s /t 1") {
		t.Fatalf("windows shutdown 脚本不符: %q", winShutdown)
	}
	winSleep, _ := sshPowerScript(ActionSleep, "windows", false)
	if !strings.Contains(winSleep, `start "" /b rundll32.exe powrprof.dll,SetSuspendState 0,1,0`) {
		t.Fatalf("windows sleep 脚本不符: %q", winSleep)
	}
	winHibernate, _ := sshPowerScript(ActionHibernate, "windows", false)
	if !strings.Contains(winHibernate, `start "" /b shutdown /h`) {
		t.Fatalf("windows hibernate 脚本不符: %q", winHibernate)
	}
	if _, err := sshPowerScript(ActionShutdown, "macos", false); err == nil {
		t.Fatalf("未知目标系统应拒绝")
	}
	if _, err := sshPowerScript("reboot", "linux", false); err == nil {
		t.Fatalf("非法动作应拒绝")
	}
	// "" 视为 linux（旧 payload 兼容）。
	legacy, err := sshPowerScript(ActionShutdown, "", false)
	if err != nil || !strings.Contains(legacy, "systemctl poweroff") {
		t.Fatalf("空目标系统应默认 linux: %q, %v", legacy, err)
	}
}

// TestExecuteSSHWindowsTarget 验证 TargetOS=windows 时下发的是 cmd 命令
// （进程内测试服务器只记录命令，不执行）。
func TestExecuteSSHWindowsTarget(t *testing.T) {
	srv := newTestSSHServer(t)
	defer srv.close()
	host, port := srv.addr()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := ExecuteSSH(ctx, SSHOptions{
		Host: host, Port: port, User: "testuser", Password: "testpass",
		TargetOS: "windows", Action: ActionShutdown,
	}); err != nil {
		t.Fatalf("windows ExecuteSSH 失败: %v", err)
	}
	if !strings.Contains(srv.lastCommand(), "shutdown /s /t 1") {
		t.Fatalf("windows 目标应下发 shutdown /s /t 1，实际: %q", srv.lastCommand())
	}
}

// TestExecuteSSHSudoSuccess 验证 v0.2.12 sudo 模式（单次会话）：
// 一条 sudo -S sh -c '...' 命令，密码经 stdin 一次喂给 sudo，
// 校验与断电在同一个 sudo 调用里完成——不再两段式（飞牛实测：
// sudo -S -v 通过后另开会话的 sudo -n 认不到时间戳）。
func TestExecuteSSHSudoSuccess(t *testing.T) {
	srv := newTestSSHServer(t)
	defer srv.close()
	host, port := srv.addr()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := ExecuteSSH(ctx, SSHOptions{
		Host: host, Port: port, User: "testuser", Password: "testpass",
		Action: ActionShutdown, UseSudo: true,
	}); err != nil {
		t.Fatalf("sudo 关机失败: %v", err)
	}
	// 单次会话：只发 1 条命令（两段式会发 2 条）。
	if srv.commandCount() != 1 {
		t.Fatalf("sudo 模式应发 1 条命令，实际 %d: %v", srv.commandCount(), srv.commands)
	}
	if got := srv.commandAt(0); !strings.HasPrefix(got, "sudo -S sh -c '") || !strings.Contains(got, "systemctl poweroff") {
		t.Fatalf("应为 sudo -S 断电脚本，实际: %q", got)
	}
	if !srv.didPowerRun() {
		t.Fatalf("sudo 密码对上后应执行内层断电脚本")
	}
	// 密码经 stdin 喂给 sudo，不进命令行。
	if got := srv.lastStdin(); got != "testpass\n" {
		t.Fatalf("sudo 应从 stdin 读到密码，实际: %q", got)
	}
	if strings.Contains(srv.commandAt(0), "testpass") {
		t.Fatalf("密码不得出现在命令行里: %q", srv.commandAt(0))
	}
}

// TestExecuteSSHSudoWrongPassword 验证 sudo 密码错时：单次会话 sudo -S
// 鉴权失败即返回，不执行内层断电命令（反向验证：若跳过校验直接断电，
// 目标会在密码错时仍关机）。
func TestExecuteSSHSudoWrongPassword(t *testing.T) {
	srv := newTestSSHServer(t)
	defer srv.close()
	host, port := srv.addr()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// 注意：SSH 登录密码与 sudo 密码是同一个；这里用错密码，
	// SSH 登录本身就会失败——为单独测 sudo 校验，登录密码用对的，
	// 但 sudo 校验期望的密码改成另一个（模拟“SSH 密码 ≠ sudo 密码”的目标）。
	srv.sudoPassword = "sudo-pass"
	_, err := ExecuteSSH(ctx, SSHOptions{
		Host: host, Port: port, User: "testuser", Password: "testpass",
		Action: ActionShutdown, UseSudo: true,
	})
	if err == nil {
		t.Fatalf("sudo 密码错应失败")
	}
	if !strings.Contains(err.Error(), "sudo 校验失败") || !strings.Contains(err.Error(), "Sorry") {
		t.Fatalf("错误应说明 sudo 校验失败并带回真实原因，实际: %v", err)
	}
	// 只发了 1 条 sudo 命令，且内层断电脚本未执行。
	if srv.commandCount() != 1 || !strings.HasPrefix(srv.commandAt(0), "sudo -S sh -c '") {
		t.Fatalf("sudo 密码错时应只发 1 条 sudo 命令，实际命令: %v", srv.commands)
	}
	if srv.didPowerRun() {
		t.Fatalf("sudo 密码错时内层断电脚本不得执行")
	}
}

// TestExecuteSSHPoweroffFailureVisible 验证 v0.2.12 失败可见：
// 目标断电命令非零退出（如 polkit 拒绝）时，错误须带退出码与真实原因。
// 反向验证：旧版 nohup 后台脚本恒回 0，本测试在旧代码下会失败
// （旧代码返回成功、收不到错误）。
// 另验证 chdir 噪音过滤：目标用户无家目录时 sshd 先打 chdir 警告，
// 错误回显须跳过它、显示真正的断电错误（2026-11-06 飞牛实测）。
func TestExecuteSSHPoweroffFailureVisible(t *testing.T) {
	srv := newTestSSHServer(t)
	defer srv.close()
	srv.powerExit = 1
	srv.powerStderr = "Call to PowerOff failed: Interactive authentication required.\n"
	srv.chdirWarning = true
	host, port := srv.addr()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := ExecuteSSH(ctx, SSHOptions{
		Host: host, Port: port, User: "testuser", Password: "testpass", Action: ActionShutdown,
	})
	if err == nil {
		t.Fatalf("断电命令失败时应返回错误（旧版 nohup 会静默成功）")
	}
	if !strings.Contains(err.Error(), "退出码 1") || !strings.Contains(err.Error(), "Interactive authentication required") {
		t.Fatalf("错误应带退出码与目标真实原因，实际: %v", err)
	}
	if strings.Contains(err.Error(), "Could not chdir") {
		t.Fatalf("错误回显应跳过 sshd 的 chdir 噪音行，实际: %v", err)
	}
}

// TestExecuteSSHDropAfterDelayIsSuccess 验证断电瞬间掐断连接：
// 连接在 sleep 1 延迟之后被掐断，视为目标正在断电（成功）。
func TestExecuteSSHDropAfterDelayIsSuccess(t *testing.T) {
	srv := newTestSSHServer(t)
	defer srv.close()
	srv.dropAfter = 1500 * time.Millisecond
	host, port := srv.addr()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := ExecuteSSH(ctx, SSHOptions{
		Host: host, Port: port, User: "testuser", Password: "testpass", Action: ActionShutdown,
	}); err != nil {
		t.Fatalf("断电后掐断连接应视为成功，实际错误: %v", err)
	}
}

// TestExecuteSSHEarlyDropIsFailure 验证连接过早掐断：
// 连接在 sleep 1 延迟之前就断，说明断电尚未开始，应报失败。
func TestExecuteSSHEarlyDropIsFailure(t *testing.T) {
	srv := newTestSSHServer(t)
	defer srv.close()
	srv.dropAfter = 100 * time.Millisecond
	host, port := srv.addr()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := ExecuteSSH(ctx, SSHOptions{
		Host: host, Port: port, User: "testuser", Password: "testpass", Action: ActionShutdown,
	}); err == nil {
		t.Fatalf("连接过早掐断应失败")
	} else if !strings.Contains(err.Error(), "中断") {
		t.Fatalf("错误应说明连接中断，实际: %v", err)
	}
}

// TestExecuteSSHWindowsIgnoresSudo 验证 Windows 目标忽略 UseSudo。
func TestExecuteSSHWindowsIgnoresSudo(t *testing.T) {
	srv := newTestSSHServer(t)
	defer srv.close()
	host, port := srv.addr()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := ExecuteSSH(ctx, SSHOptions{
		Host: host, Port: port, User: "testuser", Password: "testpass",
		TargetOS: "windows", Action: ActionShutdown, UseSudo: true,
	}); err != nil {
		t.Fatalf("windows 忽略 sudo 应成功: %v", err)
	}
	// 只发一条 cmd 命令，不含 sudo。
	if srv.commandCount() != 1 {
		t.Fatalf("windows 应只发 1 条命令，实际: %v", srv.commands)
	}
	if !strings.Contains(srv.lastCommand(), "shutdown /s /t 1") || strings.Contains(srv.lastCommand(), "sudo") {
		t.Fatalf("windows 命令不符: %q", srv.lastCommand())
	}
}

var _ = io.Discard
