package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"net/smtp"
	"net/http"
	"net/url"
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

	turn TURNConfig // 内嵌 TURN 中转配置（含临时凭据签发参数）
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
	mux.HandleFunc("/agent/turn-credentials", s.withCORS(s.handleAgentTurnCredentials))
	mux.HandleFunc("/me/quota", s.withCORS(s.handleMyQuota))
	mux.HandleFunc("/admin/users", s.withCORS(s.handleAdminUsers))
	mux.HandleFunc("/admin/users/quota", s.withCORS(s.handleAdminSetQuota))
	mux.HandleFunc("/admin/users/reset-usage", s.withCORS(s.handleAdminResetUsage))
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
	mux.HandleFunc("/webconsole", s.handleWebConsoleRoot)
	mux.HandleFunc("/webconsole/", s.handleStaticFiles)
	mux.HandleFunc("/proxyservice", s.handleProxyServiceRoot)
	mux.HandleFunc("/proxyservice/", s.handleStaticFiles)
	mux.HandleFunc("/", s.handleRoot)
	fmt.Printf("[Signaling] Server starting on http://%s\n", s.addr)
	fmt.Printf("[Signaling] Web UI: http://%s/webconsole/\n", s.addr)
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

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		http.Redirect(w, r, "/webconsole/", http.StatusFound)
		return
	}
	if r.Method == http.MethodGet && !isBuiltInPath(r.URL.Path) {
		s.renderWebConsoleBounce(w, r)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) handleWebConsoleRoot(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/webconsole/", http.StatusFound)
}

func (s *Server) handleProxyServiceRoot(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/proxyservice/", http.StatusFound)
}

func isBuiltInPath(path string) bool {
	return strings.HasPrefix(path, "/agent/") ||
		strings.HasPrefix(path, "/auth/") ||
		strings.HasPrefix(path, "/controller/") ||
		strings.HasPrefix(path, "/client/") ||
		strings.HasPrefix(path, "/download/") ||
		strings.HasPrefix(path, "/status") ||
		strings.HasPrefix(path, "/webconsole/") ||
		path == "/webconsole" ||
		strings.HasPrefix(path, "/proxyservice/") ||
		path == "/proxyservice"
}

func (s *Server) renderWebConsoleBounce(w http.ResponseWriter, r *http.Request) {
	target := r.URL.RequestURI()
	http.Redirect(w, r, "/proxyservice/?bounce="+url.QueryEscape(target), http.StatusFound)
}

func (s *Server) handleStaticFiles(w http.ResponseWriter, r *http.Request) {
	var path string
	switch {
	case strings.HasPrefix(r.URL.Path, "/webconsole/"):
		path = strings.TrimPrefix(r.URL.Path, "/webconsole")
	case strings.HasPrefix(r.URL.Path, "/proxyservice/"):
		path = strings.TrimPrefix(r.URL.Path, "/proxyservice")
	default:
		http.NotFound(w, r)
		return
	}
	if path == "" || path == "/" {
		path = "/index.html"
	}
	path = strings.TrimPrefix(path, "/")
	// 缓存策略：vendor/（xterm 等第三方，极少变）可长缓存；
	// 应用文件（index.html / controller.js / css）强制每次重验证，避免改了前端浏览器仍用旧缓存。
	if strings.HasPrefix(path, "vendor/") {
		w.Header().Set("Cache-Control", "public, max-age=604800")
	} else {
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	}
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
		http.NotFound(w, r)
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
	// 各平台的文件名候选（与 bin/ 构建产物对齐：优先精确平台名，再退回无后缀名）。
	var files []string
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "windows", "win":
		files = []string{name + "-windows-amd64.exe", name + ".exe"}
	case "linux":
		files = []string{name + "-linux-amd64", name}
	case "darwin", "mac", "macos":
		files = []string{name + "-darwin-amd64", name}
	default:
		files = []string{
			name + "-windows-amd64.exe", name + "-linux-amd64", name + "-darwin-amd64",
			name + ".exe", name,
		}
	}
	// 搜索目录：当前工作目录的 bin/ 与 .；以及 signaling 可执行文件所在目录的 bin/ 与其自身
	// （便于 systemd 等部署场景把下载用二进制放在 /usr/local/bin/ 旁边）。
	dirs := []string{"bin", "."}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		dirs = append(dirs, filepath.Join(exeDir, "bin"), exeDir)
	}
	out := make([]string, 0, len(dirs)*len(files))
	for _, d := range dirs {
		for _, f := range files {
			out = append(out, filepath.Join(d, f))
		}
	}
	return out
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

