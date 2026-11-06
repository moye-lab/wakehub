package wol

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
)

var macRegex = regexp.MustCompile(`^([0-9A-Fa-f]{2}[:-]){5}([0-9A-Fa-f]{2})$`)

// MagicPacket 标准 102 字节 WOL 魔术包 (6 字节 0xFF + 16 次 6 字节 MAC)
type MagicPacket [102]byte

// ValidateMAC 校验并规范化 MAC 地址为大写冒号分隔格式
func ValidateMAC(mac string) (string, error) {
	cleaned := strings.TrimSpace(mac)
	if !macRegex.MatchString(cleaned) {
		return "", fmt.Errorf("invalid MAC address format: %s", mac)
	}
	normalized := strings.ToUpper(strings.ReplaceAll(cleaned, "-", ":"))
	return normalized, nil
}

// BuildMagicPacket 构建标准 IEEE 802.3 WOL 魔术包
func BuildMagicPacket(mac string) (MagicPacket, error) {
	var packet MagicPacket
	normalized, err := ValidateMAC(mac)
	if err != nil {
		return packet, err
	}
	hwAddr, err := net.ParseMAC(normalized)
	if err != nil || len(hwAddr) != 6 {
		return packet, errors.New("failed to parse 6-byte hardware MAC address")
	}

	// 前 6 字节填充 0xFF 同步流
	for i := 0; i < 6; i++ {
		packet[i] = 0xFF
	}
	// 重复 16 次目标 MAC 地址
	for i := 1; i <= 16; i++ {
		copy(packet[i*6:(i+1)*6], hwAddr)
	}
	return packet, nil
}

type SendOptions struct {
	MAC              string
	BroadcastAddress string
	Port             int
	Count            int
	Interval         time.Duration
}

// Send 发送 WOL 魔术包，支持多次重发与 Context 超时控制。
// 端口越界、广播地址非法时返回错误，不再静默钳制（修复 B-N11）；
// 重发间隔响应 ctx 取消。
func Send(ctx context.Context, opts SendOptions) error {
	packet, err := BuildMagicPacket(opts.MAC)
	if err != nil {
		return err
	}
	if opts.BroadcastAddress == "" {
		opts.BroadcastAddress = "255.255.255.255"
	} else if ip := net.ParseIP(opts.BroadcastAddress); ip == nil || ip.To4() == nil {
		return fmt.Errorf("广播地址无效，应为合法 IPv4 地址: %s", opts.BroadcastAddress)
	}
	if opts.Port <= 0 || opts.Port > 65535 {
		return fmt.Errorf("WOL 端口超出范围 1-65535: %d", opts.Port)
	}
	if opts.Count <= 0 {
		opts.Count = 3
	}
	if opts.Interval <= 0 {
		opts.Interval = 150 * time.Millisecond
	}

	targetAddr := fmt.Sprintf("%s:%d", opts.BroadcastAddress, opts.Port)
	dialer := net.Dialer{Timeout: 3 * time.Second}
	conn, err := dialer.DialContext(ctx, "udp4", targetAddr)
	if err != nil {
		return fmt.Errorf("dial udp %s failed: %w", targetAddr, err)
	}
	defer conn.Close()

	for i := 0; i < opts.Count; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := conn.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
			return err
		}
		n, err := conn.Write(packet[:])
		if err != nil {
			return fmt.Errorf("write magic packet attempt %d failed: %w", i+1, err)
		}
		if n != len(packet) {
			return fmt.Errorf("incomplete magic packet write: %d/%d", n, len(packet))
		}
		if i < opts.Count-1 {
			// 重发间隔响应 ctx 取消（修复 B-N11：原 time.Sleep 不监听 ctx）。
			timer := time.NewTimer(opts.Interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return nil
}
