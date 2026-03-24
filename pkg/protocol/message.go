// protocol/message.go - 定义通信协议和消息结构
package protocol

import (
	"encoding/json"
	"fmt"
)

// MessageType 定义消息类型
type MessageType uint8

const (
	// 控制消息
	MsgTypeAuthChallenge MessageType = iota + 1 // 鉴权挑战
	MsgTypeAuthResponse                         // 鉴权响应
	MsgTypeAuthResult                           // 鉴权结果
	MsgTypePing                                 // 心跳
	MsgTypePong                                 // 心跳响应
	MsgTypeCommand                              // 命令（保留但不再使用）
	MsgTypeCommandResult                        // 命令结果（保留但不再使用）
	MsgTypeError                                // 错误

	// 数据消息
	MsgTypeData        // 数据流
	MsgTypeConnectReq  // 连接请求（新建流）
	MsgTypeConnectResp // 连接响应
	MsgTypeCloseStream // 关闭流

	// Agent配置消息（新架构）
	MsgTypeAgentConfig    // Agent配置（端口列表等）
	MsgTypeAccessPort     // 访问端口请求
	MsgTypeAccessResponse // 访问端口响应

	// HTTP 代理消息
	MsgTypeHTTPRequest  // HTTP 请求
	MsgTypeHTTPResponse // HTTP 响应
)

func (t MessageType) String() string {
	switch t {
	case MsgTypeAuthChallenge:
		return "AUTH_CHALLENGE"
	case MsgTypeAuthResponse:
		return "AUTH_RESPONSE"
	case MsgTypeAuthResult:
		return "AUTH_RESULT"
	case MsgTypePing:
		return "PING"
	case MsgTypePong:
		return "PONG"
	case MsgTypeCommand:
		return "COMMAND"
	case MsgTypeCommandResult:
		return "COMMAND_RESULT"
	case MsgTypeError:
		return "ERROR"
	case MsgTypeData:
		return "DATA"
	case MsgTypeConnectReq:
		return "CONNECT_REQ"
	case MsgTypeConnectResp:
		return "CONNECT_RESP"
	case MsgTypeCloseStream:
		return "CLOSE_STREAM"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", t)
	}
}

// Message 是顶层消息结构
type Message struct {
	Type    MessageType     `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// Marshal 序列化消息
func (m *Message) Marshal() ([]byte, error) {
	return json.Marshal(m)
}

// UnmarshalMessage 反序列化消息
func UnmarshalMessage(data []byte) (*Message, error) {
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// NewMessage 创建新消息
func NewMessage(msgType MessageType, payload interface{}) (*Message, error) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &Message{
		Type:    msgType,
		Payload: payloadBytes,
	}, nil
}

// AuthChallenge 鉴权挑战消息（控制端 -> 受控端）
type AuthChallenge struct {
	Challenge string `json:"challenge"` // Base64编码的随机数
	Timestamp int64  `json:"timestamp"` // 时间戳（防止重放）
}

// AuthResponse 鉴权响应消息（受控端 -> 控制端）
type AuthResponse struct {
	Response  string `json:"response"`  // HMAC(challenge + timestamp, key)
	Timestamp int64  `json:"timestamp"` // 时间戳
}

// AuthResult 鉴权结果（控制端 -> 受控端）
type AuthResult struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// Command 端口映射命令
type Command struct {
	Action   string `json:"action"`   // "add", "remove", "list"
	Local    string `json:"local"`    // 本地地址，如 "0.0.0.0:8080"
	Remote   string `json:"remote"`   // 远程地址，如 "0.0.0.0:3333"
	Protocol string `json:"protocol"` // "tcp" 或 "udp"
	ID       string `json:"id"`       // 映射ID（用于remove）
}

// CommandResult 命令执行结果
type CommandResult struct {
	Success bool        `json:"success"`
	Message string      `json:"message,omitempty"`
	Maps    []MapInfo   `json:"maps,omitempty"` // list命令返回
}

// MapInfo 端口映射信息
type MapInfo struct {
	ID       string `json:"id"`
	Local    string `json:"local"`
	Remote   string `json:"remote"`
	Protocol string `json:"protocol"`
	Active   bool   `json:"active"`
	ConnCount int  `json:"conn_count"`
}

// StreamConnectReq 流连接请求（新建端口映射连接）
type StreamConnectReq struct {
	StreamID uint16 `json:"stream_id"`
	Local    string `json:"local"`
	Remote   string `json:"remote"`
	Protocol string `json:"protocol"`
}

// StreamConnectResp 流连接响应
type StreamConnectResp struct {
	StreamID uint16 `json:"stream_id"`
	Success  bool   `json:"success"`
	Message  string `json:"message,omitempty"`
}

// StreamClose 关闭流
type StreamClose struct {
	StreamID uint16 `json:"stream_id"`
	Reason   string `json:"reason,omitempty"`
}

// ErrorMessage 错误消息
type ErrorMessage struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Ping ping消息
type Ping struct {
	Timestamp int64 `json:"timestamp"`
}

// Pong pong消息
type Pong struct {
	Timestamp int64 `json:"timestamp"`
}

// AgentConfig Agent配置信息（Agent主动上报）
type AgentConfig struct {
	AgentID string      `json:"agent_id"`
	Ports   []PortInfo  `json:"ports"`
	Version string      `json:"version,omitempty"`
}

// PortInfo 端口信息
type PortInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`        // 服务名称，如"SSH"、"Web"、"MySQL"
	LocalAddr   string `json:"local_addr"`  // Agent本地地址，如"127.0.0.1:22"
	Protocol    string `json:"protocol"`    // tcp/udp
	Description string `json:"description,omitempty"`
	AllowAccess bool   `json:"allow_access"` // 是否允许访问
}

// AccessPortRequest 访问端口请求
type AccessPortRequest struct {
	PortID   string `json:"port_id"`
	StreamID uint16 `json:"stream_id"`
}

// AccessPortResponse 访问端口响应
type AccessPortResponse struct {
	PortID   string `json:"port_id"`
	StreamID uint16 `json:"stream_id"`
	Success  bool   `json:"success"`
	Message  string `json:"message,omitempty"`
}

// HTTPRequest HTTP 请求消息
type HTTPRequest struct {
	ID      string            `json:"id"`       // 请求唯一ID，用于匹配响应
	Method  string            `json:"method"`   // GET, POST, PUT, DELETE 等
	URL     string            `json:"url"`      // 完整 URL
	Headers map[string]string `json:"headers"`  // 请求头
	Body    []byte            `json:"body"`     // 请求体
}

// HTTPResponse HTTP 响应消息
type HTTPResponse struct {
	ID         string            `json:"id"`         // 对应的请求ID
	StatusCode int               `json:"status_code"` // HTTP 状态码
	Headers    map[string]string `json:"headers"`     // 响应头
	Body       []byte            `json:"body"`        // 响应体
	Error      string            `json:"error,omitempty"` // 错误信息
}
