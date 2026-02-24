package dht

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/binn/tokengo/internal/identity"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/multiformats/go-multiaddr"
)

// Config DHT 配置
type Config struct {
	// 节点身份
	PrivateKeyPath string `yaml:"private_key_file"`

	// Bootstrap 节点
	BootstrapPeers []string `yaml:"bootstrap_peers"`

	// 网络监听地址
	ListenAddrs []string `yaml:"listen_addrs"`

	// 外部地址 (NAT 后使用)
	ExternalAddrs []string `yaml:"external_addrs,omitempty"`

	// DHT 模式: "server" 或 "client"
	Mode string `yaml:"mode"`

	// 服务类型: "relay", "exit", "" (bootstrap)
	ServiceType string `yaml:"service_type,omitempty"`

	// 协议前缀
	ProtocolPrefix string `yaml:"protocol_prefix,omitempty"`

	// 是否使用 IPFS Bootstrap
	UseIPFSBootstrap bool `yaml:"use_ipfs_bootstrap,omitempty"`
}

// Node DHT 节点
type Node struct {
	host     host.Host
	dht      *dht.IpfsDHT
	identity *identity.Identity
	config   *Config
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.RWMutex
	started  bool
}

// NewNode 创建 DHT 节点
func NewNode(cfg *Config) (*Node, error) {
	// 加载或生成节点身份
	id, err := identity.LoadOrGenerate(cfg.PrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("加载节点身份失败: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Node{
		identity: id,
		config:   cfg,
		ctx:      ctx,
		cancel:   cancel,
	}, nil
}

// Start 启动 DHT 节点
func (n *Node) Start(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.started {
		return nil
	}

	// 解析监听地址
	listenAddrs := make([]multiaddr.Multiaddr, 0, len(n.config.ListenAddrs))
	for _, addr := range n.config.ListenAddrs {
		ma, err := multiaddr.NewMultiaddr(addr)
		if err != nil {
			return fmt.Errorf("解析监听地址失败 %s: %w", addr, err)
		}
		listenAddrs = append(listenAddrs, ma)
	}

	// 创建连接管理器
	connMgr, err := connmgr.NewConnManager(
		100, // 最小连接数
		400, // 最大连接数
		connmgr.WithGracePeriod(time.Minute),
	)
	if err != nil {
		return fmt.Errorf("创建连接管理器失败: %w", err)
	}

	// 确定 DHT 模式选项
	var dhtOpts []dht.Option
	if n.config.Mode == "server" {
		dhtOpts = append(dhtOpts, dht.Mode(dht.ModeServer))
	} else {
		dhtOpts = append(dhtOpts, dht.Mode(dht.ModeClient))
	}
	// 注意: 不设置自定义 ProtocolPrefix，使用标准 IPFS DHT 协议 (/ipfs/kad/1.0.0)
	// 这样才能与公共 IPFS bootstrap 节点交互并填充路由表

	// 创建 libp2p Host
	var kdht *dht.IpfsDHT
	opts := []libp2p.Option{
		libp2p.Identity(n.identity.PrivKey),
		libp2p.ListenAddrs(listenAddrs...),
		libp2p.ConnectionManager(connMgr),
		libp2p.EnableNATService(),
		libp2p.EnableRelay(),
		libp2p.Routing(func(h host.Host) (routing.PeerRouting, error) {
			var err error
			kdht, err = dht.New(ctx, h, dhtOpts...)
			return kdht, err
		}),
	}

	// 添加外部地址 (如果配置了，则只广播外部地址用于服务发现)
	if len(n.config.ExternalAddrs) > 0 {
		extAddrs := make([]multiaddr.Multiaddr, 0, len(n.config.ExternalAddrs))
		for _, addr := range n.config.ExternalAddrs {
			ma, err := multiaddr.NewMultiaddr(addr)
			if err != nil {
				log.Printf("警告: 解析外部地址失败 %s: %v", addr, err)
				continue
			}
			extAddrs = append(extAddrs, ma)
		}
		if len(extAddrs) > 0 {
			opts = append(opts, libp2p.AddrsFactory(func(addrs []multiaddr.Multiaddr) []multiaddr.Multiaddr {
				// 优先使用外部地址，保留本地地址用于直连
				return append(extAddrs, addrs...)
			}))
		}
	}

	h, err := libp2p.New(opts...)
	if err != nil {
		return fmt.Errorf("创建 libp2p Host 失败: %w", err)
	}

	n.host = h
	n.dht = kdht

	// 连接 Bootstrap 节点
	if err := n.connectBootstrapPeers(ctx); err != nil {
		log.Printf("警告: 连接 Bootstrap 节点失败: %v", err)
	}

	// Bootstrap DHT
	if err := n.dht.Bootstrap(ctx); err != nil {
		return fmt.Errorf("Bootstrap DHT 失败: %w", err)
	}

	// 等待路由表填充
	log.Printf("等待 DHT 路由表填充...")
	time.Sleep(5 * time.Second)
	log.Printf("DHT 路由表大小: %d", n.dht.RoutingTable().Size())

	n.started = true
	log.Printf("DHT 节点已启动, PeerID: %s", n.identity.PeerID)
	for _, addr := range n.host.Addrs() {
		log.Printf("  监听: %s/p2p/%s", addr, n.identity.PeerID)
	}

	return nil
}

// connectBootstrapPeers 连接 Bootstrap 节点
func (n *Node) connectBootstrapPeers(ctx context.Context) error {
	var addrInfos []peer.AddrInfo

	// 解析配置的 Bootstrap 节点
	for _, peerAddr := range n.config.BootstrapPeers {
		ma, err := multiaddr.NewMultiaddr(peerAddr)
		if err != nil {
			log.Printf("警告: 解析 Bootstrap 地址失败 %s: %v", peerAddr, err)
			continue
		}
		addrInfo, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			log.Printf("警告: 解析 Bootstrap AddrInfo 失败 %s: %v", peerAddr, err)
			continue
		}
		addrInfos = append(addrInfos, *addrInfo)
	}

	// 可选添加 IPFS Bootstrap 节点
	if n.config.UseIPFSBootstrap {
		for _, ma := range dht.DefaultBootstrapPeers {
			addrInfo, err := peer.AddrInfoFromP2pAddr(ma)
			if err != nil {
				continue
			}
			addrInfos = append(addrInfos, *addrInfo)
		}
	}

	if len(addrInfos) == 0 {
		return nil
	}

	var wg sync.WaitGroup
	for _, info := range addrInfos {
		wg.Add(1)
		go func(info peer.AddrInfo) {
			defer wg.Done()
			if err := n.host.Connect(ctx, info); err != nil {
				log.Printf("警告: 连接 Bootstrap 节点失败 %s: %v", info.ID, err)
			} else {
				log.Printf("已连接 Bootstrap 节点: %s", info.ID)
			}
		}(info)
	}
	wg.Wait()

	return nil
}

