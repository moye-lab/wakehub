package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/wakehub/wakehub/internal/agent"
	"github.com/wakehub/wakehub/internal/api"
	"github.com/wakehub/wakehub/internal/audit"
	"github.com/wakehub/wakehub/internal/auth"
	"github.com/wakehub/wakehub/internal/config"
	"github.com/wakehub/wakehub/internal/db"
	"github.com/wakehub/wakehub/internal/device"
	"github.com/wakehub/wakehub/internal/scheduler"
	"github.com/wakehub/wakehub/internal/statuspoller"
)

func main() {
	cfg := config.LoadServerConfig()

	// 密码重置 CLI（契约 §2.4）：在配置校验之前处理——部署的 .env
	// 仍含占位值时也须能重置密码，不删库。
	if len(os.Args) > 1 && os.Args[1] == "reset-password" {
		os.Exit(runResetPassword(cfg))
	}

	// 启动拒绝（契约 §2.7）：默认密码与示例密钥任一出现即拒绝启动。
	if err := cfg.Validate(); err != nil {
		log.Fatalf("[WakeHub] 配置校验失败，拒绝启动：\n%v", err)
	}

	dbConn, err := device.InitDB(cfg.DBPath)
	if err != nil {
		log.Fatalf("[WakeHub] 数据库初始化失败: %v", err)
	}
	defer dbConn.Close()

	// 迁移运行器（M5，迁移文档 §1）：启动时按版本表按序应用 001–005；
	// 旧库（仅 001）原地补齐 002/003/004/005，不删库；任一迁移失败
	// 即拒绝启动。迁移目录由 WAKEHUB_MIGRATIONS_PATH 所在目录导出。
	// v0.2.9 问题 12：有待处理迁移时先备份（VACUUM INTO 一致快照，
	// 0600），再应用；应用后核对关键表行数并记日志。
	migrationsDir := filepath.Dir(cfg.MigrationsPath)
	if cfg.DBPath != ":memory:" {
		if bak, err := db.BackupBeforeMigrate(dbConn, cfg.DBPath, migrationsDir); err != nil {
			log.Fatalf("[WakeHub] 迁移前备份失败，拒绝启动: %v", err)
		} else if bak != "" {
			log.Printf("[WakeHub] 检测到待应用迁移，已备份数据库到 %s", bak)
		}
	}
	beforeCounts, _ := db.TableCounts(dbConn)
	if err := db.Migrate(dbConn, migrationsDir); err != nil {
		log.Fatalf("[WakeHub] 应用数据库迁移失败: %v", err)
	}
	afterCounts, _ := db.TableCounts(dbConn)
	log.Printf("[WakeHub] 迁移后数量核对：%s", db.CountsReport(beforeCounts, afterCounts))
	// DB 文件及 WAL 伴生文件权限收紧为 0600（审计第 17 项 + v0.2.9 问题 11；
	// 文件不存在时仅警告）。
	if cfg.DBPath != ":memory:" {
		if err := db.TightenDBFiles(cfg.DBPath); err != nil {
			log.Printf("[WakeHub] 警告：数据库文件权限收紧失败: %v", err)
		}
	}

	repo := device.NewRepository(dbConn)
	authSvc := auth.NewService(dbConn, cfg.SecretKey, cfg.TrustedProxies)
	if err := authSvc.EnsureAdmin(cfg.AdminUser, cfg.AdminPassword); err != nil {
		log.Fatalf("[WakeHub] 初始化管理员账号失败: %v", err)
	}

	// 定期清理：限流过期记录（5 分钟）与撤销表过期行（每小时），启动时各清理一次。
	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
	defer cleanupCancel()
	go authSvc.StartCleanup(cleanupCtx)

	auditLogger := audit.NewLogger(dbConn)
	// 审计日志定期清理（契约 §9.2）：保留 90 天，启动时清理一次，
	// 之后每 24 小时清理一次；实际删行时写 audit_cleanup 系统审计。
	go auditLogger.StartCleanup(cleanupCtx, 24*time.Hour, 90)

	hub := agent.NewHub(repo)
	sched := scheduler.NewEngine(repo, hub)
	// 定时执行审计（契约 §1.6 schedule_run）：每次执行完成均记一条。
	sched.SetAuditLogger(auditLogger)
	if err := sched.Start(); err != nil {
		// 调度器启动失败是致命错误（修复 B-N9）：不得只记日志带病运行，
		// 否则全部定时唤醒静默失效。
		log.Fatalf("[WakeHub] 启动任务调度器失败: %v", err)
	}
	defer sched.Stop()

	// 周期状态检测（V-N2）：默认每 5 分钟检测全部已启用设备；
	// WAKEHUB_STATUS_CHECK_INTERVAL=0 时禁用。启动即跑一轮。
	statusPoller := statuspoller.New(repo, hub, cfg.StatusCheckInterval, cfg.TaskTimeout, log.Printf)
	go statusPoller.Start(cleanupCtx)

	srv := api.NewServer(cfg, repo, authSvc, hub, sched, auditLogger)
	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      180 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Printf("[WakeHub] Server 已启动，监听地址 %s (初始管理员: %s)", cfg.ListenAddr, cfg.AdminUser)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[WakeHub] HTTP 服务异常退出: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Println("[WakeHub] 正在优雅关闭服务...")
	cleanupCancel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
}

