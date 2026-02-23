package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// ClientConfig 客户端配置
type ClientConfig struct {
	Listen             string         `yaml:"listen"`
	Timeout            time.Duration  `yaml:"timeout"`
	InsecureSkipVerify bool           `yaml:"insecure_skip_verify"` // 仅开发环境使用
	DHT                DHTConfig      `yaml:"dht,omitempty"`        // DHT 发现配置
	Bootstrap          BootstrapAPI   `yaml:"bootstrap,omitempty"`  // Bootstrap API 配置
	Fallback           FallbackConfig `yaml:"fallback,omitempty"`   // 回退地址
}

// BootstrapAPI Bootstrap API 配置
type BootstrapAPI struct {
	URL      string        `yaml:"url,omitempty"`      // Bootstrap API URL (如 https://bootstrap.example.com)
	Interval time.Duration `yaml:"interval,omitempty"` // 刷新间隔
}

// RelayConfig 中继节点配置 (盲转发模式)
type RelayConfig struct {
	Listen             string    `yaml:"listen"`
	TLS                TLSConfig `yaml:"tls"`
	InsecureSkipVerify bool      `yaml:"insecure_skip_verify"` // 仅开发环境使用
	DHT                DHTConfig `yaml:"dht,omitempty"`
}

// ExitConfig 出口节点配置
type ExitConfig struct {
	RelayAddrs          []string  `yaml:"relay_addrs"`           // 要连接的 Relay 地址列表
	OHTTPPrivateKeyFile string    `yaml:"ohttp_private_key_file"`
	AIBackend           AIBackend `yaml:"ai_backend"`
	InsecureSkipVerify  bool      `yaml:"insecure_skip_verify"` // 仅开发环境使用
	DHT                 DHTConfig `yaml:"dht,omitempty"`
}

// TLSConfig TLS 证书配置
type TLSConfig struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

// AIBackend AI 后端配置
type AIBackend struct {
	URL     string            `yaml:"url"`
	APIKey  string            `yaml:"api_key"`
	Headers map[string]string `yaml:"headers,omitempty"`
}

// DHTConfig DHT 配置
type DHTConfig struct {
	Enabled          bool     `yaml:"enabled"`
	BootstrapPeers   []string `yaml:"bootstrap_peers"`
	ListenAddrs      []string `yaml:"listen_addrs"`
	ExternalAddrs    []string `yaml:"external_addrs,omitempty"`
	PrivateKeyFile   string   `yaml:"private_key_file"`
	Mode             string   `yaml:"mode"` // "server" or "client"
	UseIPFSBootstrap bool     `yaml:"use_ipfs_bootstrap,omitempty"`
}

// FallbackConfig 静态回退配置
type FallbackConfig struct {
	RelayAddrs []string       `yaml:"relay_addrs,omitempty"` // Relay 地址列表
	Exits      []FallbackExit `yaml:"exits,omitempty"`       // Exit 节点列表（含公钥）
}

// FallbackExit 回退 Exit 节点配置
type FallbackExit struct {
	PublicKey string `yaml:"public_key"` // OHTTP 公钥 (base64 编码)
	KeyID     uint8  `yaml:"key_id"`     // OHTTP KeyID
}

// PublicKeyBytes 解码 base64 编码的公钥
func (f *FallbackExit) PublicKeyBytes() ([]byte, error) {
	return base64.StdEncoding.DecodeString(f.PublicKey)
}

// BootstrapConfig Bootstrap 节点配置
type BootstrapConfig struct {
	DHT DHTConfig `yaml:"dht"`
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

// LoadBootstrapConfig 加载 Bootstrap 节点配置
func LoadBootstrapConfig(path string) (*BootstrapConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	var cfg BootstrapConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	// 设置默认值
	if len(cfg.DHT.ListenAddrs) == 0 {
		cfg.DHT.ListenAddrs = []string{"/ip4/0.0.0.0/tcp/4001"}
	}
	cfg.DHT.Mode = "server" // Bootstrap 始终是 server 模式

	return &cfg, nil
}
