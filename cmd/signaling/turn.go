package main

// turn.go —— 内嵌进 signaling 进程的 TURN 中转服务。
//
// 设计：
//   - 用 pion/turn 在 UDP+TCP :port 上起 TURN server，凭据走 TURN REST 临时凭据
//     （username = "<expiryUnix>:<userID>"，password = base64(HMAC_SHA1(secret, username))）。
//   - AuthHandler 校验 HMAC/时效；QuotaHandler 在月度流量用满时拒绝新分配。
//   - 每个 allocation 的中继 net.PacketConn 被 meteredPacketConn 包装：累计字节、按用户 max-bps 限速、
//     用满即断。EventHandler.OnAllocationCreated 把 relayAddr(端口) 关联到 userID 与 max-bps。
//   - 后台 ticker 定时把各 allocation 的增量字节累加进 DataStore（按用户、按月）。
//   - 流量精确归属到 userID；agent 中继归属到其 owner 用户（凭据由 owner userID 签发）。

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/logging"
	"github.com/pion/turn/v4"

	"webrtc-portmap/pkg/protocol"
)

// TURNConfig 内嵌 TURN 服务的配置。
type TURNConfig struct {
	Enabled    bool
	PublicIP   string        // 对外/relay 地址（必须是客户端可达的公网 IP）
	ListenAddr string        // 绑定地址，默认 0.0.0.0
	Port       int           // 监听端口，默认 3478
	Realm      string        // TURN realm
	Secret     string        // 临时凭据 HMAC 共享密钥
	TTL        time.Duration // 临时凭据有效期
}

// flushInterval 是把中继用量累加进存储的周期。
const turnFlushInterval = 10 * time.Second

// TURNService 持有运行中的 TURN server 与计量器。
type TURNService struct {
	server *turn.Server
	meter  *turnMeter
}

// Close 关停 TURN server 与计量器。
func (s *TURNService) Close() error {
	if s == nil {
		return nil
	}
	if s.meter != nil {
		s.meter.stop()
	}
	if s.server != nil {
		return s.server.Close()
	}
	return nil
}

// issueTURNCredentials 为某 userID 生成临时 TURN 凭据与 URL 列表。
// 未启用 TURN 返回 ok=false。流量将归属到该 userID。
func (s *Server) issueTURNCredentials(userID string) (username, credential string, urls []string, ok bool) {
	if !s.turn.Enabled || strings.TrimSpace(s.turn.PublicIP) == "" || strings.TrimSpace(userID) == "" {
		return "", "", nil, false
	}
	username, credential, err := turn.GenerateLongTermTURNRESTCredentials(s.turn.Secret, userID, s.turn.TTL)
	if err != nil {
		return "", "", nil, false
	}
	port := s.turn.Port
	if port <= 0 {
		port = 3478
	}
	host := net.JoinHostPort(s.turn.PublicIP, strconv.Itoa(port))
	urls = []string{
		"turn:" + host,
		"turn:" + host + "?transport=tcp",
	}
	return username, credential, urls, true
}

// turnICEServerInfo 返回注入用的（base64 编码）ICEServerInfo；未启用返回 ok=false。
// 编码方式与 agent 端 convertICEServers 一致（URL/username/credential 均 base64）。
func (s *Server) turnICEServerInfo(userID string) (protocol.ICEServerInfo, bool) {
	username, credential, urls, ok := s.issueTURNCredentials(userID)
	if !ok {
		return protocol.ICEServerInfo{}, false
	}
	enc := make([]string, 0, len(urls))
	for _, u := range urls {
		enc = append(enc, base64.StdEncoding.EncodeToString([]byte(u)))
	}
	return protocol.ICEServerInfo{
		URLs:       enc,
		Username:   base64.StdEncoding.EncodeToString([]byte(username)),
		Credential: base64.StdEncoding.EncodeToString([]byte(credential)),
	}, true
}

