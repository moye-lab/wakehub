// Package secret 为需要可逆存储的凭据提供 AES-256-GCM 加密。
//
// 使用场景：SSH 关机适配器的密码必须在执行时还原明文（SSH 密码认证），
// 不能只存哈希。密钥由服务端 WAKEHUB_SECRET_KEY 派生
// （SHA-256("wakehub-ssh-v1" + 0x00 + secretKey)），与会话签名等
// 其他用途隔离；每次加密用随机 12 字节 nonce，密文格式为
// base64(nonce || ciphertext)。SECRET_KEY 一旦更换，旧密文即无法
// 解密——调用方须把解密失败翻译为“请重新填写并保存”，不得静默失败。
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

func deriveKey(secretKey string) []byte {
	h := sha256.Sum256([]byte("wakehub-ssh-v1\x00" + secretKey))
	return h[:]
}

// Encrypt 加密明文，返回 base64(nonce || ciphertext)。
func Encrypt(secretKey, plaintext string) (string, error) {
	if secretKey == "" {
		return "", errors.New("服务端密钥未配置，无法加密凭据")
	}
	block, err := aes.NewCipher(deriveKey(secretKey))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("生成随机 nonce 失败: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt 还原 Encrypt 的输出；密钥错误或密文被篡改时返回错误。
func Decrypt(secretKey, encoded string) (string, error) {
	if secretKey == "" {
		return "", errors.New("服务端密钥未配置，无法解密凭据")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("密文不是合法的 base64: %w", err)
	}
	block, err := aes.NewCipher(deriveKey(secretKey))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("密文过短")
	}
	nonce, sealed := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", errors.New("密文解密失败（密钥已更换或密文被篡改）")
	}
	return string(plain), nil
}
