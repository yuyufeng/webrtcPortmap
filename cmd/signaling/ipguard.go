package main

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 防爆破：同一来源 IP 连续登录失败达到阈值即升级封禁。
// 升级时长：第 1 次封 5min，第 2 次 10min，第 3 次及以后 30min（封顶）。
var ipBanLevels = []time.Duration{5 * time.Minute, 10 * time.Minute, 30 * time.Minute}

const (
	ipFailThreshold = 5             // 连续失败多少次触发封禁
	ipRecordTTL     = 1 * time.Hour // 不活动多久后清除记录（计数与等级随之衰减）
)

type ipFailRecord struct {
	fails    int       // 自上次封禁/成功以来的连续失败数
	banLevel int       // 已封禁次数（决定下次时长，索引 ipBanLevels）
	banUntil time.Time // 封禁截止时间
	lastSeen time.Time
}

type ipGuard struct {
	mu      sync.Mutex
	records map[string]*ipFailRecord
}

func newIPGuard() *ipGuard {
	return &ipGuard{records: map[string]*ipFailRecord{}}
}

// banned 返回该 IP 是否正处于封禁中，以及剩余时长。
func (g *ipGuard) banned(ip string) (bool, time.Duration) {
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	r := g.records[ip]
	if r != nil && now.Before(r.banUntil) {
		return true, r.banUntil.Sub(now)
	}
	return false, 0
}

// recordFail 记一次失败；达到阈值则按等级升级封禁。返回是否因此被封、剩余时长。
func (g *ipGuard) recordFail(ip string) (bool, time.Duration) {
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruneLocked(now)
	r := g.records[ip]
	if r == nil {
		r = &ipFailRecord{}
		g.records[ip] = r
	}
	r.lastSeen = now
	if now.Before(r.banUntil) { // 仍在封禁中，不重复升级
		return true, r.banUntil.Sub(now)
	}
	r.fails++
	if r.fails >= ipFailThreshold {
		idx := r.banLevel
		if idx >= len(ipBanLevels) {
			idx = len(ipBanLevels) - 1
		}
		dur := ipBanLevels[idx]
		r.banUntil = now.Add(dur)
		r.banLevel++
		r.fails = 0
		return true, dur
	}
	return false, 0
}

// recordSuccess 成功登录后清零连续失败计数（封禁等级保留以记忆近期滥用，超 TTL 自然清除）。
func (g *ipGuard) recordSuccess(ip string) {
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if r := g.records[ip]; r != nil {
		r.fails = 0
		r.lastSeen = now
	}
}

// pruneLocked 清理长期不活动且未在封禁中的记录（需持锁）。
func (g *ipGuard) pruneLocked(now time.Time) {
	for ip, r := range g.records {
		if now.Sub(r.lastSeen) > ipRecordTTL && now.After(r.banUntil) {
			delete(g.records, ip)
		}
	}
}

// clientIP 取请求来源 IP。默认只信 RemoteAddr（防 X-Forwarded-For 伪造绕过封禁）；
// 仅当 -trust-proxy-header 开启（部署在可信反代后）时才采信 XFF / X-Real-IP。
func (s *Server) clientIP(r *http.Request) string {
	if s.trustProxyHeader {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if ip := strings.TrimSpace(strings.Split(xff, ",")[0]); ip != "" {
				return ip
			}
		}
		if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
			return xr
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
