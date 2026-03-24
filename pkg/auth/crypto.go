// auth/crypto.go - 加密工具
package auth

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

const (
	keyLength        = 32 // AES-256

	// 加密参数
	nonceLength = 12 // GCM标准nonce长度
)

// Crypto 加密器
type Crypto struct {
	key       []byte
	keyString string
}

// NewCrypto 从密码创建加密器
func NewCrypto(password, id string) *Crypto {
	keyString := portableHash256("key|" + id + "|" + password)
	keyHash := sha256.Sum256([]byte(keyString))
	key := make([]byte, keyLength)
	copy(key, keyHash[:])
	fmt.Printf("[Auth] NewCrypto: id='%s', key=%x...\n", id, key[:8])
	return &Crypto{key: key, keyString: keyString}
}

// Encrypt 加密数据（返回base64）
func (c *Crypto) Encrypt(plaintext []byte) (string, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, nonceLength)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt 解密数据（输入base64）
func (c *Crypto) Decrypt(ciphertextB64 string) ([]byte, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return nil, fmt.Errorf("base64 decode failed: %w", err)
	}

	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	if len(ciphertext) < nonceLength {
		return nil, errors.New("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceLength], ciphertext[nonceLength:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}

// EncryptString 加密字符串
func (c *Crypto) EncryptString(plaintext string) (string, error) {
	return c.Encrypt([]byte(plaintext))
}

// DecryptString 解密为字符串
func (c *Crypto) DecryptString(ciphertext string) (string, error) {
	data, err := c.Decrypt(ciphertext)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// GenerateChallenge 生成随机挑战值（base64）
func GenerateChallenge() (string, error) {
	challenge := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, challenge); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(challenge), nil
}

// HashChallenge 计算挑战值响应
func (c *Crypto) HashChallenge(challenge string, timestamp int64) string {
	data := fmt.Sprintf("resp|%s|%s|%d", c.keyString, challenge, timestamp)
	result := portableHash256(data)
	fmt.Printf("[Auth] Go challenge response: challenge=%s, timestamp=%d, result=%s\n", challenge, timestamp, result)
	return result
}

// VerifyResponse 验证响应
func (c *Crypto) VerifyResponse(challenge string, timestamp int64, response string) bool {
	expected := c.HashChallenge(challenge, timestamp)
	return expected == response
}

func portableHash256(input string) string {
	parts := make([]byte, 0, 64)
	for i := 0; i < 8; i++ {
		sum := fnv1a32(fmt.Sprintf("%d|%s", i, input))
		parts = append(parts, []byte(fmt.Sprintf("%08x", sum))...)
	}
	return string(parts)
}

func fnv1a32(input string) uint32 {
	var hash uint32 = 2166136261
	const prime uint32 = 16777619
	for i := 0; i < len(input); i++ {
		hash ^= uint32(input[i])
		hash *= prime
	}
	return hash
}
