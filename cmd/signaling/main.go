package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"net/smtp"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
	"webrtc-portmap/pkg/protocol"
)

//go:embed all:web/static
var staticFiles embed.FS

const defaultTenantCode = "convnet"
const defaultTenantName = "convnet"

type AgentInfo struct {
	ID           string    `json:"id"`
	Token        string    `json:"token"`
	AgentKey     string    `json:"-"`
	DisplayName  string    `json:"display_name"`
	TenantCode   string    `json:"tenant_code"`
	OwnerUserID  string    `json:"owner_user_id"`
	ICEServers   []protocol.ICEServerInfo `json:"ice_servers,omitempty"`
	LastSeen     time.Time `json:"last_seen"`
	Connected    bool      `json:"connected"`
	ControllerSessionToken string `json:"-"`
	ControllerUserID       string `json:"-"`
	ControllerUsername     string `json:"-"`
	ControllerKind         string `json:"-"`
	ControllerWSConn       *websocket.Conn `json:"-"`
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
	addr                 string
	authToken            string
	webDir               string
	dataStore            *DataStore
	emailVerifyEnabled   bool
	emailVerifyRequired  bool
	sessionTTL           time.Duration
	smtpHost             string
	smtpPort             int
	smtpUser             string
	smtpPass             string
	smtpFrom             string
	mu                   sync.RWMutex
	agents               map[string]*AgentInfo
	tokens               map[string]string
}