// requireAdmin 在 requireUser 基础上要求 IsAdmin。
func (s *Server) requireAdmin(r *http.Request) (*UserSession, *UserRecord, error) {
	session, user, err := s.requireUser(r)
	if err != nil {
		return nil, nil, err
	}
	if !user.IsAdmin {
		return nil, nil, fmt.Errorf("admin role required")
	}
	return session, user, nil
}

// adminConfigFile 是 -admin-config 的 JSON 结构。
type adminConfigFile struct {
	Admins []string `json:"admins"`
}

// loadAdminConfig 读取管理员配置文件，返回 admin 键列表（"user" 或 "tenant:user"）。
func loadAdminConfig(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg adminConfigFile
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse admin config failed: %w", err)
	}
	return cfg.Admins, nil
}

// handleMyQuota GET /me/quota —— 普通登录用户查看自己的中转额度与本月用量。
func (s *Server) handleMyQuota(w http.ResponseWriter, r *http.Request) {
	_, user, err := s.requireUser(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	q := s.dataStore.UserQuota(user.ID)
	writeJSON(w, map[string]interface{}{
		"turn_enabled":        s.turn.Enabled,
		"max_bps":             q.MaxBps,
		"monthly_quota_bytes": q.MonthlyQuotaBytes,
		"used_bytes":          q.UsedBytes,
		"quota_month":         currentMonth(),
		"exhausted":           q.Exhausted,
	}, http.StatusOK)
}

// handleAdminUsers GET /admin/users —— 列出本租户用户及其额度/用量。
func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	_, user, err := s.requireAdmin(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	users := s.dataStore.ListUsers(user.TenantCode)
	list := make([]map[string]interface{}, 0, len(users))
	for _, u := range users {
		q := s.dataStore.UserQuota(u.ID)
		list = append(list, map[string]interface{}{
			"user_id":             u.ID,
			"username":            u.Username,
			"email":               u.Email,
			"is_admin":            u.IsAdmin,
			"max_bps":             u.MaxBps,
			"monthly_quota_bytes": u.MonthlyQuotaBytes,
			"used_bytes":          q.UsedBytes,
			"quota_month":         currentMonth(),
			"exhausted":           q.Exhausted,
		})
	}
	writeJSON(w, map[string]interface{}{"users": list, "turn_enabled": s.turn.Enabled}, http.StatusOK)
}

// handleAdminSetQuota POST /admin/users/quota {user_id, max_bps, monthly_quota_bytes}
func (s *Server) handleAdminSetQuota(w http.ResponseWriter, r *http.Request) {
	_, admin, err := s.requireAdmin(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	var req struct {
		UserID            string `json:"user_id"`
		MaxBps            int64  `json:"max_bps"`
		MonthlyQuotaBytes int64  `json:"monthly_quota_bytes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	target := s.dataStore.GetUserByID(req.UserID)
	if target == nil || target.TenantCode != admin.TenantCode {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	if err := s.dataStore.SetUserQuota(req.UserID, req.MaxBps, req.MonthlyQuotaBytes); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"success": true}, http.StatusOK)
}

// handleAdminResetUsage POST /admin/users/reset-usage {user_id}
func (s *Server) handleAdminResetUsage(w http.ResponseWriter, r *http.Request) {
	_, admin, err := s.requireAdmin(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	var req struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	target := s.dataStore.GetUserByID(req.UserID)
	if target == nil || target.TenantCode != admin.TenantCode {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	if err := s.dataStore.ResetUserUsage(req.UserID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"success": true}, http.StatusOK)
}

// handleAgentTurnCredentials —— agent 用其 token 拉取内嵌 TURN 临时凭据（归属到 owner 用户）。
func (s *Server) handleAgentTurnCredentials(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("Authorization")
	if token == "" {
		http.Error(w, "Missing authorization", http.StatusUnauthorized)
		return
	}
	s.mu.RLock()
	agentID, ok := s.tokens[token]
	var ownerID string
	if ok {
		if agent := s.agents[agentID]; agent != nil {
			ownerID = agent.OwnerUserID
		}
	}
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}
	if !s.turn.Enabled {
		writeJSON(w, map[string]interface{}{"turn_enabled": false}, http.StatusOK)
		return
	}
	username, credential, urls, issued := s.issueTURNCredentials(ownerID)
	if !issued {
		writeJSON(w, map[string]interface{}{"turn_enabled": false}, http.StatusOK)
		return
	}
	writeJSON(w, map[string]interface{}{
		"turn_enabled": true,
		"urls":         urls,
		"username":     username,
		"credential":   credential,
		"ttl_seconds":  int(s.turn.TTL.Seconds()),
	}, http.StatusOK)
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
		"is_admin":     user.IsAdmin,
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
		"is_admin":       user.IsAdmin,
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
				var base []protocol.ICEServerInfo
				if agent != nil {
					base = agent.ICEServers
				}
				// 注入内嵌 TURN（带当前用户临时凭据），让直连失败时可回退中转。
				return s.iceServersForUser(user.ID, base)
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
		adminConfig          = flag.String("admin-config", "", "Admin config JSON file ({\"admins\":[\"user\",\"tenant:user\"]}); listed users become admins")
		turnEnabled          = flag.Bool("turn-enabled", false, "Enable embedded TURN relay server")
		turnPublicIP         = flag.String("turn-public-ip", "", "TURN relay public IP (client-reachable); required when -turn-enabled")
		turnPort             = flag.Int("turn-port", 3478, "TURN server listen port (udp+tcp)")
		turnListen           = flag.String("turn-listen", "0.0.0.0", "TURN server bind address")
		turnRealm            = flag.String("turn-realm", "webrtc-portmap", "TURN realm")
		turnSecret           = flag.String("turn-secret", "", "TURN REST shared secret (auto-generated if empty)")
		turnTTL              = flag.Duration("turn-ttl", 12*time.Hour, "TURN ephemeral credential TTL")
	)
	flag.Parse()

	store, err := NewDataStore(*dataFile)
	if err != nil {
		fmt.Printf("[Signaling] Failed to open data store: %v\n", err)
		os.Exit(1)
	}

	// 应用管理员配置：把配置文件列出的用户标记为 admin（其余清除 admin）。
	if *adminConfig != "" {
		admins, err := loadAdminConfig(*adminConfig)
		if err != nil {
			fmt.Printf("[Signaling] Failed to load admin config: %v\n", err)
			os.Exit(1)
		}
		matched := store.ApplyAdmins(admins, defaultTenantCode)
		fmt.Printf("[Signaling] Admin config: %d configured, %d matched existing users: %v\n", len(admins), len(matched), matched)
	}

	server := NewServer(*addr, *authToken, *webDir, store, *emailVerifyEnabled, *emailVerifyRequired, *sessionTTL, *smtpHost, *smtpPort, *smtpUser, *smtpPass, *smtpFrom)
	go server.cleanupLoop()

	// 内嵌 TURN 中转服务（可选）。
	if *turnEnabled {
		secret := strings.TrimSpace(*turnSecret)
		if secret == "" {
			secret = randomHex(24)
			fmt.Printf("[Signaling] TURN secret auto-generated (set -turn-secret to persist across restarts)\n")
		}
		server.turn = TURNConfig{
			Enabled:    true,
			PublicIP:   *turnPublicIP,
			ListenAddr: *turnListen,
			Port:       *turnPort,
			Realm:      *turnRealm,
			Secret:     secret,
			TTL:        *turnTTL,
		}
		turnSvc, err := startTURNServer(store, server.turn)
		if err != nil {
			fmt.Printf("[Signaling] TURN server failed to start: %v\n", err)
			os.Exit(1)
		}
		defer turnSvc.Close()
	} else {
		fmt.Printf("[Signaling] Embedded TURN: disabled (use -turn-enabled -turn-public-ip <IP> to enable relay)\n")
	}

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
