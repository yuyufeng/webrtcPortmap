// webrtc/config.go - WebRTC配置
package webrtc

import (
	"github.com/pion/webrtc/v4"
)

// Config WebRTC配置
type Config struct {
	ICEServers []webrtc.ICEServer
}

// DefaultConfig 返回默认配置（使用Google公共STUN）
func DefaultConfig() *Config {
	return &Config{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
			{
				URLs: []string{"stun:stun1.l.google.com:19302"},
			},
		},
	}
}

// ConfigWithTURN 返回带TURN服务器的配置
func ConfigWithTURN(turnURL, username, credential string) *Config {
	cfg := DefaultConfig()
	cfg.ICEServers = append(cfg.ICEServers, webrtc.ICEServer{
		URLs:       []string{turnURL},
		Username:   username,
		Credential: credential,
	})
	return cfg
}

// SettingEngine 创建自定义设置引擎
// 可以在这里配置更高级的ICE选项
func (c *Config) SettingEngine() webrtc.SettingEngine {
	var s webrtc.SettingEngine
	// 可以在这里添加自定义设置，如：
	// - 设置NAT遍历策略
	// - 配置ICE候选收集超时
	// - 启用ICE精简模式
	return s
}

// NewConfiguration 创建pion/webrtc配置
func (c *Config) NewConfiguration() webrtc.Configuration {
	return webrtc.Configuration{
		ICEServers: c.ICEServers,
		SDPSemantics: webrtc.SDPSemanticsUnifiedPlan,
	}
}
