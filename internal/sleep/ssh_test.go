package sleep

import (
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
type testSSHServer struct {
	t           *testing.T
	listener    net.Listener
	signer      ssh.Signer
	fingerprint string
	mu          sync.Mutex
	commands    []string
	authSeen    []string // "user:password"
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
	s := &testSSHServer{t: t, signer: signer, fingerprint: hex.EncodeToString(sum[:])}
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
		go s.handleSession(channel, requests)
	}
}

func (s *testSSHServer) handleSession(channel ssh.Channel, requests <-chan *ssh.Request) {
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
	for _, want := range []string{"systemctl poweroff", "nohup", "sleep 1"} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("下发命令缺 %q，实际: %q", want, cmd)
		}
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
	shutdown, err := sshPowerScript(ActionShutdown, "linux")
	if err != nil {
		t.Fatalf("shutdown 脚本生成失败: %v", err)
	}
	for _, want := range []string{"command -v systemctl", "poweroff", "shutdown -h now", "nohup", "sleep 1"} {
		if !strings.Contains(shutdown, want) {
			t.Fatalf("shutdown 脚本缺 %q: %q", want, shutdown)
		}
	}
	sleepScript, _ := sshPowerScript(ActionSleep, "linux")
	if !strings.Contains(sleepScript, "systemctl suspend") {
		t.Fatalf("sleep 脚本不符: %q", sleepScript)
	}
	if _, err := sshPowerScript(ActionShutdown, "windows"); err != nil {
		t.Fatalf("windows shutdown 脚本生成失败: %v", err)
	}
	// Windows 分支（cmd 语法，OpenSSH 默认 shell）：shutdown 本身立即
	// 返回、1 秒后关机；sleep/hibernate 用 start /b 后台起、不等完成。
	winShutdown, _ := sshPowerScript(ActionShutdown, "windows")
	if !strings.Contains(winShutdown, "shutdown /s /t 1") {
		t.Fatalf("windows shutdown 脚本不符: %q", winShutdown)
	}
	winSleep, _ := sshPowerScript(ActionSleep, "windows")
	if !strings.Contains(winSleep, `start "" /b rundll32.exe powrprof.dll,SetSuspendState 0,1,0`) {
		t.Fatalf("windows sleep 脚本不符: %q", winSleep)
	}
	winHibernate, _ := sshPowerScript(ActionHibernate, "windows")
	if !strings.Contains(winHibernate, `start "" /b shutdown /h`) {
		t.Fatalf("windows hibernate 脚本不符: %q", winHibernate)
	}
	if _, err := sshPowerScript(ActionShutdown, "macos"); err == nil {
		t.Fatalf("未知目标系统应拒绝")
	}
	if _, err := sshPowerScript("reboot", "linux"); err == nil {
		t.Fatalf("非法动作应拒绝")
	}
	// "" 视为 linux（旧 payload 兼容）。
	legacy, err := sshPowerScript(ActionShutdown, "")
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

var _ = io.Discard
