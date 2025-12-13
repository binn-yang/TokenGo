package relay

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/binn/tokengo/internal/config"
	"github.com/binn/tokengo/internal/dht"
)

// RelayNode 中继节点
type RelayNode struct {
	cfg        *config.RelayConfig
	quicServer *QUICServer
	forwarder  *Forwarder
	dhtNode    *dht.Node
	provider   *dht.Provider
	ctx        context.Context
	cancel     context.CancelFunc
}

// New 创建中继节点
func New(cfg *config.RelayConfig) (*RelayNode, error) {
	// 加载 TLS 证书
	if cfg.TLS.CertFile == "" || cfg.TLS.KeyFile == "" {
		return nil, fmt.Errorf("TLS 证书配置缺失")
	}

	cert, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("加载 TLS 证书失败: %w", err)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"tokengo-relay"}, // ALPN 协议标识
		MinVersion:   tls.VersionTLS13,
	}

	// 警告：如果启用了不安全模式
	if cfg.InsecureSkipVerify {
		log.Println("警告: TLS 证书验证已禁用，仅用于开发环境！")
	}

	ctx, cancel := context.WithCancel(context.Background())

	node := &RelayNode{
		cfg:    cfg,
		ctx:    ctx,
		cancel: cancel,
	}

	// 盲转发模式：Exit 地址由 Client 指定
	// Relay 只负责转发，不再发现 Exit 节点
	node.forwarder = NewForwarder(cfg.InsecureSkipVerify)

	// 如果启用了 DHT，仅用于注册 Relay 服务供 Client 发现
	if cfg.DHT.Enabled {
		dhtCfg := &dht.Config{
			PrivateKeyPath:   cfg.DHT.PrivateKeyFile,
			BootstrapPeers:   cfg.DHT.BootstrapPeers,
			ListenAddrs:      cfg.DHT.ListenAddrs,
			ExternalAddrs:    cfg.DHT.ExternalAddrs,
			Mode:             "server",
			ServiceType:      "relay",
			UseIPFSBootstrap: cfg.DHT.UseIPFSBootstrap,
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
	node.quicServer = NewQUICServer(cfg.Listen, tlsConfig, node.forwarder)

	return node, nil
}

// Start 启动中继节点
func (r *RelayNode) Start() error {
	log.Printf("Relay 节点启动")
	log.Printf("监听地址: %s", r.cfg.Listen)
	log.Printf("模式: 盲转发 (Exit 地址由 Client 指定)")

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
