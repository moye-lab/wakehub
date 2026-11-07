package sleep

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 契约 §6.1：Endpoint allowlist —— 仅 RFC1918 私有 IPv4 与本机回环。
func TestValidateEndpoint(t *testing.T) {
	valid := []string{
		"http://192.168.1.29:8765",
		"http://172.16.0.5/sleep",
		"http://192.168.1.100:8080/api/sol",
		"https://192.168.1.29",
		"http://127.0.0.1:8765", // 回环：Sleep 服务可跑在 Agent 本机（M3 注记）。
	}
	for _, ep := range valid {
		if err := ValidateEndpoint(ep); err != nil {
			t.Errorf("端点 %q 应通过，got %v", ep, err)
		}
	}

	invalid := map[string]string{
		"http://8.8.8.8:8080":           "公网地址",
		"http://1.1.1.1":                "公网地址",
		"http://169.254.169.254/latest": "云元数据地址",
		"http://169.254.1.1":            "链路本地段",
		"http://example.com:8080":       "主机名",
		"http://user:pass@192.168.1.1/": "userinfo",
		"http://192.168.1.1:70000/":     "端口越界",
		"http://192.168.1.1:0/":         "端口为 0",
		"ftp://192.168.1.1/":            "非 http/https",
		"":                              "空端点",
		"http://[::1]:8080/":            "IPv6",
	}
	for ep, why := range invalid {
		if err := ValidateEndpoint(ep); err == nil {
			t.Errorf("端点 %q 应被拒（%s）", ep, why)
		}
	}
}

// 契约 §6.1：禁跟随重定向 —— 302 必须视为失败而非静默跟到另一台主机。
func TestExecuteRejectsRedirect(t *testing.T) {
	var targetHits int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	err := Execute(context.Background(), "http_sol", redirector.URL, ActionSleep)
	if err == nil {
		t.Fatalf("重定向应被拒绝")
	}
	if !strings.Contains(err.Error(), "重定向") {
		t.Fatalf("错误应提及重定向，got %v", err)
	}
	if targetHits != 0 {
		t.Fatalf("重定向目标不应被访问，实际 %d 次", targetHits)
	}
}

// 正常端点 200 → 成功；端点 500 → 失败。
func TestExecuteStatus(t *testing.T) {
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("应为 POST，got %s", r.Method)
		}
		if got := r.URL.Path; got != "/sleep" {
			t.Errorf("路径应为 /sleep，got %s", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer okSrv.Close()
	if err := Execute(context.Background(), "http_sol", okSrv.URL, ActionSleep); err != nil {
		t.Fatalf("200 端点应成功：%v", err)
	}

	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failSrv.Close()
	if err := Execute(context.Background(), "http_sol", failSrv.URL, ActionShutdown); err == nil {
		t.Fatalf("500 端点应失败")
	}
}

// Execute 先校验端点：公网地址在发请求前就被拦住。
func TestExecuteRejectsPublicEndpoint(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := Execute(ctx, "http_sol", "http://8.8.8.8:8080/sleep", ActionSleep); err == nil {
		t.Fatalf("公网端点应被拒")
	}
}

// 非法动作不发请求。
func TestExecuteInvalidAction(t *testing.T) {
	if err := Execute(context.Background(), "http_sol", "http://192.168.1.29:8765", Action("explode")); err == nil {
		t.Fatalf("非法动作应报错")
	}
}

// v0.2.9 问题 7：端点/IP 须落在 Agent 管理网段内；回环始终放行；
// Agent 未声明网段时只做基线校验。
func mustCIDR(t *testing.T, cidr string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatalf("parse CIDR %q: %v", cidr, err)
	}
	return n
}

func TestValidateEndpointInScope(t *testing.T) {
	scope := []*net.IPNet{mustCIDR(t, "192.168.1.0/24"), mustCIDR(t, "192.168.1.0/24")}
	cases := []struct {
		name   string
		ep     string
		cidrs  []*net.IPNet
		wantOK bool
		want   string
	}{
		{"网段内", "http://192.168.1.29:8765", scope, true, ""},
		{"第二网段内", "http://192.168.1.50:8009", scope, true, ""},
		{"网段外仍私有", "http://192.168.2.50:8009", scope, false, "不在执行 Agent 的管理网段内"},
		{"网段外 172 段", "http://172.16.0.5:8009", scope, false, "不在执行 Agent 的管理网段内"},
		{"回环始终放行", "http://127.0.0.1:8765", scope, true, ""},
		{"无网段声明只做基线", "http://192.168.2.50:8009", nil, true, ""},
		{"公网仍被基线拦", "http://8.8.8.8:8080", scope, false, "私有 IPv4"},
		{"主机名仍被基线拦", "http://example.com:8080", scope, false, "私有 IPv4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateEndpointInScope(tc.ep, tc.cidrs)
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

func TestValidateIPInScope(t *testing.T) {
	scope := []*net.IPNet{mustCIDR(t, "192.168.1.0/24")}
	cases := []struct {
		name   string
		ip     string
		cidrs  []*net.IPNet
		wantOK bool
		want   string
	}{
		{"网段内", "192.168.1.29", scope, true, ""},
		{"网段外", "192.168.2.50", scope, false, "不在执行 Agent 的管理网段内"},
		{"回环放行", "127.0.0.1", scope, true, ""},
		{"无网段声明放行", "192.168.1.50", nil, true, ""},
		{"非法 IP", "not-an-ip", scope, false, "不是合法 IP 地址"},
		{"IPv6", "::1", scope, false, "不是 IPv4 地址"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateIPInScope(tc.ip, tc.cidrs)
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
