package scanner

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// 真主动 ARP 评估结论（任务书第 11 项，2026-11-05 本机实测）：
//
// v0.2.0 不实现真主动 ARP，降级为「TCP-touch + 读 ARP 缓存」。
// 原因：主动 ARP 需发原始以太帧（AF_PACKET + SOCK_RAW，或 arping），
// 本机沙箱内 root 也打不开 raw socket：AF_PACKET SOCK_RAW 与
// SOCK_DGRAM 均报 operation not permitted（cap_net_raw 不在有效集）；
// arping 同样依赖 raw socket。部署容器内是否配 CAP_NET_RAW 未验证，
// 且本机无法验证发现效果——写一个测不到的代码等于虚报。
// 现有 TCP-touch 法已在 2026-11-05 用户 VM 实测中跑通过全链路
// （扫描 192.168.1.1/24 发现 6 台以上设备），v0.2.0 保留并修好其缺陷
// （跳过网络/广播地址、信号量响应 ctx 取消、扫描超时由上层控制）。
// 若未来部署环境确认 CAP_NET_RAW 可用，可在 ScanSubnet 内再加一层
// ARP 请求后再读 /proc/net/arp；接口与返回值不需改。

// DiscoveredDevice 是扫描发现的一台设备（Agent 端原始结果）。
type DiscoveredDevice struct {
	IP     string `json:"ip"`
	MAC    string `json:"mac"`
	Vendor string `json:"vendor"`
}

// maxConcurrentDials 是单次扫描的 TCP 探测并发上限（契约 §7.3）。
const maxConcurrentDials = 32

// dialTimeout 是单台主机的 TCP 轻触超时：只用于“碰一下”让内核
// 填充 ARP 缓存，不做端口存活判定（端口关闭≠关机）。
const dialTimeout = 250 * time.Millisecond

// ValidateScanCIDR 校验扫描目标网段（契约 §7.2）：
//   - 必须是合法 CIDR；
//   - 必须是私有 IPv4 网段（禁止扫描公网）；
//   - 掩码不小于 /24（/32 单主机允许，< /24 拒绝，避免误扫大网段）。
//
// 成功时返回规范化后的 CIDR（主机位清零，如 192.168.1.1/24 → 192.168.1.0/24）。
func ValidateScanCIDR(cidr string) (string, error) {
	_, ipnet, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil {
		return "", fmt.Errorf("目标网段格式错误: %s", cidr)
	}
	if ipnet.IP.To4() == nil {
		return "", fmt.Errorf("只支持 IPv4 网段: %s", cidr)
	}
	ones, bits := ipnet.Mask.Size()
	if bits != 32 || ones < 24 {
		return "", fmt.Errorf("网段掩码不得小于 /24: %s", cidr)
	}
	if !ipnet.IP.IsPrivate() {
		return "", fmt.Errorf("禁止扫描公网网段: %s", cidr)
	}
	return ipnet.String(), nil
}

// CIDRWithinAllowed 判断请求网段是否落在白名单内（契约 §7.2）：
// 请求 CIDR 须等于白名单中某一项，或是其子网。
func CIDRWithinAllowed(requested string, allowed []string) bool {
	_, reqNet, err := net.ParseCIDR(requested)
	if err != nil {
		return false
	}
	reqOnes, _ := reqNet.Mask.Size()
	for _, a := range allowed {
		_, aNet, err := net.ParseCIDR(a)
		if err != nil {
			continue
		}
		aOnes, _ := aNet.Mask.Size()
		if aOnes <= reqOnes && aNet.Contains(reqNet.IP) {
			return true
		}
	}
	return false
}

// HostsInCIDR 枚举网段内可扫描的主机 IP（契约 §7.2）：
// 跳过网络地址（.0）与广播地址（.255）。/31 按 RFC 3021 两地址皆为
// 主机，/32 本身就是单主机，均不跳过。
func HostsInCIDR(cidr string) ([]net.IP, error) {
	normalized, err := ValidateScanCIDR(cidr)
	if err != nil {
		return nil, err
	}
	_, ipnet, _ := net.ParseCIDR(normalized)
	ones, _ := ipnet.Mask.Size()
	total := uint32(1) << (32 - ones)
	base := binary.BigEndian.Uint32(ipnet.IP.To4())
	hosts := make([]net.IP, 0, total)
	for i := uint32(0); i < total; i++ {
		// 仅当掩码小于 /31 时才有网络地址与广播地址的概念。
		if ones < 31 && (i == 0 || i == total-1) {
			continue
		}
		b := make(net.IP, 4)
		binary.BigEndian.PutUint32(b, base+i)
		hosts = append(hosts, b)
	}
	return hosts, nil
}