func NewServer(addr, authToken, webDir string, store *DataStore, emailVerifyEnabled, emailVerifyRequired bool, sessionTTL time.Duration, smtpHost string, smtpPort int, smtpUser, smtpPass, smtpFrom string) *Server {
	return &Server{
		addr:                addr,
		authToken:           authToken,
		webDir:              webDir,
		dataStore:           store,
		emailVerifyEnabled:  emailVerifyEnabled,
		emailVerifyRequired: emailVerifyRequired,
		sessionTTL:          sessionTTL,
		smtpHost:            smtpHost,
		smtpPort:            smtpPort,
		smtpUser:            smtpUser,
		smtpPass:            smtpPass,
		smtpFrom:            smtpFrom,
		agents:              make(map[string]*AgentInfo),
		tokens:              make(map[string]string),
	}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/agent/register", s.withCORS(s.handleAgentRegister))
	mux.HandleFunc("/agent/heartbeat", s.withCORS(s.handleAgentHeartbeat))
	mux.HandleFunc("/agent/poll", s.withCORS(s.handleAgentPoll))
	mux.HandleFunc("/agent/send", s.withCORS(s.handleAgentSend))
	mux.HandleFunc("/auth/send-code", s.withCORS(s.handleAuthSendCode))
	mux.HandleFunc("/auth/register", s.withCORS(s.handleAuthRegister))
	mux.HandleFunc("/auth/login", s.withCORS(s.handleAuthLogin))
	mux.HandleFunc("/auth/me", s.withCORS(s.handleAuthMe))
	mux.HandleFunc("/auth/change-password", s.withCORS(s.handleAuthChangePassword))
	mux.HandleFunc("/controller/list", s.withCORS(s.handleControllerList))
	mux.HandleFunc("/controller/agent/delete", s.withCORS(s.handleControllerAgentDelete))
	mux.HandleFunc("/controller/agents/register", s.withCORS(s.handleControllerAgentRegister))
	mux.HandleFunc("/controller/connect", s.withCORS(s.handleControllerConnect))
	mux.HandleFunc("/controller/disconnect", s.withCORS(s.handleControllerDisconnect))
	mux.HandleFunc("/controller/poll", s.withCORS(s.handleControllerPoll))
	mux.HandleFunc("/controller/send", s.withCORS(s.handleControllerSend))
	mux.HandleFunc("/controller/ws", s.handleControllerWS)
	mux.HandleFunc("/client/list", s.withCORS(s.handleClientList))
	mux.HandleFunc("/client/connect", s.withCORS(s.handleClientConnect))
	mux.HandleFunc("/client/disconnect", s.withCORS(s.handleClientDisconnect))
	mux.HandleFunc("/client/ws", s.handleClientWS)
	mux.HandleFunc("/download/agent", s.withCORS(s.handleDownloadAgent))
	mux.HandleFunc("/download/agent/windows", s.withCORS(s.handleDownloadAgentWindows))
	mux.HandleFunc("/download/agent/linux", s.withCORS(s.handleDownloadAgentLinux))
	mux.HandleFunc("/download/agent/mac", s.withCORS(s.handleDownloadAgentMac))
	mux.HandleFunc("/download/client", s.withCORS(s.handleDownloadClient))
	mux.HandleFunc("/download/client/windows", s.withCORS(s.handleDownloadClientWindows))
	mux.HandleFunc("/download/client/linux", s.withCORS(s.handleDownloadClientLinux))
	mux.HandleFunc("/download/client/mac", s.withCORS(s.handleDownloadClientMac))
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
		strings.HasPrefix(r.URL.Path, "/client/") ||
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

func (s *Server) handleDownloadAgent(w http.ResponseWriter, r *http.Request) {
	s.handleBinaryDownload(w, r, "agent", "")
}

func (s *Server) handleDownloadAgentWindows(w http.ResponseWriter, r *http.Request) {
	s.handleBinaryDownload(w, r, "agent", "windows")
}

func (s *Server) handleDownloadAgentLinux(w http.ResponseWriter, r *http.Request) {
	s.handleBinaryDownload(w, r, "agent", "linux")
}

func (s *Server) handleDownloadAgentMac(w http.ResponseWriter, r *http.Request) {
	s.handleBinaryDownload(w, r, "agent", "darwin")
}

func (s *Server) handleDownloadClient(w http.ResponseWriter, r *http.Request) {
	s.handleBinaryDownload(w, r, "client", "")
}

func (s *Server) handleDownloadClientWindows(w http.ResponseWriter, r *http.Request) {
	s.handleBinaryDownload(w, r, "client", "windows")
}

func (s *Server) handleDownloadClientLinux(w http.ResponseWriter, r *http.Request) {
	s.handleBinaryDownload(w, r, "client", "linux")
}

func (s *Server) handleDownloadClientMac(w http.ResponseWriter, r *http.Request) {
	s.handleBinaryDownload(w, r, "client", "darwin")
}

func (s *Server) handleBinaryDownload(w http.ResponseWriter, r *http.Request, name, platform string) {
	candidates := binaryCandidates(name, platform)
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			filename := filepath.Base(candidate)
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
			http.ServeFile(w, r, candidate)
			return
		}
	}
	http.Error(w, fmt.Sprintf("%s binary not found, please build it first", name), http.StatusNotFound)
}

func binaryCandidates(name, platform string) []string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "windows", "win":
		return []string{
			filepath.Join("bin", name+"-windows-amd64.exe"),
			filepath.Join("bin", name+".exe"),
			filepath.Join(".", name+"-windows-amd64.exe"),
			filepath.Join(".", name+".exe"),
		}
	case "linux":
		return []string{
			filepath.Join("bin", name+"-linux-amd64"),
			filepath.Join("bin", name),
			filepath.Join(".", name+"-linux-amd64"),
			filepath.Join(".", name),
		}
	case "darwin", "mac", "macos":
		return []string{
			filepath.Join("bin", name+"-darwin-amd64"),
			filepath.Join("bin", name),
			filepath.Join(".", name+"-darwin-amd64"),
			filepath.Join(".", name),
		}
	default:
		return []string{
			filepath.Join("bin", name+"-windows-amd64.exe"),
			filepath.Join("bin", name+"-linux-amd64"),
			filepath.Join("bin", name+"-darwin-amd64"),
			filepath.Join("bin", name+".exe"),
			filepath.Join("bin", name),
			filepath.Join(".", name+"-windows-amd64.exe"),
			filepath.Join(".", name+"-linux-amd64"),
			filepath.Join(".", name+"-darwin-amd64"),
			filepath.Join(".", name+".exe"),
			filepath.Join(".", name),
		}
	}
}