// Stop 停止 DHT 节点
func (n *Node) Stop() error {
	n.mu.Lock()
	defer n.mu.Unlock()

	if !n.started {
		return nil
	}

	n.cancel()

	if n.dht != nil {
		if err := n.dht.Close(); err != nil {
			log.Printf("警告: 关闭 DHT 失败: %v", err)
		}
	}

	if n.host != nil {
		if err := n.host.Close(); err != nil {
			return fmt.Errorf("关闭 Host 失败: %w", err)
		}
	}

	n.started = false
	log.Printf("DHT 节点已停止")
	return nil
}

// Host 返回 libp2p Host
func (n *Node) Host() host.Host {
	return n.host
}

// DHT 返回 Kademlia DHT
func (n *Node) DHT() *dht.IpfsDHT {
	return n.dht
}

// PeerID 返回节点 PeerID
func (n *Node) PeerID() peer.ID {
	return n.identity.PeerID
}

// Identity 返回节点身份
func (n *Node) Identity() *identity.Identity {
	return n.identity
}

// Addrs 返回节点地址列表
func (n *Node) Addrs() []multiaddr.Multiaddr {
	if n.host == nil {
		return nil
	}
	return n.host.Addrs()
}

// FullAddrs 返回完整的 p2p 地址 (包含 PeerID)
func (n *Node) FullAddrs() []string {
	if n.host == nil {
		return nil
	}

	addrs := make([]string, 0, len(n.host.Addrs()))
	for _, addr := range n.host.Addrs() {
		addrs = append(addrs, fmt.Sprintf("%s/p2p/%s", addr, n.identity.PeerID))
	}
	return addrs
}

// ConnectedPeers 返回已连接的对等节点数
func (n *Node) ConnectedPeers() int {
	if n.host == nil {
		return 0
	}
	return len(n.host.Network().Peers())
}
