package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pion/webrtc/v4"
)

//go:embed all:web/static
var staticFiles embed.FS

type AgentInfo struct {
	ID           string    `json:"id"`
	Token        string    `json:"token"`
	AgentKey     string    `json:"-"`
	LastSeen     time.Time `json:"last_seen"`
	Connected    bool      `json:"connected"`
	ControllerCh chan *SignalMessage `json:"-"`
	AgentCh      chan *SignalMessage `json:"-"`
}

type SignalMessage struct {
	Type      string                    `json:"type"`
	AgentID   string                    `json:"agent_id,omitempty"`
	SDP       *webrtc.SessionDescription `json:"sdp,omitempty"`
	Candidate *webrtc.ICECandidateInit   `json:"candidate,omitempty"`
	Token     string                    `json:"token,omitempty"`
}

type Server struct {
	addr      string
	authToken string
	webDir    string
	mu        sync.RWMutex
	agents    map[string]*AgentInfo
	tokens    map[string]string
}

func NewServer(addr, authToken, webDir string) *Server {
	return &Server{
		addr:      addr,
		authToken: authToken,
		webDir:    webDir,
		agents:    make(map[string]*AgentInfo),
		tokens:    make(map[string]string),
	}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/agent/register", s.withCORS(s.handleAgentRegister))
	mux.HandleFunc("/agent/heartbeat", s.withCORS(s.handleAgentHeartbeat))
	mux.HandleFunc("/agent/poll", s.withCORS(s.handleAgentPoll))
	mux.HandleFunc("/agent/send", s.withCORS(s.handleAgentSend))
	mux.HandleFunc("/controller/list", s.withCORS(s.handleControllerList))
	mux.HandleFunc("/controller/connect", s.withCORS(s.handleControllerConnect))
	mux.HandleFunc("/controller/poll", s.withCORS(s.handleControllerPoll))
	mux.HandleFunc("/controller/send", s.withCORS(s.handleControllerSend))
	mux.HandleFunc("/status", s.withCORS(s.handleStatus))
	mux.HandleFunc("/", s.handleStaticFiles)
	fmt.Printf("[Signaling] Server starting on http://%s\n", s.addr)
	fmt.Printf("[Signaling] Web UI: http://%s/\n", s.addr)
	return http.ListenAndServe(s.addr, mux)
}

func (s *Server) withCORS(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		handler(w, r)
	}
}

func (s *Server) handleStaticFiles(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/agent/") ||
		strings.HasPrefix(r.URL.Path, "/controller/") ||
		strings.HasPrefix(r.URL.Path, "/status") {
		http.NotFound(w, r)
		return
	}
	path := r.URL.Path
	if path == "/" {
		path = "/index.html"
	}
	path = strings.TrimPrefix(path, "/")
	if s.webDir != "" {
		filePath := filepath.Join(s.webDir, path)
		if info, err := os.Stat(filePath); err == nil && !info.IsDir() {
			http.ServeFile(w, r, filePath)
			return
		}
	}
	webPath := "web/static/" + path
	data, err := staticFiles.ReadFile(webPath)
	if err != nil {
		data, err = staticFiles.ReadFile("web/static/index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
		return
	}
	w.Header().Set("Content-Type", getContentType(path))
	w.Write(data)
}

func getContentType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".js":
		return "application/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".ico":
		return "image/x-icon"
	default:
		return "application/octet-stream"
	}
}

