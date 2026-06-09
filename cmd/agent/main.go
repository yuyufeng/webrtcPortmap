package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
	"webrtc-portmap/pkg/auth"
	"webrtc-portmap/pkg/protocol"
	"webrtc-portmap/pkg/terminal"
	"webrtc-portmap/pkg/tunnel"
	wr "webrtc-portmap/pkg/webrtc"
)

const maxHTTPResponseChunkSize = 96 * 1024

// SignalMessage 信令消息
type SignalMessage struct {
	Type      string                     `json:"type"`
	AgentID   string                     `json:"agent_id,omitempty"`
	SDP       *webrtc.SessionDescription `json:"sdp,omitempty"`
	Candidate *webrtc.ICECandidateInit   `json:"candidate,omitempty"`
	Token     string                     `json:"token,omitempty"`
}

type wsProxyConn struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
}

type Agent struct {
	id        string
	displayName string
	ownerHash string
	password  string
	sigURL    string
	sigToken  string
	token     string
	config    *wr.Config
	useServerTurn bool
	httpClient *http.Client
	
	peer        *wr.Peer
	handshaker  *auth.Handshaker
	tunnelMgr   *tunnel.Manager
	clientTunnel *tunnel.ClientManager
	
	authenticated bool
	stopChan      chan struct{}
	
	// 端口配置
	ports []protocol.PortInfo

	wsMu    sync.RWMutex
	wsConns map[string]*wsProxyConn

	// 内嵌终端（持久 PTY 会话，生命周期独立于 WebRTC 连接）
	termEnabled  bool
	termShell    string
	termArgs     []string
	termBufBytes int
	termCwd      string
	termMu       sync.Mutex
	term         *terminal.Session

	reconnectMu      sync.Mutex
	reconnectRunning bool
}

func convertICEServers(servers []webrtc.ICEServer) []protocol.ICEServerInfo {
	result := make([]protocol.ICEServerInfo, 0, len(servers))
	for _, server := range servers {
		if len(server.URLs) == 0 {
			continue
		}
		credential := ""
		switch v := server.Credential.(type) {
		case string:
			credential = v
		case fmt.Stringer:
			credential = v.String()
		case nil:
			credential = ""
		default:
			credential = fmt.Sprint(v)
		}
		urls := make([]string, 0, len(server.URLs))
		for _, rawURL := range server.URLs {
			urls = append(urls, base64.StdEncoding.EncodeToString([]byte(rawURL)))
		}
		result = append(result, protocol.ICEServerInfo{
			URLs:       urls,
			Username:   base64.StdEncoding.EncodeToString([]byte(server.Username)),
			Credential: base64.StdEncoding.EncodeToString([]byte(credential)),
		})
	}
	return result
}

