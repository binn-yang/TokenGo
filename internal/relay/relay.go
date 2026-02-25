package relay

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/binn/tokengo/internal/cert"
	"github.com/binn/tokengo/internal/config"
	"github.com/binn/tokengo/internal/dht"
	"github.com/binn/tokengo/internal/identity"
)

// RelayNode 中继节点
type RelayNode struct {
	cfg        *config.RelayConfig
	quicServer *QUICServer
	registry   *Registry
	dhtNode    *dht.Node
	provider   *dht.Provider
	ctx        context.Context
	cancel     context.CancelFunc
}

// New 创建中继节点
func New(cfg *config.RelayConfig) (*RelayNode, error) {
	ctx, cancel := context.WithCancel(context.Background())

	node := &RelayNode{
		cfg:    cfg,
		ctx:    ctx,
		cancel: cancel,
	}

	// 创建 Exit 注册表
	node.registry = NewRegistry()

	// 加载或创建 DHT 身份
	var id*identity.Identity
	var err error

	if cfg.DHT.PrivateKeyFile != "" {
		id, err = identity.LoadOrGenerate(cfg.DHT.PrivateKeyFile)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("加载 DHT 身份失败: %w", err)
		}
	} else {
		// 如果没有配置身份密钥，生成临时身份
		id, err = identity.Generate()
		if err != nil {
			cancel()
			return nil, fmt.Errorf("生成 DHT 身份失败: %w", err)
		}
	}

	// 生成绑定 PeerID 的 TLS 证书（自动生成）
	tlsCert, err := cert.GeneratePeerIDCert(id.PrivKey, "./certs")
	if err != nil {
		cancel()
		return nil, fmt.Errorf("生成 TLS 证书失败: %w", err)
	}
	log.Printf("已自动生成 TLS 证书 (PeerID: %s)", id.PeerID)

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{*tlsCert},
		NextProtos:   []string{"tokengo-relay", "tokengo-exit"},
		MinVersion:   tls.VersionTLS13,
	}

	// DHT 始终启用（私有网络）
	if len(cfg.DHT.ListenAddrs) > 0 || cfg.DHT.PrivateKeyFile != "" {
		dhtCfg := &dht.Config{
			PrivateKeyPath: cfg.DHT.PrivateKeyFile,
			BootstrapPeers: cfg.DHT.BootstrapPeers,
			ListenAddrs:    cfg.DHT.ListenAddrs,
			ExternalAddrs:  cfg.DHT.ExternalAddrs,
			Mode:           "server",
			ServiceType:    "relay",
		}

		dhtNode, err := dht.NewNode(dhtCfg)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("创建 DHT 节点失败: %w", err)
		}
		node.dhtNode = dhtNode
		node.provider = dht.NewProvider(dhtNode, "relay")
	}

	// 创建 QUIC 服务器
	node.quicServer = NewQUICServer(cfg.Listen, tlsConfig, node.registry)

	return node, nil
}

// Start 启动中继节点
func (r *RelayNode) Start() error {
	log.Printf("Relay 节点启动")
	log.Printf("监听地址: %s", r.cfg.Listen)
	log.Printf("模式: 反向隧道 (Exit 主动连接注册)")

	// 启动 Registry 清理任务
	r.registry.StartCleanup(r.ctx, 60*time.Second)

	// 启动 DHT 节点（仅用于注册 Relay 服务）
	if r.dhtNode != nil {
		if err := r.dhtNode.Start(r.ctx); err != nil {
			return fmt.Errorf("启动 DHT 节点失败: %w", err)
		}
		log.Printf("DHT PeerID: %s", r.dhtNode.PeerID())

		// 注册 Relay 服务到 DHT (供 Client 发现)
		serviceInfo := &dht.ServiceInfo{
			PeerID:      r.dhtNode.PeerID(),
			ServiceType: "relay",
			Addrs:       r.dhtNode.FullAddrs(),
		}
		if err := r.provider.Register(serviceInfo); err != nil {
			log.Printf("警告: 注册服务到 DHT 失败: %v", err)
		}
	}

	// 处理关闭信号
	go r.handleShutdown()

	// 启动 QUIC 服务器
	return r.quicServer.Start(r.ctx)
}

// handleShutdown 处理优雅关闭
func (r *RelayNode) handleShutdown() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	<-sigChan
	log.Println("收到关闭信号，正在关闭...")
	r.Stop()
}

// Stop 停止中继节点
func (r *RelayNode) Stop() error {
	// 停止 DHT 服务
	if r.provider != nil {
		r.provider.Unregister()
	}
	if r.dhtNode != nil {
		r.dhtNode.Stop()
	}

	r.cancel()
	return r.quicServer.Stop()
}

// Ready 返回就绪信号 channel，当 Relay 节点成功启动后会关闭该 channel
func (r *RelayNode) Ready() <-chan struct{} {
	return r.quicServer.Ready()
}
