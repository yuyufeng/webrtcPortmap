// auth/crypto.go - 加密工具
package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/pbkdf2"
)

const (
	// 密钥派生参数
	pbkdf2Iterations = 100000
	keyLength        = 32 // AES-256
	saltLength       = 32

	// 加密参数
	nonceLength = 12 // GCM标准nonce长度
)

// Crypto 加密器
type Crypto struct {
	key []byte
}

// NewCrypto 从密码创建加密器
// 使用固定的salt（基于ID）确保相同ID+密码产生相同密钥
func NewCrypto(password, id string) *Crypto {
	// 使用ID作为salt，确保相同ID+密码总能派生相同密钥
	fmt.Printf("[Auth] NewCrypto: password='%s', id='%s'\n", password, id)
	salt := deriveSalt(id)
	fmt.Printf("[Auth] salt=%x\n", salt[:8])
	key := pbkdf2.Key([]byte(password), salt, pbkdf2Iterations, keyLength, sha256.New)
	fmt.Printf("[Auth] key=%x...\n", key[:8])
	return &Crypto{key: key}
}

// deriveSalt 从ID派生固定salt
func deriveSalt(id string) []byte {
	h := sha256.Sum256([]byte(id))
	return h[:saltLength]
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

// HashChallenge 计算挑战值的HMAC-SHA256响应
func (c *Crypto) HashChallenge(challenge string, timestamp int64) string {
	data := fmt.Sprintf("%s:%d", challenge, timestamp)
	fmt.Printf("[Auth] Go计算HMAC: challenge=%s, timestamp=%d, data=%s, keyLen=%d\n", challenge, timestamp, data, len(c.key))
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte(data))
	result := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	fmt.Printf("[Auth] Go HMAC结果: %s\n", result)
	return result
}

// VerifyResponse 验证响应
func (c *Crypto) VerifyResponse(challenge string, timestamp int64, response string) bool {
	expected := c.HashChallenge(challenge, timestamp)
	return expected == response
}
