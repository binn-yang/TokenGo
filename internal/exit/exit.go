package exit

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/binn/tokengo/internal/config"
	"github.com/binn/tokengo/internal/crypto"
	"github.com/binn/tokengo/internal/dht"
)

// ExitNode 出口节点
type ExitNode struct {
	cfg          *config.ExitConfig
	tunnel       *TunnelClient
	ohttpHandler *OHTTPHandler
	dhtNode      *dht.Node
	discovery    *dht.Discovery
	provider     *dht.Provider
	publicKey    []byte
	keyID        uint8
	staticRelay  string // 静态 Relay 地址（用于 serve 命令）
}

// New 创建出口节点（DHT 发现模式）
func New(cfg *config.ExitConfig) (*ExitNode, error) {
	// DHT 必须启用以发现 Relay
	if !cfg.DHT.Enabled {
		return nil, fmt.Errorf("必须启用 DHT 配置以发现 Relay 节点")
	}

	return newExitNode(cfg, "")
}

// NewStatic 创建出口节点（静态地址模式，用于 serve 命令）
func NewStatic(cfg *config.ExitConfig, relayAddr string) (*ExitNode, error) {
	return newExitNode(cfg, relayAddr)
}

// newExitNode 内部构造函数
func newExitNode(cfg *config.ExitConfig, staticRelay string) (*ExitNode, error) {
	// 加载私钥
	privKeyData, err := os.ReadFile(cfg.OHTTPPrivateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("读取私钥文件失败: %w", err)
	}

	privateKey, err := base64.StdEncoding.DecodeString(string(privKeyData))
	if err != nil {
		return nil, fmt.Errorf("解码私钥失败: %w", err)
	}

	// 优先使用显式配置的公钥路径，否则回退到私钥文件 + ".pub"
	pubKeyPath := cfg.OHTTPPublicKeyFile
	if pubKeyPath == "" {
		pubKeyPath = cfg.OHTTPPrivateKeyFile + ".pub"
	}
	pubKeyData, err := os.ReadFile(pubKeyPath)
	if err != nil {
		return nil, fmt.Errorf("读取公钥文件失败: %w", err)
	}

	keyID, publicKey, err := crypto.LoadPublicKeyConfig(string(pubKeyData))
	if err != nil {
		return nil, fmt.Errorf("解析公钥配置失败: %w", err)
	}

	// 创建 AI 客户端
	aiClient := NewAIClient(cfg.AIBackend.URL, cfg.AIBackend.APIKey, cfg.AIBackend.Headers)

	// 创建 OHTTP 处理器
	ohttpHandler, err := NewOHTTPHandler(keyID, privateKey, publicKey, aiClient)
	if err != nil {
		return nil, fmt.Errorf("创建 OHTTP 处理器失败: %w", err)
	}

	// 计算公钥哈希 (用于在 Relay 侧标识此 Exit)
	pubKeyHash := crypto.PubKeyHash(publicKey)

	// 编码 KeyConfig (注册到 Relay 时附带，供 Client 查询)
	keyConfig := crypto.EncodeKeyConfig(keyID, publicKey)

	node := &ExitNode{
		cfg:         cfg,
		ohttpHandler: ohttpHandler,
		publicKey:    publicKey,
		keyID:        keyID,
		staticRelay:  staticRelay,
	}

	// 静态模式（用于 serve 命令）
	if staticRelay != "" {
		node.tunnel = NewTunnelClientStatic(staticRelay, pubKeyHash, keyConfig, ohttpHandler, cfg.InsecureSkipVerify)
		return node, nil
	}

	// DHT 发现模式
	// 创建 DHT 节点
	dhtCfg := &dht.Config{
		PrivateKeyPath:   cfg.DHT.PrivateKeyFile,
		BootstrapPeers:   cfg.DHT.BootstrapPeers,
		ListenAddrs:      cfg.DHT.ListenAddrs,
		ExternalAddrs:    cfg.DHT.ExternalAddrs,
		Mode:             "server",
		ServiceType:      "exit",
		UseIPFSBootstrap: cfg.DHT.UseIPFSBootstrap,
	}

	dhtNode, err := dht.NewNode(dhtCfg)
	if err != nil {
		return nil, fmt.Errorf("创建 DHT 节点失败: %w", err)
	}
	node.dhtNode = dhtNode
	node.discovery = dht.NewDiscovery(dhtNode)
	node.provider = dht.NewProvider(dhtNode, "exit")

	// 创建反向隧道客户端（传入 DHT 发现器）
	node.tunnel = NewTunnelClient(node.discovery, pubKeyHash, keyConfig, ohttpHandler, cfg.InsecureSkipVerify)

	return node, nil
}

// Start 启动出口节点
func (e *ExitNode) Start() error {
	ctx := context.Background()

	// 1. 先启动 DHT 节点（仅 DHT 模式）
	if e.dhtNode != nil {
		if err := e.dhtNode.Start(ctx); err != nil {
			return fmt.Errorf("启动 DHT 节点失败: %w", err)
		}
		log.Printf("DHT 节点已启动, PeerID: %s", e.dhtNode.PeerID())

		// 注册服务到 DHT
		serviceInfo := &dht.ServiceInfo{
			PeerID:      e.dhtNode.PeerID(),
			ServiceType: "exit",
			Addrs:       e.dhtNode.FullAddrs(),
			PublicKey:   e.publicKey,
			KeyID:       e.keyID,
		}
		if err := e.provider.Register(serviceInfo); err != nil {
			log.Printf("警告: 注册服务到 DHT 失败: %v", err)
		}
	}

	// 打印公钥信息
	pubKeyConfig := crypto.EncodeKeyConfig(e.keyID, e.publicKey)
	pubKeyBase64 := base64.StdEncoding.EncodeToString(pubKeyConfig)
	pubKeyHash := crypto.PubKeyHash(e.publicKey)
	log.Printf("")
	log.Printf("Exit 公钥 (供 Client 使用):")
	log.Printf("  %s", pubKeyBase64)
	log.Printf("Exit pubKeyHash: %s", pubKeyHash)
	log.Printf("")
	log.Printf("AI 后端: %s", e.cfg.AIBackend.URL)

	// 打印连接模式
	if e.staticRelay != "" {
		log.Printf("Relay 地址: %s (静态)", e.staticRelay)
	} else {
		log.Printf("正在通过 DHT 发现 Relay...")
	}
	log.Printf("")

	// 优雅关闭
	go e.handleShutdown()

	// 2. 启动反向隧道
	return e.tunnel.Start(context.Background())
}

// handleShutdown 处理优雅关闭
func (e *ExitNode) handleShutdown() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	<-sigChan
	log.Println("收到关闭信号，正在关闭...")

	if err := e.Stop(); err != nil {
		log.Printf("关闭失败: %v", err)
	}
}

// Ready 返回一个在 Exit 首次注册到 Relay 后关闭的 channel
func (e *ExitNode) Ready() <-chan struct{} {
	return e.tunnel.Ready()
}

// Stop 停止出口节点
func (e *ExitNode) Stop() error {
	// 停止 DHT 服务
	if e.provider != nil {
		e.provider.Unregister()
	}
	if e.dhtNode != nil {
		e.dhtNode.Stop()
	}

	// 停止反向隧道
	if e.tunnel != nil {
		return e.tunnel.Stop()
	}
	return nil
}
