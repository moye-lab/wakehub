package wol

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestValidateMAC(t *testing.T) {
	validCases := map[string]string{
		"aa:bb:cc:dd:ee:ff": "AA:BB:CC:DD:EE:FF",
		"00-11-22-33-44-55": "00:11:22:33:44:55",
		"A1:B2:C3:D4:E5:F6": "A1:B2:C3:D4:E5:F6",
	}
	for input, expected := range validCases {
		got, err := ValidateMAC(input)
		if err != nil {
			t.Fatalf("expected valid MAC for %s, got err: %v", input, err)
		}
		if got != expected {
			t.Fatalf("expected %s, got %s", expected, got)
		}
	}

	invalidCases := []string{"invalid", "00:11:22:33:44", "00:11:22:33:44:GG", ""}
	for _, input := range invalidCases {
		if _, err := ValidateMAC(input); err == nil {
			t.Fatalf("expected error for invalid MAC: %s", input)
		}
	}
}

func TestBuildMagicPacket(t *testing.T) {
	pkt, err := BuildMagicPacket("01:02:03:04:05:06")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pkt) != 102 {
		t.Fatalf("expected 102 bytes, got %d", len(pkt))
	}
	if !bytes.Equal(pkt[0:6], []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}) {
		t.Fatalf("invalid header sync stream")
	}
	expectedMAC := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
	for i := 1; i <= 16; i++ {
		segment := pkt[i*6 : (i+1)*6]
		if !bytes.Equal(segment, expectedMAC) {
			t.Fatalf("mismatch at repetition %d: got %v", i, segment)
		}
	}
}

// B-N11：端口越界必须报错，不再静默钳制为 9。
func TestSendPortOutOfRange(t *testing.T) {
	for _, port := range []int{-1, 0, 65536, 70000} {
		opts := SendOptions{MAC: "01:02:03:04:05:06", BroadcastAddress: "255.255.255.255", Port: port, Count: 1}
		if err := Send(context.Background(), opts); err == nil {
			t.Fatalf("端口 %d 应报错", port)
		}
	}
}

// B-N11：广播地址非法必须报错（空地址除外：空表示用默认 255.255.255.255）。
func TestSendBroadcastInvalid(t *testing.T) {
	for _, addr := range []string{"not-an-ip", "256.1.1.1", "example.com"} {
		opts := SendOptions{MAC: "01:02:03:04:05:06", BroadcastAddress: addr, Port: 9, Count: 1}
		if err := Send(context.Background(), opts); err == nil {
			t.Fatalf("广播地址 %q 应报错", addr)
		}
	}
}

// 合法参数应真实发包（UDP 发往本机回环，无监听者亦不报错）。
func TestSendLocalhost(t *testing.T) {
	opts := SendOptions{MAC: "01:02:03:04:05:06", BroadcastAddress: "127.0.0.1", Port: 9, Count: 1}
	if err := Send(context.Background(), opts); err != nil {
		t.Fatalf("发往 127.0.0.1:9 应成功：%v", err)
	}
}

// B-N11：重发间隔必须响应 ctx 取消，而不是 time.Sleep 死等。
func TestSendContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	opts := SendOptions{MAC: "01:02:03:04:05:06", BroadcastAddress: "127.0.0.1", Port: 9, Count: 5, Interval: time.Second}
	err := Send(ctx, opts)
	if err == nil {
		t.Fatalf("取消后应返回错误")
	}
	if time.Since(start) > 800*time.Millisecond {
		t.Fatalf("取消后应在 1 个间隔内返回，实际耗时 %v", time.Since(start))
	}
}
