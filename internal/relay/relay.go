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

	"github.com/binn/tokengo/internal/config"
	"github.com/binn/tokengo/internal/dht"
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
		NextProtos:   []string{"tokengo-relay", "tokengo-exit"}, // 支持 Client 和 Exit 两种 ALPN
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

	// 创建 Exit 注册表（替代原有的 Forwarder）
	node.registry = NewRegistry()

	// DHT 始终启用（私有网络）
	// 如果配置了 DHT 相关字段，则启动 DHT 节点
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
