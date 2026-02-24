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
	// 服务命名空间
	RelayServiceNamespace = "/tokengo/relay/v1"
	ExitServiceNamespace  = "/tokengo/exit/v1"

	// Exit 公钥存储前缀
	ExitPubKeyPrefix = "/tokengo/exit-pubkey/"

	// Provider 刷新间隔
	ProviderRefreshInterval = 3 * time.Minute
)

// ExitKeyInfo Exit 节点公钥信息（存储在 DHT 中）
type ExitKeyInfo struct {
	KeyID     uint8  `json:"key_id"`
	PublicKey []byte `json:"public_key"`
}

// ServiceInfo 服务信息
type ServiceInfo struct {
	PeerID      peer.ID
	ServiceType string // "relay" or "exit"
	Addrs       []string
	PublicKey   []byte // OHTTP 公钥 (仅 Exit)
	KeyID       uint8  // OHTTP KeyID (仅 Exit)
}

// Provider 服务提供者管理
type Provider struct {
	node        *Node
	serviceType string
	namespace   string
	serviceInfo *ServiceInfo
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	mu          sync.RWMutex
	registered  bool
}

// NewProvider 创建服务提供者
func NewProvider(node *Node, serviceType string) *Provider {
	var namespace string
	switch serviceType {
	case "relay":
		namespace = RelayServiceNamespace
	case "exit":
		namespace = ExitServiceNamespace
	default:
		namespace = "/tokengo/" + serviceType + "/v1"
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Provider{
		node:        node,
		serviceType: serviceType,
		namespace:   namespace,
		ctx:         ctx,
		cancel:      cancel,
	}
}

// Register 注册服务到 DHT（带重试）
func (p *Provider) Register(info *ServiceInfo) error {
	p.mu.Lock()
	p.serviceInfo = info
	p.mu.Unlock()

	// 创建服务 CID
	c, err := p.createServiceCID()
	if err != nil {
		return fmt.Errorf("创建服务 CID 失败: %w", err)
	}

	// 带重试的注册（等待 DHT 路由表填充）
	backoff := 3 * time.Second
	const maxBackoff = 30 * time.Second
	const maxRetries = 5

	var lastErr error
	for i := 0; i < maxRetries; i++ {
		if i > 0 {
			log.Printf("DHT 注册重试 (%d/%d)，等待 %v...", i+1, maxRetries, backoff)
			select {
			case <-time.After(backoff):
			case <-p.ctx.Done():
				return fmt.Errorf("服务已停止")
			}
			backoff = time.Duration(float64(backoff) * 2)
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}

		if err := p.node.DHT().Provide(p.ctx, c, true); err != nil {
			lastErr = err
			log.Printf("DHT Provide 失败: %v", err)
			continue
		}

		// 注册成功
		lastErr = nil
		break
	}

	if lastErr != nil {
		return fmt.Errorf("注册服务失败（重试 %d 次）: %w", maxRetries, lastErr)
	}

	// Exit 节点：存储公钥到 DHT
	if info.PublicKey != nil && len(info.PublicKey) > 0 {
		if err := p.storePublicKey(p.ctx); err != nil {
			log.Printf("警告: 存储公钥到 DHT 失败: %v", err)
		} else {
			log.Printf("已存储 Exit 公钥到 DHT")
		}
	}

	p.mu.Lock()
	p.registered = true
	p.mu.Unlock()
	log.Printf("已注册服务到 DHT: %s (PeerID: %s)", p.namespace, p.node.PeerID())

	// 启动心跳刷新
	p.wg.Add(1)
	go p.refreshLoop()

	return nil
}

// createServiceCID 创建服务标识 CID
func (p *Provider) createServiceCID() (cid.Cid, error) {
	// 使用命名空间创建 CID
	hash, err := multihash.Sum([]byte(p.namespace), multihash.SHA2_256, -1)
	if err != nil {
		return cid.Cid{}, err
	}
	return cid.NewCidV1(cid.Raw, hash), nil
}

// storePublicKey 存储 Exit 公钥到 DHT
func (p *Provider) storePublicKey(ctx context.Context) error {
	if p.serviceInfo == nil || p.serviceInfo.PublicKey == nil {
		return nil
	}

	// 构建 DHT key
	key := ExitPubKeyPrefix + p.node.PeerID().String()

	// 序列化公钥信息
	info := ExitKeyInfo{
		KeyID:     p.serviceInfo.KeyID,
		PublicKey: p.serviceInfo.PublicKey,
	}
	value, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("序列化公钥信息失败: %w", err)
	}

	// 存储到 DHT
	if err := p.node.DHT().PutValue(ctx, key, value); err != nil {
		return fmt.Errorf("存储到 DHT 失败: %w", err)
	}

	return nil
}

// refreshLoop 定期刷新 Provider 记录
func (p *Provider) refreshLoop() {
	defer p.wg.Done()

	ticker := time.NewTicker(ProviderRefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.mu.RLock()
			if !p.registered {
				p.mu.RUnlock()
				continue
			}
			serviceInfo := p.serviceInfo
			p.mu.RUnlock()

			c, err := p.createServiceCID()
			if err != nil {
				log.Printf("警告: 刷新服务 CID 失败: %v", err)
				continue
			}

			if err := p.node.DHT().Provide(p.ctx, c, true); err != nil {
				log.Printf("警告: 刷新服务注册失败: %v", err)
			} else {
				log.Printf("已刷新服务注册: %s", p.namespace)
			}

			// Exit 节点：刷新公钥
			if serviceInfo != nil && serviceInfo.PublicKey != nil {
				if err := p.storePublicKey(p.ctx); err != nil {
					log.Printf("警告: 刷新公钥失败: %v", err)
				}
			}
		}
	}
}

// Unregister 取消注册服务
func (p *Provider) Unregister() {
	p.mu.Lock()
	p.registered = false
	p.mu.Unlock()

	p.cancel()
	p.wg.Wait()

	log.Printf("已取消服务注册: %s", p.namespace)
}

// ServiceType 返回服务类型
func (p *Provider) ServiceType() string {
	return p.serviceType
}

// Namespace 返回服务命名空间
func (p *Provider) Namespace() string {
	return p.namespace
}

// IsRegistered 返回是否已注册
func (p *Provider) IsRegistered() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.registered
}
