package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type TenantRecord struct {
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type UserRecord struct {
	ID             string    `json:"id"`
	UserHash       string    `json:"user_hash"`
	TenantCode     string    `json:"tenant_code"`
	Username       string    `json:"username"`
	Email          string    `json:"email"`
	PasswordSalt   string    `json:"password_salt"`
	PasswordHash   string    `json:"password_hash"`
	EmailVerified  bool      `json:"email_verified"`
	CreatedAt      time.Time `json:"created_at"`
	LastLoginAt    time.Time `json:"last_login_at"`
}

type AgentRegistration struct {
	AgentID       string    `json:"agent_id"`
	TenantCode    string    `json:"tenant_code"`
	OwnerUserID   string    `json:"owner_user_id"`
	OwnerUserHash string    `json:"owner_user_hash"`
	DisplayName   string    `json:"display_name"`
	Description   string    `json:"description"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type VerificationCode struct {
	Email     string    `json:"email"`
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
	Verified  bool      `json:"verified"`
}

type UserSession struct {
	Token      string    `json:"token"`
	UserID     string    `json:"user_id"`
	TenantCode string    `json:"tenant_code"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type PersistentState struct {
	Tenants           map[string]*TenantRecord      `json:"tenants"`
	Users             map[string]*UserRecord        `json:"users"`
	UsersByTenantName map[string]string             `json:"users_by_tenant_name"`
	UsersByHash       map[string]string             `json:"users_by_hash"`
	Agents            map[string]*AgentRegistration `json:"agents"`
	VerificationCodes map[string]*VerificationCode  `json:"verification_codes"`
	Sessions          map[string]*UserSession       `json:"sessions"`
}

type DataStore struct {
	path string
	mu   sync.RWMutex
	data *PersistentState
}

func NewDataStore(path string) (*DataStore, error) {
	ds := &DataStore{
		path: path,
		data: &PersistentState{
			Tenants:           map[string]*TenantRecord{},
			Users:             map[string]*UserRecord{},
			UsersByTenantName: map[string]string{},
			UsersByHash:       map[string]string{},
			Agents:            map[string]*AgentRegistration{},
			VerificationCodes: map[string]*VerificationCode{},
			Sessions:          map[string]*UserSession{},
		},
	}
	if path == "" {
		return ds, nil
	}
	if err := ds.load(); err != nil {
		return nil, err
	}
	return ds, nil
}

func (ds *DataStore) load() error {
	if ds.path == "" {
		return nil
	}
	raw, err := os.ReadFile(ds.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	var state PersistentState
	if err := json.Unmarshal(raw, &state); err != nil {
		return err
	}
	if state.Tenants == nil {
		state.Tenants = map[string]*TenantRecord{}
	}
	if state.Users == nil {
		state.Users = map[string]*UserRecord{}
	}
	if state.UsersByTenantName == nil {
		state.UsersByTenantName = map[string]string{}
	}
	if state.UsersByHash == nil {
		state.UsersByHash = map[string]string{}
	}
	if state.Agents == nil {
		state.Agents = map[string]*AgentRegistration{}
	}
	if state.VerificationCodes == nil {
		state.VerificationCodes = map[string]*VerificationCode{}
	}
	if state.Sessions == nil {
		state.Sessions = map[string]*UserSession{}
	}
	changed := false
	rebuildUserHashIndex := len(state.UsersByHash) == 0
	for userID, user := range state.Users {
		if user == nil {
			continue
		}
		if strings.TrimSpace(user.UserHash) == "" {
			user.UserHash = randomToken("uh")
			changed = true
		}
		if rebuildUserHashIndex || state.UsersByHash[user.UserHash] == "" {
			state.UsersByHash[user.UserHash] = userID
			changed = true
		}
	}

	ds.data = &state
	if changed {
		return ds.saveLocked()
	}
	return nil
}

func (ds *DataStore) saveLocked() error {
	if ds.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(ds.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(ds.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ds.path, raw, 0o600)
}

func normalizeTenantCode(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeUsername(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func tenantUserKey(tenantCode, username string) string {
	return normalizeTenantCode(tenantCode) + ":" + normalizeUsername(username)
}

func randomHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func hashWithSalt(salt, plain string) string {
	sum := sha256.Sum256([]byte(salt + "|" + plain))
	return hex.EncodeToString(sum[:])
}

func newPasswordHash(plain string) (string, string) {
	salt := randomHex(16)
	return salt, hashWithSalt(salt, plain)
}

func verifyPasswordHash(salt, hash, plain string) bool {
	return hashWithSalt(salt, plain) == hash
}

func randomCode(length int) string {
	var b strings.Builder
	for i := 0; i < length; i++ {
		n, _ := rand.Int(rand.Reader, big.NewInt(10))
		b.WriteByte(byte('0' + n.Int64()))
	}
	return b.String()
}

func randomToken(prefix string) string {
	return fmt.Sprintf("%s_%s", prefix, randomHex(16))
}

func (ds *DataStore) RegisterUser(tenantCode, tenantName, username, email, password string, emailVerified bool) (*UserRecord, error) {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	tenantCode = normalizeTenantCode(tenantCode)
	username = normalizeUsername(username)
	email = normalizeEmail(email)

	if tenantCode == "" || username == "" || password == "" {
		return nil, fmt.Errorf("tenant_code, username and password are required")
	}
	key := tenantUserKey(tenantCode, username)
	if _, exists := ds.data.UsersByTenantName[key]; exists {
		return nil, fmt.Errorf("user already exists")
	}
	if _, exists := ds.data.Tenants[tenantCode]; !exists {
		ds.data.Tenants[tenantCode] = &TenantRecord{
			Code:      tenantCode,
			Name:      strings.TrimSpace(tenantName),
			CreatedAt: time.Now(),
		}
	}

	salt, hash := newPasswordHash(password)
	user := &UserRecord{
		ID:            randomToken("usr"),
		UserHash:      randomToken("uh"),
		TenantCode:    tenantCode,
		Username:      username,
		Email:         email,
		PasswordSalt:  salt,
		PasswordHash:  hash,
		EmailVerified: emailVerified,
		CreatedAt:     time.Now(),
	}
	ds.data.Users[user.ID] = user
	ds.data.UsersByTenantName[key] = user.ID
	ds.data.UsersByHash[user.UserHash] = user.ID
	if err := ds.saveLocked(); err != nil {
		return nil, err
	}
	return user, nil
}

func (ds *DataStore) FindUser(tenantCode, username string) *UserRecord {
	ds.mu.RLock()
	defer ds.mu.RUnlock()
	userID := ds.data.UsersByTenantName[tenantUserKey(tenantCode, username)]
	if userID == "" {
		return nil
	}
	return ds.data.Users[userID]
}

func (ds *DataStore) FindUserByHash(userHash string) *UserRecord {
	ds.mu.RLock()
	defer ds.mu.RUnlock()
	userID := ds.data.UsersByHash[strings.TrimSpace(userHash)]
	if userID == "" {
		return nil
	}
	return ds.data.Users[userID]
}

func (ds *DataStore) MarkEmailVerified(email string) error {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	email = normalizeEmail(email)
	for _, user := range ds.data.Users {
		if normalizeEmail(user.Email) == email {
			user.EmailVerified = true
		}
	}
	return ds.saveLocked()
}

func (ds *DataStore) SaveVerificationCode(email, code string, expiresAt time.Time) error {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	ds.data.VerificationCodes[normalizeEmail(email)] = &VerificationCode{
		Email:     normalizeEmail(email),
		Code:      code,
		ExpiresAt: expiresAt,
		Verified:  false,
	}
	return ds.saveLocked()
}

func (ds *DataStore) VerifyEmailCode(email, code string) error {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	record := ds.data.VerificationCodes[normalizeEmail(email)]
	if record == nil {
		return fmt.Errorf("verification code not found")
	}
	if time.Now().After(record.ExpiresAt) {
		return fmt.Errorf("verification code expired")
	}
	if strings.TrimSpace(code) == "" || record.Code != strings.TrimSpace(code) {
		return fmt.Errorf("invalid verification code")
	}
	record.Verified = true
	return ds.saveLocked()
}

func (ds *DataStore) IsEmailVerified(email string) bool {
	ds.mu.RLock()
	defer ds.mu.RUnlock()
	record := ds.data.VerificationCodes[normalizeEmail(email)]
	return record != nil && record.Verified && time.Now().Before(record.ExpiresAt)
}

func (ds *DataStore) CreateSession(user *UserRecord, ttl time.Duration) (*UserSession, error) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	if user == nil {
		return nil, fmt.Errorf("user is required")
	}
	session := &UserSession{
		Token:      randomToken("sess"),
		UserID:     user.ID,
		TenantCode: user.TenantCode,
		ExpiresAt:  time.Now().Add(ttl),
	}
	user.LastLoginAt = time.Now()
	ds.data.Sessions[session.Token] = session
	if err := ds.saveLocked(); err != nil {
		return nil, err
	}
	return session, nil
}

func (ds *DataStore) GetSession(token string) (*UserSession, *UserRecord) {
	ds.mu.RLock()
	defer ds.mu.RUnlock()
	session := ds.data.Sessions[token]
	if session == nil || time.Now().After(session.ExpiresAt) {
		return nil, nil
	}
	user := ds.data.Users[session.UserID]
	if user == nil {
		return nil, nil
	}
	return session, user
}

func (ds *DataStore) UpsertAgentByUserHash(userHash, agentID, displayName, description string) (*AgentRegistration, error) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	userID := ds.data.UsersByHash[strings.TrimSpace(userHash)]
	if userID == "" {
		return nil, fmt.Errorf("owner hash not found")
	}
	user := ds.data.Users[userID]
	if user == nil {
		return nil, fmt.Errorf("owner user not found")
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" || displayName == "" {
		return nil, fmt.Errorf("agent_id and display_name are required")
	}
	now := time.Now()
	record := ds.data.Agents[agentID]
	if record != nil && record.OwnerUserID != user.ID {
		return nil, fmt.Errorf("agent_id already belongs to another account")
	}
	if record == nil {
		record = &AgentRegistration{
			AgentID:      agentID,
			TenantCode:   user.TenantCode,
			OwnerUserID:  user.ID,
			OwnerUserHash: user.UserHash,
			DisplayName:  strings.TrimSpace(displayName),
			Description:  strings.TrimSpace(description),
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		ds.data.Agents[agentID] = record
	} else {
		record.TenantCode = user.TenantCode
		record.OwnerUserID = user.ID
		record.OwnerUserHash = user.UserHash
		record.DisplayName = strings.TrimSpace(displayName)
		record.Description = strings.TrimSpace(description)
		record.UpdatedAt = now
	}
	if err := ds.saveLocked(); err != nil {
		return nil, err
	}
	return record, nil
}

func (ds *DataStore) GetAgent(agentID string) *AgentRegistration {
	ds.mu.RLock()
	defer ds.mu.RUnlock()
	return ds.data.Agents[strings.TrimSpace(agentID)]
}

func (ds *DataStore) ListUserAgents(ownerUserID string) []*AgentRegistration {
	ds.mu.RLock()
	defer ds.mu.RUnlock()
	list := make([]*AgentRegistration, 0)
	for _, record := range ds.data.Agents {
		if record.OwnerUserID == ownerUserID {
			list = append(list, record)
		}
	}
	return list
}

func (ds *DataStore) DeleteUserAgent(ownerUserID, agentID string) error {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return fmt.Errorf("agent_id is required")
	}
	record := ds.data.Agents[agentID]
	if record == nil {
		return fmt.Errorf("agent not found")
	}
	if record.OwnerUserID != ownerUserID {
		return fmt.Errorf("agent does not belong to current user")
	}
	delete(ds.data.Agents, agentID)
	return ds.saveLocked()
}
