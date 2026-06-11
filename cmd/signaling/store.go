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
	"sort"
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

	// 管理员标记（由启动时 -admin-config 配置文件指定，非持久权威来源）。
	IsAdmin bool `json:"is_admin,omitempty"`

	// TURN 中转额度（0 表示不限）：
	MaxBps            int64  `json:"max_bps,omitempty"`             // 每会话带宽上限（字节/秒）
	MonthlyQuotaBytes int64  `json:"monthly_quota_bytes,omitempty"` // 每月累计中转流量上限（字节）
	UsedBytes         int64  `json:"used_bytes,omitempty"`          // 本月已用中转流量（字节）
	QuotaMonth        string `json:"quota_month,omitempty"`         // 当前计量月份，格式 "2006-01"
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

func (ds *DataStore) ChangeUserPassword(userID, oldPassword, newPassword string) error {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	user := ds.data.Users[userID]
	if user == nil {
		return fmt.Errorf("user not found")
	}
	if strings.TrimSpace(newPassword) == "" {
		return fmt.Errorf("new password is required")
	}
	if !verifyPasswordHash(user.PasswordSalt, user.PasswordHash, oldPassword) {
		return fmt.Errorf("invalid old password")
	}
	salt, hash := newPasswordHash(newPassword)
	user.PasswordSalt = salt
	user.PasswordHash = hash
	return ds.saveLocked()
}

// currentMonth 返回当前计量月份键，格式 "2006-01"。
func currentMonth() string {
	return time.Now().Format("2006-01")
}

// GetUserByID 按 ID 返回用户（只读）。
func (ds *DataStore) GetUserByID(userID string) *UserRecord {
	ds.mu.RLock()
	defer ds.mu.RUnlock()
	return ds.data.Users[userID]
}

// ListUsers 返回指定租户下的全部用户副本（按用户名排序），供管理员查看。
// 返回浅拷贝，避免外部读到时与内部并发写竞争。
func (ds *DataStore) ListUsers(tenantCode string) []UserRecord {
	ds.mu.RLock()
	defer ds.mu.RUnlock()
	tenantCode = normalizeTenantCode(tenantCode)
	result := make([]UserRecord, 0, len(ds.data.Users))
	for _, user := range ds.data.Users {
		if user == nil {
			continue
		}
		if tenantCode != "" && user.TenantCode != tenantCode {
			continue
		}
		result = append(result, *user)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Username < result[j].Username })
	return result
}

// SetUserQuota 设置某用户的带宽上限与月度流量上限（字节）。
func (ds *DataStore) SetUserQuota(userID string, maxBps, monthlyQuotaBytes int64) error {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	user := ds.data.Users[userID]
	if user == nil {
		return fmt.Errorf("user not found")
	}
	if maxBps < 0 {
		maxBps = 0
	}
	if monthlyQuotaBytes < 0 {
		monthlyQuotaBytes = 0
	}
	user.MaxBps = maxBps
	user.MonthlyQuotaBytes = monthlyQuotaBytes
	return ds.saveLocked()
}

// rollMonthLocked 在调用方已持锁的前提下，按当前月份滚动用量（跨月清零）。
func rollMonthLocked(user *UserRecord) {
	m := currentMonth()
	if user.QuotaMonth != m {
		user.QuotaMonth = m
		user.UsedBytes = 0
	}
}

// AddUserUsage 累加某用户本月已用中转流量；跨月自动清零后再计。
// delta<=0 时仅做跨月滚动检查。返回累加后的本月用量。
func (ds *DataStore) AddUserUsage(userID string, delta int64) (int64, error) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	user := ds.data.Users[userID]
	if user == nil {
		return 0, fmt.Errorf("user not found")
	}
	rollMonthLocked(user)
	if delta > 0 {
		user.UsedBytes += delta
	}
	if err := ds.saveLocked(); err != nil {
		return user.UsedBytes, err
	}
	return user.UsedBytes, nil
}

