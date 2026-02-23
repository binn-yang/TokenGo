package dht

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multihash"
)

const (
	// 发现超时
	DiscoveryTimeout = 30 * time.Second
	// 缓存刷新间隔
	CacheRefreshInterval = 2 * time.Minute
	// 最大发现数量
	MaxDiscoveryCount = 20
)

// ExitNodeInfo Exit 节点信息（含公钥）
type ExitNodeInfo struct {
	peer.AddrInfo
	PublicKey []byte // OHTTP 公钥
	KeyID     uint8  // OHTTP KeyID
}

// Discovery 服务发现器
type Discovery struct {
	node  *Node
	cache *serviceCache
	ctx   context.Context
	cancel context.CancelFunc
	wg    sync.WaitGroup
}

// serviceCache 服务缓存
type serviceCache struct {
	mu            sync.RWMutex
	relays        []peer.AddrInfo
	exits         []peer.AddrInfo
	exitsWithKeys []ExitNodeInfo // Exit 节点（含公钥）
	relayTTL      time.Time
	exitTTL       time.Time
	exitKeysTTL   time.Time
}

// NewDiscovery 创建服务发现器
func NewDiscovery(node *Node) *Discovery {
	ctx, cancel := context.WithCancel(context.Background())
	return &Discovery{
		node:   node,
		cache:  &serviceCache{},
		ctx:    ctx,
		cancel: cancel,
	}
}

// Start 启动后台发现任务
func (d *Discovery) Start() {
	d.wg.Add(1)
	go d.refreshLoop()
}

// Stop 停止发现器
func (d *Discovery) Stop() {
	d.cancel()
	d.wg.Wait()
}

// refreshLoop 定期刷新服务缓存
func (d *Discovery) refreshLoop() {
	defer d.wg.Done()

	// 启动时立即发现
	d.refreshRelays()
	d.refreshExits()

	ticker := time.NewTicker(CacheRefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
			d.refreshRelays()
			d.refreshExits()
		}
	}
}

// refreshRelays 刷新 Relay 缓存
func (d *Discovery) refreshRelays() {
	ctx, cancel := context.WithTimeout(d.ctx, DiscoveryTimeout)
	defer cancel()

	peers, err := d.findProviders(ctx, RelayServiceNamespace)
	if err != nil {
		log.Printf("警告: 发现 Relay 节点失败: %v", err)
		return
	}

	d.cache.mu.Lock()
	d.cache.relays = peers
	d.cache.relayTTL = time.Now().Add(CacheRefreshInterval)
	d.cache.mu.Unlock()

	if len(peers) > 0 {
		log.Printf("发现 %d 个 Relay 节点", len(peers))
	}
}

// refreshExits 刷新 Exit 缓存
func (d *Discovery) refreshExits() {
	ctx, cancel := context.WithTimeout(d.ctx, DiscoveryTimeout)
	defer cancel()

	peers, err := d.findProviders(ctx, ExitServiceNamespace)
	if err != nil {
		log.Printf("警告: 发现 Exit 节点失败: %v", err)
		return
	}

	d.cache.mu.Lock()
	d.cache.exits = peers
	d.cache.exitTTL = time.Now().Add(CacheRefreshInterval)
	d.cache.mu.Unlock()

	if len(peers) > 0 {
		log.Printf("发现 %d 个 Exit 节点", len(peers))
	}
}

// findProviders 查找服务提供者
func (d *Discovery) findProviders(ctx context.Context, namespace string) ([]peer.AddrInfo, error) {
	// 创建服务 CID
	hash, err := multihash.Sum([]byte(namespace), multihash.SHA2_256, -1)
	if err != nil {
		return nil, fmt.Errorf("创建哈希失败: %w", err)
	}
	c := cid.NewCidV1(cid.Raw, hash)

	// 查找提供者
	peerChan := d.node.DHT().FindProvidersAsync(ctx, c, MaxDiscoveryCount)

	var peers []peer.AddrInfo
	for p := range peerChan {
		if p.ID == d.node.PeerID() {
			// 跳过自己
			continue
		}
		if len(p.Addrs) > 0 {
			peers = append(peers, p)
		}
	}

	return peers, nil
}

// DiscoverRelays 发现 Relay 节点
func (d *Discovery) DiscoverRelays(ctx context.Context) ([]peer.AddrInfo, error) {
	// 检查缓存
	d.cache.mu.RLock()
	if time.Now().Before(d.cache.relayTTL) && len(d.cache.relays) > 0 {
		peers := make([]peer.AddrInfo, len(d.cache.relays))
		copy(peers, d.cache.relays)
		d.cache.mu.RUnlock()
		return peers, nil
	}
	d.cache.mu.RUnlock()

	// 重新发现
	peers, err := d.findProviders(ctx, RelayServiceNamespace)
	if err != nil {
		return nil, err
	}

	// 更新缓存
	d.cache.mu.Lock()
	d.cache.relays = peers
	d.cache.relayTTL = time.Now().Add(CacheRefreshInterval)
	d.cache.mu.Unlock()

	return peers, nil
}

