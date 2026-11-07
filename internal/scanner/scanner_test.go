package scanner

import (
	"context"
	"testing"
	"time"
)

func TestValidateScanCIDR(t *testing.T) {
	cases := []struct {
		in       string
		want     string
		wantFail bool
	}{
		{"192.168.1.1/24", "192.168.1.0/24", false}, // 主机位清零
		{"192.168.1.0/24", "192.168.1.0/24", false},
		{"192.168.1.5/32", "192.168.1.5/32", false}, // 单主机允许
		{"172.16.0.0/25", "172.16.0.0/25", false},
		{"8.8.8.0/24", "", true},     // 公网拒绝
		{"1.1.1.1/32", "", true},     // 公网单主机也拒绝
		{"192.168.1.0/16", "", true}, // 掩码小于 /24 拒绝
		{"192.168.1.0/23", "", true}, // /23 拒绝
		{"not-a-cidr", "", true},     // 格式错误
		{"2001:db8::/32", "", true},  // IPv6 拒绝
		{"", "", true},
	}
	for _, c := range cases {
		got, err := ValidateScanCIDR(c.in)
		if c.wantFail {
			if err == nil {
				t.Errorf("ValidateScanCIDR(%q) 应失败，却返回 %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ValidateScanCIDR(%q) 意外失败: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ValidateScanCIDR(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHostsInCIDR_SkipsNetworkAndBroadcast(t *testing.T) {
	hosts, err := HostsInCIDR("192.168.1.0/24")
	if err != nil {
		t.Fatalf("HostsInCIDR: %v", err)
	}
	if len(hosts) != 254 {
		t.Fatalf("/24 应有 254 台主机，实际 %d", len(hosts))
	}
	if hosts[0].String() != "192.168.1.1" {
		t.Errorf("首台应为 192.168.1.1，实际 %s", hosts[0])
	}
	if hosts[len(hosts)-1].String() != "192.168.1.254" {
		t.Errorf("末台应为 192.168.1.254，实际 %s", hosts[len(hosts)-1])
	}
	for _, h := range hosts {
		if h.String() == "192.168.1.0" || h.String() == "192.168.1.255" {
			t.Errorf("不应包含网络/广播地址: %s", h)
		}
	}
}

func TestHostsInCIDR_EdgeMasks(t *testing.T) {
	// /31：RFC 3021，两地址皆为主机。
	hosts, err := HostsInCIDR("192.168.1.0/31")
	if err != nil {
		t.Fatalf("HostsInCIDR /31: %v", err)
	}
	if len(hosts) != 2 {
		t.Errorf("/31 应有 2 台主机，实际 %d", len(hosts))
	}
	// /32：本身就是单主机。
	hosts, err = HostsInCIDR("192.168.1.7/32")
	if err != nil {
		t.Fatalf("HostsInCIDR /32: %v", err)
	}
	if len(hosts) != 1 || hosts[0].String() != "192.168.1.7" {
		t.Errorf("/32 应为 [192.168.1.7]，实际 %v", hosts)
	}
	// /25：126 台。
	hosts, err = HostsInCIDR("192.168.1.0/25")
	if err != nil {
		t.Fatalf("HostsInCIDR /25: %v", err)
	}
	if len(hosts) != 126 {
		t.Errorf("/25 应有 126 台主机，实际 %d", len(hosts))
	}
}

func TestCIDRWithinAllowed(t *testing.T) {
	allowed := []string{"192.168.1.0/24", "192.168.1.0/24"}
	cases := []struct {
		req  string
		want bool
	}{
		{"192.168.1.0/24", true}, // 等于白名单项
		{"192.168.1.0/25", true}, // 白名单的子网
		{"192.168.1.128/25", true},
		{"192.168.1.0/24", true},
		{"10.0.1.0/24", false},    // 白名单外
		{"192.168.1.0/16", false}, // 比白名单大，不是子网
		{"8.8.8.0/24", false},
		{"not-a-cidr", false},
	}
	for _, c := range cases {
		if got := CIDRWithinAllowed(c.req, allowed); got != c.want {
			t.Errorf("CIDRWithinAllowed(%q) = %v, want %v", c.req, got, c.want)
		}
	}
	if CIDRWithinAllowed("192.168.1.0/24", nil) {
		t.Error("白名单为空时应拒绝一切网段")
	}
}

func TestLookupVendor(t *testing.T) {
	cases := []struct {
		mac  string
		want string
	}{
		{"DC:A6:32:11:22:33", "Raspberry Pi"},
		{"dc:a6:32:11:22:33", "Raspberry Pi"},  // 小写 MAC
		{"00:0C:29:AA:BB:CC", "VMware"},        // v0.2.0 新增
		{"00:16:3E:AA:BB:CC", "Xen"},           // v0.2.0 新增
		{"00:1C:42:AA:BB:CC", "Parallels"},     // v0.2.0 新增
		{"08:00:27:AA:BB:CC", "VirtualBox"},    // v0.2.0 新增
		{"00:11:32:AA:BB:CC", "Synology Inc."}, // v0.1.0 保留
		{"AA:BB:CC:DD:EE:FF", "Unknown Vendor"},
		{"00:11", "Unknown Vendor"}, // 过短
		{"", "Unknown Vendor"},
	}
	for _, c := range cases {
		if got := LookupVendor(c.mac); got != c.want {
			t.Errorf("LookupVendor(%q) = %q, want %q", c.mac, got, c.want)
		}
	}
}

// TestScanSubnet_NoHostsSmoke 是扫描主循环的冒烟测试：对一个确定无
// 主机的网段扫描，应正常返回空结果且不挂死（.0/.255 跳过逻辑已由
// HostsInCIDR 单测覆盖，此处验证“探测循环 + ARP 读取”整条路通）。
// 注意：本机无路由时 dial 快速失败，耗时远小于 250ms 超时；
// 250ms 是单台上限，总耗时约 254 台 / 32 并发 × 250ms ≈ 2s。
func TestScanSubnet_NoHostsSmoke(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	start := time.Now()
	got, err := ScanSubnet(ctx, "10.255.255.0/24")
	if err != nil {
		t.Fatalf("ScanSubnet: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("无主机网段应返回空结果，实际 %+v", got)
	}
	if d := time.Since(start); d > 12*time.Second {
		t.Errorf("扫描耗时过长: %v", d)
	}
}
