package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// ClientConfig 客户端配置
type ClientConfig struct {
	Listen         string        `yaml:"listen"`
	Timeout        time.Duration `yaml:"timeout"`
	BootstrapPeers []string      `yaml:"bootstrap_peers,omitempty"` // 可选，覆盖内置默认值
}

// RelayConfig 中继节点配置 (盲转发模式)
// TLS 证书自动生成（绑定 PeerID），无需配置
type RelayConfig struct {
	Listen string    `yaml:"listen"`
	DHT    DHTConfig `yaml:"dht,omitempty"`
}

// ExitConfig 出口节点配置
// TLS 证书验证通过 PeerID 自动完成，无需配置 insecure_skip_verify
type ExitConfig struct {
	OHTTPPrivateKeyFile string    `yaml:"ohttp_private_key_file"`
	OHTTPPublicKeyFile  string    `yaml:"ohttp_public_key_file,omitempty"` // 可选，默认为私钥文件 + ".pub"
	AIBackend           AIBackend `yaml:"ai_backend"`
	DHT                 DHTConfig `yaml:"dht,omitempty"`
}

// AIBackend AI 后端配置
type AIBackend struct {
	URL     string            `yaml:"url"`
	APIKey  string            `yaml:"api_key"`
	Headers map[string]string `yaml:"headers,omitempty"`
}

// DHTConfig DHT 配置
type DHTConfig struct {
	BootstrapPeers []string `yaml:"bootstrap_peers,omitempty"`
	ListenAddrs    []string `yaml:"listen_addrs,omitempty"`
	ExternalAddrs  []string `yaml:"external_addrs,omitempty"`
	PrivateKeyFile string   `yaml:"private_key_file,omitempty"`
	Mode           string   `yaml:"mode,omitempty"` // "server" or "client"
}

// LoadClientConfig 加载客户端配置
func LoadClientConfig(path string) (*ClientConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	var cfg ClientConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	// 设置默认值
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:8080"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}

	return &cfg, nil
}

// LoadRelayConfig 加载中继节点配置
func LoadRelayConfig(path string) (*RelayConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	var cfg RelayConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	// 设置默认值
	if cfg.Listen == "" {
		cfg.Listen = ":4433"
	}

	return &cfg, nil
}

// LoadExitConfig 加载出口节点配置
func LoadExitConfig(path string) (*ExitConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	var cfg ExitConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	// 设置默认值
	if cfg.AIBackend.URL == "" {
		cfg.AIBackend.URL = "http://localhost:11434"
	}

	return &cfg, nil
}
