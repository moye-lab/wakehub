package config

import (
	"net"
	"strings"
	"testing"
	"time"
)

// TestAgentConfigValidate 覆盖契约 §12.1：Agent ID/名/网段未配置或
// 不合法时拒绝启动（M8 补）。
func TestAgentConfigValidate(t *testing.T) {
	valid := AgentConfig{
		AgentID:     "agent-home-lan",
		AgentName:   "Home-HostNetwork-Agent",
		Subnet:      "192.168.1.0/24",
		ServerWSURL: "ws://127.0.0.1:8080/api/v1/ws/agent",
		TaskTimeout: 10 * time.Second,
	}
	cases := []struct {
		name    string
		mutate  func(*AgentConfig)
		wantErr bool
		want    string // 期望错误文案包含的子串
	}{
		{"valid", func(c *AgentConfig) {}, false, ""},
		{"missing id", func(c *AgentConfig) { c.AgentID = "" }, true, "WAKEHUB_AGENT_ID 未配置"},
		{"missing name", func(c *AgentConfig) { c.AgentName = "" }, true, "WAKEHUB_AGENT_NAME 未配置"},
		{"missing subnet", func(c *AgentConfig) { c.Subnet = "" }, true, "WAKEHUB_AGENT_SUBNET 未配置"},
		{"invalid cidr", func(c *AgentConfig) { c.Subnet = "192.168.1.0/33" }, true, "不是合法 CIDR"},
		{"subnet not cidr", func(c *AgentConfig) { c.Subnet = "192.168.1.65" }, true, "不是合法 CIDR"},
		{"reserved local", func(c *AgentConfig) { c.AgentID = "local" }, true, `保留值 "local"`},
		{"bad id format", func(c *AgentConfig) { c.AgentID = "-bad" }, true, "格式非法"},
		{"id with space", func(c *AgentConfig) { c.AgentID = "a b" }, true, "格式非法"},
		{"all missing", func(c *AgentConfig) { c.AgentID, c.AgentName, c.Subnet = "", "", "" }, true, "WAKEHUB_AGENT_ID 未配置"},
		{"ws public rejected", func(c *AgentConfig) { c.ServerWSURL = "ws://192.168.1.2:8080/api/v1/ws/agent" }, true, "明文 ws://"},
		{"ws public allowed by flag", func(c *AgentConfig) {
			c.ServerWSURL = "ws://192.168.1.2:8080/api/v1/ws/agent"
			c.AllowInsecureWS = true
		}, false, ""},
		{"wss public ok", func(c *AgentConfig) { c.ServerWSURL = "wss://wol.example.com/api/v1/ws/agent" }, false, ""},
		{"http scheme rejected", func(c *AgentConfig) { c.ServerWSURL = "http://127.0.0.1:8080/api/v1/ws/agent" }, true, "ws:// 或 wss://"},
		{"empty ws url", func(c *AgentConfig) { c.ServerWSURL = "" }, true, "ws:// 或 wss://"},
		{"zero task timeout", func(c *AgentConfig) { c.TaskTimeout = 0 }, true, "WAKEHUB_AGENT_TASK_TIMEOUT 须大于 0"},
		{"negative task timeout", func(c *AgentConfig) { c.TaskTimeout = -time.Second }, true, "WAKEHUB_AGENT_TASK_TIMEOUT 须大于 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid
			tc.mutate(&cfg)
			err := cfg.Validate()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望 Validate 报错，实际通过")
				}
				if !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("错误文案应包含 %q，实际：%v", tc.want, err)
				}
			} else if err != nil {
				t.Fatalf("合法配置不应报错：%v", err)
			}
		})
	}
}

// TestLoadAgentConfigNoDefaults：取消代码默认值后，未设环境变量时
// ID/名/网段为空（由 Validate 拦截，不再静默用旧默认值运行）。
func TestLoadAgentConfigNoDefaults(t *testing.T) {
	t.Setenv("WAKEHUB_AGENT_ID", "")
	t.Setenv("WAKEHUB_AGENT_NAME", "")
	t.Setenv("WAKEHUB_AGENT_SUBNET", "")
	cfg := LoadAgentConfig()
	if cfg.AgentID != "" || cfg.AgentName != "" || cfg.Subnet != "" {
		t.Fatalf("未配置时应为空，实际 id=%q name=%q subnet=%q", cfg.AgentID, cfg.AgentName, cfg.Subnet)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatalf("空配置的 Validate 应报错")
	}
}

