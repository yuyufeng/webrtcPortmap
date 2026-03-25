// webrtc/config.go - WebRTC配置
package webrtc

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/pion/webrtc/v4"
)

// Config WebRTC配置
type Config struct {
	ICEServers []webrtc.ICEServer
}

// ICEConfigFile ICE 配置文件结构
type ICEConfigFile struct {
	STUNServers []STUNServerConfig `json:"stun_servers,omitempty"`
	TURNServers []TURNServerConfig `json:"turn_servers,omitempty"`
}

// STUNServerConfig STUN服务器配置
type STUNServerConfig struct {
	URLs     string `json:"urls"`
	Priority int    `json:"priority,omitempty"` // 优先级，数字越小优先级越高
}

// TURNServerConfig TURN服务器配置
type TURNServerConfig struct {
	URLs       string `json:"urls"`
	Username   string `json:"username,omitempty"`
	Credential string `json:"credential,omitempty"`
	Priority   int    `json:"priority,omitempty"`
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

// LoadICEConfigFromFile 从配置文件加载ICE服务器配置
func LoadICEConfigFromFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read ICE config file failed: %w", err)
	}

	var fileCfg ICEConfigFile
	if err := json.Unmarshal(data, &fileCfg); err != nil {
		return nil, fmt.Errorf("parse ICE config file failed: %w", err)
	}

	cfg := &Config{
		ICEServers: make([]webrtc.ICEServer, 0),
	}

	// 添加 STUN 服务器
	for _, stun := range fileCfg.STUNServers {
		if stun.URLs != "" {
			cfg.ICEServers = append(cfg.ICEServers, webrtc.ICEServer{
				URLs: []string{stun.URLs},
			})
		}
	}

	// 添加 TURN 服务器
	for _, turn := range fileCfg.TURNServers {
		if turn.URLs != "" {
			cfg.ICEServers = append(cfg.ICEServers, webrtc.ICEServer{
				URLs:       []string{turn.URLs},
				Username:   turn.Username,
				Credential: turn.Credential,
			})
		}
	}

	if len(cfg.ICEServers) == 0 {
		return nil, fmt.Errorf("no valid ICE servers found in config file")
	}

	return cfg, nil
}

// NewConfig 创建配置，支持多种方式
// 优先级：命令行TURN参数 > 配置文件 > 默认配置
func NewConfig(iceConfigFile, turnURL, turnUser, turnPass string) (*Config, error) {
	// 1. 使用命令行TURN参数
	if turnURL != "" {
		cfg := ConfigWithTURN(turnURL, turnUser, turnPass)
		fmt.Printf("[WebRTC] Using TURN server: %s\n", turnURL)
		return cfg, nil
	}

	// 2. 尝试从配置文件加载
	if iceConfigFile != "" {
		cfg, err := LoadICEConfigFromFile(iceConfigFile)
		if err != nil {
			return nil, err
		}
		fmt.Printf("[WebRTC] Loaded %d ICE servers from config file\n", len(cfg.ICEServers))
		return cfg, nil
	}

	// 3. 使用默认配置
	fmt.Printf("[WebRTC] Using default STUN servers (P2P only, no TURN relay)\n")
	return DefaultConfig(), nil
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
		ICEServers:   c.ICEServers,
		SDPSemantics: webrtc.SDPSemanticsUnifiedPlan,
	}
}

// PrintICEServers 打印当前配置的ICE服务器列表
func (c *Config) PrintICEServers() {
	fmt.Printf("[WebRTC] Configured ICE servers (%d):\n", len(c.ICEServers))
	for i, server := range c.ICEServers {
		for _, url := range server.URLs {
			if server.Username != "" {
				fmt.Printf("  [%d] %s (user: %s)\n", i+1, url, server.Username)
			} else {
				fmt.Printf("  [%d] %s\n", i+1, url)
			}
		}
	}
}
