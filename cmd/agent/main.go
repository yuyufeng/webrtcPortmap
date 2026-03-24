package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pion/webrtc/v4"
	"webrtc-portmap/pkg/auth"
	"webrtc-portmap/pkg/protocol"
	"webrtc-portmap/pkg/tunnel"
	wr "webrtc-portmap/pkg/webrtc"
)

// SignalMessage 信令消息
type SignalMessage struct {
	Type      string                     `json:"type"`
	AgentID   string                     `json:"agent_id,omitempty"`
	SDP       *webrtc.SessionDescription `json:"sdp,omitempty"`
	Candidate *webrtc.ICECandidateInit   `json:"candidate,omitempty"`
	Token     string                     `json:"token,omitempty"`
}

type Agent struct {
	id        string
	password  string
	sigURL    string
	sigToken  string
	token     string
	config    *wr.Config
	httpClient *http.Client
	
	peer        *wr.Peer
	handshaker  *auth.Handshaker
	tunnelMgr   *tunnel.Manager
	
	authenticated bool
	stopChan      chan struct{}
	
	// 端口配置
	ports []protocol.PortInfo
}

func main() {
	var (
		id        = flag.String("id", "", "Agent ID (required)")
		password  = flag.String("password", "", "Password (required)")
		sigURL    = flag.String("signal", "http://localhost:8443", "Signaling server URL")
		sigToken  = flag.String("signal-token", "", "Signaling server auth token")
		stun      = flag.String("stun", "stun:stun.l.google.com:19302", "STUN server")
		turn      = flag.String("turn", "", "TURN server URL (optional)")
		turnUser  = flag.String("turn-user", "", "TURN username")
		turnPass  = flag.String("turn-pass", "", "TURN password")
		portsFile = flag.String("ports", "", "Ports config JSON file (optional)")
	)
	flag.Parse()

	if *id == "" || *password == "" {
		fmt.Println("Usage: agent -id <agent_id> -password <password> [-ports <ports.json>]")
		flag.PrintDefaults()
		os.Exit(1)
	}

	agent := &Agent{
		id:         *id,
		password:   *password,
		sigURL:     *sigURL,
		sigToken:   *sigToken,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		stopChan:   make(chan struct{}),
	}

	// 加载端口配置
	if *portsFile != "" {
		if err := agent.loadPortsConfig(*portsFile); err != nil {
			fmt.Printf("[Agent] Failed to load ports config: %v\n", err)
			os.Exit(1)
		}
	} else {
		// 默认配置：常见服务
		fmt.Printf("[Agent] Using default port configuration (no -ports flag provided)\n")
		agent.ports = []protocol.PortInfo{
			{ID: "ssh", Name: "SSH", LocalAddr: "127.0.0.1:22", Protocol: "tcp", AllowAccess: true},
			{ID: "http", Name: "HTTP", LocalAddr: "127.0.0.1:80", Protocol: "tcp", AllowAccess: true},
			{ID: "https", Name: "HTTPS", LocalAddr: "127.0.0.1:443", Protocol: "tcp", AllowAccess: true},
		}
	}

	// 初始化WebRTC配置
	if *turn != "" {
		agent.config = wr.ConfigWithTURN(*turn, *turnUser, *turnPass)
	} else {
		agent.config = wr.DefaultConfig()
	}
	if *stun != "" {
		agent.config.ICEServers = append([]webrtc.ICEServer{{URLs: []string{*stun}}}, agent.config.ICEServers...)
	}

	// 初始化隧道管理器
	agent.tunnelMgr = tunnel.NewManager(agent)

	fmt.Printf("[Agent] ID: %s\n", *id)
	fmt.Printf("[Agent] Configured ports: %d (allow_access=true only)\n", len(agent.ports))
	for _, p := range agent.ports {
		if p.AllowAccess {
			fmt.Printf("  - %s (%s): %s [%s]\n", p.Name, p.ID, p.LocalAddr, p.Protocol)
		}
	}
	fmt.Printf("[Agent] Signaling server: %s\n", *sigURL)
	fmt.Printf("[Agent] Press Ctrl+C to exit\n")

	// 注册到信令服务器
	if err := agent.register(); err != nil {
		fmt.Printf("[Agent] Registration failed: %v\n", err)
		os.Exit(1)
	}

	// 启动心跳
	go agent.heartbeatLoop()

	// 启动信令轮询
	go agent.signalingLoop()

	// 等待中断
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\n[Agent] Shutting down...")
	close(agent.stopChan)
	if agent.peer != nil {
		agent.peer.Close()
	}
}