// ResetUserUsage 把某用户本月已用流量清零（管理员手动重置/加额时用）。
func (ds *DataStore) ResetUserUsage(userID string) error {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	user := ds.data.Users[userID]
	if user == nil {
		return fmt.Errorf("user not found")
	}
	user.QuotaMonth = currentMonth()
	user.UsedBytes = 0
	return ds.saveLocked()
}

// UserQuotaSnapshot 是给 TURN 计量/鉴权用的额度快照。
type UserQuotaSnapshot struct {
	Found             bool
	MaxBps            int64
	MonthlyQuotaBytes int64
	UsedBytes         int64
	Exhausted         bool // 月度额度已用满（MonthlyQuotaBytes>0 且 UsedBytes>=上限）
}

// UserQuota 返回某用户的额度快照（含跨月滚动后的本月用量判定）。
// 注意：此方法只读，不落盘；跨月清零会在下一次 AddUserUsage 时持久化。
func (ds *DataStore) UserQuota(userID string) UserQuotaSnapshot {
	ds.mu.RLock()
	defer ds.mu.RUnlock()
	user := ds.data.Users[userID]
	if user == nil {
		return UserQuotaSnapshot{}
	}
	used := user.UsedBytes
	if user.QuotaMonth != currentMonth() {
		used = 0 // 逻辑上已跨月清零
	}
	exhausted := user.MonthlyQuotaBytes > 0 && used >= user.MonthlyQuotaBytes
	return UserQuotaSnapshot{
		Found:             true,
		MaxBps:            user.MaxBps,
		MonthlyQuotaBytes: user.MonthlyQuotaBytes,
		UsedBytes:         used,
		Exhausted:         exhausted,
	}
}

// UserUsageExhausted 返回某用户本月中转额度是否已用满（用于 TURN QuotaHandler）。
func (ds *DataStore) UserUsageExhausted(userID string) bool {
	return ds.UserQuota(userID).Exhausted
}

// ApplyAdmins 按给定的 admin 键集合（"tenant:username" 或 "username"，后者按默认租户）
// 重置所有用户的 IsAdmin 标记：命中者置 true，其余清 false。启动时调用一次。
// 返回实际命中的管理员用户名列表。
func (ds *DataStore) ApplyAdmins(adminKeys []string, defaultTenant string) []string {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	want := map[string]bool{}
	for _, raw := range adminKeys {
		k := strings.TrimSpace(raw)
		if k == "" {
			continue
		}
		if !strings.Contains(k, ":") {
			k = normalizeTenantCode(defaultTenant) + ":" + normalizeUsername(k)
		} else {
			parts := strings.SplitN(k, ":", 2)
			k = tenantUserKey(parts[0], parts[1])
		}
		want[k] = true
	}

	var matched []string
	changed := false
	for _, user := range ds.data.Users {
		if user == nil {
			continue
		}
		shouldBeAdmin := want[tenantUserKey(user.TenantCode, user.Username)]
		if user.IsAdmin != shouldBeAdmin {
			user.IsAdmin = shouldBeAdmin
			changed = true
		}
		if shouldBeAdmin {
			matched = append(matched, user.Username)
		}
	}
	if changed {
		_ = ds.saveLocked()
	}
	return matched
}

// AdminResetUserPassword 管理员重置某用户密码（不校验旧密码）。
func (ds *DataStore) AdminResetUserPassword(userID, newPassword string) error {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	user := ds.data.Users[userID]
	if user == nil {
		return fmt.Errorf("user not found")
	}
	if strings.TrimSpace(newPassword) == "" {
		return fmt.Errorf("new password is required")
	}
	salt, hash := newPasswordHash(newPassword)
	user.PasswordSalt = salt
	user.PasswordHash = hash
	return ds.saveLocked()
}

