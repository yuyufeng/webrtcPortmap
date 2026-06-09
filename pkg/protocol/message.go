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
	MsgTypeHalfCloseStream // 半关闭流（关闭写方向）
	MsgTypeCloseStream // 关闭流

	// Agent配置消息（新架构）
	MsgTypeAgentConfig    // Agent配置（端口列表等）
	MsgTypeAccessPort     // 访问端口请求
	MsgTypeAccessResponse // 访问端口响应

	// HTTP 代理消息
	MsgTypeHTTPRequest  // HTTP 请求
	MsgTypeHTTPResponse // HTTP 响应

	// WebSocket 代理消息
	MsgTypeWSOpen
	MsgTypeWSOpenAck
	MsgTypeWSData
	MsgTypeWSClose
	MsgTypeWSError

	// 内嵌终端消息（持久 PTY 会话）
	MsgTypeTermOpen   // 控制端 -> Agent：附着/打开终端，请求回放
	MsgTypeTermData   // Agent -> 控制端：终端输出（可带 replay 标记）
	MsgTypeTermInput  // 控制端 -> Agent：键盘输入
	MsgTypeTermResize // 控制端 -> Agent：调整窗口大小
	MsgTypeTermExit   // Agent -> 控制端：shell 进程已退出
	MsgTypeTermClose  // 控制端 -> Agent：结束（可选重启）终端会话
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
	case MsgTypeHalfCloseStream:
		return "HALF_CLOSE_STREAM"
	case MsgTypeCloseStream:
		return "CLOSE_STREAM"
	case MsgTypeAgentConfig:
		return "AGENT_CONFIG"
	case MsgTypeAccessPort:
		return "ACCESS_PORT"
	case MsgTypeAccessResponse:
		return "ACCESS_RESPONSE"
	case MsgTypeHTTPRequest:
		return "HTTP_REQUEST"
	case MsgTypeHTTPResponse:
		return "HTTP_RESPONSE"
	case MsgTypeWSOpen:
		return "WS_OPEN"
	case MsgTypeWSOpenAck:
		return "WS_OPEN_ACK"
	case MsgTypeWSData:
		return "WS_DATA"
	case MsgTypeWSClose:
		return "WS_CLOSE"
	case MsgTypeWSError:
		return "WS_ERROR"
	case MsgTypeTermOpen:
		return "TERM_OPEN"
	case MsgTypeTermData:
		return "TERM_DATA"
	case MsgTypeTermInput:
		return "TERM_INPUT"
	case MsgTypeTermResize:
		return "TERM_RESIZE"
	case MsgTypeTermExit:
		return "TERM_EXIT"
	case MsgTypeTermClose:
		return "TERM_CLOSE"
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

// StreamHalfClose 半关闭流（仅关闭写方向）
type StreamHalfClose struct {
	StreamID uint16 `json:"stream_id"`
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
	AgentID    string          `json:"agent_id"`
	Ports      []PortInfo      `json:"ports"`
	ICEServers []ICEServerInfo `json:"ice_servers,omitempty"`
	Version    string          `json:"version,omitempty"`
	Terminal   *TerminalInfo   `json:"terminal,omitempty"` // 内嵌终端能力（启用时才有值）
}

// TerminalInfo 描述 Agent 的内嵌终端能力
type TerminalInfo struct {
	Enabled bool   `json:"enabled"`         // 是否启用内嵌终端
	Shell   string `json:"shell,omitempty"` // 实际使用的 shell，如 cmd/powershell/bash/sh
}

// ICEServerInfo 可序列化的 ICE server 配置
type ICEServerInfo struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
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
	ID      string            `json:"id"`                // 请求唯一ID，用于匹配响应
	PortID  string            `json:"port_id,omitempty"` // 目标端口配置ID
	Method  string            `json:"method"`            // GET, POST, PUT, DELETE 等
	Path    string            `json:"path,omitempty"`    // 请求路径，必须以 / 开头
	URL     string            `json:"url,omitempty"`     // 兼容旧版，完整 URL
	Headers map[string]string `json:"headers"`           // 请求头
	Body    []byte            `json:"body"`              // 请求体，JSON 中为 base64
}

// HTTPResponse HTTP 响应消息
type HTTPResponse struct {
	ID         string            `json:"id"`         // 对应的请求ID
	StatusCode int               `json:"status_code"` // HTTP 状态码
	Headers    map[string]string `json:"headers"`     // 响应头
	Body       []byte            `json:"body"`        // 响应体
	ChunkIndex int               `json:"chunk_index,omitempty"` // 分片序号，从0开始
	TotalChunks int              `json:"total_chunks,omitempty"` // 总分片数
	Done       bool              `json:"done,omitempty"` // 是否最后一个分片/完整响应
	Error      string            `json:"error,omitempty"` // 错误信息
}

// WSOpenRequest WebSocket 打开请求
type WSOpenRequest struct {
	SocketID string            `json:"socket_id"`
	PortID   string            `json:"port_id,omitempty"`
	Path     string            `json:"path,omitempty"`
	URL      string            `json:"url,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
}

// WSOpenAck WebSocket 打开响应
type WSOpenAck struct {
	SocketID string `json:"socket_id"`
	Success  bool   `json:"success"`
	Error    string `json:"error,omitempty"`
}

// WSData WebSocket 数据帧
type WSData struct {
	SocketID string `json:"socket_id"`
	Data     []byte `json:"data"`
	Text     bool   `json:"text"`
}

// WSClose WebSocket 关闭消息
type WSClose struct {
	SocketID string `json:"socket_id"`
	Code     int    `json:"code,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// WSError WebSocket 错误消息
type WSError struct {
	SocketID string `json:"socket_id"`
	Error    string `json:"error"`
}

// ==================== 内嵌终端 ====================

// TermOpenRequest 控制端请求打开/附着终端
type TermOpenRequest struct {
	Cols int `json:"cols,omitempty"` // 列数
	Rows int `json:"rows,omitempty"` // 行数
}

// TermData Agent 向控制端推送的终端输出
type TermData struct {
	Data   []byte `json:"data"`             // 原始字节（JSON 中为 base64）
	Replay bool   `json:"replay,omitempty"` // true 表示这是重连后的历史回放
}

// TermInput 控制端发送给 Agent 的键盘输入
type TermInput struct {
	Data []byte `json:"data"` // 原始字节（JSON 中为 base64）
}

// TermResize 控制端请求调整终端窗口大小
type TermResize struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// TermExit Agent 通知控制端 shell 进程已退出
type TermExit struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}
