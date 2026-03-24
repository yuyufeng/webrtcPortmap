// auth/handshake.go - 鉴权握手协议
package auth

import (
	"encoding/json"
	"fmt"
	"time"

	"webrtc-portmap/pkg/protocol"
)

const (
	// 时间戳容错窗口（防止时钟不同步和重放攻击）
	timestampTolerance = 60 * time.Second
)

// HandshakeState 握手状态
type HandshakeState int

const (
	HandshakeIdle HandshakeState = iota
	HandshakeChallengeSent
	HandshakeChallengeReceived
	HandshakeAuthenticated
	HandshakeFailed
)

// Handshaker 握手管理器
type Handshaker struct {
	state       HandshakeState
	crypto      *Crypto
	challenge   string
	timestamp   int64
	isInitiator bool // 是否是发起方（控制端）
}

// NewHandshaker 创建握手管理器
func NewHandshaker(password, id string, isInitiator bool) *Handshaker {
	return &Handshaker{
		state:       HandshakeIdle,
		crypto:      NewCrypto(password, id),
		isInitiator: isInitiator,
	}
}

// State 返回当前状态
func (h *Handshaker) State() HandshakeState {
	return h.state
}

// IsAuthenticated 是否已鉴权
func (h *Handshaker) IsAuthenticated() bool {
	return h.state == HandshakeAuthenticated
}

// CreateChallenge 创建挑战消息（控制端调用）
func (h *Handshaker) CreateChallenge() (*protocol.Message, error) {
	if h.state != HandshakeIdle {
		return nil, fmt.Errorf("invalid state: %d", h.state)
	}

	challenge, err := GenerateChallenge()
	if err != nil {
		return nil, fmt.Errorf("generate challenge failed: %w", err)
	}

	h.challenge = challenge
	h.timestamp = time.Now().UnixMilli() // 使用毫秒时间戳

	payload := protocol.AuthChallenge{
		Challenge: challenge,
		Timestamp: h.timestamp,
	}

	msg, err := protocol.NewMessage(protocol.MsgTypeAuthChallenge, payload)
	if err != nil {
		return nil, err
	}

	h.state = HandshakeChallengeSent
	return msg, nil
}

// HandleChallenge 处理挑战消息（受控端调用）
func (h *Handshaker) HandleChallenge(msg *protocol.Message) (*protocol.Message, error) {
	if h.state != HandshakeIdle {
		return nil, fmt.Errorf("invalid state: %d", h.state)
	}

	var challenge protocol.AuthChallenge
	if err := json.Unmarshal(msg.Payload, &challenge); err != nil {
		return nil, fmt.Errorf("unmarshal challenge failed: %w", err)
	}

	// 验证时间戳
	if !isValidTimestamp(challenge.Timestamp) {
		return nil, fmt.Errorf("invalid timestamp: %d", challenge.Timestamp)
	}

	h.challenge = challenge.Challenge
	h.timestamp = challenge.Timestamp

	// 生成响应
	response := h.crypto.HashChallenge(challenge.Challenge, challenge.Timestamp)

	payload := protocol.AuthResponse{
		Response:  response,
		Timestamp: time.Now().UnixMilli(), // 使用毫秒时间戳
	}

	respMsg, err := protocol.NewMessage(protocol.MsgTypeAuthResponse, payload)
	if err != nil {
		return nil, err
	}

	h.state = HandshakeChallengeReceived
	return respMsg, nil
}

// HandleResponse 处理响应消息（控制端调用）
func (h *Handshaker) HandleResponse(msg *protocol.Message) (*protocol.Message, error) {
	if h.state != HandshakeChallengeSent {
		return nil, fmt.Errorf("invalid state: %d", h.state)
	}

	var response protocol.AuthResponse
	if err := json.Unmarshal(msg.Payload, &response); err != nil {
		return nil, fmt.Errorf("unmarshal response failed: %w", err)
	}

	// 验证时间戳
	if !isValidTimestamp(response.Timestamp) {
		return nil, fmt.Errorf("invalid timestamp: %d", response.Timestamp)
	}

	// 验证响应
	if !h.crypto.VerifyResponse(h.challenge, h.timestamp, response.Response) {
		h.state = HandshakeFailed
		payload := protocol.AuthResult{
			Success: false,
			Message: "authentication failed: invalid response",
		}
		msg, _ := protocol.NewMessage(protocol.MsgTypeAuthResult, payload)
		return msg, fmt.Errorf("authentication failed")
	}

	// 鉴权成功
	h.state = HandshakeAuthenticated
	payload := protocol.AuthResult{
		Success: true,
		Message: "authentication successful",
	}

	resultMsg, err := protocol.NewMessage(protocol.MsgTypeAuthResult, payload)
	if err != nil {
		return nil, err
	}

	return resultMsg, nil
}

// HandleResult 处理鉴权结果（受控端调用）
func (h *Handshaker) HandleResult(msg *protocol.Message) error {
	if h.state != HandshakeChallengeReceived {
		return fmt.Errorf("invalid state: %d", h.state)
	}

	var result protocol.AuthResult
	if err := json.Unmarshal(msg.Payload, &result); err != nil {
		return fmt.Errorf("unmarshal result failed: %w", err)
	}

	if !result.Success {
		h.state = HandshakeFailed
		return fmt.Errorf("authentication failed: %s", result.Message)
	}

	h.state = HandshakeAuthenticated
	return nil
}

// isValidTimestamp 验证时间戳是否在容错窗口内
// 支持Unix秒和Unix毫秒两种格式
func isValidTimestamp(ts int64) bool {
	now := time.Now().Unix()
	
	// 判断是毫秒还是秒（毫秒时间戳通常大于1e12）
	if ts > 1e12 {
		// 毫秒时间戳，转换为秒
		ts = ts / 1000
	}
	
	diff := now - ts
	if diff < 0 {
		diff = -diff
	}
	return diff <= int64(timestampTolerance.Seconds())
}

// EncryptMessage 加密消息（鉴权后使用）
func (h *Handshaker) EncryptMessage(data []byte) (string, error) {
	if !h.IsAuthenticated() {
		return "", fmt.Errorf("not authenticated")
	}
	return h.crypto.Encrypt(data)
}

// DecryptMessage 解密消息（鉴权后使用）
func (h *Handshaker) DecryptMessage(ciphertext string) ([]byte, error) {
	if !h.IsAuthenticated() {
		return nil, fmt.Errorf("not authenticated")
	}
	return h.crypto.Decrypt(ciphertext)
}
