// webrtc/peer.go - PeerConnection管理
package webrtc

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"webrtc-portmap/pkg/protocol"
)

// Peer 封装PeerConnection
type Peer struct {
	pc          *webrtc.PeerConnection
	config      *Config
	dataChannel *webrtc.DataChannel
	
	// 回调函数
	onDataChannelOpen  func()
	onDataChannelClose func()
	onMessage          func([]byte)
	onICECandidate     func(*webrtc.ICECandidate)
	onConnectionState  func(webrtc.PeerConnectionState)
	
	// 状态
	mu            sync.RWMutex
	connected     bool
	dataChanOpen  bool
	candidateChan chan *webrtc.ICECandidate
}

// NewPeer 创建新的Peer
func NewPeer(config *Config) (*Peer, error) {
	webrtcConfig := config.NewConfiguration()
	
	api := webrtc.NewAPI()
	pc, err := api.NewPeerConnection(webrtcConfig)
	if err != nil {
		return nil, fmt.Errorf("create peer connection failed: %w", err)
	}

	p := &Peer{
		pc:            pc,
		config:        config,
		candidateChan: make(chan *webrtc.ICECandidate, 32),
	}

	// 设置ICE候选回调
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			p.candidateChan <- c
			if p.onICECandidate != nil {
				p.onICECandidate(c)
			}
		}
	})

	// 设置连接状态变化回调
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		fmt.Printf("[WebRTC] Connection state changed: %s\n", s.String())
		p.mu.Lock()
		p.connected = s == webrtc.PeerConnectionStateConnected
		p.mu.Unlock()
		if p.onConnectionState != nil {
			p.onConnectionState(s)
		}

		switch s {
		case webrtc.PeerConnectionStateConnected:
			// 获取连接使用的本地和远端候选地址信息
			go p.logConnectionType()
		case webrtc.PeerConnectionStateFailed:
			fmt.Printf("[WebRTC] Connection failed! Possible reasons:\n")
			fmt.Printf("  - P2P direct connection blocked by NAT/Firewall\n")
			fmt.Printf("  - No TURN server configured or TURN credentials invalid\n")
			fmt.Printf("  - ICE candidates could not be exchanged properly\n")
			fmt.Printf("[WebRTC] To enable relay through TURN server, use:\n")
			fmt.Printf("  -turn turn:your.turn.server:3478 -turn-user username -turn-pass password\n")
		case webrtc.PeerConnectionStateDisconnected:
			fmt.Printf("[WebRTC] Connection disconnected, may attempt to reconnect...\n")
		}
	})

	// 设置ICE连接状态变化回调（更细粒度的ICE状态）
	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		fmt.Printf("[WebRTC] ICE connection state: %s\n", s.String())
	})

	// 设置ICE候选收集状态回调
	pc.OnICEGatheringStateChange(func(s webrtc.ICEGatheringState) {
		fmt.Printf("[WebRTC] ICE gathering state: %s\n", s.String())
	})

	// 设置数据通道回调（作为answerer时）
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		fmt.Printf("[WebRTC] New data channel: %s\n", dc.Label())
		p.setupDataChannel(dc)
	})

	return p, nil
}

// SetOnConnectionState 设置连接状态变化回调
func (p *Peer) SetOnConnectionState(fn func(webrtc.PeerConnectionState)) {
	p.onConnectionState = fn
}

// Close 关闭PeerConnection
func (p *Peer) Close() error {
	if p.pc != nil {
		return p.pc.Close()
	}
	return nil
}

// CreateOffer 创建Offer（作为发起方）
func (p *Peer) CreateOffer() (*webrtc.SessionDescription, error) {
	// 创建数据通道
	dc, err := p.pc.CreateDataChannel("portmap", &webrtc.DataChannelInit{
		Ordered:    boolPtr(true),
		Negotiated: boolPtr(false),
	})
	if err != nil {
		return nil, fmt.Errorf("create data channel failed: %w", err)
	}
	p.setupDataChannel(dc)

	offer, err := p.pc.CreateOffer(nil)
	if err != nil {
		return nil, fmt.Errorf("create offer failed: %w", err)
	}

	if err := p.pc.SetLocalDescription(offer); err != nil {
		return nil, fmt.Errorf("set local description failed: %w", err)
	}

	// 等待ICE收集（可选：可以使用trickle ICE）
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	if err := p.gatherICECandidates(ctx); err != nil {
		// 继续，不完全阻塞
		fmt.Printf("[WebRTC] ICE gathering warning: %v\n", err)
	}

	return p.pc.LocalDescription(), nil
}

// CreateAnswer 创建Answer（作为响应方）
func (p *Peer) CreateAnswer(offer *webrtc.SessionDescription) (*webrtc.SessionDescription, error) {
	if err := p.pc.SetRemoteDescription(*offer); err != nil {
		return nil, fmt.Errorf("set remote description failed: %w", err)
	}

	answer, err := p.pc.CreateAnswer(nil)
	if err != nil {
		return nil, fmt.Errorf("create answer failed: %w", err)
	}

	if err := p.pc.SetLocalDescription(answer); err != nil {
		return nil, fmt.Errorf("set local description failed: %w", err)
	}

	// 等待ICE收集
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	if err := p.gatherICECandidates(ctx); err != nil {
		fmt.Printf("[WebRTC] ICE gathering warning: %v\n", err)
	}

	return p.pc.LocalDescription(), nil
}

