// tunnel/client.go - 控制端隧道管理器
package tunnel

import (
	"encoding/json"
	"fmt"
	"net"
	"sync"

	"webrtc-portmap/pkg/protocol"
)

// ClientManager 控制端隧道管理器
type ClientManager struct {
	streams   map[uint16]*ClientStream // streamID -> stream
	mu        sync.RWMutex
	handler   MessageHandler
}

// ClientStream 控制端流
type ClientStream struct {
	ID       uint16
	Conn     net.Conn
	MapID    string
	Protocol string
}

// NewClientManager 创建控制端管理器
func NewClientManager(handler MessageHandler) *ClientManager {
	return &ClientManager{
		streams: make(map[uint16]*ClientStream),
		handler: handler,
	}
}

// HandleConnectRequest 处理连接请求（从受控端）
func (m *ClientManager) HandleConnectRequest(payload []byte) error {
	var req protocol.StreamConnectReq
	if err := json.Unmarshal(payload, &req); err != nil {
		return err
	}

	fmt.Printf("[Client] Received connect request: stream=%d, remote=%s\n", req.StreamID, req.Remote)

	// 连接到远程目标
	conn, err := net.Dial(req.Protocol, req.Remote)
	if err != nil {
		// 发送失败响应
		resp := protocol.StreamConnectResp{
			StreamID: req.StreamID,
			Success:  false,
			Message:  err.Error(),
		}
		msg, _ := protocol.NewMessage(protocol.MsgTypeConnectResp, resp)
		m.handler.SendMessage(msg)
		return fmt.Errorf("failed to connect to %s: %w", req.Remote, err)
	}

	// 注册流
	m.mu.Lock()
	m.streams[req.StreamID] = &ClientStream{
		ID:       req.StreamID,
		Conn:     conn,
		Protocol: req.Protocol,
	}
	m.mu.Unlock()

	// 发送成功响应
	resp := protocol.StreamConnectResp{
		StreamID: req.StreamID,
		Success:  true,
	}
	msg, _ := protocol.NewMessage(protocol.MsgTypeConnectResp, resp)
	if err := m.handler.SendMessage(msg); err != nil {
		m.closeStream(req.StreamID)
		return err
	}

	// 启动读取循环
	go m.readLoop(req.StreamID, conn)

	return nil
}

// readLoop 读取远程数据
func (m *ClientManager) readLoop(streamID uint16, conn net.Conn) {
	defer m.closeStream(streamID)

	buf := make([]byte, 32*1024)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			// 连接关闭或错误
			return
		}

		// 打包数据
		data := m.packData(streamID, buf[:n])
		msg := &protocol.Message{
			Type:    protocol.MsgTypeData,
			Payload: data,
		}

		if err := m.handler.SendMessage(msg); err != nil {
			fmt.Printf("[Client] Failed to send data: %v\n", err)
			return
		}
	}
}

// packData 打包数据
func (m *ClientManager) packData(streamID uint16, data []byte) []byte {
	type dataMsg struct {
		StreamID uint16 `json:"sid"`
		Data     []byte `json:"data"`
	}
	msg := dataMsg{StreamID: streamID, Data: data}
	b, _ := json.Marshal(msg)
	return b
}

// HandleDataMessage 处理数据消息
func (m *ClientManager) HandleDataMessage(payload []byte) error {
	type dataMsg struct {
		StreamID uint16 `json:"sid"`
		Data     []byte `json:"data"`
	}
	var msg dataMsg
	if err := json.Unmarshal(payload, &msg); err != nil {
		return err
	}

	m.mu.RLock()
	stream, exists := m.streams[msg.StreamID]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("stream %d not found", msg.StreamID)
	}

	if _, err := stream.Conn.Write(msg.Data); err != nil {
		fmt.Printf("[Client] Write error on stream %d: %v\n", msg.StreamID, err)
		m.closeStream(msg.StreamID)
		return err
	}

	return nil
}

// closeStream 关闭流
func (m *ClientManager) closeStream(streamID uint16) {
	m.mu.Lock()
	stream, exists := m.streams[streamID]
	if exists {
		delete(m.streams, streamID)
	}
	m.mu.Unlock()

	if !exists {
		return
	}

	if stream.Conn != nil {
		stream.Conn.Close()
	}

	// 发送关闭消息
	closeMsg := protocol.StreamClose{
		StreamID: streamID,
		Reason:   "remote closed",
	}
	msg, _ := protocol.NewMessage(protocol.MsgTypeCloseStream, closeMsg)
	m.handler.SendMessage(msg)

	fmt.Printf("[Client] Stream %d closed\n", streamID)
}

// HandleCloseStream 处理关闭流消息
func (m *ClientManager) HandleCloseStream(payload []byte) error {
	var closeMsg protocol.StreamClose
	if err := json.Unmarshal(payload, &closeMsg); err != nil {
		return err
	}

	fmt.Printf("[Client] Received close for stream %d: %s\n", closeMsg.StreamID, closeMsg.Reason)
	
	m.mu.Lock()
	stream, exists := m.streams[closeMsg.StreamID]
	if exists {
		delete(m.streams, closeMsg.StreamID)
	}
	m.mu.Unlock()

	if exists && stream.Conn != nil {
		stream.Conn.Close()
	}

	return nil
}

// CloseAll 关闭所有流
func (m *ClientManager) CloseAll() {
	m.mu.Lock()
	streams := make([]uint16, 0, len(m.streams))
	for id := range m.streams {
		streams = append(streams, id)
	}
	m.mu.Unlock()

	for _, id := range streams {
		m.closeStream(id)
	}
}