// iceServersForUser 把内嵌 TURN（带该用户临时凭据）prepend 到 agent 上报的 ICE 列表前。
func (s *Server) iceServersForUser(userID string, base []protocol.ICEServerInfo) []protocol.ICEServerInfo {
	if turnInfo, ok := s.turnICEServerInfo(userID); ok {
		return append([]protocol.ICEServerInfo{turnInfo}, base...)
	}
	return base
}

// parseTURNUserID 从 TURN REST username "<expiry>:<userID>" 解析出 userID。
func parseTURNUserID(username string) string {
	parts := strings.SplitN(username, ":", 2)
	if len(parts) != 2 {
		return ""
	}
	return parts[1]
}

// startTURNServer 启动内嵌 TURN server。失败返回 error（调用方决定是否致命）。
func startTURNServer(store *DataStore, cfg TURNConfig) (*TURNService, error) {
	relayIP := net.ParseIP(strings.TrimSpace(cfg.PublicIP))
	if relayIP == nil {
		return nil, fmt.Errorf("turn: -turn-public-ip 必须为合法 IP（客户端可达的公网地址），当前=%q", cfg.PublicIP)
	}
	bindIP := strings.TrimSpace(cfg.ListenAddr)
	if bindIP == "" {
		bindIP = "0.0.0.0"
	}
	port := cfg.Port
	if port <= 0 {
		port = 3478
	}
	realm := strings.TrimSpace(cfg.Realm)
	if realm == "" {
		realm = "webrtc-portmap"
	}

	listenAddr := net.JoinHostPort(bindIP, strconv.Itoa(port))

	udpConn, err := net.ListenPacket("udp4", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("turn: listen udp %s failed: %w", listenAddr, err)
	}
	tcpListener, err := net.Listen("tcp4", listenAddr)
	if err != nil {
		_ = udpConn.Close()
		return nil, fmt.Errorf("turn: listen tcp %s failed: %w", listenAddr, err)
	}

	meter := newTURNMeter(store)
	gen := &meteringRelayGenerator{relayIP: relayIP, bindIP: bindIP, meter: meter}
	loggerFactory := logging.NewDefaultLoggerFactory()

	authHandler := turn.LongTermTURNRESTAuthHandler(cfg.Secret, loggerFactory.NewLogger("turn"))

	quotaHandler := func(username, realm string, srcAddr net.Addr) (ok bool) {
		uid := parseTURNUserID(username)
		if uid == "" {
			return true
		}
		if store.UserUsageExhausted(uid) {
			fmt.Printf("[TURN] Reject allocation: user=%s monthly quota exhausted\n", uid)
			return false
		}
		return true
	}

	eventHandler := turn.EventHandler{
		OnAllocationCreated: func(srcAddr, dstAddr net.Addr, protocol, username, realm string, relayAddr net.Addr, requestedPort int) {
			port := addrPort(relayAddr)
			if port == 0 {
				return
			}
			uid := parseTURNUserID(username)
			q := store.UserQuota(uid)
			meter.attach(port, uid, q.MaxBps, q.Exhausted)
		},
	}

	server, err := turn.NewServer(turn.ServerConfig{
		Realm:         realm,
		AuthHandler:   authHandler,
		QuotaHandler:  quotaHandler,
		EventHandler:  eventHandler,
		LoggerFactory: loggerFactory,
		PacketConnConfigs: []turn.PacketConnConfig{
			{PacketConn: udpConn, RelayAddressGenerator: gen},
		},
		ListenerConfigs: []turn.ListenerConfig{
			{Listener: tcpListener, RelayAddressGenerator: gen},
		},
	})
	if err != nil {
		_ = udpConn.Close()
		_ = tcpListener.Close()
		meter.stop()
		return nil, fmt.Errorf("turn: NewServer failed: %w", err)
	}

	meter.start()
	fmt.Printf("[TURN] Embedded TURN server listening on %s (udp+tcp), relay=%s realm=%q ttl=%s\n",
		listenAddr, relayIP.String(), realm, cfg.TTL)
	return &TURNService{server: server, meter: meter}, nil
}