// loadPortsConfig 加载端口配置
func (a *Agent) loadPortsConfig(filename string) error {
	fmt.Printf("[Agent] Loading ports config from: %s\n", filename)
	data, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("read file failed: %w", err)
	}
	
	var config struct {
		Ports []protocol.PortInfo `json:"ports"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parse json failed: %w", err)
	}
	
	a.ports = config.Ports
	fmt.Printf("[Agent] Loaded %d ports from config file\n", len(a.ports))
	for _, p := range a.ports {
		fmt.Printf("  - %s (%s): %s [access=%v]\n", p.Name, p.ID, p.LocalAddr, p.AllowAccess)
	}
	return nil
}

// register 注册到信令服务器
func (a *Agent) register() error {
	reqBody := map[string]string{
		"id":         a.id,
		"auth_token": a.sigToken,
		"agent_key":  a.password,
	}
	data, _ := json.Marshal(reqBody)

	resp, err := a.httpClient.Post(
		a.sigURL+"/agent/register",
		"application/json",
		bytes.NewReader(data),
	)
	if err != nil {
		return fmt.Errorf("register request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("register failed: status %d", resp.StatusCode)
	}

	var result struct {
		Token   string `json:"token"`
		AgentID string `json:"agent_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode response failed: %w", err)
	}

	a.token = result.Token
	fmt.Printf("[Agent] Registered successfully, token=%s\n", a.token[:8])
	return nil
}

// heartbeatLoop 心跳循环
func (a *Agent) heartbeatLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			req, _ := http.NewRequest("POST", a.sigURL+"/agent/heartbeat", nil)
			req.Header.Set("Authorization", a.token)
			resp, err := a.httpClient.Do(req)
			if err != nil {
				fmt.Printf("[Agent] Heartbeat failed: %v\n", err)
				continue
			}
			resp.Body.Close()
		case <-a.stopChan:
			return
		}
	}
}

// signalingLoop 信令轮询循环
func (a *Agent) signalingLoop() {
	for {
		select {
		case <-a.stopChan:
			return
		default:
		}

		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		msg, err := a.pollMessage(ctx)
		cancel()

		if err != nil {
			time.Sleep(1 * time.Second)
			continue
		}

		if msg != nil {
			a.handleSignalingMessage(msg)
		}
	}
}

// pollMessage 轮询消息
func (a *Agent) pollMessage(ctx context.Context) (*SignalMessage, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", a.sigURL+"/agent/poll", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", a.token)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("poll failed: status %d", resp.StatusCode)
	}

	var msg SignalMessage
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
		return nil, err
	}

	return &msg, nil
}

// handleSignalingMessage 处理信令消息
func (a *Agent) handleSignalingMessage(msg *SignalMessage) {
	switch msg.Type {
	case "offer":
		if msg.SDP != nil {
			a.handleOffer(msg.SDP)
		}
	case "candidate":
		if msg.Candidate != nil && a.peer != nil {
			if err := a.peer.AddICECandidate(msg.Candidate); err != nil {
				fmt.Printf("[Agent] Failed to add ICE candidate: %v\n", err)
			}
		}
	}
}

// handleOffer 处理Offer
func (a *Agent) handleOffer(offer *webrtc.SessionDescription) {
	fmt.Printf("[Agent] Received offer, creating answer...\n")

	// 创建Peer
	peer, err := wr.NewPeer(a.config)
	if err != nil {
		fmt.Printf("[Agent] Failed to create peer: %v\n", err)
		return
	}
	a.peer = peer

	// 设置ICE候选回调
	peer.SetOnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			a.sendSignalingMessage(&SignalMessage{
				Type:      "candidate",
				Candidate: func() *webrtc.ICECandidateInit { init := c.ToJSON(); return &init }(),
			})
		}
	})

	// 设置消息处理
	peer.SetOnMessage(a.handleMessage)

	// 创建Answer
	answer, err := peer.CreateAnswer(offer)
	if err != nil {
		fmt.Printf("[Agent] Failed to create answer: %v\n", err)
		return
	}

	// 发送Answer
	if err := a.sendSignalingMessage(&SignalMessage{
		Type: "answer",
		SDP:  answer,
	}); err != nil {
		fmt.Printf("[Agent] Failed to send answer: %v\n", err)
		return
	}

	// 等待数据通道打开并处理鉴权
	go a.handleDataChannel()
}