func bearerToken(r *http.Request) string {
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		return strings.TrimSpace(authHeader[7:])
	}
	return authHeader
}

func (s *Server) requireUser(r *http.Request) (*UserSession, *UserRecord, error) {
	if s.dataStore == nil {
		return nil, nil, fmt.Errorf("data store is not configured")
	}
	token := bearerToken(r)
	if token == "" {
		return nil, nil, fmt.Errorf("missing authorization")
	}
	session, user := s.dataStore.GetSession(token)
	if session == nil || user == nil {
		return nil, nil, fmt.Errorf("invalid session")
	}
	return session, user, nil
}

func (s *Server) sendVerificationEmail(email, code string) error {
	if s.smtpHost == "" || s.smtpFrom == "" {
		fmt.Printf("[Signaling][Email] verification code for %s: %s\n", email, code)
		return nil
	}
	addr := fmt.Sprintf("%s:%d", s.smtpHost, s.smtpPort)
	body := strings.Join([]string{
		fmt.Sprintf("To: %s", email),
		fmt.Sprintf("From: %s", s.smtpFrom),
		"Subject: WebRTC PortMap verification code",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		fmt.Sprintf("Your verification code is: %s", code),
		"Code expires in 10 minutes.",
	}, "\r\n")
	var auth smtp.Auth
	if s.smtpUser != "" {
		auth = smtp.PlainAuth("", s.smtpUser, s.smtpPass, s.smtpHost)
	}
	return smtp.SendMail(addr, auth, s.smtpFrom, []string{email}, []byte(body))
}

