// tunnel/manager.go - 端口映射管理器
package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"webrtc-portmap/pkg/protocol"
)

// StreamID 流ID生成器
type StreamID struct {
	value uint32
}

func (s *StreamID) Next() uint16 {
	return uint16(atomic.AddUint32(&s.value, 1) & 0xFFFF)
}

// MessageHandler 消息处理器接口
type MessageHandler interface {
	SendMessage(msg *protocol.Message) error
}

// Map 端口映射
type Map struct {
	ID       string
	Local    string
	Remote   string
	Protocol string
	listener net.Listener
	connMap  map[uint16]net.Conn // streamID -> conn
	mu       sync.RWMutex
	active   bool
	cancel   context.CancelFunc
}

// Manager 端口映射管理器
type Manager struct {
	maps      map[string]*Map // id -> map
	mu        sync.RWMutex
	handler   MessageHandler
	streamID  StreamID
	
	// 流管理
	streams   map[uint16]*Stream // streamID -> stream
	streamMu  sync.RWMutex
}

// Stream 数据流
type Stream struct {
	ID       uint16
	MapID    string
	Conn     net.Conn
	LocalAddr string
	RemoteAddr string
}

// NewManager 创建管理器
func NewManager(handler MessageHandler) *Manager {
	return &Manager{
		maps:    make(map[string]*Map),
		streams: make(map[uint16]*Stream),
		handler: handler,
	}
}

// AddMap 添加端口映射（受控端调用）
func (m *Manager) AddMap(local, remote, proto, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.maps[id]; exists {
		return fmt.Errorf("map with id %s already exists", id)
	}

	if proto != "tcp" && proto != "udp" {
		return fmt.Errorf("unsupported protocol: %s", proto)
	}

	mapEntry := &Map{
		ID:       id,
		Local:    local,
		Remote:   remote,
		Protocol: proto,
		connMap:  make(map[uint16]net.Conn),
		active:   true,
	}

	// 启动监听
	ctx, cancel := context.WithCancel(context.Background())
	mapEntry.cancel = cancel

	switch proto {
	case "tcp":
		listener, err := net.Listen("tcp", local)
		if err != nil {
			return fmt.Errorf("failed to listen on %s: %w", local, err)
		}
		mapEntry.listener = listener
		go m.tcpAcceptLoop(ctx, mapEntry)
	case "udp":
		// UDP实现稍复杂，先返回错误
		return fmt.Errorf("UDP not implemented yet")
	}

	m.maps[id] = mapEntry
	fmt.Printf("[Tunnel] Added %s map: %s -> %s (id=%s)\n", proto, local, remote, id)
	return nil
}

// RemoveMap 移除端口映射
func (m *Manager) RemoveMap(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	mapEntry, exists := m.maps[id]
	if !exists {
		return fmt.Errorf("map not found: %s", id)
	}

	// 关闭监听
	if mapEntry.cancel != nil {
		mapEntry.cancel()
	}
	if mapEntry.listener != nil {
		mapEntry.listener.Close()
	}

	// 关闭所有连接
	mapEntry.mu.Lock()
	for streamID, conn := range mapEntry.connMap {
		conn.Close()
		m.removeStreamInternal(streamID)
	}
	mapEntry.mu.Unlock()

	delete(m.maps, id)
	fmt.Printf("[Tunnel] Removed map: %s\n", id)
	return nil
}

// GetMaps 获取所有映射
func (m *Manager) GetMaps() []protocol.MapInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var infos []protocol.MapInfo
	for id, m := range m.maps {
		m.mu.RLock()
		info := protocol.MapInfo{
			ID:        id,
			Local:     m.Local,
			Remote:    m.Remote,
			Protocol:  m.Protocol,
			Active:    m.active,
			ConnCount: len(m.connMap),
		}
		m.mu.RUnlock()
		infos = append(infos, info)
	}
	return infos
}

// tcpAcceptLoop TCP接受循环
func (m *Manager) tcpAcceptLoop(ctx context.Context, mapEntry *Map) {
	for {
		conn, err := mapEntry.listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				fmt.Printf("[Tunnel] Accept error: %v\n", err)
				time.Sleep(100 * time.Millisecond)
				continue
			}
		}

		go m.handleLocalConn(ctx, mapEntry, conn)
	}
}