func main() {
	var (
		id          = flag.String("id", "", "Agent ID (required)")
		name        = flag.String("name", "", "Agent display name (optional, defaults to id)")
		ownerHash   = flag.String("owner-hash", "", "User hash from Web UI (required)")
		password    = flag.String("password", "", "Local auth password (required)")
		sigURL      = flag.String("signal", "http://localhost:8443", "Signaling server URL")
		sigToken    = flag.String("signal-token", "", "Signaling server auth token")
		stun        = flag.String("stun", "stun:stun.l.google.com:19302", "STUN server")
		turn        = flag.String("turn", "", "TURN server URL (optional)")
		turnUser    = flag.String("turn-user", "", "TURN username")
		turnPass    = flag.String("turn-pass", "", "TURN password")
		iceConfig   = flag.String("ice-config", "", "ICE servers config JSON file (optional, overrides -turn)")
		portsFile   = flag.String("ports", "", "Ports config JSON file (optional)")
		termEnabled = flag.Bool("terminal", false, "Enable embedded persistent terminal (ttyd-like)")
		termShell   = flag.String("terminal-shell", "", "Terminal shell: cmd/powershell/bash/sh or full path (default: platform shell)")
		termArgs    = flag.String("terminal-args", "", "Extra args passed to the shell (space-separated). Default for powershell/pwsh: -NoLogo -ExecutionPolicy Bypass")
		termBufKB   = flag.Int("terminal-buffer", 256, "Terminal replay buffer size in KB")
		termCwd     = flag.String("terminal-cwd", "", "Terminal working directory (default: current directory)")
		useServerTurn = flag.Bool("use-server-turn", true, "Fetch embedded TURN relay credentials from the signaling server")
	)
	flag.Parse()

	if *id == "" || *ownerHash == "" || *password == "" {
		fmt.Println("Usage: agent -id <agent_id> -owner-hash <user_hash> -password <password> [-name <display_name>] [-ports <ports.json>]")
		flag.PrintDefaults()
		os.Exit(1)
	}
	displayName := *name
	if strings.TrimSpace(displayName) == "" {
		displayName = *id
	}

	// 终端 shell 启动参数：用户显式提供则用其值（按空白切分）；
	// 否则按 shell 取合理默认（powershell/pwsh 默认 -NoLogo -ExecutionPolicy Bypass）。
	termShellArgs := strings.Fields(*termArgs)
	if len(termShellArgs) == 0 {
		termShellArgs = terminal.DefaultShellArgs(terminal.ResolveShell(*termShell))
	}

	agent := &Agent{
		id:          *id,
		displayName: displayName,
		ownerHash:   *ownerHash,
		password:    *password,
		sigURL:      *sigURL,
		sigToken:    *sigToken,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
		stopChan:    make(chan struct{}),
		wsConns:     make(map[string]*wsProxyConn),
		useServerTurn: *useServerTurn,
		termEnabled: *termEnabled,
		termShell:   *termShell,
		termArgs:    termShellArgs,
		termBufBytes: *termBufKB * 1024,
		termCwd:     *termCwd,
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
	var err error
	agent.config, err = wr.NewConfig(*iceConfig, *turn, *turnUser, *turnPass)
	if err != nil {
		fmt.Printf("[Agent] Failed to load ICE config: %v\n", err)
		os.Exit(1)
	}
	// 如果命令行指定了额外的 STUN，添加到列表开头
	if *stun != "" && *iceConfig == "" {
		agent.config.ICEServers = append([]webrtc.ICEServer{{URLs: []string{*stun}}}, agent.config.ICEServers...)
	}
	agent.config.PrintICEServers()

	// 初始化隧道管理器
	agent.tunnelMgr = tunnel.NewManager(agent)
	agent.clientTunnel = tunnel.NewClientManager(agent)

	fmt.Printf("[Agent] ID: %s\n", *id)
	fmt.Printf("[Agent] Display Name: %s\n", displayName)
	fmt.Printf("[Agent] Owner Hash: %s\n", *ownerHash)
	fmt.Printf("[Agent] Configured ports: %d (allow_access=true only)\n", len(agent.ports))
	for _, p := range agent.ports {
		if p.AllowAccess {
			fmt.Printf("  - %s (%s): %s [%s]\n", p.Name, p.ID, p.LocalAddr, p.Protocol)
		}
	}
	if agent.termEnabled {
		argsStr := "(none)"
		if len(agent.termArgs) > 0 {
			argsStr = strings.Join(agent.termArgs, " ")
		}
		fmt.Printf("[Agent] Embedded terminal: ENABLED (shell=%s, args=%s, buffer=%dKB)\n", terminal.ResolveShell(agent.termShell), argsStr, *termBufKB)
	} else {
		fmt.Printf("[Agent] Embedded terminal: disabled (use -terminal to enable)\n")
	}
	fmt.Printf("[Agent] Signaling server: %s\n", *sigURL)
	fmt.Printf("[Agent] Press Ctrl+C to exit\n")

	// 注册到信令服务器（带退避重试）
	if err := agent.registerWithBackoff(); err != nil {
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
	agent.closeTerminal()
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
	reqBody := map[string]interface{}{
		"id":           a.id,
		"auth_token":   a.sigToken,
		"owner_hash":   a.ownerHash,
		"display_name": a.displayName,
		"ice_servers":  convertICEServers(a.config.ICEServers),
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
		body, _ := io.ReadAll(resp.Body)
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = fmt.Sprintf("status %d", resp.StatusCode)
		}
		return fmt.Errorf("register failed: %s", msg)
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

func (a *Agent) registerWithBackoff() error {
	delay := 1 * time.Second
	for {
		select {
		case <-a.stopChan:
			return fmt.Errorf("agent is stopping")
		default:
		}

		if err := a.register(); err == nil {
			return nil
		} else {
			fmt.Printf("[Agent] Registration failed: %v\n", err)
		}

		if delay > 5*time.Minute {
			delay = 5 * time.Minute
		}
		fmt.Printf("[Agent] Retry registration in %s\n", delay)
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-a.stopChan:
			timer.Stop()
			return fmt.Errorf("agent is stopping")
		}
		if delay < 5*time.Minute {
			delay *= 2
			if delay > 5*time.Minute {
				delay = 5 * time.Minute
			}
		}
	}
}

func (a *Agent) triggerReconnect(reason string) {
	a.reconnectMu.Lock()
	if a.reconnectRunning {
		a.reconnectMu.Unlock()
		return
	}
	a.reconnectRunning = true
	a.reconnectMu.Unlock()

	go func() {
		defer func() {
			a.reconnectMu.Lock()
			a.reconnectRunning = false
			a.reconnectMu.Unlock()
		}()
		fmt.Printf("[Agent] Reconnect triggered: %s\n", reason)
		if err := a.registerWithBackoff(); err != nil {
			fmt.Printf("[Agent] Reconnect stopped: %v\n", err)
		}
	}()
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
				a.triggerReconnect(fmt.Sprintf("heartbeat request failed: %v", err))
				continue
			}
			if resp.StatusCode == http.StatusUnauthorized {
				resp.Body.Close()
				fmt.Printf("[Agent] Heartbeat unauthorized, token may be expired\n")
				a.triggerReconnect("heartbeat unauthorized")
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
			if strings.Contains(err.Error(), "status 401") || strings.Contains(strings.ToLower(err.Error()), "unauthorized") {
				a.triggerReconnect(fmt.Sprintf("poll unauthorized: %v", err))
			}
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
	case "disconnect":
		fmt.Printf("[Agent] Received disconnect signal, closing current session\n")
		a.cleanup()
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

// peerConfig 返回本次连接使用的 WebRTC 配置：在 agent 自身 ICE 配置基础上，
// 按需附加信令服务下发的内嵌 TURN 临时凭据，让 agent 也能收集 relay 候选
//（流量归属到 owner 用户）。用户显式用 -turn/-ice-config 配的项仍保留。
func (a *Agent) peerConfig() *wr.Config {
	if !a.useServerTurn || a.token == "" {
		return a.config
	}
	servers := a.fetchServerTURN()
	if len(servers) == 0 {
		return a.config
	}
	merged := &wr.Config{ICEServers: make([]webrtc.ICEServer, 0, len(a.config.ICEServers)+len(servers))}
	merged.ICEServers = append(merged.ICEServers, servers...) // server TURN 在前
	merged.ICEServers = append(merged.ICEServers, a.config.ICEServers...)
	return merged
}

// fetchServerTURN 向信令服务拉取内嵌 TURN 临时凭据，转成 webrtc.ICEServer 列表。
func (a *Agent) fetchServerTURN() []webrtc.ICEServer {
	req, err := http.NewRequest(http.MethodGet, a.sigURL+"/agent/turn-credentials", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", a.token)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		fmt.Printf("[Agent] Fetch server TURN failed: %v\n", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var body struct {
		TurnEnabled bool     `json:"turn_enabled"`
		URLs        []string `json:"urls"`
		Username    string   `json:"username"`
		Credential  string   `json:"credential"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil
	}
	if !body.TurnEnabled || len(body.URLs) == 0 {
		return nil
	}
	fmt.Printf("[Agent] Using server TURN relay (%d url) for this connection\n", len(body.URLs))
	return []webrtc.ICEServer{{
		URLs:       body.URLs,
		Username:   body.Username,
		Credential: body.Credential,
	}}
}

// handleOffer 处理Offer
func (a *Agent) handleOffer(offer *webrtc.SessionDescription) {
	fmt.Printf("[Agent] Received offer, creating answer...\n")

	// 新连接强制抢占旧会话（last-connection-wins）：否则某用户忘记断开后，
	// 换设备/换地点时会被僵死的旧连接永久占用、永远无法接入。
	// cleanup() 对终端只 Detach 不杀进程，新连接随后 AttachWithReplay 即可
	// 无缝接管（断线不重置反馈）；连接到达本函数前已过信令层账户/归属鉴权。
	if a.peer != nil || a.authenticated {
		fmt.Printf("[Agent] Preempting existing session for the new offer (last connection wins)\n")
		a.cleanup()
	}

	// 创建Peer（按需合并信令服务下发的临时 TURN 中转凭据）
	peer, err := wr.NewPeer(a.peerConfig())
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

	peer.SetOnConnectionState(func(s webrtc.PeerConnectionState) {
		switch s {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			// 仅在 ICE 彻底失败或连接关闭时清理。
			// Disconnected 是可恢复的瞬态（relay 切换/短暂丢包都会经过它），
			// 此时若立刻 cleanup 会拆掉本可自愈或回退到 TURN 中转的连接。
			fmt.Printf("[Agent] Peer state=%s, cleaning up session\n", s.String())
			a.cleanup()
		case webrtc.PeerConnectionStateDisconnected:
			fmt.Printf("[Agent] Peer state=disconnected (transient), waiting for recovery or failure...\n")
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
		a.triggerReconnect(fmt.Sprintf("send signaling failed: %v", err))
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		a.triggerReconnect("send signaling unauthorized")
		return fmt.Errorf("send signaling unauthorized")
	}
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
	if a.authenticated {
		fmt.Printf("[Agent] Data channel already authenticated, re-sending agent config\n")
		a.sendAgentConfig()
	}
}

// handleMessage 处理接收到的消息
func (a *Agent) handleMessage(data []byte) {
	fmt.Printf("[Agent] Received message: %s\n", string(data))

	var msg protocol.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		fmt.Printf("[Agent] Failed to unmarshal message: %v\n", err)
		return
	}

	// 鉴权消息始终优先处理，避免旧 authenticated 状态把新的 challenge 当成普通消息吞掉。
	switch msg.Type {
	case protocol.MsgTypeAuthChallenge, protocol.MsgTypeAuthResult:
		a.handleAuthMessage(data)
		return
	}
	
	// 未鉴权前，处理鉴权消息
	if !a.authenticated {
		a.handleAuthMessage(data)
		return
	}

	// 鉴权后，处理普通消息
	switch msg.Type {
	case protocol.MsgTypeConnectReq:
		if err := a.clientTunnel.HandleConnectRequest(msg.Payload); err != nil {
			fmt.Printf("[Agent] Handle connect request failed: %v\n", err)
		}
	case protocol.MsgTypeConnectResp:
		if err := a.tunnelMgr.HandleConnectResponse(msg.Payload); err != nil {
			fmt.Printf("[Agent] Handle connect response failed: %v\n", err)
		}
	case protocol.MsgTypeHalfCloseStream:
		if err := a.clientTunnel.HandleHalfCloseStream(msg.Payload); err != nil {
			fmt.Printf("[Agent] Handle half-close stream failed: %v\n", err)
		}
	case protocol.MsgTypeData:
		if err := a.clientTunnel.HandleDataMessage(msg.Payload); err != nil {
			fmt.Printf("[Agent] Handle tunnel data failed: %v\n", err)
		}
	case protocol.MsgTypeCloseStream:
		if err := a.clientTunnel.HandleCloseStream(msg.Payload); err != nil {
			fmt.Printf("[Agent] Handle close stream failed: %v\n", err)
		}
	case protocol.MsgTypeAccessPort:
		a.handleAccessPort(msg.Payload)
	case protocol.MsgTypeHTTPRequest:
		go a.handleHTTPRequest(msg.Payload)
	case protocol.MsgTypeWSOpen:
		go a.handleWSOpen(msg.Payload)
	case protocol.MsgTypeWSData:
		go a.handleWSData(msg.Payload)
	case protocol.MsgTypeWSClose:
		go a.handleWSClose(msg.Payload)
	case protocol.MsgTypeTermOpen:
		go a.handleTermOpen(msg.Payload)
	case protocol.MsgTypeTermInput:
		a.handleTermInput(msg.Payload)
	case protocol.MsgTypeTermResize:
		a.handleTermResize(msg.Payload)
	case protocol.MsgTypeTermClose:
		a.handleTermClose(msg.Payload)
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
		a.sendAgentConfig()
		go func() {
			time.Sleep(300 * time.Millisecond)
			if a.authenticated && a.peer != nil && a.peer.IsDataChannelOpen() {
				fmt.Printf("[Agent] Re-sending agent config after auth stabilization\n")
				a.sendAgentConfig()
			}
		}()
	}
}

// sendAgentConfig 发送Agent配置（端口列表）
func (a *Agent) sendAgentConfig() {
	config := protocol.AgentConfig{
		AgentID:    a.id,
		Ports:      a.ports,
		ICEServers: convertICEServers(a.config.ICEServers),
		Version:    "1.0",
	}
	if a.termEnabled {
		config.Terminal = &protocol.TerminalInfo{
			Enabled: true,
			Shell:   terminal.ResolveShell(a.termShell),
		}
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

	targetURL, err := a.resolveHTTPRequestTarget(&req)
	if err != nil {
		a.sendHTTPError(req.ID, err.Error())
		return
	}

	fmt.Printf("[Agent] HTTP %s %s (port=%s)\n", req.Method, targetURL, req.PortID)

	// 创建 HTTP 客户端
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	// 创建请求
	httpReq, err := http.NewRequest(req.Method, targetURL, bytes.NewReader(req.Body))
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
	}

	// 复制响应头
	for key, values := range httpResp.Header {
		if len(values) > 0 {
			resp.Headers[key] = strings.Join(values, "\n")
		}
	}

	if err := a.sendHTTPResponseChunks(resp, body); err != nil {
		fmt.Printf("[Agent] Failed to send HTTP response: %v\n", err)
	} else {
		fmt.Printf("[Agent] HTTP response sent: %d, body size: %d\n", resp.StatusCode, len(body))
	}
}

func (a *Agent) resolveHTTPRequestTarget(req *protocol.HTTPRequest) (string, error) {
	if req == nil {
		return "", fmt.Errorf("empty request")
	}

	if req.PortID != "" {
		portInfo := a.findAccessiblePort(req.PortID)
		if portInfo == nil {
			return "", fmt.Errorf("port %s not found or access denied", req.PortID)
		}
		if portInfo.Protocol != "tcp" {
			return "", fmt.Errorf("port %s protocol %s is not supported for HTTP proxy", req.PortID, portInfo.Protocol)
		}

		path := strings.TrimSpace(req.Path)
		if path == "" {
			path = "/"
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		pathURL, err := url.ParseRequestURI(path)
		if err != nil {
			return "", fmt.Errorf("invalid path: %w", err)
		}

		scheme := inferHTTPScheme(portInfo)
		return (&url.URL{
			Scheme: scheme,
			Host:   portInfo.LocalAddr,
			Path:   pathURL.Path,
			RawQuery: pathURL.RawQuery,
			Fragment: pathURL.Fragment,
		}).String(), nil
	}

	if strings.TrimSpace(req.URL) == "" {
		return "", fmt.Errorf("missing port_id or url")
	}

	targetURL, err := url.Parse(req.URL)
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}
	if targetURL.Host == "" {
		return "", fmt.Errorf("invalid url host")
	}

	portInfo := a.findPortByLocalAddr(targetURL.Host)
	if portInfo == nil || !portInfo.AllowAccess {
		return "", fmt.Errorf("target host %s is not in allowed ports", targetURL.Host)
	}

	return targetURL.String(), nil
}

func (a *Agent) findAccessiblePort(portID string) *protocol.PortInfo {
	for i := range a.ports {
		if a.ports[i].ID == portID && a.ports[i].AllowAccess {
			return &a.ports[i]
		}
	}
	return nil
}

func (a *Agent) findPortByLocalAddr(localAddr string) *protocol.PortInfo {
	for i := range a.ports {
		if a.ports[i].LocalAddr == localAddr {
			return &a.ports[i]
		}
	}
	return nil
}

func inferHTTPScheme(portInfo *protocol.PortInfo) string {
	if portInfo == nil {
		return "http"
	}
	if portInfo.ID == "https" || strings.HasSuffix(portInfo.LocalAddr, ":443") {
		return "https"
	}
	if strings.Contains(strings.ToLower(portInfo.Name), "https") {
		return "https"
	}
	return "http"
}

func inferWSScheme(portInfo *protocol.PortInfo) string {
	if portInfo == nil {
		return "ws"
	}
	if portInfo.ID == "https" || strings.HasSuffix(portInfo.LocalAddr, ":443") {
		return "wss"
	}
	if strings.Contains(strings.ToLower(portInfo.Name), "https") {
		return "wss"
	}
	return "ws"
}

func (a *Agent) resolveWSTarget(req *protocol.WSOpenRequest) (string, error) {
	if req == nil {
		return "", fmt.Errorf("empty websocket request")
	}
	if req.PortID != "" {
		portInfo := a.findAccessiblePort(req.PortID)
		if portInfo == nil {
			return "", fmt.Errorf("port %s not found or access denied", req.PortID)
		}
		if portInfo.Protocol != "tcp" {
			return "", fmt.Errorf("port %s protocol %s is not supported for websocket proxy", req.PortID, portInfo.Protocol)
		}
		path := strings.TrimSpace(req.Path)
		if path == "" {
			path = "/"
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		pathURL, err := url.ParseRequestURI(path)
		if err != nil {
			return "", fmt.Errorf("invalid websocket path: %w", err)
		}
		return (&url.URL{
			Scheme:   inferWSScheme(portInfo),
			Host:     portInfo.LocalAddr,
			Path:     pathURL.Path,
			RawQuery: pathURL.RawQuery,
			Fragment: pathURL.Fragment,
		}).String(), nil
	}

	if strings.TrimSpace(req.URL) == "" {
		return "", fmt.Errorf("missing port_id or url")
	}
	targetURL, err := url.Parse(req.URL)
	if err != nil {
		return "", fmt.Errorf("invalid websocket url: %w", err)
	}
	if targetURL.Host == "" {
		return "", fmt.Errorf("invalid websocket url host")
	}
	portInfo := a.findPortByLocalAddr(targetURL.Host)
	if portInfo == nil || !portInfo.AllowAccess {
		return "", fmt.Errorf("target host %s is not in allowed ports", targetURL.Host)
	}
	return targetURL.String(), nil
}

func (a *Agent) handleWSOpen(payload []byte) {
	var req protocol.WSOpenRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		a.sendWSError("", fmt.Sprintf("failed to parse ws open request: %v", err))
		return
	}

	fmt.Printf("[Agent][DC-WS] Open request socket=%s port=%s path=%s url=%s\n", req.SocketID, req.PortID, req.Path, req.URL)
	targetURL, err := a.resolveWSTarget(&req)
	if err != nil {
		fmt.Printf("[Agent][DC-WS] Resolve target failed socket=%s: %v\n", req.SocketID, err)
		a.sendWSOpenAck(req.SocketID, false, err.Error())
		return
	}
	fmt.Printf("[Agent][DC-WS] Dial target socket=%s -> %s\n", req.SocketID, targetURL)

	header := http.Header{}
	for key, value := range req.Headers {
		header.Set(key, value)
	}

	conn, _, err := websocket.DefaultDialer.Dial(targetURL, header)
	if err != nil {
		fmt.Printf("[Agent][DC-WS] Dial failed socket=%s: %v\n", req.SocketID, err)
		a.sendWSOpenAck(req.SocketID, false, fmt.Sprintf("websocket dial failed: %v", err))
		return
	}

	a.wsMu.Lock()
	a.wsConns[req.SocketID] = &wsProxyConn{conn: conn}
	a.wsMu.Unlock()

	fmt.Printf("[Agent][DC-WS] Open success socket=%s\n", req.SocketID)
	a.sendWSOpenAck(req.SocketID, true, "")
	go a.readWSLoop(req.SocketID, conn)
}

func (a *Agent) handleWSData(payload []byte) {
	var data protocol.WSData
	if err := json.Unmarshal(payload, &data); err != nil {
		a.sendWSError("", fmt.Sprintf("failed to parse ws data: %v", err))
		return
	}

	conn := a.getWSConn(data.SocketID)
	if conn == nil {
		fmt.Printf("[Agent][DC-WS] Write ignored, socket not found: %s\n", data.SocketID)
		a.sendWSError(data.SocketID, "websocket not found")
		return
	}

	messageType := websocket.BinaryMessage
	if data.Text {
		messageType = websocket.TextMessage
	}
	fmt.Printf("[Agent][DC-WS] Write socket=%s text=%v size=%d\n", data.SocketID, data.Text, len(data.Data))
	conn.writeMu.Lock()
	err := conn.conn.WriteMessage(messageType, data.Data)
	conn.writeMu.Unlock()
	if err != nil {
		fmt.Printf("[Agent][DC-WS] Write failed socket=%s: %v\n", data.SocketID, err)
		a.sendWSError(data.SocketID, fmt.Sprintf("websocket write failed: %v", err))
		a.closeWSConn(data.SocketID, websocket.CloseInternalServerErr, "write failed")
	}
}

func (a *Agent) handleWSClose(payload []byte) {
	var closeMsg protocol.WSClose
	if err := json.Unmarshal(payload, &closeMsg); err != nil {
		return
	}
	a.closeWSConn(closeMsg.SocketID, closeMsg.Code, closeMsg.Reason)
}

func (a *Agent) readWSLoop(socketID string, conn *websocket.Conn) {
	defer a.closeWSConn(socketID, websocket.CloseNormalClosure, "closed")
	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			fmt.Printf("[Agent][DC-WS] Read failed socket=%s: %v\n", socketID, err)
			if closeErr, ok := err.(*websocket.CloseError); ok {
				a.sendWSClose(socketID, closeErr.Code, closeErr.Text)
			} else {
				a.sendWSError(socketID, fmt.Sprintf("websocket read failed: %v", err))
			}
			return
		}

		fmt.Printf("[Agent][DC-WS] Read socket=%s text=%v size=%d\n", socketID, messageType == websocket.TextMessage, len(data))
		msgType := protocol.MsgTypeWSData
		payload := protocol.WSData{
			SocketID: socketID,
			Data:     data,
			Text:     messageType == websocket.TextMessage,
		}
		msg, _ := protocol.NewMessage(msgType, payload)
		if err := a.sendMessage(msg); err != nil {
			fmt.Printf("[Agent] Failed to send websocket data: %v\n", err)
			return
		}
	}
}

func (a *Agent) sendWSOpenAck(socketID string, success bool, errText string) {
	msg, _ := protocol.NewMessage(protocol.MsgTypeWSOpenAck, protocol.WSOpenAck{
		SocketID: socketID,
		Success:  success,
		Error:    errText,
	})
	_ = a.sendMessage(msg)
}

func (a *Agent) sendWSClose(socketID string, code int, reason string) {
	msg, _ := protocol.NewMessage(protocol.MsgTypeWSClose, protocol.WSClose{
		SocketID: socketID,
		Code:     code,
		Reason:   reason,
	})
	_ = a.sendMessage(msg)
}

func (a *Agent) sendWSError(socketID string, errText string) {
	msg, _ := protocol.NewMessage(protocol.MsgTypeWSError, protocol.WSError{
		SocketID: socketID,
		Error:    errText,
	})
	_ = a.sendMessage(msg)
}

func (a *Agent) getWSConn(socketID string) *wsProxyConn {
	a.wsMu.RLock()
	defer a.wsMu.RUnlock()
	return a.wsConns[socketID]
}

func (a *Agent) closeWSConn(socketID string, code int, reason string) {
	a.wsMu.Lock()
	wsConn := a.wsConns[socketID]
	if wsConn != nil {
		delete(a.wsConns, socketID)
	}
	a.wsMu.Unlock()

	if wsConn != nil {
		fmt.Printf("[Agent][DC-WS] Close socket=%s code=%d reason=%s\n", socketID, code, reason)
		wsConn.writeMu.Lock()
		if code != 0 {
			_ = wsConn.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(2*time.Second))
		}
		_ = wsConn.conn.Close()
		wsConn.writeMu.Unlock()
	}
}

func (a *Agent) sendHTTPResponseChunks(resp protocol.HTTPResponse, body []byte) error {
	if len(body) == 0 {
		resp.Done = true
		msg, _ := protocol.NewMessage(protocol.MsgTypeHTTPResponse, resp)
		return a.sendMessage(msg)
	}

	totalChunks := (len(body) + maxHTTPResponseChunkSize - 1) / maxHTTPResponseChunkSize
	for i := 0; i < totalChunks; i++ {
		start := i * maxHTTPResponseChunkSize
		end := start + maxHTTPResponseChunkSize
		if end > len(body) {
			end = len(body)
		}

		chunkResp := protocol.HTTPResponse{
			ID:          resp.ID,
			StatusCode:  resp.StatusCode,
			Headers:     resp.Headers,
			Body:        body[start:end],
			ChunkIndex:  i,
			TotalChunks: totalChunks,
			Done:        i == totalChunks-1,
		}
		if i > 0 {
			chunkResp.Headers = nil
		}

		msg, _ := protocol.NewMessage(protocol.MsgTypeHTTPResponse, chunkResp)
		if err := a.sendMessage(msg); err != nil {
			return err
		}
	}
	return nil
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
	// 终端会话只解除输出回调（Detach），不杀进程——断线后进程继续运行，
	// 输出继续写入环形缓冲，重连时即可回放，做到“断线不重置反馈”。
	a.detachTerminal()
	if a.clientTunnel != nil {
		a.clientTunnel.CloseAll()
	}
	a.wsMu.Lock()
	for socketID, wsConn := range a.wsConns {
		wsConn.writeMu.Lock()
		_ = wsConn.conn.Close()
		wsConn.writeMu.Unlock()
		delete(a.wsConns, socketID)
	}
	a.wsMu.Unlock()
	if a.peer != nil {
		a.peer.Close()
		a.peer = nil
	}
	a.authenticated = false
}

// ==================== 内嵌终端 ====================

// ensureTerminal 惰性创建（或在旧会话已退出时重建）持久终端会话。
// 一个 agent 始终只持有一个独享的终端会话。
func (a *Agent) ensureTerminal(cols, rows int) (*terminal.Session, error) {
	a.termMu.Lock()
	defer a.termMu.Unlock()

	if a.term != nil && a.term.Alive() {
		return a.term, nil
	}
	// 旧会话已退出则先回收，再开新会话。
	if a.term != nil {
		_ = a.term.Close()
		a.term = nil
	}

	sess, err := terminal.New(terminal.Config{
		Shell:      a.termShell,
		Args:       a.termArgs,
		BufferSize: a.termBufBytes,
		Cols:       cols,
		Rows:       rows,
		Dir:        a.termCwd,
	})
	if err != nil {
		return nil, err
	}
	a.term = sess
	fmt.Printf("[Agent][Term] Started session shell=%s\n", sess.Shell())
	return sess, nil
}

func (a *Agent) currentTerminal() *terminal.Session {
	a.termMu.Lock()
	defer a.termMu.Unlock()
	return a.term
}

// handleTermOpen 控制端打开/重新附着终端：挂接实时输出并先回放历史缓冲。
func (a *Agent) handleTermOpen(payload []byte) {
	if !a.termEnabled {
		a.sendTermExit(-1, "terminal is not enabled on this agent")
		return
	}

	var req protocol.TermOpenRequest
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &req); err != nil {
			fmt.Printf("[Agent][Term] Bad open request: %v\n", err)
		}
	}

	sess, err := a.ensureTerminal(req.Cols, req.Rows)
	if err != nil {
		fmt.Printf("[Agent][Term] Failed to start terminal: %v\n", err)
		a.sendTermExit(-1, fmt.Sprintf("failed to start terminal: %v", err))
		return
	}

	if req.Cols > 0 && req.Rows > 0 {
		_ = sess.Resize(req.Cols, req.Rows)
	}

	// 实时输出回调：始终发送到当前 peer（断线后 peer 失效则发送报错被忽略）。
	sink := func(b []byte) {
		msg, err := protocol.NewMessage(protocol.MsgTypeTermData, protocol.TermData{Data: b})
		if err != nil {
			return
		}
		_ = a.sendMessage(msg)
	}

	// 原子地：先发回放快照，再挂接实时 sink，保证字节顺序。
	sess.AttachWithReplay(func(snapshot []byte) {
		if len(snapshot) == 0 {
			return
		}
		msg, err := protocol.NewMessage(protocol.MsgTypeTermData, protocol.TermData{Data: snapshot, Replay: true})
		if err != nil {
			return
		}
		_ = a.sendMessage(msg)
		fmt.Printf("[Agent][Term] Replayed %d bytes on attach\n", len(snapshot))
	}, sink)

	// 进程退出时通知控制端。
	sess.SetOnExit(func(code int) {
		a.sendTermExit(code, "shell exited")
	})
}

func (a *Agent) handleTermInput(payload []byte) {
	sess := a.currentTerminal()
	if sess == nil {
		return
	}
	var in protocol.TermInput
	if err := json.Unmarshal(payload, &in); err != nil {
		return
	}
	if len(in.Data) == 0 {
		return
	}
	if err := sess.Write(in.Data); err != nil {
		fmt.Printf("[Agent][Term] Write failed: %v\n", err)
	}
}

func (a *Agent) handleTermResize(payload []byte) {
	sess := a.currentTerminal()
	if sess == nil {
		return
	}
	var rs protocol.TermResize
	if err := json.Unmarshal(payload, &rs); err != nil {
		return
	}
	if err := sess.Resize(rs.Cols, rs.Rows); err != nil {
		fmt.Printf("[Agent][Term] Resize failed: %v\n", err)
	}
}

// handleTermClose 控制端主动结束终端会话（彻底关闭进程）。
func (a *Agent) handleTermClose(_ []byte) {
	a.closeTerminal()
}

// detachTerminal 仅解除输出回调，进程与缓冲继续保留（断线时调用）。
func (a *Agent) detachTerminal() {
	a.termMu.Lock()
	sess := a.term
	a.termMu.Unlock()
	if sess != nil {
		sess.Detach()
	}
}

// closeTerminal 彻底关闭终端会话（关停进程，下一次打开会重建）。
func (a *Agent) closeTerminal() {
	a.termMu.Lock()
	sess := a.term
	a.term = nil
	a.termMu.Unlock()
	if sess != nil {
		_ = sess.Close()
		fmt.Printf("[Agent][Term] Session closed\n")
	}
}

func (a *Agent) sendTermData(data []byte, replay bool) {
	msg, err := protocol.NewMessage(protocol.MsgTypeTermData, protocol.TermData{Data: data, Replay: replay})
	if err != nil {
		return
	}
	_ = a.sendMessage(msg)
}

func (a *Agent) sendTermExit(code int, message string) {
	msg, err := protocol.NewMessage(protocol.MsgTypeTermExit, protocol.TermExit{Code: code, Message: message})
	if err != nil {
		return
	}
	_ = a.sendMessage(msg)
}