// addrPort 从 net.Addr 取端口号（UDP/TCP）。
func addrPort(a net.Addr) int {
	switch v := a.(type) {
	case *net.UDPAddr:
		return v.Port
	case *net.TCPAddr:
		return v.Port
	}
	// 退化解析 "host:port"
	if _, p, err := net.SplitHostPort(a.String()); err == nil {
		if n, err := strconv.Atoi(p); err == nil {
			return n
		}
	}
	return 0
}

// ==================== 计量器 ====================

// allocMeter 跟踪单个 allocation 的中继用量与限速。
type allocMeter struct {
	port   int
	userID string

	total   atomic.Int64 // 本 allocation 累计中继字节（读+写）
	flushed int64        // 已累加进存储的字节（meter.mu 保护）

	exhausted atomic.Bool
	closer    func() error // 关闭底层中继 conn（用满即断）
	closeOnce sync.Once

	// 简单令牌桶限速（双向共享一个上限 = max-bps）：
	rlMu      sync.Mutex
	bps       int64
	allowance float64
	last      time.Time
}

// rateLimit 按 max-bps 对 n 字节做令牌桶限速（bps<=0 不限）。
func (am *allocMeter) rateLimit(n int) {
	am.rlMu.Lock()
	bps := am.bps
	if bps <= 0 {
		am.rlMu.Unlock()
		return
	}
	now := time.Now()
	if am.last.IsZero() {
		am.last = now
	}
	am.allowance += now.Sub(am.last).Seconds() * float64(bps)
	am.last = now
	if max := float64(bps); am.allowance > max { // 突发上限 = 1 秒额度
		am.allowance = max
	}
	am.allowance -= float64(n)
	var sleep time.Duration
	if am.allowance < 0 {
		sleep = time.Duration(-am.allowance / float64(bps) * float64(time.Second))
	}
	am.rlMu.Unlock()
	if sleep > 0 {
		time.Sleep(sleep)
	}
}

func (am *allocMeter) forceClose() {
	am.closeOnce.Do(func() {
		if am.closer != nil {
			_ = am.closer()
		}
	})
}

type turnMeter struct {
	store *DataStore

	mu     sync.Mutex
	allocs map[int]*allocMeter

	stopCh chan struct{}
	doneCh chan struct{}
}