// TestLoadServerStatusCheckInterval 覆盖周期检测间隔解析（V-N2）：
// 默认 300s；显式 0 为禁用；1–29s 钳制为 30s；非法值回落默认。
func TestLoadServerStatusCheckInterval(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want time.Duration
	}{
		{"default", "", 300 * time.Second},
		{"explicit 600", "600", 600 * time.Second},
		{"zero disables", "0", 0},
		{"10 clamps to 30", "10", 30 * time.Second},
		{"29 clamps to 30", "29", 30 * time.Second},
		{"30 stays", "30", 30 * time.Second},
		{"garbage falls back", "abc", 300 * time.Second},
		{"negative falls back", "-5", 300 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WAKEHUB_STATUS_CHECK_INTERVAL", tc.env)
			if got := LoadServerConfig().StatusCheckInterval; got != tc.want {
				t.Fatalf("StatusCheckInterval = %v，想 %v", got, tc.want)
			}
		})
	}
}

// TestValidateServerWSURL 覆盖 v0.2.9 问题 3：回环 ws 放行、公网默认
// 要求 wss、明文 ws 须显式开关；与 TLS_SKIP_VERIFY 相互独立。
func TestValidateServerWSURL(t *testing.T) {
	cases := []struct {
		name          string
		rawurl        string
		allowInsecure bool
		wantErr       bool
		want          string
	}{
		{"ws loopback 127.0.0.1", "ws://127.0.0.1:8080/api/v1/ws/agent", false, false, ""},
		{"ws loopback 127.0.0.2", "ws://127.0.0.2:8080/api/v1/ws/agent", false, false, ""},
		{"ws localhost", "ws://localhost:8080/api/v1/ws/agent", false, false, ""},
		{"ws localhost upper", "ws://LOCALHOST:8080/api/v1/ws/agent", false, false, ""},
		{"ws ::1", "ws://[::1]:8080/api/v1/ws/agent", false, false, ""},
		{"wss loopback", "wss://127.0.0.1:8080/api/v1/ws/agent", false, false, ""},
		{"wss public", "wss://wol.example.com/api/v1/ws/agent", false, false, ""},
		{"wss public no port", "wss://wol.example.com/api/v1/ws/agent", false, false, ""},
		{"ws lan rejected", "ws://192.168.1.2:8080/api/v1/ws/agent", false, true, "明文 ws://"},
		{"ws lan allowed by flag", "ws://192.168.1.2:8080/api/v1/ws/agent", true, false, ""},
		{"ws public rejected", "ws://203.0.113.10:8080/api/v1/ws/agent", false, true, "WAKEHUB_AGENT_ALLOW_INSECURE_WS=1"},
		{"ws hostname rejected", "ws://server.local:8080/api/v1/ws/agent", false, true, "明文 ws://"},
		{"http scheme", "http://127.0.0.1:8080/api/v1/ws/agent", false, true, "ws:// 或 wss://"},
		{"https scheme", "https://127.0.0.1:8080/api/v1/ws/agent", false, true, "ws:// 或 wss://"},
		{"empty", "", false, true, "ws:// 或 wss://"},
		{"no host", "ws:///api/v1/ws/agent", false, true, "缺少主机"},
		{"garbage", "://bad url", false, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateServerWSURL(tc.rawurl, tc.allowInsecure)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望报错，实际通过")
				}
				if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("错误文案应包含 %q，实际：%v", tc.want, err)
				}
			} else if err != nil {
				t.Fatalf("合法地址不应报错：%v", err)
			}
		})
	}
}