// SetRemoteDescription 设置远端描述
func (p *Peer) SetRemoteDescription(desc *webrtc.SessionDescription) error {
	return p.pc.SetRemoteDescription(*desc)
}

// AddICECandidate 添加ICE候选
func (p *Peer) AddICECandidate(candidate *webrtc.ICECandidateInit) error {
	return p.pc.AddICECandidate(*candidate)
}

// gatherICECandidates 等待ICE候选收集完成
func (p *Peer) gatherICECandidates(ctx context.Context) error {
	// 简单实现：等待一段时间让ICE候选收集
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(3 * time.Second):
		return nil
	}
}

// setupDataChannel 设置数据通道回调
func (p *Peer) setupDataChannel(dc *webrtc.DataChannel) {
	p.dataChannel = dc

	dc.OnOpen(func() {
		fmt.Printf("[DataChannel] Open: %s\n", dc.Label())
		p.mu.Lock()
		p.dataChanOpen = true
		p.mu.Unlock()
		if p.onDataChannelOpen != nil {
			p.onDataChannelOpen()
		}
	})

	dc.OnClose(func() {
		fmt.Printf("[DataChannel] Close: %s\n", dc.Label())
		p.mu.Lock()
		p.dataChanOpen = false
		p.mu.Unlock()
		if p.onDataChannelClose != nil {
			p.onDataChannelClose()
		}
	})

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		if p.onMessage != nil {
			p.onMessage(msg.Data)
		}
	})
}

// SendMessage 发送消息
func (p *Peer) SendMessage(data []byte) error {
	p.mu.RLock()
	open := p.dataChanOpen
	p.mu.RUnlock()

	if !open {
		return fmt.Errorf("data channel not open")
	}

	return p.dataChannel.Send(data)
}

// SendJSON 发送JSON消息
func (p *Peer) SendJSON(v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return p.SendMessage(data)
}

// SendProtocolMessage 发送协议消息
func (p *Peer) SendProtocolMessage(msg *protocol.Message) error {
	return p.SendJSON(msg)
}

// IsDataChannelOpen 检查数据通道是否打开
func (p *Peer) IsDataChannelOpen() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.dataChanOpen
}

// WaitForDataChannelOpen 等待数据通道打开
func (p *Peer) WaitForDataChannelOpen(timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			if p.IsDataChannelOpen() {
				return true
			}
		}
	}
}

// SetOnDataChannelOpen 设置数据通道打开回调
func (p *Peer) SetOnDataChannelOpen(fn func()) {
	p.onDataChannelOpen = fn
}

// SetOnDataChannelClose 设置数据通道关闭回调
func (p *Peer) SetOnDataChannelClose(fn func()) {
	p.onDataChannelClose = fn
}

// SetOnMessage 设置消息回调
func (p *Peer) SetOnMessage(fn func([]byte)) {
	p.onMessage = fn
}

// SetOnICECandidate 设置ICE候选回调
func (p *Peer) SetOnICECandidate(fn func(*webrtc.ICECandidate)) {
	p.onICECandidate = fn
}

// GetICECandidates 获取ICE候选列表（用于信令交换）
func (p *Peer) GetICECandidates() []*webrtc.ICECandidate {
	var candidates []*webrtc.ICECandidate
	
	// 非阻塞获取所有当前候选
	for {
		select {
		case c := <-p.candidateChan:
			candidates = append(candidates, c)
		default:
			return candidates
		}
	}
}

// logConnectionType 检测并记录连接类型（直连/中继）
func (p *Peer) logConnectionType() {
	if p.pc == nil {
		return
	}

	// 获取统计信息来检查连接类型
	stats := p.pc.GetStats()
	for _, stat := range stats {
		if candidatePair, ok := stat.(webrtc.ICECandidatePairStats); ok && candidatePair.Nominated {
			localCandidate := stats[candidatePair.LocalCandidateID]
			remoteCandidate := stats[candidatePair.RemoteCandidateID]

			if local, ok := localCandidate.(webrtc.ICECandidateStats); ok {
				if remote, ok := remoteCandidate.(webrtc.ICECandidateStats); ok {
					connType := "P2P Direct"
					if local.CandidateType == webrtc.ICECandidateTypeRelay || remote.CandidateType == webrtc.ICECandidateTypeRelay {
						connType = "TURN Relay"
					} else if local.CandidateType == webrtc.ICECandidateTypePrflx || remote.CandidateType == webrtc.ICECandidateTypePrflx {
						connType = "P2P Peer Reflexive"
					} else if local.CandidateType == webrtc.ICECandidateTypeSrflx || remote.CandidateType == webrtc.ICECandidateTypeSrflx {
						connType = "P2P Server Reflexive (STUN)"
					}

					fmt.Printf("[WebRTC] Connection established via: %s\n", connType)
					fmt.Printf("[WebRTC] Local: %s:%d (%s)\n", local.IP, local.Port, local.CandidateType.String())
					fmt.Printf("[WebRTC] Remote: %s:%d (%s)\n", remote.IP, remote.Port, remote.CandidateType.String())
					return
				}
			}
		}
	}

	// 如果无法获取详细统计，显示基本连接信息
	fmt.Printf("[WebRTC] Connection established (type unknown, stats unavailable)\n")
}

// Helper functions
func boolPtr(b bool) *bool {
	return &b
}