func (s *Server) handleAuthSendCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.emailVerifyEnabled {
		http.Error(w, "email verification is disabled", http.StatusBadRequest)
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	email := normalizeEmail(req.Email)
	if email == "" {
		http.Error(w, "email is required", http.StatusBadRequest)
		return
	}
	code := randomCode(6)
	expiresAt := time.Now().Add(10 * time.Minute)
	if err := s.dataStore.SaveVerificationCode(email, code, expiresAt); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.sendVerificationEmail(email, code); err != nil {
		http.Error(w, fmt.Sprintf("failed to send verification email: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{
		"success":    true,
		"expires_at": expiresAt,
		"required":   s.emailVerifyRequired,
	}, http.StatusOK)
}

func (s *Server) handleAuthRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		TenantCode       string `json:"tenant_code"`
		TenantName       string `json:"tenant_name"`
		Username         string `json:"username"`
		Email            string `json:"email"`
		Password         string `json:"password"`
		VerificationCode string `json:"verification_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.TenantCode) == "" {
		req.TenantCode = defaultTenantCode
	}
	if strings.TrimSpace(req.TenantName) == "" {
		req.TenantName = defaultTenantName
	}
	emailVerified := false
	if s.emailVerifyEnabled {
		if strings.TrimSpace(req.VerificationCode) != "" {
			if err := s.dataStore.VerifyEmailCode(req.Email, req.VerificationCode); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			emailVerified = true
		} else if s.emailVerifyRequired {
			http.Error(w, "verification_code is required", http.StatusBadRequest)
			return
		}
	}
	user, err := s.dataStore.RegisterUser(req.TenantCode, req.TenantName, req.Username, req.Email, req.Password, emailVerified)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]interface{}{
		"success":         true,
		"user_id":         user.ID,
		"user_hash":       user.UserHash,
		"tenant_code":     user.TenantCode,
		"username":        user.Username,
		"email_verified":  user.EmailVerified,
		"email_required":  s.emailVerifyRequired,
		"email_enabled":   s.emailVerifyEnabled,
	}, http.StatusOK)
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		TenantCode string `json:"tenant_code"`
		Username   string `json:"username"`
		Password   string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.TenantCode) == "" {
		req.TenantCode = defaultTenantCode
	}
	user := s.dataStore.FindUser(req.TenantCode, req.Username)
	if user == nil || !verifyPasswordHash(user.PasswordSalt, user.PasswordHash, req.Password) {
		http.Error(w, "invalid username or password", http.StatusUnauthorized)
		return
	}
	if s.emailVerifyRequired && !user.EmailVerified {
		http.Error(w, "email verification required", http.StatusForbidden)
		return
	}
	session, err := s.dataStore.CreateSession(user, s.sessionTTL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{
		"success":      true,
		"token":        session.Token,
		"user_hash":    user.UserHash,
		"tenant_code":  user.TenantCode,
		"username":     user.Username,
		"email":        user.Email,
		"expires_at":   session.ExpiresAt,
	}, http.StatusOK)
}

func (s *Server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	session, user, err := s.requireUser(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	writeJSON(w, map[string]interface{}{
		"user_id":        user.ID,
		"user_hash":      user.UserHash,
		"tenant_code":    user.TenantCode,
		"username":       user.Username,
		"email":          user.Email,
		"email_verified": user.EmailVerified,
		"expires_at":     session.ExpiresAt,
	}, http.StatusOK)
}

func (s *Server) handleAuthChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, user, err := s.requireUser(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.OldPassword) == "" || strings.TrimSpace(req.NewPassword) == "" {
		http.Error(w, "old_password and new_password are required", http.StatusBadRequest)
		return
	}
	if err := s.dataStore.ChangeUserPassword(user.ID, req.OldPassword, req.NewPassword); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]interface{}{
		"success": true,
		"message": "password updated",
	}, http.StatusOK)
}

func (s *Server) handleAgentRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID          string `json:"id"`
		AuthToken   string `json:"auth_token"`
		OwnerHash   string `json:"owner_hash"`
		DisplayName string `json:"display_name"`
		Description string `json:"description"`
		ICEServers  []protocol.ICEServerInfo `json:"ice_servers"`
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
	if s.dataStore == nil {
		http.Error(w, "Agent registration storage not configured", http.StatusInternalServerError)
		return
	}
	if strings.TrimSpace(req.OwnerHash) == "" {
		http.Error(w, "Missing owner_hash", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.DisplayName) == "" {
		req.DisplayName = req.ID
	}
	registration, err := s.dataStore.UpsertAgentByUserHash(req.OwnerHash, req.ID, req.DisplayName, req.Description)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existingAgent, ok := s.agents[req.ID]; ok {
		close(existingAgent.ControllerCh)
		close(existingAgent.AgentCh)
		delete(s.tokens, existingAgent.Token)
		fmt.Printf("[Signaling] Agent re-registered: %s\n", req.ID)
	}
	token := generateToken()
	agent := &AgentInfo{
		ID:           req.ID,
		Token:        token,
		DisplayName:  registration.DisplayName,
		TenantCode:   registration.TenantCode,
		OwnerUserID:  registration.OwnerUserID,
		ICEServers:   req.ICEServers,
		LastSeen:     time.Now(),
		Connected:    false,
		ControllerCh: make(chan *SignalMessage, 10),
		AgentCh:      make(chan *SignalMessage, 10),
	}
	s.agents[req.ID] = agent
	s.tokens[token] = req.ID
	fmt.Printf("[Signaling] Agent registered: %s (token=%s)\n", req.ID, token[:8])
	resp := map[string]string{
		"token":        token,
		"agent_id":     req.ID,
		"display_name": registration.DisplayName,
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
	s.handleOwnedAgentList(w, r)
}

func (s *Server) handleControllerAgentDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	session, user, err := s.requireUser(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	var req struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if err := s.dataStore.DeleteUserAgent(user.ID, req.AgentID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var (
		agent *AgentInfo
		ok    bool
	)
	s.mu.Lock()
	agent, ok = s.agents[req.AgentID]
	if ok {
		delete(s.tokens, agent.Token)
		delete(s.agents, req.AgentID)
	}
	s.mu.Unlock()

	if ok {
		if agent.ControllerWSConn != nil {
			_ = agent.ControllerWSConn.Close()
		}
		select {
		case agent.ControllerCh <- &SignalMessage{Type: "disconnect"}:
		default:
		}
		close(agent.ControllerCh)
		close(agent.AgentCh)
		fmt.Printf("[Signaling] Agent deleted by user %s: %s (session=%s)\n", user.Username, req.AgentID, session.Token)
	} else {
		fmt.Printf("[Signaling] Agent deleted by user %s: %s\n", user.Username, req.AgentID)
	}

	writeJSON(w, map[string]interface{}{
		"success":  true,
		"agent_id": req.AgentID,
	}, http.StatusOK)
}

func (s *Server) handleClientList(w http.ResponseWriter, r *http.Request) {
	s.handleOwnedAgentList(w, r)
}

func (s *Server) handleOwnedAgentList(w http.ResponseWriter, r *http.Request) {
	_, user, err := s.requireUser(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	ownedAgents := s.dataStore.ListUserAgents(user.ID)
	s.mu.RLock()
	var list []map[string]interface{}
	for _, record := range ownedAgents {
		agent := s.agents[record.AgentID]
		online := agent != nil && time.Since(agent.LastSeen) < 30*time.Second
		connected := agent != nil && agent.Connected
		list = append(list, map[string]interface{}{
			"id":           record.AgentID,
			"display_name": record.DisplayName,
			"description":  record.Description,
			"ice_servers":  func() []protocol.ICEServerInfo {
				if agent == nil {
					return nil
				}
				return agent.ICEServers
			}(),
			"online":       online,
			"connected":    connected,
		})
	}
	s.mu.RUnlock()
	writeJSON(w, list, http.StatusOK)
}

func (s *Server) handleControllerConnect(w http.ResponseWriter, r *http.Request) {
	s.handleOwnedAgentConnect(w, r, "web")
}

func (s *Server) handleClientConnect(w http.ResponseWriter, r *http.Request) {
	s.handleOwnedAgentConnect(w, r, "client")
}

func (s *Server) handleControllerDisconnect(w http.ResponseWriter, r *http.Request) {
	s.handleOwnedAgentDisconnect(w, r, "web")
}

func (s *Server) handleClientDisconnect(w http.ResponseWriter, r *http.Request) {
	s.handleOwnedAgentDisconnect(w, r, "client")
}

func (s *Server) handleOwnedAgentConnect(w http.ResponseWriter, r *http.Request, kind string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	session, user, err := s.requireUser(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	var req struct {
		AgentID string `json:"agent_id"`
		Force   bool   `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	registration := s.dataStore.GetAgent(req.AgentID)
	if registration == nil || registration.OwnerUserID != user.ID {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}

	var (
		prevWS   *websocket.Conn
		takeover bool
		busyInfo map[string]interface{}
	)
	s.mu.Lock()
	agent, ok := s.agents[req.AgentID]
	if !ok {
		s.mu.Unlock()
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}
	if agent.ControllerSessionToken != "" && agent.ControllerSessionToken != session.Token {
		if !req.Force {
			busyInfo = map[string]interface{}{
				"busy":            true,
				"agent_id":        req.AgentID,
				"controller_user": agent.ControllerUsername,
				"controller_kind": agent.ControllerKind,
			}
			s.mu.Unlock()
			writeJSON(w, busyInfo, http.StatusConflict)
			return
		}
		takeover = true
		prevWS = agent.ControllerWSConn
	}
	agent.Connected = true
	agent.ControllerSessionToken = session.Token
	agent.ControllerUserID = user.ID
	agent.ControllerUsername = user.Username
	agent.ControllerKind = kind
	agent.ControllerWSConn = nil
	s.mu.Unlock()

	if takeover {
		fmt.Printf("[Signaling] Forcing takeover of agent %s by %s (%s)\n", req.AgentID, user.Username, kind)
		if prevWS != nil {
			_ = prevWS.Close()
		}
		select {
		case agent.ControllerCh <- &SignalMessage{Type: "disconnect"}:
		default:
			fmt.Printf("[Signaling] Agent %s control channel busy, disconnect signal dropped\n", req.AgentID)
		}
	}
	fmt.Printf("[Signaling] Controller connected to agent: %s\n", req.AgentID)
	resp := map[string]interface{}{
		"success":  true,
		"agent_id": req.AgentID,
		"takeover": takeover,
	}
	writeJSON(w, resp, http.StatusOK)
}

