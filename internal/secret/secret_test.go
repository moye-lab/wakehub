package secret

import (
	"strings"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	enc, err := Encrypt("test-secret-key", "feiniu-admin-password")
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if enc == "feiniu-admin-password" || enc == "" {
		t.Fatalf("密文不应等于或为空明文: %q", enc)
	}
	plain, err := Decrypt("test-secret-key", enc)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if plain != "feiniu-admin-password" {
		t.Fatalf("解密结果不符: %q", plain)
	}
	// 同一明文两次加密结果不同（随机 nonce）。
	enc2, _ := Encrypt("test-secret-key", "feiniu-admin-password")
	if enc == enc2 {
		t.Fatalf("两次加密结果相同，nonce 疑似未随机")
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	enc, _ := Encrypt("key-a", "secret")
	if _, err := Decrypt("key-b", enc); err == nil {
		t.Fatalf("用错的密钥解密应失败")
	}
}

func TestDecryptTamperedFails(t *testing.T) {
	enc, _ := Encrypt("key-a", "secret")
	raw := []byte(enc)
	raw[len(raw)-1] ^= 0x01
	if _, err := Decrypt("key-a", string(raw)); err == nil {
		t.Fatalf("篡改后的密文解密应失败")
	}
}

func TestEncryptDecryptEmpty(t *testing.T) {
	if _, err := Encrypt("", "x"); err == nil {
		t.Fatalf("空密钥加密应失败")
	}
	if _, err := Decrypt("", "eA=="); err == nil {
		t.Fatalf("空密钥解密应失败")
	}
	if _, err := Decrypt("k", "not-base64!!!"); err == nil {
		t.Fatalf("非法 base64 解密应失败")
	}
	if _, err := Decrypt("k", "AA=="); err == nil {
		t.Fatalf("过短密文解密应失败")
	}
	enc, err := Encrypt("k", "")
	if err != nil {
		t.Fatalf("空明文加密失败: %v", err)
	}
	plain, err := Decrypt("k", enc)
	if err != nil || plain != "" {
		t.Fatalf("空明文往返失败: %q, %v", plain, err)
	}
	_ = strings.TrimSpace
}
