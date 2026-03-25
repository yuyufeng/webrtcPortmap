package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
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
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Online      bool   `json:"online"`
	Connected   bool   `json:"connected"`
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
	maps          []localMapSpec
	mapsApplied   bool
	mapsMu        sync.Mutex
	inputMu       sync.Mutex
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
	)
	var mappings mapFlags
	flag.Var(&mappings, "map", "Local port mapping in the form <local_addr>=<port_id>, e.g. 127.0.0.1:18080=http")
	flag.Parse()

	if *username == "" || *userPassword == "" {
		fmt.Println("Usage: client -username <user> -user-password <pass> [-list] | [-agent <agent_id> -agent-password <password> -map 127.0.0.1:18080=http]")
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
	}
	client.tunnelMgr = tunnel.NewManager(client)
	if *turn != "" {
		client.config = wr.ConfigWithTURN(*turn, *turnUser, *turnPass)
	} else {
		client.config = wr.DefaultConfig()
	}
	if *stun != "" {
		client.config.ICEServers = append([]webrtc.ICEServer{{URLs: []string{*stun}}}, client.config.ICEServers...)
	}

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
		fmt.Printf("[Client] 已选择 Agent: %s (%s)\n", selected.DisplayName, selected.ID)
		return nil
	}
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
	if err := c.connectSignalWS(); err != nil {
		return err
	}
	peer, err := wr.NewPeer(c.config)
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
	peer.SetOnMessage(c.handleMessage)
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
		c.applyMappings()
	case protocol.MsgTypeConnectResp:
		if err := c.tunnelMgr.HandleConnectResponse(msg.Payload); err != nil {
			fmt.Printf("[Client] Connect response failed: %v\n", err)
		}
	case protocol.MsgTypeHalfCloseStream:
		if err := c.tunnelMgr.HandleCloseStream(msg.Payload); err != nil {
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
	case protocol.MsgTypePing:
		var ping protocol.Ping
		if err := json.Unmarshal(msg.Payload, &ping); err == nil {
			pong, _ := protocol.NewMessage(protocol.MsgTypePong, protocol.Pong{Timestamp: ping.Timestamp})
			_ = c.sendMessage(pong)
		}
	default:
		fmt.Printf("[Client] Ignored message type: %s\n", msg.Type.String())
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
			mapID := selected.ID + "@" + local
			if err := c.tunnelMgr.AddMap(local, selected.LocalAddr, selected.Protocol, mapID); err != nil {
				fmt.Printf("[Client] 本地端口不可用，请重新指定: %v\n", err)
				continue
			}
			c.maps = append(c.maps, localMapSpec{LocalAddr: local, PortID: selected.ID})
			fmt.Printf("[Client] Local mapping ready: %s -> %s (%s)\n", local, selected.LocalAddr, selected.Name)
			break
		}
	}
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