// sendSignalingMessage 发送信令消息
func (a *Agent) sendSignalingMessage(msg *SignalMessage) error {
	data, _ := json.Marshal(msg)
	req, _ := http.NewRequest("POST", a.sigURL+"/agent/send", bytes.NewReader(data))
	req.Header.Set("Authorization", a.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// handleDataChannel 处理数据通道
func (a *Agent) handleDataChannel() {
	// 检查peer是否有效
	if a.peer == nil {
		fmt.Printf("[Agent] Peer is nil, cannot handle data channel\n")
		return
	}
	
	// 在DataChannel可能收到消息前就初始化handshaker
	a.handshaker = auth.NewHandshaker(a.password, a.id, false)
	
	if !a.peer.WaitForDataChannelOpen(30 * time.Second) {
		fmt.Printf("[Agent] Data channel open timeout\n")
		a.cleanup()
		return
	}

	fmt.Printf("[Agent] Data channel opened, waiting for authentication...\n")
}

// handleMessage 处理接收到的消息
func (a *Agent) handleMessage(data []byte) {
	fmt.Printf("[Agent] Received message: %s\n", string(data))
	
	// 未鉴权前，处理鉴权消息
	if !a.authenticated {
		a.handleAuthMessage(data)
		return
	}

	// 鉴权后，处理普通消息
	var msg protocol.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		fmt.Printf("[Agent] Failed to unmarshal message: %v\n", err)
		return
	}

	switch msg.Type {
	case protocol.MsgTypeAccessPort:
		a.handleAccessPort(msg.Payload)
	case 16: // MsgTypeHTTPRequest
		go a.handleHTTPRequest(msg.Payload)
	case protocol.MsgTypePing:
		a.handlePing(&msg)
	default:
		fmt.Printf("[Agent] Unknown message type: %d\n", msg.Type)
	}
}

// handleAuthMessage 处理鉴权消息
func (a *Agent) handleAuthMessage(data []byte) {
	// 安全检查：确保handshaker已初始化
	if a.handshaker == nil {
		fmt.Printf("[Agent] Handshaker not initialized, skipping message\n")
		return
	}
	
	var msg protocol.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		fmt.Printf("[Agent] Auth message unmarshal failed: %v\n", err)
		return
	}

	switch msg.Type {
	case protocol.MsgTypeAuthChallenge:
		resp, err := a.handshaker.HandleChallenge(&msg)
		if err != nil {
			fmt.Printf("[Agent] Handle challenge failed: %v\n", err)
			a.cleanup()
			return
		}
		a.sendMessage(resp)

	case protocol.MsgTypeAuthResult:
		if err := a.handshaker.HandleResult(&msg); err != nil {
			fmt.Printf("[Agent] Authentication failed: %v\n", err)
			a.cleanup()
			return
		}
		a.authenticated = true
		fmt.Printf("[Agent] Authentication successful\n")
		// 鉴权成功后发送Agent配置
		a.sendAgentConfig()
	}
}

// sendAgentConfig 发送Agent配置（端口列表）
func (a *Agent) sendAgentConfig() {
	config := protocol.AgentConfig{
		AgentID: a.id,
		Ports:   a.ports,
		Version: "1.0",
	}
	
	msg, err := protocol.NewMessage(protocol.MsgTypeAgentConfig, config)
	if err != nil {
		fmt.Printf("[Agent] Failed to create config message: %v\n", err)
		return
	}
	
	fmt.Printf("[Agent] Sending config with %d ports (msg type: %d)\n", len(a.ports), msg.Type)
	if err := a.sendMessage(msg); err != nil {
		fmt.Printf("[Agent] Failed to send config: %v\n", err)
	} else {
		fmt.Printf("[Agent] Config sent successfully\n")
	}
}

