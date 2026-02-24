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
	provider     *dht.Provider
	publicKey    []byte
	keyID        uint8
}

// New 创建出口节点
func New(cfg *config.ExitConfig) (*ExitNode, error) {
	// 加载私钥
	privKeyData, err := os.ReadFile(cfg.OHTTPPrivateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("读取私钥文件失败: %w", err)
	}

	privateKey, err := base64.StdEncoding.DecodeString(string(privKeyData))
	if err != nil {
		return nil, fmt.Errorf("解码私钥失败: %w", err)
	}

	// 从私钥派生公钥 (需要重新生成密钥对获取公钥，或从配置读取)
	// 为简化，这里假设公钥文件与私钥文件同目录，后缀为 .pub
	pubKeyPath := cfg.OHTTPPrivateKeyFile + ".pub"
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

	// 创建反向隧道客户端
	tunnel := NewTunnelClient(cfg.RelayAddrs, pubKeyHash, ohttpHandler, cfg.InsecureSkipVerify)

	node := &ExitNode{
		cfg:          cfg,
		tunnel:       tunnel,
		ohttpHandler: ohttpHandler,
		publicKey:    publicKey,
		keyID:        keyID,
	}

	// 如果启用了 DHT，创建 DHT 节点
	if cfg.DHT.Enabled {
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
		node.provider = dht.NewProvider(dhtNode, "exit")
	}

	return node, nil
}

// Start 启动出口节点
func (e *ExitNode) Start() error {
	// 启动 DHT 节点
	if e.dhtNode != nil {
		ctx := context.Background()
		if err := e.dhtNode.Start(ctx); err != nil {
			return fmt.Errorf("启动 DHT 节点失败: %w", err)
		}

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
	log.Printf("Relay 地址: %v", e.cfg.RelayAddrs)
	log.Printf("")
	log.Printf("正在通过反向隧道连接 Relay...")

	// 优雅关闭
	go e.handleShutdown()

	// 启动反向隧道 (阻塞)
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
