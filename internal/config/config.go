package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type ServerConfig struct {
	ListenAddr       string
	DBPath           string
	MigrationsPath   string
	WebDistPath      string
	AdminUser        string
	AdminPassword    string
	SecretKey        string
	EnrollToken      string
	TrustedProxies   []*net.IPNet
	WSAllowedOrigins []string
	CookieSecure     bool
	DefaultRetries   int
	// TaskTimeout 是服务端等待一次 Agent 任务的上限（v0.2.9 问题 6）：
	// 默认 15s，比 Agent 执行上限（默认 10s）多 5s，给回包留余量。
	// 环境变量 WAKEHUB_SERVER_TASK_TIMEOUT（如 15s、1m）；非法值回落默认。
	TaskTimeout time.Duration
	// StatusCheckInterval 是周期状态检测间隔（V-N2）：默认 5 分钟；
	// 0 表示禁用；1–29 秒钳制为 30 秒，避免过密检测打爆 Agent。
	StatusCheckInterval time.Duration
}

type AgentConfig struct {
	ServerWSURL       string
	AgentID           string
	AgentName         string
	AgentToken        string
	Subnet            string
	HeartbeatInterval time.Duration
	// TLSSkipVerify 为 true 时跳过服务端证书验证（契约 §3.5）。
	// 默认 false；仅当显式配置 WAKEHUB_AGENT_TLS_SKIP_VERIFY=1 时才
	// 跳过，启动时输出警告。公网 Agent 必须保持默认（验证证书）。
	TLSSkipVerify bool
	// TaskTimeout 是单个任务的执行上限（v0.2.9 问题 6）：默认 10s，
	// 环境变量 WAKEHUB_AGENT_TASK_TIMEOUT；非法值回落默认。
	// 服务端等待上限（默认 15s）应比它长，否则回包可能在到达前被取消。
	TaskTimeout time.Duration
	// SkipSubnetCheck 为 true 时跳过启动时的网段-网卡校验
	//（v0.2.9 问题 8）：只给特殊组网（网段靠路由到达、本机无直连网卡）
	// 用，正常部署不要开。环境变量 WAKEHUB_AGENT_SKIP_SUBNET_CHECK=1。
	SkipSubnetCheck bool
	// AllowInsecureWS 为 true 时允许 Agent 用明文 ws:// 连非回环地址
	//（v0.2.9 问题 3）。默认 false：非回环一律要求 wss://，回环
	//（127.0.0.0/8、::1、localhost）不受影响。本开关与 TLSSkipVerify
	// 相互独立：前者管“是否加密传输”，后者管“是否验证证书”。
	AllowInsecureWS bool
	// TokenFile 是审批下发的 per-agent token 的持久化路径
	//（WAKEHUB_AGENT_TOKEN_FILE，默认 ./data/agent-token）。
	// 首次自助注册获批后，Agent 把新 token 写进该文件（0600），
	// 重启后优先使用文件里的 token，不再用入网 token 重入网。
	TokenFile string
}

// 占位值集合（契约 §2.7）：任一出现即拒绝启动。install.sh 负责在安装时
// 生成随机值替换它们；.env.example 里的值只是占位，不能直接启动。
var (
	placeholderAdminPasswords = []string{
		"wakehub123",
		"replace-with-a-strong-admin-password",
	}
	placeholderSecretKeys = []string{
		"change-me-in-production-wakehub-secret",
		"replace-with-strong-secret-key",
		"replace-with-random-secret-key",
	}
	placeholderEnrollTokens = []string{
		"local-agent-secret-token",
		"replace-with-random-agent-token",
	}
)