// handleAccessPort 处理访问端口请求
func (a *Agent) handleAccessPort(payload []byte) {
	fmt.Printf("[Agent] Received access port request: %s\n", string(payload))
	
	var req protocol.AccessPortRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		fmt.Printf("[Agent] Failed to unmarshal access request: %v\n", err)
		return
	}
	
	fmt.Printf("[Agent] Access request: port_id=%s, stream_id=%d\n", req.PortID, req.StreamID)

	// 查找端口配置
	var portInfo *protocol.PortInfo
	for i := range a.ports {
		if a.ports[i].ID == req.PortID && a.ports[i].AllowAccess {
			portInfo = &a.ports[i]
			break
		}
	}

	if portInfo == nil {
		// 端口不存在或不允许访问
		fmt.Printf("[Agent] Port not found: %s\n", req.PortID)
		resp := protocol.AccessPortResponse{
			PortID:   req.PortID,
			StreamID: req.StreamID,
			Success:  false,
			Message:  "Port not found or access denied",
		}
		msg, _ := protocol.NewMessage(protocol.MsgTypeAccessResponse, resp)
		if err := a.sendMessage(msg); err != nil {
			fmt.Printf("[Agent] Failed to send access response: %v\n", err)
		}
		return
	}

	// 创建到本地端口的连接
	fmt.Printf("[Agent] Accessing port %s (%s) -> %s\n", req.PortID, portInfo.Name, portInfo.LocalAddr)
	
	// TODO: 建立到本地端口的TCP连接并通过DataChannel转发
	// 这里简化处理，直接返回成功
	resp := protocol.AccessPortResponse{
		PortID:   req.PortID,
		StreamID: req.StreamID,
		Success:  true,
		Message:  fmt.Sprintf("Connected to %s", portInfo.LocalAddr),
	}
	msg, _ := protocol.NewMessage(protocol.MsgTypeAccessResponse, resp)
	if err := a.sendMessage(msg); err != nil {
		fmt.Printf("[Agent] Failed to send access response: %v\n", err)
	} else {
		fmt.Printf("[Agent] Access response sent successfully\n")
	}
}

// handleHTTPRequest 处理 HTTP 请求
func (a *Agent) handleHTTPRequest(payload []byte) {
	var req protocol.HTTPRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		fmt.Printf("[Agent] Failed to unmarshal HTTP request: %v\n", err)
		a.sendHTTPError("", "Failed to parse request")
		return
	}

	fmt.Printf("[Agent] HTTP %s %s\n", req.Method, req.URL)

	// 创建 HTTP 客户端
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	// 创建请求
	httpReq, err := http.NewRequest(req.Method, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		a.sendHTTPError(req.ID, fmt.Sprintf("Failed to create request: %v", err))
		return
	}

	// 设置请求头
	for key, value := range req.Headers {
		httpReq.Header.Set(key, value)
	}

	// 发送请求
	httpResp, err := client.Do(httpReq)
	if err != nil {
		a.sendHTTPError(req.ID, fmt.Sprintf("Request failed: %v", err))
		return
	}
	defer httpResp.Body.Close()

	// 读取响应体
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		a.sendHTTPError(req.ID, fmt.Sprintf("Failed to read response: %v", err))
		return
	}

	// 构建响应
	resp := protocol.HTTPResponse{
		ID:         req.ID,
		StatusCode: httpResp.StatusCode,
		Headers:    make(map[string]string),
		Body:       body,
	}

	// 复制响应头
	for key, values := range httpResp.Header {
		if len(values) > 0 {
			resp.Headers[key] = values[0]
		}
	}

	msg, _ := protocol.NewMessage(protocol.MsgTypeHTTPResponse, resp)
	if err := a.sendMessage(msg); err != nil {
		fmt.Printf("[Agent] Failed to send HTTP response: %v\n", err)
	} else {
		fmt.Printf("[Agent] HTTP response sent: %d, body size: %d\n", resp.StatusCode, len(body))
	}
}

// sendHTTPError 发送 HTTP 错误响应
func (a *Agent) sendHTTPError(id string, errorMsg string) {
	resp := protocol.HTTPResponse{
		ID:     id,
		Error:  errorMsg,
		Body:   []byte(errorMsg),
	}
	msg, _ := protocol.NewMessage(protocol.MsgTypeHTTPResponse, resp)
	a.sendMessage(msg)
}

// handlePing 处理心跳
func (a *Agent) handlePing(msg *protocol.Message) {
	var ping protocol.Ping
	if err := json.Unmarshal(msg.Payload, &ping); err != nil {
		return
	}

	pong := protocol.Pong{Timestamp: ping.Timestamp}
	resp, _ := protocol.NewMessage(protocol.MsgTypePong, pong)
	a.sendMessage(resp)
}

// sendMessage 实现MessageHandler接口
func (a *Agent) SendMessage(msg *protocol.Message) error {
	return a.sendMessage(msg)
}

func (a *Agent) sendMessage(msg *protocol.Message) error {
	if a.peer == nil {
		return fmt.Errorf("peer not connected")
	}
	return a.peer.SendJSON(msg)
}

// cleanup 清理资源
func (a *Agent) cleanup() {
	if a.peer != nil {
		a.peer.Close()
		a.peer = nil
	}
	a.authenticated = false
}
