package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
	"golang.org/x/term"
	"webrtc-portmap/pkg/auth"
	"webrtc-portmap/pkg/protocol"
	"webrtc-portmap/pkg/tunnel"
	wr "webrtc-portmap/pkg/webrtc"
)

const defaultTenantCode = "convnet"

type SignalMessage struct {
	Type      string                     `json:"type"`
	AgentID   string                     `json:"agent_id,omitempty"`
	SDP       *webrtc.SessionDescription `json:"sdp,omitempty"`
	Candidate *webrtc.ICECandidateInit   `json:"candidate,omitempty"`
	Token     string                     `json:"token,omitempty"`
}

type AgentInfo struct {
	ID          string                   `json:"id"`
	DisplayName string                   `json:"display_name"`
	Description string                   `json:"description"`
	ICEServers  []protocol.ICEServerInfo `json:"ice_servers"`
	Online      bool                     `json:"online"`
	Connected   bool                     `json:"connected"`
}

type connectBusyInfo struct {
	Busy           bool   `json:"busy"`
	AgentID        string `json:"agent_id"`
	ControllerUser string `json:"controller_user"`
	ControllerKind string `json:"controller_kind"`
}

type localMapSpec struct {
	LocalAddr string
	PortID    string
}

type mapFlags []localMapSpec

func (m *mapFlags) String() string {
	if len(*m) == 0 {
		return ""
	}
	parts := make([]string, 0, len(*m))
	for _, item := range *m {
		parts = append(parts, item.LocalAddr+"="+item.PortID)
	}
	return strings.Join(parts, ",")
}

func (m *mapFlags) Set(value string) error {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return fmt.Errorf("empty map value")
	}
	parts := strings.SplitN(raw, "=", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid map format %q, expected <local_addr>=<port_id>", value)
	}
	localAddr := normalizeLocalAddr(parts[0])
	portID := strings.TrimSpace(parts[1])
	if portID == "" {
		return fmt.Errorf("missing port_id in %q", value)
	}
	*m = append(*m, localMapSpec{
		LocalAddr: localAddr,
		PortID:    portID,
	})
	return nil
}

func normalizeLocalAddr(value string) string {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return raw
	}
	if !strings.Contains(raw, ":") {
		return "127.0.0.1:" + raw
	}
	return raw
}

func splitHostPort(addr string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, err
	}
	return host, port, nil
}

type Client struct {
	signalURL     string
	username      string
	userPassword  string
	agentID       string
	agentPassword string
	sessionToken  string
	httpClient    *http.Client
	signalConn    *websocket.Conn
	config        *wr.Config
	peer          *wr.Peer
	handshaker    *auth.Handshaker
	tunnelMgr     *tunnel.Manager
	stopChan      chan struct{}
	authenticated bool
	agentConfig   *protocol.AgentConfig
	selectedAgent *AgentInfo
	maps          []localMapSpec
	mapsApplied   bool
	mapsMu        sync.Mutex
	inputMu       sync.Mutex
	mappingPromptStarted bool

	// 终端模式
	termMode    bool
	termOnce    sync.Once
	termQuitOnce sync.Once
	termRestore func()
}