func (s *Server) handleOwnedAgentDisconnect(w http.ResponseWriter, r *http.Request, kind string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	session, user, err := s.requireUser(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	var req struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	registration := s.dataStore.GetAgent(req.AgentID)
	if registration == nil || registration.OwnerUserID != user.ID {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}

	var notify bool
	s.mu.Lock()
	agent, ok := s.agents[req.AgentID]
	if ok && agent.ControllerSessionToken == session.Token && agent.ControllerKind == kind {
		agent.Connected = false
		agent.ControllerSessionToken = ""
		agent.ControllerUserID = ""
		agent.ControllerUsername = ""
		agent.ControllerKind = ""
		agent.ControllerWSConn = nil
		notify = true
	}
	s.mu.Unlock()

	if notify && ok {
		select {
		case agent.ControllerCh <- &SignalMessage{Type: "disconnect"}:
		default:
			fmt.Printf("[Signaling] Agent %s control channel busy, disconnect signal dropped\n", req.AgentID)
		}
	}
	writeJSON(w, map[string]interface{}{"success": true, "agent_id": req.AgentID}, http.StatusOK)
}

func (s *Server) handleControllerPoll(w http.ResponseWriter, r *http.Request) {
	session, user, err := s.requireUser(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	agentID := r.URL.Query().Get("agent_id")
	if agentID == "" {
		http.Error(w, "Missing agent_id", http.StatusBadRequest)
		return
	}
	registration := s.dataStore.GetAgent(agentID)
	if registration == nil || registration.OwnerUserID != user.ID {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}
	s.mu.RLock()
	agent, ok := s.agents[agentID]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}
	if agent.ControllerSessionToken != session.Token {
		http.Error(w, "Agent is being used by another session", http.StatusConflict)
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
	session, user, err := s.requireUser(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	agentID := r.URL.Query().Get("agent_id")
	if agentID == "" {
		http.Error(w, "Missing agent_id", http.StatusBadRequest)
		return
	}
	registration := s.dataStore.GetAgent(agentID)
	if registration == nil || registration.OwnerUserID != user.ID {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}
	s.mu.RLock()
	agent, ok := s.agents[agentID]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}
	if agent.ControllerSessionToken != session.Token {
		http.Error(w, "Agent is being used by another session", http.StatusConflict)
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

func (s *Server) handleControllerWS(w http.ResponseWriter, r *http.Request) {
	s.handleOwnedAgentWS(w, r, "web")
}

func (s *Server) handleClientWS(w http.ResponseWriter, r *http.Request) {
	s.handleOwnedAgentWS(w, r, "client")
}

func (s *Server) handleOwnedAgentWS(w http.ResponseWriter, r *http.Request, kind string) {
	session, user, err := s.requireUser(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	agentID := r.URL.Query().Get("agent_id")
	if agentID == "" {
		http.Error(w, "Missing agent_id", http.StatusBadRequest)
		return
	}
	registration := s.dataStore.GetAgent(agentID)
	if registration == nil || registration.OwnerUserID != user.ID {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}
	s.mu.RLock()
	agent, ok := s.agents[agentID]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}
	if agent.ControllerSessionToken != session.Token {
		http.Error(w, "Agent is being used by another session", http.StatusConflict)
		return
	}

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	s.mu.Lock()
	agent.Connected = true
	agent.ControllerSessionToken = session.Token
	agent.ControllerUserID = user.ID
	agent.ControllerUsername = user.Username
	agent.ControllerKind = kind
	agent.ControllerWSConn = conn
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if current, exists := s.agents[agentID]; exists && current == agent && current.ControllerSessionToken == session.Token && current.ControllerWSConn == conn {
			current.Connected = false
			current.ControllerSessionToken = ""
			current.ControllerUserID = ""
			current.ControllerUsername = ""
			current.ControllerKind = ""
			current.ControllerWSConn = nil
		}
		s.mu.Unlock()
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg SignalMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			select {
			case agent.ControllerCh <- &msg:
			default:
			}
		}
	}()

	for {
		select {
		case <-done:
			return
		case msg := <-agent.AgentCh:
			if err := conn.WriteJSON(msg); err != nil {
				return
			}
		}
	}
}

func (s *Server) handleControllerAgentRegister(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "manual agent registration is no longer required", http.StatusGone)
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
		if s.dataStore != nil {
			s.dataStore.mu.Lock()
			now := time.Now()
			for token, session := range s.dataStore.data.Sessions {
				if now.After(session.ExpiresAt) {
					delete(s.dataStore.data.Sessions, token)
				}
			}
			for email, code := range s.dataStore.data.VerificationCodes {
				if now.After(code.ExpiresAt) {
					delete(s.dataStore.data.VerificationCodes, email)
				}
			}
			_ = s.dataStore.saveLocked()
			s.dataStore.mu.Unlock()
		}
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
		addr                 = flag.String("addr", "0.0.0.0:8443", "Signaling server listen address")
		authToken            = flag.String("token", "", "Optional server auth token for agent registration")
		webDir               = flag.String("web-dir", "", "External web directory (optional, uses embedded files if not set)")
		dataFile             = flag.String("data", "data/signaling.json", "Persistent data file for tenants/users/agents")
		emailVerifyEnabled   = flag.Bool("email-verify-enabled", true, "Enable email verification flow")
		emailVerifyRequired  = flag.Bool("email-verify-required", false, "Require verified email before login")
		sessionTTL           = flag.Duration("session-ttl", 24*time.Hour, "Controller login session TTL")
		smtpHost             = flag.String("smtp-host", "", "SMTP host for verification email (optional)")
		smtpPort             = flag.Int("smtp-port", 587, "SMTP port")
		smtpUser             = flag.String("smtp-user", "", "SMTP username")
		smtpPass             = flag.String("smtp-pass", "", "SMTP password")
		smtpFrom             = flag.String("smtp-from", "", "SMTP from address")
	)
	flag.Parse()

	store, err := NewDataStore(*dataFile)
	if err != nil {
		fmt.Printf("[Signaling] Failed to open data store: %v\n", err)
		os.Exit(1)
	}
	server := NewServer(*addr, *authToken, *webDir, store, *emailVerifyEnabled, *emailVerifyRequired, *sessionTTL, *smtpHost, *smtpPort, *smtpUser, *smtpPass, *smtpFrom)
	go server.cleanupLoop()

	fmt.Printf("[Signaling] Starting on http://%s\n", *addr)
	if *authToken != "" {
		fmt.Printf("[Signaling] Agent registration token enabled\n")
	}
	fmt.Printf("[Signaling] Data file: %s\n", *dataFile)
	fmt.Printf("[Signaling] Email verification enabled=%v required=%v\n", *emailVerifyEnabled, *emailVerifyRequired)
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
