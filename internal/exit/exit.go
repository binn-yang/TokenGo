package exit

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/binn/tokengo/internal/config"
	"github.com/binn/tokengo/internal/crypto"
	"github.com/binn/tokengo/internal/dht"
)

// ExitNode 出口节点
type ExitNode struct {
	cfg          *config.ExitConfig
	server       *http.Server
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
	aiClient := NewAIClient(cfg.AIBackend.URL, cfg.AIBackend.APIKey)

	// 创建 OHTTP 处理器
	ohttpHandler, err := NewOHTTPHandler(keyID, privateKey, publicKey, aiClient)
	if err != nil {
		return nil, fmt.Errorf("创建 OHTTP 处理器失败: %w", err)
	}

	node := &ExitNode{
		cfg:          cfg,
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

	mux := http.NewServeMux()

	// 注册 OHTTP 端点
	mux.HandleFunc("/ohttp", e.ohttpHandler.HandleOHTTP)
	mux.HandleFunc("/ohttp-keys", e.ohttpHandler.HandleKeys)

	// 健康检查端点
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	e.server = &http.Server{
		Addr:         e.cfg.Listen,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// 配置 TLS (如果提供了证书)
	if e.cfg.TLS.CertFile != "" && e.cfg.TLS.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(e.cfg.TLS.CertFile, e.cfg.TLS.KeyFile)
		if err != nil {
			return fmt.Errorf("加载 TLS 证书失败: %w", err)
		}
		e.server.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
	}

	// 启动服务器
	log.Printf("Exit 节点启动，监听 %s", e.cfg.Listen)
	log.Printf("AI 后端: %s", e.cfg.AIBackend.URL)

	// 打印公钥，方便客户端配置
	pubKeyConfig := crypto.EncodeKeyConfig(e.keyID, e.publicKey)
	pubKeyBase64 := base64.StdEncoding.EncodeToString(pubKeyConfig)
	log.Printf("")
	log.Printf("Exit 公钥 (供 Client 使用):")
	log.Printf("  %s", pubKeyBase64)
	log.Printf("")
	log.Printf("Client 静态模式连接示例:")
	log.Printf("  tokengo client --relay <RELAY_ADDR> --exit <THIS_EXIT_ADDR> --exit-public-key %s", pubKeyBase64)

	// 优雅关闭
	go e.handleShutdown()

	if e.server.TLSConfig != nil {
		return e.server.ListenAndServeTLS("", "")
	}
	return e.server.ListenAndServe()
}

// handleShutdown 处理优雅关闭
func (e *ExitNode) handleShutdown() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	<-sigChan
	log.Println("收到关闭信号，正在关闭...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := e.server.Shutdown(ctx); err != nil {
		log.Printf("关闭服务器失败: %v", err)
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

	// 停止 HTTP 服务器
	if e.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return e.server.Shutdown(ctx)
	}
	return nil
}
