package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// NodeInfo 节点信息
type NodeInfo struct {
	Address   string `json:"address"`              // 节点地址 (host:port)
	Type      string `json:"type"`                 // 节点类型: "relay" 或 "exit"
	PublicKey []byte `json:"public_key,omitempty"` // OHTTP 公钥 (仅 Exit)
}

// NodesResponse Bootstrap API 响应
type NodesResponse struct {
	Relays []NodeInfo `json:"relays"`
	Exits  []NodeInfo `json:"exits"`
}

// Client Bootstrap API 客户端
type Client struct {
	baseURL    string
	httpClient *http.Client
	cache      *nodeCache
	interval   time.Duration
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

// nodeCache 节点缓存
type nodeCache struct {
	mu     sync.RWMutex
	relays []NodeInfo
	exits  []NodeInfo
	ttl    time.Time
}

// NewClient 创建 Bootstrap API 客户端
func NewClient(baseURL string, interval time.Duration) *Client {
	if interval == 0 {
		interval = 2 * time.Minute
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		cache:    &nodeCache{},
		interval: interval,
		ctx:      ctx,
		cancel:   cancel,
	}
}

// Start 启动后台刷新
func (c *Client) Start() {
	c.wg.Add(1)
	go c.refreshLoop()
}

// Stop 停止客户端
func (c *Client) Stop() {
	c.cancel()
	c.wg.Wait()
}

// refreshLoop 定期刷新节点列表
func (c *Client) refreshLoop() {
	defer c.wg.Done()

	// 立即获取一次
	c.refresh()

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.refresh()
		}
	}
}

// refresh 刷新节点列表
func (c *Client) refresh() {
	nodes, err := c.fetchNodes()
	if err != nil {
		log.Printf("Bootstrap API 刷新失败: %v", err)
		return
	}

	c.cache.mu.Lock()
	c.cache.relays = nodes.Relays
	c.cache.exits = nodes.Exits
	c.cache.ttl = time.Now().Add(c.interval)
	c.cache.mu.Unlock()

	log.Printf("Bootstrap: 发现 %d 个 Relay, %d 个 Exit", len(nodes.Relays), len(nodes.Exits))
}

// fetchNodes 从 API 获取节点列表
func (c *Client) fetchNodes() (*NodesResponse, error) {
	url := fmt.Sprintf("%s/nodes", c.baseURL)

	req, err := http.NewRequestWithContext(c.ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API 返回错误: %d - %s", resp.StatusCode, string(body))
	}

	var nodes NodesResponse
	if err := json.NewDecoder(resp.Body).Decode(&nodes); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}

	return &nodes, nil
}

// GetRelays 获取 Relay 节点列表
func (c *Client) GetRelays() []NodeInfo {
	c.cache.mu.RLock()
	defer c.cache.mu.RUnlock()

	result := make([]NodeInfo, len(c.cache.relays))
	copy(result, c.cache.relays)
	return result
}

// GetExits 获取 Exit 节点列表
func (c *Client) GetExits() []NodeInfo {
	c.cache.mu.RLock()
	defer c.cache.mu.RUnlock()

	result := make([]NodeInfo, len(c.cache.exits))
	copy(result, c.cache.exits)
	return result
}

// HasNodes 是否有缓存的节点
func (c *Client) HasNodes() bool {
	c.cache.mu.RLock()
	defer c.cache.mu.RUnlock()
	return len(c.cache.relays) > 0 || len(c.cache.exits) > 0
}

// DiscoverRelays 发现 Relay 节点 (兼容 DHT 接口)
func (c *Client) DiscoverRelays(ctx context.Context) ([]NodeInfo, error) {
	// 检查缓存
	c.cache.mu.RLock()
	if time.Now().Before(c.cache.ttl) && len(c.cache.relays) > 0 {
		result := make([]NodeInfo, len(c.cache.relays))
		copy(result, c.cache.relays)
		c.cache.mu.RUnlock()
		return result, nil
	}
	c.cache.mu.RUnlock()

	// 重新获取
	nodes, err := c.fetchNodes()
	if err != nil {
		return nil, err
	}

	c.cache.mu.Lock()
	c.cache.relays = nodes.Relays
	c.cache.exits = nodes.Exits
	c.cache.ttl = time.Now().Add(c.interval)
	c.cache.mu.Unlock()

	return nodes.Relays, nil
}