// ScanSubnet 扫描一个子网：对每台主机做一次 TCP 轻触（80 端口，
// 只为触发内核 ARP 解析），再读 /proc/net/arp 拿 MAC。
// 网络地址与广播地址不探测；信号量获取响应 ctx 取消。
// 返回值只含 ARP 缓存里有记录的主机——无响应主机不代表不存在，
// 只是本次未发现（契约 §7.3：单台无响应是正常阴性结果）。
func ScanSubnet(ctx context.Context, cidr string) ([]DiscoveredDevice, error) {
	hosts, err := HostsInCIDR(cidr)
	if err != nil {
		return nil, err
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrentDials)

loop:
	for _, ip := range hosts {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break loop
		}
		wg.Add(1)
		go func(ip net.IP) {
			defer wg.Done()
			defer func() { <-sem }()
			d := net.Dialer{Timeout: dialTimeout}
			conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), "80"))
			if err != nil {
				return
			}
			_ = conn.Close()
		}(ip)
	}
	wg.Wait()

	return readARPTable(cidr)
}

// readARPTable 读取 /proc/net/arp，返回落在指定网段内的设备。
// ARP 缓存不可用时返回空列表（不报错：无记录即未发现）。
func readARPTable(cidr string) ([]DiscoveredDevice, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return []DiscoveredDevice{}, nil
	}
	f, err := os.Open("/proc/net/arp")
	if err != nil {
		return []DiscoveredDevice{}, nil
	}
	defer f.Close()

	list := []DiscoveredDevice{}
	sc := bufio.NewScanner(f)
	_ = sc.Scan() // 跳过表头
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 {
			continue
		}
		parsedIP := net.ParseIP(fields[0])
		mac := strings.ToUpper(fields[3])
		if parsedIP == nil || !ipnet.Contains(parsedIP) || mac == "00:00:00:00:00:00" {
			continue
		}
		list = append(list, DiscoveredDevice{
			IP:     fields[0],
			MAC:    mac,
			Vendor: LookupVendor(mac),
		})
	}
	return list, nil
}

// ouiTable 是常见厂商前缀表（v0.1.0 的 13 条全部保留，v0.2.0 新增
// 虚拟化与常见消费厂商前缀）。覆盖范围：常见消费/网络设备与虚拟化
// 平台。未命中时返回 "Unknown Vendor"——完整 OUI 需查 IEEE 注册库，
// 本表不假装全覆盖。
var ouiTable = map[string]string{
	"00:11:32": "Synology Inc.",
	"24:5E:BE": "QNAP Systems",
	"DC:A6:32": "Raspberry Pi",
	"E4:5F:01": "Raspberry Pi",
	"B8:27:EB": "Raspberry Pi",
	"00:15:5D": "Microsoft Hyper-V",
	"00:50:56": "VMware",
	"00:0C:29": "VMware",
	"00:16:3E": "Xen",
	"00:1C:42": "Parallels",
	"52:54:00": "QEMU/KVM",
	"08:00:27": "VirtualBox",
	"00:E0:4C": "Realtek",
	"00:E0:97": "Realtek",
	"18:31:BF": "ASUSTek",
	"F0:2F:74": "ASUSTek",
	"3C:7C:3F": "ASUSTek",
	"D8:BB:C1": "Micro-Star (MSI)",
	"E0:D5:5E": "GIGA-BYTE",
	"00:1A:2B": "Apple",
	"3C:22:FB": "Apple",
	"00:1B:21": "Intel",
	"00:1E:67": "Intel",
	"FC:AA:14": "Xiaomi",
	"64:CC:2E": "Xiaomi",
	"00:23:12": "Huawei",
	"48:46:FB": "Huawei",
}

// LookupVendor 根据 MAC 前 3 字节查厂商。未命中返回 "Unknown Vendor"。
func LookupVendor(mac string) string {
	mac = strings.ToUpper(mac)
	if len(mac) < 8 {
		return "Unknown Vendor"
	}
	if v, ok := ouiTable[mac[:8]]; ok {
		return v
	}
	return "Unknown Vendor"
}