func main() {
	var (
		signalURL     = flag.String("signal", "http://localhost:8443", "Signaling server URL")
		username      = flag.String("username", "", "Login username")
		userPassword  = flag.String("user-password", "", "Login password")
		agentID       = flag.String("agent", "", "Agent ID to connect")
		agentPassword = flag.String("agent-password", "", "Agent local auth password")
		listOnly      = flag.Bool("list", false, "List my agents and exit")
		stun          = flag.String("stun", "stun:stun.l.google.com:19302", "STUN server")
		turn          = flag.String("turn", "", "TURN server URL (optional)")
		turnUser      = flag.String("turn-user", "", "TURN username")
		turnPass      = flag.String("turn-pass", "", "TURN password")
		iceConfig     = flag.String("ice-config", "", "ICE servers config JSON file (optional, overrides -turn)")
		termMode      = flag.Bool("term", false, "Attach to the agent's embedded terminal (interactive shell)")
	)
	var mappings mapFlags
	flag.Var(&mappings, "map", "Local port mapping in the form <local_addr>=<port_id>, e.g. 127.0.0.1:18080=http")
	flag.Parse()

	if *username == "" || *userPassword == "" {
		fmt.Println("Usage:")
		fmt.Println("  client -signal <url> -username <user> -user-password <pass>")
		fmt.Println("  client -signal <url> -username <user> -user-password <pass> -list")
		fmt.Println("  client -signal <url> -username <user> -user-password <pass> -agent <agent_id> -agent-password <password> [-map 127.0.0.1:18080=http]")
		fmt.Println("  client -signal <url> -username <user> -user-password <pass> -agent <agent_id> -agent-password <password> -term")
		flag.PrintDefaults()
		os.Exit(1)
	}
	client := &Client{
		signalURL:     strings.TrimRight(*signalURL, "/"),
		username:      *username,
		userPassword:  *userPassword,
		agentID:       *agentID,
		agentPassword: *agentPassword,
		httpClient:    &http.Client{Timeout: 45 * time.Second},
		stopChan:      make(chan struct{}),
		maps:          mappings,
		termMode:      *termMode,
	}
	client.tunnelMgr = tunnel.NewManager(client)
	var err error
	client.config, err = wr.NewConfig(*iceConfig, *turn, *turnUser, *turnPass)
	if err != nil {
		fmt.Printf("[Client] Failed to load ICE config: %v\n", err)
		os.Exit(1)
	}
	// 如果命令行指定了额外的 STUN，添加到列表开头
	if *stun != "" && *iceConfig == "" {
		client.config.ICEServers = append([]webrtc.ICEServer{{URLs: []string{*stun}}}, client.config.ICEServers...)
	}
	client.config.PrintICEServers()

	if err := client.login(); err != nil {
		fmt.Printf("[Client] Login failed: %v\n", err)
		os.Exit(1)
	}

	agents, err := client.listAgents()
	if err != nil {
		fmt.Printf("[Client] List agents failed: %v\n", err)
		os.Exit(1)
	}
	printAgents(agents)
	if *listOnly {
		return
	}
	if client.agentID == "" {
		if err := client.promptSelectAgent(agents); err != nil {
			fmt.Printf("[Client] Select agent failed: %v\n", err)
			os.Exit(1)
		}
	}
	if client.selectedAgent == nil {
		for i := range agents {
			if agents[i].ID == client.agentID {
				client.selectedAgent = &agents[i]
				break
			}
		}
	}
	if client.agentPassword == "" {
		client.agentPassword = strings.TrimSpace(client.promptLine("请输入 Agent 本地密码: "))
		if client.agentPassword == "" {
			fmt.Println("[Client] Agent password is required")
			os.Exit(1)
		}
	}

	if err := client.connect(); err != nil {
		fmt.Printf("[Client] Connect failed: %v\n", err)
		os.Exit(1)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\n[Client] Shutting down...")
	close(client.stopChan)
	client.cleanup()
}

func printAgents(agents []AgentInfo) {
	fmt.Printf("[Client] My agents (%d):\n", len(agents))
	for i, agent := range agents {
		status := "offline"
		if agent.Online {
			status = "online"
		}
		fmt.Printf("  %d) %s (%s) [%s]\n", i+1, agent.DisplayName, agent.ID, status)
	}
}

func (c *Client) promptLine(prompt string) string {
	c.inputMu.Lock()
	defer c.inputMu.Unlock()
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

func (c *Client) promptSelectAgent(agents []AgentInfo) error {
	if len(agents) == 0 {
		return fmt.Errorf("no available agents")
	}
	for {
		value := c.promptLine("请选择要连接的 Agent 编号: ")
		index, err := strconv.Atoi(value)
		if err != nil || index < 1 || index > len(agents) {
			fmt.Println("[Client] 无效编号，请重新输入")
			continue
		}
		selected := agents[index-1]
		c.agentID = selected.ID
		c.selectedAgent = &selected
		fmt.Printf("[Client] 已选择 Agent: %s (%s)\n", selected.DisplayName, selected.ID)
		return nil
	}
}

func iceServerInfosToConfig(infos []protocol.ICEServerInfo) *wr.Config {
	cfg := &wr.Config{ICEServers: make([]webrtc.ICEServer, 0, len(infos))}
	for _, info := range infos {
		if len(info.URLs) == 0 {
			continue
		}
		urls := make([]string, 0, len(info.URLs))
		for _, encoded := range info.URLs {
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				fmt.Printf("[Client] Skip invalid ICE URL encoding: %v\n", err)
				continue
			}
			urls = append(urls, string(decoded))
		}
		if len(urls) == 0 {
			continue
		}
		username := ""
		if info.Username != "" {
			if decoded, err := base64.StdEncoding.DecodeString(info.Username); err == nil {
				username = string(decoded)
			}
		}
		credential := ""
		if info.Credential != "" {
			if decoded, err := base64.StdEncoding.DecodeString(info.Credential); err == nil {
				credential = string(decoded)
			}
		}
		cfg.ICEServers = append(cfg.ICEServers, webrtc.ICEServer{
			URLs:       urls,
			Username:   username,
			Credential: credential,
		})
	}
	return cfg
}

func (c *Client) login() error {
	body := map[string]string{
		"tenant_code": defaultTenantCode,
		"username":    c.username,
		"password":    c.userPassword,
	}
	data, _ := json.Marshal(body)
	resp, err := c.httpClient.Post(c.signalURL+"/auth/login", "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var msg bytes.Buffer
		_, _ = msg.ReadFrom(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(msg.String()))
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	c.sessionToken = result.Token
	fmt.Printf("[Client] Login successful: %s\n", c.username)
	return nil
}

func (c *Client) listAgents() ([]AgentInfo, error) {
	req, _ := http.NewRequest(http.MethodGet, c.signalURL+"/client/list", nil)
	req.Header.Set("Authorization", "Bearer "+c.sessionToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var msg bytes.Buffer
		_, _ = msg.ReadFrom(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(msg.String()))
	}
	var agents []AgentInfo
	if err := json.NewDecoder(resp.Body).Decode(&agents); err != nil {
		return nil, err
	}
	return agents, nil
}

func (c *Client) connect() error {
	takeover, err := c.claimAgentControl(false)
	if err != nil {
		return err
	}
	if takeover {
		fmt.Printf("[Client] Waiting for previous session to close...\n")
		time.Sleep(500 * time.Millisecond)
	}
	if err := c.connectSignalWS(); err != nil {
		return err
	}
	peerConfig := c.config
	if c.selectedAgent != nil && len(c.selectedAgent.ICEServers) > 0 {
		peerConfig = iceServerInfosToConfig(c.selectedAgent.ICEServers)
		fmt.Printf("[Client] Using ICE servers provided by agent (%d)\n", len(peerConfig.ICEServers))
	}
	peer, err := wr.NewPeer(peerConfig)
	if err != nil {
		return err
	}
	c.peer = peer
	c.handshaker = auth.NewHandshaker(c.agentPassword, c.agentID, true)

	peer.SetOnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}
		init := candidate.ToJSON()
		_ = c.sendSignalingMessage(&SignalMessage{
			Type:      "candidate",
			Candidate: &init,
		})
	})
	peer.SetOnMessage(func(data []byte) {
		if !c.termMode {
			fmt.Printf("[Client] Raw data channel message: %d bytes\n", len(data))
		}
		c.handleMessage(data)
	})
	peer.SetOnDataChannelOpen(func() {
		fmt.Printf("[Client] Data channel opened, starting authentication...\n")
		if err := c.startAuthentication(); err != nil {
			fmt.Printf("[Client] Authentication start failed: %v\n", err)
		}
	})

	offer, err := peer.CreateOffer()
	if err != nil {
		return err
	}
	if err := c.sendSignalingMessage(&SignalMessage{
		Type: "offer",
		SDP:  offer,
	}); err != nil {
		return err
	}
	fmt.Printf("[Client] WebRTC offer sent, waiting for answer...\n")
	return nil
}

