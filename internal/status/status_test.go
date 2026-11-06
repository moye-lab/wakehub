package status

import (
	"context"
	"net"
	"strings"
	"testing"
)

// 契约 §4.5：检测异常 ≠ 离线 —— 非法 IP 应为 unknown 并说明原因。
func TestCheckInvalidIP(t *testing.T) {
	for _, ip := range []string{"", "999.1.1.1", "not-an-ip", "::1"} {
		res := Check(context.Background(), ip, "", "ping", 0)
		if res.State != StateUnknown {
			t.Errorf("IP %q 应为 unknown，got %s", ip, res.State)
		}
		if res.ErrorReason == "" {
			t.Errorf("IP %q 应附错误原因", ip)
		}
	}
}

// B-N10：Ping 参数注入被拒 —— 以 "-" 开头的输入不是合法 IPv4，
// 应在校验层拦住，绝不拼进 ping 命令行。
func TestCheckPingInjection(t *testing.T) {
	for _, ip := range []string{"-x", "--help", "-c 1 127.0.0.1", "127.0.0.1; rm -rf /", "192.168.1.1 && id"} {
		res := Check(context.Background(), ip, "", "ping", 0)
		if res.State != StateUnknown {
			t.Errorf("注入输入 %q 应为 unknown，got %s", ip, res.State)
		}
		if res.ErrorReason == "" {
			t.Errorf("注入输入 %q 应附错误原因", ip)
		}
	}
}

// 契约 §4.5：TCP 端口关闭 ≠ 关机 —— ECONNREFUSED 应为 unknown。
func TestCheckTCPRefusedIsUnknown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close() // 随即关闭：该端口必然被拒绝。

	res := Check(context.Background(), "127.0.0.1", "", "tcp", port)
	if res.State != StateUnknown {
		t.Fatalf("端口关闭应为 unknown，got %s", res.State)
	}
	if !strings.Contains(res.ErrorReason, "端口关闭") {
		t.Fatalf("应说明端口关闭无法判定关机，got %q", res.ErrorReason)
	}
	if res.Method != "tcp" || res.CheckedAt.IsZero() {
		t.Fatalf("应返回检测方式与检测时间：%+v", res)
	}
}

// TCP 端口开放 → online。
func TestCheckTCPOpenIsOnline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	res := Check(context.Background(), "127.0.0.1", "", "tcp", port)
	if res.State != StateOnline {
		t.Fatalf("端口开放应为 online，got %s (%q)", res.State, res.ErrorReason)
	}
}

// ARP 缓存未命中 ≠ 离线 —— TEST-NET 地址不在缓存中，应为 unknown。
func TestCheckARPCacheMissIsUnknown(t *testing.T) {
	res := Check(context.Background(), "192.0.2.99", "", "arp", 0)
	if res.State != StateUnknown {
		t.Fatalf("ARP 缓存未命中应为 unknown，got %s", res.State)
	}
	if !strings.Contains(res.ErrorReason, "ARP 缓存") {
		t.Fatalf("应说明是缓存检测未命中，got %q", res.ErrorReason)
	}
}

// TCP 端口越界 → unknown（不静默钳制）。
func TestCheckTCPBadPort(t *testing.T) {
	res := Check(context.Background(), "127.0.0.1", "", "tcp", 70000)
	if res.State != StateUnknown {
		t.Fatalf("越界端口应为 unknown，got %s", res.State)
	}
}

// 检测应返回检测耗时（DurationMs ≥ 0）。
func TestCheckReturnsDuration(t *testing.T) {
	res := Check(context.Background(), "127.0.0.1", "", "tcp", 1)
	if res.LatencyMs < 0 {
		t.Fatalf("耗时不应为负：%+v", res)
	}
	if res.CheckedAt.IsZero() {
		t.Fatalf("应返回检测时间")
	}
}