// DeleteUser 删除用户及其关联数据（用户索引、会话、归属的 agent 注册）。
// 返回被一并删除的 agent_id 列表，供调用方清理内存中的在线 agent 状态。
func (ds *DataStore) DeleteUser(userID string) ([]string, error) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	user := ds.data.Users[userID]
	if user == nil {
		return nil, fmt.Errorf("user not found")
	}
	delete(ds.data.UsersByTenantName, tenantUserKey(user.TenantCode, user.Username))
	delete(ds.data.UsersByHash, user.UserHash)
	delete(ds.data.Users, userID)
	// 该用户的登录会话
	for token, sess := range ds.data.Sessions {
		if sess != nil && sess.UserID == userID {
			delete(ds.data.Sessions, token)
		}
	}
	// 该用户归属的 agent 注册
	var removedAgents []string
	for agentID, rec := range ds.data.Agents {
		if rec != nil && rec.OwnerUserID == userID {
			removedAgents = append(removedAgents, agentID)
			delete(ds.data.Agents, agentID)
		}
	}
	if err := ds.saveLocked(); err != nil {
		return removedAgents, err
	}
	return removedAgents, nil
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

// GetUserByHash 按用户 hash 解析用户（用于 client 以 user hash 做第一层身份）。
func (ds *DataStore) GetUserByHash(userHash string) *UserRecord {
	ds.mu.RLock()
	defer ds.mu.RUnlock()
	userID := ds.data.UsersByHash[strings.TrimSpace(userHash)]
	if userID == "" {
		return nil
	}
	return ds.data.Users[userID]
}

// randomAgentID 生成随机 agent 内部句柄；调用方需在持锁下校验全局唯一。
func randomAgentID() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return "agent-" + hex.EncodeToString(b)
}

// findAgentByOwnerNameLocked 在持锁状态下按 (owner, 名称) 查找登记记录。
// 名称为用户视角的主标识——同名即同一 agent。
func (ds *DataStore) findAgentByOwnerNameLocked(ownerUserID, name string) *AgentRegistration {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	for _, rec := range ds.data.Agents {
		if rec.OwnerUserID == ownerUserID && rec.DisplayName == name {
			return rec
		}
	}
	return nil
}

// UpsertAgentByUserHash 按 owner 登记/更新 agent。
//
// 身份模型：在某用户名下，display_name（名称）为主标识——同名即同一 agent，
// 重连/重启按名称命中并复用其记录与 agent_id；agent_id 仅作全局唯一内部句柄，
// 缺省或与已有记录（可能属于他人）冲突时自动生成，不再作为必填项。
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
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = agentID // 名称缺省回退到 id
	}
	now := time.Now()

	// 1) 在该 owner 名下按名称优先匹配：命中则视为同一 agent，复用其记录与 agent_id。
	if rec := ds.findAgentByOwnerNameLocked(user.ID, name); rec != nil {
		rec.TenantCode = user.TenantCode
		rec.OwnerUserID = user.ID
		rec.OwnerUserHash = user.UserHash
		rec.Description = strings.TrimSpace(description)
		rec.UpdatedAt = now
		if err := ds.saveLocked(); err != nil {
			return nil, err
		}
		return rec, nil
	}

	// 2) 未按名称命中 → 选定一个全局唯一的 agent_id：
	//    缺省、或与已有记录冲突（不唯一）则自动生成，直至唯一。
	if agentID == "" || ds.data.Agents[agentID] != nil {
		for {
			agentID = randomAgentID()
			if ds.data.Agents[agentID] == nil {
				break
			}
		}
	}
	if name == "" {
		name = agentID // 名称与 id 均为空时用生成的 id 兜底
	}

	record := &AgentRegistration{
		AgentID:       agentID,
		TenantCode:    user.TenantCode,
		OwnerUserID:   user.ID,
		OwnerUserHash: user.UserHash,
		DisplayName:   name,
		Description:   strings.TrimSpace(description),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	ds.data.Agents[agentID] = record
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