// DiscoverExits 发现 Exit 节点
func (d *Discovery) DiscoverExits(ctx context.Context) ([]peer.AddrInfo, error) {
	// 检查缓存
	d.cache.mu.RLock()
	if time.Now().Before(d.cache.exitTTL) && len(d.cache.exits) > 0 {
		peers := make([]peer.AddrInfo, len(d.cache.exits))
		copy(peers, d.cache.exits)
		d.cache.mu.RUnlock()
		return peers, nil
	}
	d.cache.mu.RUnlock()

	// 重新发现
	peers, err := d.findProviders(ctx, ExitServiceNamespace)
	if err != nil {
		return nil, err
	}

	// 更新缓存
	d.cache.mu.Lock()
	d.cache.exits = peers
	d.cache.exitTTL = time.Now().Add(CacheRefreshInterval)
	d.cache.mu.Unlock()

	return peers, nil
}

// DiscoverExitsWithKeys 发现 Exit 节点并获取公钥
func (d *Discovery) DiscoverExitsWithKeys(ctx context.Context) ([]ExitNodeInfo, error) {
	// 检查缓存
	d.cache.mu.RLock()
	if time.Now().Before(d.cache.exitKeysTTL) && len(d.cache.exitsWithKeys) > 0 {
		exits := make([]ExitNodeInfo, len(d.cache.exitsWithKeys))
		copy(exits, d.cache.exitsWithKeys)
		d.cache.mu.RUnlock()
		return exits, nil
	}
	d.cache.mu.RUnlock()

	// 发现 Exit 节点
	peers, err := d.findProviders(ctx, ExitServiceNamespace)
	if err != nil {
		return nil, err
	}

	// 获取每个 Exit 的公钥
	var exits []ExitNodeInfo
	for _, p := range peers {
		// 从 DHT 获取公钥
		key := ExitPubKeyPrefix + p.ID.String()
		value, err := d.node.DHT().GetValue(ctx, key)
		if err != nil {
			log.Printf("警告: 获取 Exit %s 公钥失败: %v", p.ID, err)
			continue
		}

		var keyInfo ExitKeyInfo
		if err := json.Unmarshal(value, &keyInfo); err != nil {
			log.Printf("警告: 解析 Exit %s 公钥失败: %v", p.ID, err)
			continue
		}

		exits = append(exits, ExitNodeInfo{
			AddrInfo:  p,
			PublicKey: keyInfo.PublicKey,
			KeyID:     keyInfo.KeyID,
		})
	}

	// 更新缓存
	d.cache.mu.Lock()
	d.cache.exitsWithKeys = exits
	d.cache.exitKeysTTL = time.Now().Add(CacheRefreshInterval)
	d.cache.mu.Unlock()

	if len(exits) > 0 {
		log.Printf("发现 %d 个 Exit 节点（含公钥）", len(exits))
	}

	return exits, nil
}

// GetCachedExitsWithKeys 获取缓存的 Exit 节点（含公钥）
func (d *Discovery) GetCachedExitsWithKeys() []ExitNodeInfo {
	d.cache.mu.RLock()
	defer d.cache.mu.RUnlock()

	exits := make([]ExitNodeInfo, len(d.cache.exitsWithKeys))
	copy(exits, d.cache.exitsWithKeys)
	return exits
}

// GetCachedRelays 获取缓存的 Relay 节点
func (d *Discovery) GetCachedRelays() []peer.AddrInfo {
	d.cache.mu.RLock()
	defer d.cache.mu.RUnlock()

	peers := make([]peer.AddrInfo, len(d.cache.relays))
	copy(peers, d.cache.relays)
	return peers
}

// GetCachedExits 获取缓存的 Exit 节点
func (d *Discovery) GetCachedExits() []peer.AddrInfo {
	d.cache.mu.RLock()
	defer d.cache.mu.RUnlock()

	peers := make([]peer.AddrInfo, len(d.cache.exits))
	copy(peers, d.cache.exits)
	return peers
}

// RelayCount 返回已发现的 Relay 数量
func (d *Discovery) RelayCount() int {
	d.cache.mu.RLock()
	defer d.cache.mu.RUnlock()
	return len(d.cache.relays)
}

// ExitCount 返回已发现的 Exit 数量
func (d *Discovery) ExitCount() int {
	d.cache.mu.RLock()
	defer d.cache.mu.RUnlock()
	return len(d.cache.exits)
}