func newTURNMeter(store *DataStore) *turnMeter {
	return &turnMeter{
		store:  store,
		allocs: map[int]*allocMeter{},
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

func (m *turnMeter) start() {
	go m.flushLoop()
}

func (m *turnMeter) stop() {
	select {
	case <-m.stopCh:
		// already stopped
	default:
		close(m.stopCh)
		<-m.doneCh
	}
}

// add 在 AllocatePacketConn 时登记一个 allocation（userID 稍后由 attach 填入）。
func (m *turnMeter) add(am *allocMeter) {
	m.mu.Lock()
	m.allocs[am.port] = am
	m.mu.Unlock()
}

// get 按中继端口取 allocMeter。
func (m *turnMeter) get(port int) *allocMeter {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.allocs[port]
}

// attach 在 OnAllocationCreated 时把 userID 与 max-bps 绑定到 allocation。
func (m *turnMeter) attach(port int, userID string, maxBps int64, exhausted bool) {
	am := m.get(port)
	if am == nil {
		return
	}
	am.userID = userID
	am.rlMu.Lock()
	am.bps = maxBps
	am.rlMu.Unlock()
	am.exhausted.Store(exhausted)
	fmt.Printf("[TURN] Allocation created: port=%d user=%s maxBps=%d exhausted=%v\n", port, userID, maxBps, exhausted)
}

// remove 在中继 conn 关闭时结算并移除 allocation。
func (m *turnMeter) remove(port int) {
	m.mu.Lock()
	am := m.allocs[port]
	delete(m.allocs, port)
	m.mu.Unlock()
	if am != nil {
		m.flushOne(am)
		fmt.Printf("[TURN] Allocation closed: port=%d user=%s totalBytes=%d\n", port, am.userID, am.total.Load())
	}
}

// flushOne 把单个 allocation 的增量字节累加进存储。
func (m *turnMeter) flushOne(am *allocMeter) {
	if am.userID == "" {
		return
	}
	total := am.total.Load()
	delta := total - am.flushed
	if delta <= 0 {
		return
	}
	if _, err := m.store.AddUserUsage(am.userID, delta); err != nil {
		fmt.Printf("[TURN] AddUserUsage failed user=%s: %v\n", am.userID, err)
		return
	}
	am.flushed = total
}

// flushLoop 周期性把所有 allocation 的增量用量落库，并对超额用户切断活动会话。
func (m *turnMeter) flushLoop() {
	defer close(m.doneCh)
	ticker := time.NewTicker(turnFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			// 退出前做一次最终结算
			m.flushAll()
			return
		case <-ticker.C:
			m.flushAll()
		}
	}
}

func (m *turnMeter) flushAll() {
	m.mu.Lock()
	snapshot := make([]*allocMeter, 0, len(m.allocs))
	for _, am := range m.allocs {
		snapshot = append(snapshot, am)
	}
	m.mu.Unlock()

	for _, am := range snapshot {
		m.flushOne(am)
		if am.userID == "" {
			continue
		}
		if m.store.UserUsageExhausted(am.userID) {
			am.exhausted.Store(true)
			am.forceClose() // 月度额度用满，切断活动中继
		}
	}
}

// ==================== 中继地址生成器 + 计量 conn ====================

// meteringRelayGenerator 实现 turn.RelayAddressGenerator，
// 为每个 allocation 分配一个被 meteredPacketConn 包装的中继 UDP 套接字。
type meteringRelayGenerator struct {
	relayIP net.IP
	bindIP  string
	meter   *turnMeter
}

func (g *meteringRelayGenerator) Validate() error {
	if g.relayIP == nil {
		return errors.New("turn: relay public IP not set")
	}
	return nil
}

func (g *meteringRelayGenerator) AllocatePacketConn(network string, requestedPort int) (net.PacketConn, net.Addr, error) {
	conn, err := net.ListenPacket(network, net.JoinHostPort(g.bindIP, strconv.Itoa(requestedPort)))
	if err != nil {
		return nil, nil, err
	}
	la, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		_ = conn.Close()
		return nil, nil, errors.New("turn: unexpected relay local addr type")
	}
	relayAddr := &net.UDPAddr{IP: g.relayIP, Port: la.Port}

	am := &allocMeter{port: la.Port, closer: conn.Close}
	g.meter.add(am)

	return &meteredPacketConn{PacketConn: conn, meter: g.meter, am: am}, relayAddr, nil
}

func (g *meteringRelayGenerator) AllocateConn(network string, requestedPort int) (net.Conn, net.Addr, error) {
	// 仅支持 UDP 中继（标准 WebRTC TURN 用法）；TCP 中继(RFC6062)不支持。
	return nil, nil, errors.New("turn: TCP relay allocation not supported")
}

// meteredPacketConn 包装中继 net.PacketConn：累计字节、限速、用满即断。
type meteredPacketConn struct {
	net.PacketConn
	meter *turnMeter
	am    *allocMeter
}

func (c *meteredPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	if c.am.exhausted.Load() {
		// 月度额度用满：丢弃出站数据（假装已发送，避免上层报错忙循环）。
		return len(p), nil
	}
	c.am.rateLimit(len(p))
	n, err := c.PacketConn.WriteTo(p, addr)
	if n > 0 {
		c.am.total.Add(int64(n))
	}
	return n, err
}

func (c *meteredPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, addr, err := c.PacketConn.ReadFrom(p)
	if n > 0 {
		c.am.total.Add(int64(n))
		c.am.rateLimit(n)
	}
	return n, addr, err
}

func (c *meteredPacketConn) Close() error {
	c.meter.remove(c.am.port)
	return c.PacketConn.Close()
}