func (s *Server) handleAgentRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID        string `json:"id"`
		AuthToken string `json:"auth_token"`
		AgentKey  string `json:"agent_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		http.Error(w, "Missing agent ID", http.StatusBadRequest)
		return
	}
	if s.authToken != "" && req.AuthToken != s.authToken {
		http.Error(w, "Invalid auth token", http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existingAgent, ok := s.agents[req.ID]; ok {
		if existingAgent.AgentKey != "" && existingAgent.AgentKey != req.AgentKey {
			http.Error(w, "Agent ID already registered with different key", http.StatusForbidden)
			return
		}
		close(existingAgent.ControllerCh)
		close(existingAgent.AgentCh)
		delete(s.tokens, existingAgent.Token)
		fmt.Printf("[Signaling] Agent re-registered: %s\n", req.ID)
	}
	token := generateToken()
	agent := &AgentInfo{
		ID:           req.ID,
		Token:        token,
		AgentKey:     req.AgentKey,
		LastSeen:     time.Now(),
		Connected:    false,
		ControllerCh: make(chan *SignalMessage, 10),
		AgentCh:      make(chan *SignalMessage, 10),
	}
	s.agents[req.ID] = agent
	s.tokens[token] = req.ID
	fmt.Printf("[Signaling] Agent registered: %s (token=%s)\n", req.ID, token[:8])
	resp := map[string]string{
		"token":    token,
		"agent_id": req.ID,
	}
	writeJSON(w, resp, http.StatusOK)
}

func (s *Server) handleAgentHeartbeat(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("Authorization")
	if token == "" {
		http.Error(w, "Missing authorization", http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	agentID, ok := s.tokens[token]
	if !ok {
		s.mu.Unlock()
		http.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}
	agent, ok := s.agents[agentID]
	if ok {
		agent.LastSeen = time.Now()
	}
	s.mu.Unlock()
	if !ok {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleAgentPoll(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("Authorization")
	if token == "" {
		http.Error(w, "Missing authorization", http.StatusUnauthorized)
		return
	}
	s.mu.RLock()
	agentID, ok := s.tokens[token]
	if !ok {
		s.mu.RUnlock()
		http.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}
	agent, ok := s.agents[agentID]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}
	ctx := r.Context()
	select {
	case msg := <-agent.ControllerCh:
		writeJSON(w, msg, http.StatusOK)
	case <-ctx.Done():
		w.WriteHeader(http.StatusRequestTimeout)
	case <-time.After(30 * time.Second):
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleAgentSend(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("Authorization")
	if token == "" {
		http.Error(w, "Missing authorization", http.StatusUnauthorized)
		return
	}
	s.mu.RLock()
	agentID, ok := s.tokens[token]
	if !ok {
		s.mu.RUnlock()
		http.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}
	agent, ok := s.agents[agentID]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}
	var msg SignalMessage
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	select {
	case agent.AgentCh <- &msg:
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Channel full", http.StatusServiceUnavailable)
	}
}

func (s *Server) handleControllerList(w http.ResponseWriter, r *http.Request) {
	if s.authToken != "" {
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer "+s.authToken {
			http.Error(w, "Invalid authorization", http.StatusUnauthorized)
			return
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var list []map[string]interface{}
	for id, agent := range s.agents {
		online := time.Since(agent.LastSeen) < 30*time.Second
		list = append(list, map[string]interface{}{
			"id":        id,
			"online":    online,
			"connected": agent.Connected,
		})
	}
	writeJSON(w, list, http.StatusOK)
}

func (s *Server) handleControllerConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.authToken != "" {
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer "+s.authToken {
			http.Error(w, "Invalid authorization", http.StatusUnauthorized)
			return
		}
	}
	var req struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	agent, ok := s.agents[req.AgentID]
	if !ok {
		s.mu.Unlock()
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}
	agent.Connected = true
	s.mu.Unlock()
	fmt.Printf("[Signaling] Controller connected to agent: %s\n", req.AgentID)
	resp := map[string]interface{}{
		"success":  true,
		"agent_id": req.AgentID,
	}
	writeJSON(w, resp, http.StatusOK)
}

func (s *Server) handleControllerPoll(w http.ResponseWriter, r *http.Request) {
	if s.authToken != "" {
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer "+s.authToken {
			http.Error(w, "Invalid authorization", http.StatusUnauthorized)
			return
		}
	}
	agentID := r.URL.Query().Get("agent_id")
	if agentID == "" {
		http.Error(w, "Missing agent_id", http.StatusBadRequest)
		return
	}
	s.mu.RLock()
	agent, ok := s.agents[agentID]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}
	ctx := r.Context()
	select {
	case msg := <-agent.AgentCh:
		writeJSON(w, msg, http.StatusOK)
	case <-ctx.Done():
		w.WriteHeader(http.StatusRequestTimeout)
	case <-time.After(30 * time.Second):
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleControllerSend(w http.ResponseWriter, r *http.Request) {
	if s.authToken != "" {
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer "+s.authToken {
			http.Error(w, "Invalid authorization", http.StatusUnauthorized)
			return
		}
	}
	agentID := r.URL.Query().Get("agent_id")
	if agentID == "" {
		http.Error(w, "Missing agent_id", http.StatusBadRequest)
		return
	}
	s.mu.RLock()
	agent, ok := s.agents[agentID]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}
	var msg SignalMessage
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	select {
	case agent.ControllerCh <- &msg:
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Channel full", http.StatusServiceUnavailable)
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	agentCount := len(s.agents)
	s.mu.RUnlock()
	status := map[string]interface{}{
		"status":      "running",
		"agent_count": agentCount,
	}
	writeJSON(w, status, http.StatusOK)
}

func (s *Server) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.Lock()
		for id, agent := range s.agents {
			if time.Since(agent.LastSeen) > 120*time.Second {
				fmt.Printf("[Signaling] Agent expired: %s\n", id)
				close(agent.ControllerCh)
				close(agent.AgentCh)
				delete(s.tokens, agent.Token)
				delete(s.agents, id)
			}
		}
		s.mu.Unlock()
	}
}

func generateToken() string {
	return fmt.Sprintf("tok_%d", time.Now().UnixNano())
}

func writeJSON(w http.ResponseWriter, v interface{}, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func main() {
	var (
		addr      = flag.String("addr", "0.0.0.0:8443", "Signaling server listen address")
		authToken = flag.String("token", "", "Authentication token (required for agent registration and controller access)")
		webDir    = flag.String("web-dir", "", "External web directory (optional, uses embedded files if not set)")
	)
	flag.Parse()

	server := NewServer(*addr, *authToken, *webDir)
	go server.cleanupLoop()

	fmt.Printf("[Signaling] Starting on http://%s\n", *addr)
	if *authToken != "" {
		fmt.Printf("[Signaling] Authentication enabled\n")
	}
	fmt.Printf("[Signaling] Press Ctrl+C to exit\n")

	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		fmt.Println("\n[Signaling] Shutting down...")
		os.Exit(0)
	}()

	if err := server.Start(); err != nil {
		fmt.Printf("[Signaling] Server error: %v\n", err)
		os.Exit(1)
	}
}