// Validate 检查启动拒绝项（契约 §2.7）：管理员默认密码、示例密钥、
// 入网 token 占位值任一出现即返回错误，调用方据此 fatal 退出。
// 注意：即使用户表非空（环境变量里的密码不会被用作种子），占位值
// 同样拒绝启动——部署升级前须按迁移文档 §4.2 处理 .env。
func (c ServerConfig) Validate() error {
	var problems []string
	for _, p := range placeholderAdminPasswords {
		if c.AdminPassword == p {
			problems = append(problems, fmt.Sprintf("WAKEHUB_ADMIN_PASSWORD 仍为占位值 %q，请改为随机强密码（install.sh 会自动生成；已部署的老库改密走改密接口）", p))
			break
		}
	}
	for _, p := range placeholderSecretKeys {
		if c.SecretKey == p {
			problems = append(problems, fmt.Sprintf("WAKEHUB_SECRET_KEY 仍为占位值 %q，请改为随机值（install.sh 会自动生成）", p))
			break
		}
	}
	for _, p := range placeholderEnrollTokens {
		if c.EnrollToken == p {
			problems = append(problems, fmt.Sprintf("WAKEHUB_AGENT_ENROLL_TOKEN 仍为占位值 %q，请改为随机值（install.sh 会自动生成）", p))
			break
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("拒绝启动：\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

func LoadServerConfig() ServerConfig {
	retries, err := strconv.Atoi(getEnv("WAKEHUB_WOL_RETRIES", "3"))
	if err != nil || retries < 1 {
		retries = 3
	}
	// 周期状态检测间隔（秒，V-N2）：非法值回落默认 300；显式 0 为禁用；
	// 1–29 秒钳制为 30 秒。
	intervalSec, err := strconv.Atoi(getEnv("WAKEHUB_STATUS_CHECK_INTERVAL", "300"))
	if err != nil || intervalSec < 0 {
		intervalSec = 300
	}
	if intervalSec > 0 && intervalSec < 30 {
		intervalSec = 30
	}
	return ServerConfig{
		ListenAddr:          getEnv("WAKEHUB_LISTEN_ADDR", ":8080"),
		DBPath:              getEnv("WAKEHUB_DB_PATH", "./data/wakehub.db"),
		MigrationsPath:      getEnv("WAKEHUB_MIGRATIONS_PATH", "./migrations/001_init.sql"),
		WebDistPath:         getEnv("WAKEHUB_WEB_DIST", "./web/dist"),
		AdminUser:           getEnv("WAKEHUB_ADMIN_USER", "admin"),
		AdminPassword:       getEnv("WAKEHUB_ADMIN_PASSWORD", "wakehub123"),
		SecretKey:           getEnv("WAKEHUB_SECRET_KEY", "change-me-in-production-wakehub-secret"),
		EnrollToken:         getEnv("WAKEHUB_AGENT_ENROLL_TOKEN", "replace-with-random-agent-token"),
		TrustedProxies:      parseTrustedProxies(getEnv("WAKEHUB_TRUSTED_PROXIES", "")),
		WSAllowedOrigins:    parseCommaList(getEnv("WAKEHUB_WS_ALLOWED_ORIGINS", "")),
		CookieSecure:        getEnv("WAKEHUB_COOKIE_SECURE", "") == "1",
		DefaultRetries:      retries,
		TaskTimeout:         parseDurationEnv("WAKEHUB_SERVER_TASK_TIMEOUT", 15*time.Second),
		StatusCheckInterval: time.Duration(intervalSec) * time.Second,
	}
}

func LoadAgentConfig() AgentConfig {
	// 契约 §12.1：Agent ID/名/网段取消代码默认值（旧默认值 agent-lan-01 /
	// Home LAN Agent / 192.168.1.0/24 与服务端、install.sh 三处互不一致，
	// 见审计第 7 项）。三者由 install.sh 或运维显式配置；未配置时
	// AgentConfig.Validate() 拒绝启动，不再静默用默认值运行。
	return AgentConfig{
		ServerWSURL:       getEnv("WAKEHUB_SERVER_WS_URL", "ws://127.0.0.1:8080/api/v1/ws/agent"),
		AgentID:           getEnv("WAKEHUB_AGENT_ID", ""),
		AgentName:         getEnv("WAKEHUB_AGENT_NAME", ""),
		AgentToken:        getEnv("WAKEHUB_AGENT_TOKEN", "local-agent-secret-token"),
		Subnet:            getEnv("WAKEHUB_AGENT_SUBNET", ""),
		HeartbeatInterval: 15 * time.Second,
		TLSSkipVerify:     getEnv("WAKEHUB_AGENT_TLS_SKIP_VERIFY", "") == "1",
		AllowInsecureWS:   getEnv("WAKEHUB_AGENT_ALLOW_INSECURE_WS", "") == "1",
		TaskTimeout:       parseDurationEnv("WAKEHUB_AGENT_TASK_TIMEOUT", 10*time.Second),
		SkipSubnetCheck:   getEnv("WAKEHUB_AGENT_SKIP_SUBNET_CHECK", "") == "1",
		TokenFile:         getEnv("WAKEHUB_AGENT_TOKEN_FILE", "./data/agent-token"),
	}
}

// agentIDPattern 与契约 §3.4 的预签发校验一致。
var agentIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// ValidateServerWSURL 校验 Agent 的 Server WebSocket 地址（v0.2.9 问题 3）：
//   - 必须是 ws:// 或 wss://，主机不能为空；
//   - wss:// 一律放行；
//   - ws:// 仅回环（127.0.0.0/8、::1、localhost）放行；
//   - ws:// 连非回环地址时，须显式 allowInsecure=true
//     （WAKEHUB_AGENT_ALLOW_INSECURE_WS=1），否则拒绝启动。
//
// 注意：WakeHub Server 本身不终止 TLS，wss:// 要求 Server 在 TLS
// 反向代理（或隧道）之后；本函数只校验地址，不校验连通性。
func ValidateServerWSURL(rawurl string, allowInsecure bool) error {
	u, err := url.Parse(strings.TrimSpace(rawurl))
	if err != nil || u == nil {
		return fmt.Errorf("WAKEHUB_SERVER_WS_URL %q 无法解析：%v", rawurl, err)
	}
	switch u.Scheme {
	case "ws", "wss":
	default:
		return fmt.Errorf("WAKEHUB_SERVER_WS_URL %q 须以 ws:// 或 wss:// 开头", rawurl)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("WAKEHUB_SERVER_WS_URL %q 缺少主机", rawurl)
	}
	if u.Scheme == "wss" {
		return nil
	}
	if isLoopbackHost(host) {
		return nil
	}
	if allowInsecure {
		return nil
	}
	return fmt.Errorf("WAKEHUB_SERVER_WS_URL %q 是明文 ws:// 且主机 %q 不是回环地址：Agent token 与任务内容将以明文传输；请改用 wss://（Server 须在 TLS 反向代理之后），或显式设置 WAKEHUB_AGENT_ALLOW_INSECURE_WS=1", rawurl, host)
}

// isLoopbackHost 报告主机名是否为回环地址：127.0.0.0/8、::1、localhost。
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// Validate 检查 Agent 启动必需配置（契约 §12.1）：ID/名/网段未配置
// 或不合法时返回错误，调用方据此 fatal 退出。token 不在此校验：
// token 可能来自环境变量，也可能来自 TokenFile（审批后持久化），
// 缺 token 时握手阶段自会 400/401，不在启动时拦截。
func (c AgentConfig) Validate() error {
	var problems []string
	if err := ValidateServerWSURL(c.ServerWSURL, c.AllowInsecureWS); err != nil {
		problems = append(problems, err.Error())
	}
	switch {
	case strings.TrimSpace(c.AgentID) == "":
		problems = append(problems, "WAKEHUB_AGENT_ID 未配置：请显式设置 Agent 唯一 ID（install.sh 会自动配置）")
	case c.AgentID == "local":
		problems = append(problems, `WAKEHUB_AGENT_ID 不得为保留值 "local"（local 节点已退役，远程 Agent 不得冒认，见契约 §3.2）`)
	case !agentIDPattern.MatchString(c.AgentID):
		problems = append(problems, fmt.Sprintf("WAKEHUB_AGENT_ID %q 格式非法：须匹配 ^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$", c.AgentID))
	}
	if strings.TrimSpace(c.AgentName) == "" {
		problems = append(problems, "WAKEHUB_AGENT_NAME 未配置：请显式设置 Agent 显示名（install.sh 会自动配置）")
	}
	switch {
	case strings.TrimSpace(c.Subnet) == "":
		problems = append(problems, "WAKEHUB_AGENT_SUBNET 未配置：请显式设置 Agent 所在网段 CIDR（须与本机局域网一致，如 192.168.1.0/24；install.sh 会自动配置）")
	case func() bool { _, _, err := net.ParseCIDR(c.Subnet); return err != nil }():
		problems = append(problems, fmt.Sprintf("WAKEHUB_AGENT_SUBNET %q 不是合法 CIDR（如 192.168.1.0/24）", c.Subnet))
	}
	// v0.2.9 问题 6：任务超时 ≤0 时 WithTimeout 立即过期，任务永远做不成。
	if c.TaskTimeout <= 0 {
		problems = append(problems, "WAKEHUB_AGENT_TASK_TIMEOUT 须大于 0（如 10s）")
	}
	if len(problems) > 0 {
		return fmt.Errorf("拒绝启动：\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// parseTrustedProxies 解析逗号分隔的 CIDR 列表。非法条目直接丢弃
// （fail-safe：解析失败的代理不被信任，X-Forwarded-For 不被采纳）。
func parseTrustedProxies(raw string) []*net.IPNet {
	var out []*net.IPNet
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, network, err := net.ParseCIDR(item); err == nil {
			out = append(out, network)
		}
	}
	return out
}

func parseCommaList(raw string) []string {
	var out []string
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

// parseDurationEnv 解析时长环境变量（v0.2.9 问题 6）：接受 Go 时长格式
// （如 15s、1m、500ms）；未设置、非法或 ≤0 时回落默认值。

// LocalNetworks 返回本机已启用网卡的 IPv4 网段（回环除外，v0.2.9 问题 8）。
// 容器 host 网络下看到的就是宿主机的网卡；拿不到网卡信息时返回错误，
// 调用方据此降级为警告（不因此拒绝启动）。
func LocalNetworks() ([]*net.IPNet, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []*net.IPNet
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() {
				continue
			}
			out = append(out, &net.IPNet{IP: ip4.Mask(ipnet.Mask), Mask: ipnet.Mask})
		}
	}
	return out, nil
}

// ValidateSubnetOnNetworks 检查配置网段是否落在任一本机网段内
// （v0.2.9 问题 8）：本机网段须包含配置网段——配置网段可以是本机网段的
// 子网（如本机 /24、配置 /25）；反过来（配置比本机网段大）不行。
// 不匹配时返回本机实际网段，方便用户纠正。
func ValidateSubnetOnNetworks(subnet string, networks []*net.IPNet) error {
	_, cfg, err := net.ParseCIDR(strings.TrimSpace(subnet))
	if err != nil {
		return fmt.Errorf("WAKEHUB_AGENT_SUBNET %q 不是合法 CIDR：%v", subnet, err)
	}
	if cfg.IP.To4() == nil {
		return fmt.Errorf("WAKEHUB_AGENT_SUBNET %q 不是 IPv4 网段，Agent 只管理 IPv4 局域网", subnet)
	}
	cfgOnes, _ := cfg.Mask.Size()
	for _, n := range networks {
		if n == nil {
			continue
		}
		ones, _ := n.Mask.Size()
		if ones <= cfgOnes && n.Contains(cfg.IP) {
			return nil
		}
	}
	return fmt.Errorf("WAKEHUB_AGENT_SUBNET %q 与本机网卡不符：本机网段为 %s；请改成与本机局域网一致的网段（特殊组网可用 WAKEHUB_AGENT_SKIP_SUBNET_CHECK=1 跳过本检查）", subnet, formatNetworks(networks))
}

func formatNetworks(networks []*net.IPNet) string {
	if len(networks) == 0 {
		return "（未检测到任何 IPv4 网段）"
	}
	parts := make([]string, 0, len(networks))
	for _, n := range networks {
		if n != nil {
			parts = append(parts, n.String())
		}
	}
	return strings.Join(parts, "、")
}
func parseDurationEnv(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(getEnv(key, ""))
	if raw == "" {
		return def
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	return def
}