// handleLocalConn 处理本地连接
func (m *Manager) handleLocalConn(ctx context.Context, mapEntry *Map, conn net.Conn) {
	streamID := m.streamID.Next()

	// 注册流
	m.streamMu.Lock()
	m.streams[streamID] = &Stream{
		ID:         streamID,
		MapID:      mapEntry.ID,
		Conn:       conn,
		LocalAddr:  conn.LocalAddr().String(),
		RemoteAddr: conn.RemoteAddr().String(),
	}
	m.streamMu.Unlock()

	mapEntry.mu.Lock()
	mapEntry.connMap[streamID] = conn
	mapEntry.mu.Unlock()

	fmt.Printf("[Tunnel] New connection: stream=%d, from=%s\n", streamID, conn.RemoteAddr())

	// 发送连接请求给控制端
	req := protocol.StreamConnectReq{
		StreamID: streamID,
		Local:    mapEntry.Local,
		Remote:   mapEntry.Remote,
		Protocol: mapEntry.Protocol,
	}

	msg, err := protocol.NewMessage(protocol.MsgTypeConnectReq, req)
	if err != nil {
		fmt.Printf("[Tunnel] Failed to create connect request: %v\n", err)
		m.closeStream(streamID)
		return
	}

	if err := m.handler.SendMessage(msg); err != nil {
		fmt.Printf("[Tunnel] Failed to send connect request: %v\n", err)
		m.closeStream(streamID)
		return
	}

	// 读取本地数据并转发
	buf := make([]byte, 32*1024)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			if err == io.EOF {
				// 仅关闭远端写方向，保留读方向继续接收响应数据。
				fmt.Printf("[Tunnel] Local write side closed on stream %d, notifying remote half-close\n", streamID)
				halfClose := protocol.StreamHalfClose{StreamID: streamID}
				msg, _ := protocol.NewMessage(protocol.MsgTypeHalfCloseStream, halfClose)
				if sendErr := m.handler.SendMessage(msg); sendErr != nil {
					fmt.Printf("[Tunnel] Failed to send half-close on stream %d: %v\n", streamID, sendErr)
					m.closeStream(streamID)
				}
				return
			}
			fmt.Printf("[Tunnel] Read error on stream %d: %v\n", streamID, err)
			break
		}

		// 打包数据消息 [streamID(2) + length(4) + data(n)]
		data := m.packData(streamID, buf[:n])
		fmt.Printf("[Tunnel] Forwarding %d bytes from local to remote on stream %d\n", n, streamID)
		
		dataMsg := &protocol.Message{
			Type:    protocol.MsgTypeData,
			Payload: data,
		}

		if err := m.handler.SendMessage(dataMsg); err != nil {
			fmt.Printf("[Tunnel] Failed to send data on stream %d: %v\n", streamID, err)
			break
		}
	}

	m.closeStream(streamID)
}

// packData 打包数据 [streamID(2) + length(4) + data(n)]
func (m *Manager) packData(streamID uint16, data []byte) []byte {
	// 简化为JSON格式，实际生产环境可以用更高效的二进制格式
	type dataMsg struct {
		StreamID uint16 `json:"sid"`
		Data     []byte `json:"data"`
	}
	msg := dataMsg{StreamID: streamID, Data: data}
	b, _ := json.Marshal(msg)
	return b
}

// HandleDataMessage 处理数据消息（从控制端收到）
func (m *Manager) HandleDataMessage(payload []byte) error {
	// 解析数据消息
	type dataMsg struct {
		StreamID uint16 `json:"sid"`
		Data     []byte `json:"data"`
	}
	var msg dataMsg
	if err := json.Unmarshal(payload, &msg); err != nil {
		return err
	}
	fmt.Printf("[Tunnel] Received %d bytes from remote for stream %d\n", len(msg.Data), msg.StreamID)

	m.streamMu.RLock()
	stream, exists := m.streams[msg.StreamID]
	m.streamMu.RUnlock()

	if !exists {
		fmt.Printf("[Tunnel] Drop remote data for missing stream %d\n", msg.StreamID)
		return fmt.Errorf("stream %d not found", msg.StreamID)
	}

	if _, err := stream.Conn.Write(msg.Data); err != nil {
		fmt.Printf("[Tunnel] Write error on stream %d: %v\n", msg.StreamID, err)
		m.closeStream(msg.StreamID)
		return err
	}
	fmt.Printf("[Tunnel] Wrote %d bytes from remote to local on stream %d\n", len(msg.Data), msg.StreamID)

	return nil
}