// runResetPassword 是 reset-password CLI（契约 §2.4）：交互式读取两次
// 新密码（终端不回显），bcrypt 入库并更新 password_changed_at，全部
// 会话失效。仅限能执行服务器本机命令的运维人员使用；不删库。
func runResetPassword(cfg config.ServerConfig) int {
	dbConn, err := device.InitDB(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[WakeHub] 数据库初始化失败: %v\n", err)
		return 1
	}
	defer dbConn.Close()
	migrationsDir := filepath.Dir(cfg.MigrationsPath)
	if cfg.DBPath != ":memory:" {
		if bak, err := db.BackupBeforeMigrate(dbConn, cfg.DBPath, migrationsDir); err != nil {
			fmt.Fprintf(os.Stderr, "[WakeHub] 迁移前备份失败，拒绝执行: %v\n", err)
			return 1
		} else if bak != "" {
			fmt.Printf("[WakeHub] 检测到待应用迁移，已备份数据库到 %s\n", bak)
		}
	}
	if err := db.Migrate(dbConn, migrationsDir); err != nil {
		fmt.Fprintf(os.Stderr, "[WakeHub] 应用数据库迁移失败: %v\n", err)
		return 1
	}
	// v0.2.9 问题 11：数据库文件及 WAL 伴生文件收紧为 0600（与主启动一致）。
	if cfg.DBPath != ":memory:" {
		if err := db.TightenDBFiles(cfg.DBPath); err != nil {
			fmt.Fprintf(os.Stderr, "[WakeHub] 警告：数据库文件权限收紧失败: %v\n", err)
		}
	}
	authSvc := auth.NewService(dbConn, cfg.SecretKey, cfg.TrustedProxies)
	username, err := authSvc.AdminUsername()
	if err != nil {
		fmt.Fprintln(os.Stderr, "[WakeHub] 未找到管理员账号：请先正常启动服务完成首次初始化。")
		return 1
	}
	pw1, err := readPassword("新密码（至少 8 位）: ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "[WakeHub] 读取密码失败: %v\n", err)
		return 1
	}
	pw2, err := readPassword("再次输入新密码: ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "[WakeHub] 读取密码失败: %v\n", err)
		return 1
	}
	if pw1 != pw2 {
		fmt.Fprintln(os.Stderr, "[WakeHub] 两次输入的密码不一致。")
		return 1
	}
	if err := authSvc.ResetPassword(username, pw1); err != nil {
		var ve *auth.ValidationError
		if errors.As(err, &ve) {
			fmt.Fprintf(os.Stderr, "[WakeHub] 密码不符合要求：%s\n", ve.Error())
			return 1
		}
		fmt.Fprintf(os.Stderr, "[WakeHub] 重置密码失败: %v\n", err)
		return 1
	}
	fmt.Printf("[WakeHub] 管理员 %s 的密码已重置，全部会话已失效，请用新密码登录。\n", username)
	return 0
}

// stdinReader 是 reset-password 两次读密码共用的缓冲读取器：每次调用都
// 新建 bufio.Reader 会把管道里剩余的输入一起缓冲走并丢弃，导致第二
// 次读取直接 EOF。
var stdinReader = bufio.NewReader(os.Stdin)

// readPassword 从终端读取一行密码并关闭回显（Linux，纯标准库实现，
// 不引入新依赖）。stdin 不是终端或 ioctl 失败时回退为明文读取并警告。
func readPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	fd := int(os.Stdin.Fd())
	oldState, err := ioctlGetTermios(fd)
	if err != nil {
		fmt.Fprintln(os.Stderr, "（警告：无法关闭终端回显，输入内容将可见）")
		line, rerr := stdinReader.ReadString('\n')
		return strings.TrimRight(line, "\r\n"), rerr
	}
	newState := *oldState
	newState.Lflag &^= syscall.ECHO
	newState.Lflag |= syscall.ICANON | syscall.ISIG
	if err := ioctlSetTermios(fd, &newState); err != nil {
		line, rerr := stdinReader.ReadString('\n')
		return strings.TrimRight(line, "\r\n"), rerr
	}
	defer ioctlSetTermios(fd, oldState)
	line, err := stdinReader.ReadString('\n')
	fmt.Fprintln(os.Stderr)
	return strings.TrimRight(line, "\r\n"), err
}

func ioctlGetTermios(fd int) (*syscall.Termios, error) {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&termios)))
	if errno != 0 {
		return nil, errno
	}
	return &termios, nil
}

func ioctlSetTermios(fd int, termios *syscall.Termios) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.TCSETS), uintptr(unsafe.Pointer(termios)))
	if errno != 0 {
		return errno
	}
	return nil
}
