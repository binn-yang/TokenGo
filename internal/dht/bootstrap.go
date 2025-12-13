package dht

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/binn/tokengo/internal/config"
)

// BootstrapNode Bootstrap 节点
type BootstrapNode struct {
	node *Node
	cfg  *config.BootstrapConfig
}

// NewBootstrapNode 创建 Bootstrap 节点
func NewBootstrapNode(cfg *config.BootstrapConfig) (*BootstrapNode, error) {
	dhtCfg := &Config{
		PrivateKeyPath:   cfg.DHT.PrivateKeyFile,
		BootstrapPeers:   cfg.DHT.BootstrapPeers,
		ListenAddrs:      cfg.DHT.ListenAddrs,
		ExternalAddrs:    cfg.DHT.ExternalAddrs,
		Mode:             "server", // Bootstrap 始终是 server 模式
		UseIPFSBootstrap: cfg.DHT.UseIPFSBootstrap,
	}

	node, err := NewNode(dhtCfg)
	if err != nil {
		return nil, fmt.Errorf("创建 DHT 节点失败: %w", err)
	}

	return &BootstrapNode{
		node: node,
		cfg:  cfg,
	}, nil
}

// Start 启动 Bootstrap 节点
func (b *BootstrapNode) Start(ctx context.Context) error {
	log.Printf("Bootstrap 节点启动中...")

	if err := b.node.Start(ctx); err != nil {
		return fmt.Errorf("启动 DHT 节点失败: %w", err)
	}

	log.Printf("Bootstrap 节点已就绪")
	log.Printf("PeerID: %s", b.node.PeerID())
	log.Printf("完整地址:")
	for _, addr := range b.node.FullAddrs() {
		log.Printf("  %s", addr)
	}

	return nil
}

// Run 运行 Bootstrap 节点 (阻塞直到收到停止信号)
func (b *BootstrapNode) Run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := b.Start(ctx); err != nil {
		return err
	}

	// 等待停止信号
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	<-sigCh
	log.Printf("收到停止信号，正在关闭...")

	return b.Stop()
}

// Stop 停止 Bootstrap 节点
func (b *BootstrapNode) Stop() error {
	return b.node.Stop()
}

// PeerID 返回 PeerID 字符串
func (b *BootstrapNode) PeerID() string {
	return b.node.PeerID().String()
}

// FullAddrs 返回完整地址列表
func (b *BootstrapNode) FullAddrs() []string {
	return b.node.FullAddrs()
}