// HandleConnectResponse 处理连接响应
func (m *Manager) HandleConnectResponse(payload []byte) error {
	var resp protocol.StreamConnectResp
	if err := json.Unmarshal(payload, &resp); err != nil {
		return err
	}

	if !resp.Success {
		fmt.Printf("[Tunnel] Connect failed for stream %d: %s\n", resp.StreamID, resp.Message)
		m.closeStream(resp.StreamID)
		return fmt.Errorf("connect failed: %s", resp.Message)
	}

	fmt.Printf("[Tunnel] Stream %d connected to remote\n", resp.StreamID)
	return nil
}

// closeStream 关闭流
func (m *Manager) closeStream(streamID uint16) {
	m.closeStreamWithNotify(streamID, true)
}

func (m *Manager) closeStreamWithNotify(streamID uint16, notifyRemote bool) {
	m.streamMu.Lock()
	stream, exists := m.streams[streamID]
	if exists {
		delete(m.streams, streamID)
	}
	m.streamMu.Unlock()

	if !exists {
		return
	}

	if stream.Conn != nil {
		stream.Conn.Close()
	}

	// 从map的connMap中移除
	m.mu.RLock()
	mapEntry, exists := m.maps[stream.MapID]
	m.mu.RUnlock()

	if exists {
		mapEntry.mu.Lock()
		delete(mapEntry.connMap, streamID)
		mapEntry.mu.Unlock()
	}

	if notifyRemote {
		// 发送关闭消息给对端
		closeMsg := protocol.StreamClose{
			StreamID: streamID,
			Reason:   "local closed",
		}
		msg, _ := protocol.NewMessage(protocol.MsgTypeCloseStream, closeMsg)
		m.handler.SendMessage(msg)
	}

	fmt.Printf("[Tunnel] Stream %d closed\n", streamID)
}

// removeStreamInternal 内部移除流（不加锁）
func (m *Manager) removeStreamInternal(streamID uint16) {
	m.streamMu.Lock()
	stream, exists := m.streams[streamID]
	if exists {
		delete(m.streams, streamID)
	}
	m.streamMu.Unlock()

	if exists && stream.Conn != nil {
		stream.Conn.Close()
	}
}

// HandleCloseStream 处理流关闭消息
func (m *Manager) HandleCloseStream(payload []byte) error {
	var closeMsg protocol.StreamClose
	if err := json.Unmarshal(payload, &closeMsg); err != nil {
		return err
	}

	fmt.Printf("[Tunnel] Received close for stream %d: %s\n", closeMsg.StreamID, closeMsg.Reason)
	m.closeStreamWithNotify(closeMsg.StreamID, false)
	return nil
}

// HandleHalfCloseStream 处理流半关闭消息，只关闭本地连接写方向，保留读方向继续接收剩余数据。
func (m *Manager) HandleHalfCloseStream(payload []byte) error {
	var halfClose protocol.StreamHalfClose
	if err := json.Unmarshal(payload, &halfClose); err != nil {
		return err
	}

	m.streamMu.RLock()
	stream, exists := m.streams[halfClose.StreamID]
	m.streamMu.RUnlock()
	if !exists {
		fmt.Printf("[Tunnel] Ignore half-close for missing stream %d\n", halfClose.StreamID)
		return nil
	}

	if tcpConn, ok := stream.Conn.(*net.TCPConn); ok {
		fmt.Printf("[Tunnel] Received half-close for stream %d, closing local write side\n", halfClose.StreamID)
		return tcpConn.CloseWrite()
	}

	fmt.Printf("[Tunnel] Received half-close for stream %d, tcp half-close unsupported, closing stream\n", halfClose.StreamID)
	m.closeStream(halfClose.StreamID)
	return nil
}