// TestTaskTimeoutEnv 覆盖 v0.2.9 问题 6：两端超时分开配置；非法值回落
// 各自默认（服务端 15s、Agent 10s），服务端默认比 Agent 多 5s。
func TestTaskTimeoutEnv(t *testing.T) {
	serverCases := []struct {
		name string
		env  string
		want time.Duration
	}{
		{"default", "", 15 * time.Second},
		{"explicit 30s", "30s", 30 * time.Second},
		{"explicit 1m", "1m", time.Minute},
		{"garbage falls back", "abc", 15 * time.Second},
		{"zero falls back", "0s", 15 * time.Second},
		{"negative falls back", "-5s", 15 * time.Second},
	}
	for _, tc := range serverCases {
		t.Run("server/"+tc.name, func(t *testing.T) {
			t.Setenv("WAKEHUB_SERVER_TASK_TIMEOUT", tc.env)
			if got := LoadServerConfig().TaskTimeout; got != tc.want {
				t.Fatalf("Server TaskTimeout = %v, want %v", got, tc.want)
			}
		})
	}
	agentCases := []struct {
		name string
		env  string
		want time.Duration
	}{
		{"default", "", 10 * time.Second},
		{"explicit 5s", "5s", 5 * time.Second},
		{"explicit 500ms", "500ms", 500 * time.Millisecond},
		{"garbage falls back", "abc", 10 * time.Second},
		{"zero falls back", "0s", 10 * time.Second},
		{"negative falls back", "-5s", 10 * time.Second},
	}
	for _, tc := range agentCases {
		t.Run("agent/"+tc.name, func(t *testing.T) {
			t.Setenv("WAKEHUB_AGENT_TASK_TIMEOUT", tc.env)
			if got := LoadAgentConfig().TaskTimeout; got != tc.want {
				t.Fatalf("Agent TaskTimeout = %v, want %v", got, tc.want)
			}
		})
	}
	// 默认值关系：服务端等待比 Agent 执行多 5s，给回包留余量。
	t.Run("server default exceeds agent default", func(t *testing.T) {
		t.Setenv("WAKEHUB_SERVER_TASK_TIMEOUT", "")
		t.Setenv("WAKEHUB_AGENT_TASK_TIMEOUT", "")
		if got := LoadServerConfig().TaskTimeout; got <= LoadAgentConfig().TaskTimeout {
			t.Fatalf("服务端默认超时 %v 应大于 Agent 默认 %v", got, LoadAgentConfig().TaskTimeout)
		}
	})
}

// TestValidateSubnetOnNetworks 覆盖 v0.2.9 问题 8：配置网段须落在
// 任一本机网段内；子网可接受、超网拒绝；回环与 IPv6 不作本机网段。
func TestValidateSubnetOnNetworks(t *testing.T) {
	networks := []*net.IPNet{mustCIDRForTest(t, "192.168.1.0/24"), mustCIDRForTest(t, "192.168.1.0/24")}
	cases := []struct {
		name     string
		subnet   string
		networks []*net.IPNet
		wantOK   bool
		want     string
	}{
		{"精确匹配", "192.168.1.0/24", networks, true, ""},
		{"第二网段", "192.168.1.0/24", networks, true, ""},
		{"子网可接受", "192.168.1.128/25", networks, true, ""},
		{"超网拒绝", "192.168.1.0/23", networks, false, "与本机网卡不符"},
		{"其他网段拒绝", "172.16.0.0/24", networks, false, "与本机网卡不符"},
		{"无本机网段拒绝", "192.168.1.0/24", nil, false, "未检测到任何 IPv4 网段"},
		{"非法 CIDR", "192.168.1.0/33", networks, false, "不是合法 CIDR"},
		{"非 CIDR", "192.168.1.65", networks, false, "不是合法 CIDR"},
		{"IPv6 拒绝", "fd00::/64", networks, false, "不是 IPv4 网段"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSubnetOnNetworks(tc.subnet, tc.networks)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("应通过，got %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("应被拒")
				}
				if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("错误应含 %q，got %v", tc.want, err)
				}
			}
		})
	}
}

func mustCIDRForTest(t *testing.T, cidr string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatalf("parse CIDR %q: %v", cidr, err)
	}
	return n
}

// TestLocalNetworks 只断言枚举本身可用且结果干净（不依赖具体网卡）：
// 返回的须全是 IPv4 非回环网段。
func TestLocalNetworks(t *testing.T) {
	networks, err := LocalNetworks()
	if err != nil {
		t.Fatalf("LocalNetworks 不应报错：%v", err)
	}
	for _, n := range networks {
		if n == nil || n.IP.To4() == nil {
			t.Errorf("本机网段须为 IPv4，got %v", n)
		}
		if n.IP.IsLoopback() {
			t.Errorf("本机网段不应含回环，got %v", n)
		}
	}
}

// TestDetectPrimarySubnet 覆盖 v0.3.0 网段自动检测：有网卡时返回首个
// 非回环 IPv4 网段（与 LocalNetworks()[0] 一致）；沙箱无网卡时返回 ""。
func TestDetectPrimarySubnet(t *testing.T) {
	networks, err := LocalNetworks()
	if err != nil {
		t.Fatalf("LocalNetworks 不应报错：%v", err)
	}
	got := DetectPrimarySubnet()
	if len(networks) == 0 {
		if got != "" {
			t.Fatalf("无本机网段时应返回空，got %q", got)
		}
		return
	}
	if got != networks[0].String() {
		t.Fatalf("应返回首个本机网段 %q，got %q", networks[0].String(), got)
	}
	if _, _, err := net.ParseCIDR(got); err != nil {
		t.Fatalf("返回的网段须为合法 CIDR，got %q：%v", got, err)
	}
}