func (c *Client) claimAgentControl(force bool) (bool, error) {
	body := map[string]interface{}{
		"agent_id": c.agentID,
		"force":    force,
	}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, c.signalURL+"/client/connect", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+c.sessionToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		var busy connectBusyInfo
		if err := json.NewDecoder(resp.Body).Decode(&busy); err != nil {
			return false, fmt.Errorf("agent is busy")
		}
		answer := strings.ToLower(strings.TrimSpace(c.promptLine(
			fmt.Sprintf("Agent %s 当前正由 %s (%s) 使用，是否强行断开之前的会话？[y/N]: ",
				busy.AgentID, busy.ControllerUser, busy.ControllerKind))))
		if answer == "y" || answer == "yes" {
			return c.claimAgentControl(true)
		}
		return false, fmt.Errorf("agent is busy")
	}

	if resp.StatusCode != http.StatusOK {
		var msg bytes.Buffer
		_, _ = msg.ReadFrom(resp.Body)
		return false, fmt.Errorf("connect claim failed: status %d: %s", resp.StatusCode, strings.TrimSpace(msg.String()))
	}

	var result struct {
		Success  bool `json:"success"`
		Takeover bool `json:"takeover"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, err
	}
	return result.Takeover, nil
}

func (c *Client) connectSignalWS() error {
	wsURL := strings.TrimPrefix(c.signalURL, "http://")
	wsURL = strings.TrimPrefix(wsURL, "https://")
	if strings.HasPrefix(c.signalURL, "https://") {
		wsURL = "wss://" + wsURL
	} else {
		wsURL = "ws://" + wsURL
	}
	wsURL = wsURL + "/client/ws?agent_id=" + c.agentID

	header := http.Header{}
	header.Set("Authorization", "Bearer "+c.sessionToken)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		return err
	}
	c.signalConn = conn
	go c.signalReadLoop()
	return nil
}

func (c *Client) signalReadLoop() {
	for {
		_, data, err := c.signalConn.ReadMessage()
		if err != nil {
			fmt.Printf("[Client] Signal websocket closed: %v\n", err)
			return
		}
		var signal SignalMessage
		if err := json.Unmarshal(data, &signal); err != nil {
			fmt.Printf("[Client] Signal websocket decode failed: %v\n", err)
			continue
		}
		c.handleSignalingMessage(&signal)
	}
}

func (c *Client) handleSignalingMessage(msg *SignalMessage) {
	switch msg.Type {
	case "answer":
		if msg.SDP != nil && c.peer != nil {
			if err := c.peer.SetRemoteDescription(msg.SDP); err != nil {
				fmt.Printf("[Client] Set remote description failed: %v\n", err)
			}
		}
	case "candidate":
		if msg.Candidate != nil && c.peer != nil {
			if err := c.peer.AddICECandidate(msg.Candidate); err != nil {
				fmt.Printf("[Client] Add ICE candidate failed: %v\n", err)
			}
		}
	}
}

func (c *Client) sendSignalingMessage(msg *SignalMessage) error {
	if c.signalConn == nil {
		return fmt.Errorf("signal websocket not connected")
	}
	return c.signalConn.WriteJSON(msg)
}

func (c *Client) startAuthentication() error {
	msg, err := c.handshaker.CreateChallenge()
	if err != nil {
		return err
	}
	return c.sendMessage(msg)
}

func (c *Client) handleMessage(data []byte) {
	var msg protocol.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		fmt.Printf("[Client] Invalid protocol message: %v\n", err)
		return
	}

	if !c.authenticated {
		c.handleAuthMessage(&msg)
		return
	}

	switch msg.Type {
	case protocol.MsgTypeConnectResp, protocol.MsgTypeHalfCloseStream, protocol.MsgTypeData, protocol.MsgTypeCloseStream:
		fmt.Printf("[Client] Received tunnel message type=%s payload=%d bytes\n", msg.Type.String(), len(msg.Payload))
	}

	switch msg.Type {
	case protocol.MsgTypeAgentConfig:
		var cfg protocol.AgentConfig
		if err := json.Unmarshal(msg.Payload, &cfg); err != nil {
			fmt.Printf("[Client] Parse agent config failed: %v\n", err)
			return
		}
		c.agentConfig = &cfg
		fmt.Printf("[Client] Agent config received: %d ports\n", len(cfg.Ports))
		for _, p := range cfg.Ports {
			if p.AllowAccess {
				fmt.Printf("  - %s (%s): %s\n", p.Name, p.ID, p.LocalAddr)
			}
		}
		if c.termMode {
			if cfg.Terminal == nil || !cfg.Terminal.Enabled {
				fmt.Printf("[Client] Agent has no embedded terminal enabled (start agent with -terminal)\n")
				return
			}
			go c.startTerminal()
		} else {
			go c.applyMappings()
		}
	case protocol.MsgTypeConnectResp:
		if err := c.tunnelMgr.HandleConnectResponse(msg.Payload); err != nil {
			fmt.Printf("[Client] Connect response failed: %v\n", err)
		}
	case protocol.MsgTypeHalfCloseStream:
		if err := c.tunnelMgr.HandleHalfCloseStream(msg.Payload); err != nil {
			fmt.Printf("[Client] Half-close stream failed: %v\n", err)
		}
	case protocol.MsgTypeData:
		if err := c.tunnelMgr.HandleDataMessage(msg.Payload); err != nil {
			fmt.Printf("[Client] Tunnel data failed: %v\n", err)
		}
	case protocol.MsgTypeCloseStream:
		if err := c.tunnelMgr.HandleCloseStream(msg.Payload); err != nil {
			fmt.Printf("[Client] Close stream failed: %v\n", err)
		}
	case protocol.MsgTypeTermData:
		c.handleTermData(msg.Payload)
	case protocol.MsgTypeTermExit:
		c.handleTermExit(msg.Payload)
	case protocol.MsgTypePing:
		var ping protocol.Ping
		if err := json.Unmarshal(msg.Payload, &ping); err == nil {
			pong, _ := protocol.NewMessage(protocol.MsgTypePong, protocol.Pong{Timestamp: ping.Timestamp})
			_ = c.sendMessage(pong)
		}
	default:
		if !c.termMode {
			fmt.Printf("[Client] Ignored message type: %s\n", msg.Type.String())
		}
	}
}

func (c *Client) handleAuthMessage(msg *protocol.Message) {
	switch msg.Type {
	case protocol.MsgTypeAuthResponse:
		resultMsg, err := c.handshaker.HandleResponse(msg)
		if resultMsg != nil {
			_ = c.sendMessage(resultMsg)
		}
		if err != nil {
			fmt.Printf("[Client] Authentication failed: %v\n", err)
			return
		}
		c.authenticated = c.handshaker.IsAuthenticated()
		if c.authenticated {
			fmt.Printf("[Client] Authentication successful\n")
		}
	case protocol.MsgTypePing:
		var ping protocol.Ping
		if err := json.Unmarshal(msg.Payload, &ping); err == nil {
			pong, _ := protocol.NewMessage(protocol.MsgTypePong, protocol.Pong{Timestamp: ping.Timestamp})
			_ = c.sendMessage(pong)
		}
	default:
		fmt.Printf("[Client] Waiting auth, ignored message type: %s\n", msg.Type.String())
	}
}

func (c *Client) applyMappings() {
	c.mapsMu.Lock()
	defer c.mapsMu.Unlock()
	if c.mapsApplied {
		return
	}
	if c.agentConfig == nil {
		return
	}
	if c.mappingPromptStarted {
		return
	}
	c.mappingPromptStarted = true
	if len(c.maps) == 0 {
		c.promptMappings()
	}

	portByID := make(map[string]protocol.PortInfo)
	for _, p := range c.agentConfig.Ports {
		portByID[p.ID] = p
	}

	for _, mapping := range c.maps {
		port, ok := portByID[mapping.PortID]
		if !ok || !port.AllowAccess {
			fmt.Printf("[Client] Skip mapping %s=%s: port not found or not allowed\n", mapping.LocalAddr, mapping.PortID)
			continue
		}
		mapID := mapping.PortID + "@" + mapping.LocalAddr
		if err := c.tunnelMgr.AddMap(mapping.LocalAddr, port.LocalAddr, port.Protocol, mapID); err != nil {
			fmt.Printf("[Client] Add mapping failed %s=%s: %v\n", mapping.LocalAddr, mapping.PortID, err)
			continue
		}
		fmt.Printf("[Client] Local mapping ready: %s -> %s (%s)\n", mapping.LocalAddr, port.LocalAddr, port.Name)
	}
	c.mapsApplied = true
}

func (c *Client) promptMappings() {
	if c.agentConfig == nil {
		return
	}
	available := make([]protocol.PortInfo, 0)
	for _, port := range c.agentConfig.Ports {
		if port.AllowAccess {
			available = append(available, port)
		}
	}
	if len(available) == 0 {
		fmt.Println("[Client] Agent has no accessible services")
		return
	}

	mode := strings.ToLower(strings.TrimSpace(c.promptLine("选择映射方式：1) 默认全部映射  2) 自定义映射  （直接回车默认）: ")))
	if mode == "" || mode == "1" || mode == "default" || mode == "d" {
		c.maps = append(c.maps, c.buildDefaultMappings(available)...)
		for _, mapping := range c.maps {
			fmt.Printf("[Client] 默认映射计划: %s -> %s\n", mapping.LocalAddr, mapping.PortID)
		}
		return
	}

	for {
		fmt.Println("[Client] 可映射的服务：")
		for i, port := range available {
			fmt.Printf("  %d) %s (%s -> %s)\n", i+1, port.Name, port.ID, port.LocalAddr)
		}
		choice := c.promptLine("选择服务编号（直接回车结束）: ")
		if choice == "" {
			if len(c.maps) == 0 {
				fmt.Println("[Client] 未配置任何本地映射")
			}
			return
		}
		index, err := strconv.Atoi(choice)
		if err != nil || index < 1 || index > len(available) {
			fmt.Println("[Client] 无效编号，请重新输入")
			continue
		}
		selected := available[index-1]
		for {
			local := normalizeLocalAddr(c.promptLine(fmt.Sprintf("为服务 %s 指定本地监听地址/端口（如 127.0.0.1:18080 或 18080）: ", selected.ID)))
			if local == "" {
				fmt.Println("[Client] 本地端口不能为空")
				continue
			}
			if c.hasMappingForLocal(local) {
				fmt.Println("[Client] 本地端口已在当前计划中使用，请重新指定")
				continue
			}
			c.maps = append(c.maps, localMapSpec{LocalAddr: local, PortID: selected.ID})
			fmt.Printf("[Client] 已加入映射计划: %s -> %s (%s)\n", local, selected.LocalAddr, selected.Name)
			break
		}
	}
}

func (c *Client) hasMappingForLocal(local string) bool {
	for _, mapping := range c.maps {
		if mapping.LocalAddr == local {
			return true
		}
	}
	return false
}

func (c *Client) buildDefaultMappings(ports []protocol.PortInfo) []localMapSpec {
	result := make([]localMapSpec, 0, len(ports))
	used := make(map[string]struct{})
	for _, port := range ports {
		addr := c.nextDefaultLocalAddr(port, used)
		used[addr] = struct{}{}
		result = append(result, localMapSpec{
			LocalAddr: addr,
			PortID:    port.ID,
		})
	}
	return result
}

func (c *Client) nextDefaultLocalAddr(port protocol.PortInfo, used map[string]struct{}) string {
	host := "127.0.0.1"
	basePort := c.defaultPortForService(port)
	for i := 0; i < 100; i++ {
		addr := fmt.Sprintf("%s:%d", host, basePort+i)
		if _, exists := used[addr]; exists {
			continue
		}
		if !c.canListen(addr) {
			continue
		}
		return addr
	}
	return fmt.Sprintf("%s:%d", host, basePort)
}

func (c *Client) defaultPortForService(port protocol.PortInfo) int {
	id := strings.ToLower(strings.TrimSpace(port.ID))
	name := strings.ToLower(strings.TrimSpace(port.Name))
	switch {
	case id == "http" || strings.Contains(name, "http"):
		return 18080
	case id == "https" || strings.Contains(name, "https"):
		return 18443
	case id == "mysql" || strings.Contains(name, "mysql"):
		return 13306
	}

	_, remotePort, err := splitHostPort(port.LocalAddr)
	if err == nil && remotePort > 0 {
		if remotePort < 10000 {
			return remotePort + 10000
		}
		return remotePort
	}
	return 18000
}

func (c *Client) canListen(addr string) bool {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

func (c *Client) SendMessage(msg *protocol.Message) error {
	return c.sendMessage(msg)
}

func (c *Client) sendMessage(msg *protocol.Message) error {
	if c.peer == nil {
		return fmt.Errorf("peer not connected")
	}
	return c.peer.SendJSON(msg)
}

func (c *Client) cleanup() {
	// 恢复本地终端模式（若处于终端模式）。注意：仅断开连接，
	// 不向 agent 发送 TermClose —— agent 端会话保持运行，下次 -term 重连可回放。
	if c.termRestore != nil {
		c.termRestore()
		c.termRestore = nil
	}
	if c.sessionToken != "" && c.agentID != "" {
		body, _ := json.Marshal(map[string]string{"agent_id": c.agentID})
		req, _ := http.NewRequest(http.MethodPost, c.signalURL+"/client/disconnect", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.sessionToken)
		_, _ = c.httpClient.Do(req)
	}
	if c.tunnelMgr != nil {
		for _, item := range c.tunnelMgr.GetMaps() {
			_ = c.tunnelMgr.RemoveMap(item.ID)
		}
	}
	if c.peer != nil {
		_ = c.peer.Close()
		c.peer = nil
	}
	if c.signalConn != nil {
		_ = c.signalConn.Close()
		c.signalConn = nil
	}
	c.authenticated = false
}

// ==================== 终端模式 ====================

// startTerminal 把本地控制台切到 raw 模式，并与 agent 的持久终端会话双向桥接。
// 仅运行一次（AgentConfig 可能重复到达）。按 Ctrl-] 退出（退出仅断开，不结束远端会话）。
func (c *Client) startTerminal() {
	c.termOnce.Do(func() {
		fd := int(os.Stdin.Fd())
		isTTY := term.IsTerminal(fd)
		if isTTY {
			if oldState, err := term.MakeRaw(fd); err == nil {
				c.termRestore = func() { _ = term.Restore(fd, oldState) }
			}
		}

		cols, rows := 80, 24
		if w, h, err := term.GetSize(fd); err == nil && w > 0 && h > 0 {
			cols, rows = w, h
		}

		openMsg, _ := protocol.NewMessage(protocol.MsgTypeTermOpen, protocol.TermOpenRequest{Cols: cols, Rows: rows})
		_ = c.sendMessage(openMsg)
		fmt.Fprintf(os.Stderr, "\r\n[client] 终端已附着（按 Ctrl-] 退出，远端会话保持运行）\r\n")

		c.watchResize(fd)

		go c.stdinLoop()
	})
}

// stdinLoop 读取本地输入并转发到 agent；遇到 Ctrl-](0x1d) 退出。
func (c *Client) stdinLoop() {
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			data := buf[:n]
			if idx := bytes.IndexByte(data, 0x1d); idx >= 0 {
				if idx > 0 {
					c.sendTermInput(data[:idx])
				}
				c.quitTerminal()
				return
			}
			c.sendTermInput(data)
		}
		if err != nil {
			return
		}
	}
}

func (c *Client) sendTermInput(data []byte) {
	if len(data) == 0 {
		return
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	msg, err := protocol.NewMessage(protocol.MsgTypeTermInput, protocol.TermInput{Data: cp})
	if err != nil {
		return
	}
	_ = c.sendMessage(msg)
}

func (c *Client) sendTermResize(cols, rows int) {
	msg, err := protocol.NewMessage(protocol.MsgTypeTermResize, protocol.TermResize{Cols: cols, Rows: rows})
	if err != nil {
		return
	}
	_ = c.sendMessage(msg)
}

func (c *Client) handleTermData(payload []byte) {
	var d protocol.TermData
	if err := json.Unmarshal(payload, &d); err != nil {
		return
	}
	if len(d.Data) > 0 {
		_, _ = os.Stdout.Write(d.Data)
	}
}

func (c *Client) handleTermExit(payload []byte) {
	var e protocol.TermExit
	_ = json.Unmarshal(payload, &e)
	fmt.Fprintf(os.Stderr, "\r\n[client] 远端 shell 已退出 code=%d %s\r\n", e.Code, e.Message)
	c.quitTerminal()
}

// quitTerminal 退出终端模式：恢复本地终端并结束进程（不结束远端会话）。
// 用 sync.Once 保证只执行一次，避免 stdin 与退出通知并发触发导致重复 close。
func (c *Client) quitTerminal() {
	c.termQuitOnce.Do(func() {
		if c.termRestore != nil {
			c.termRestore()
			c.termRestore = nil
		}
		close(c.stopChan)
		c.cleanup()
		os.Exit(0)
	})
}
