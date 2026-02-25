package dht

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// DefaultBootstrapPeers 硬编码的默认 bootstrap peers (编译进二进制)
// 这些是已部署的 Relay 节点 DHT 地址，作为网络入口
var DefaultBootstrapPeers = []string{
	// 主服务器 DHT 端口 (43.156.60.67:4003)
	"/ip4/43.156.60.67/tcp/4003/p2p/12D3KooWCjYH5XUjVRi6DymRZpLj2pDAFxnK3xJ8gcJQMgswT6fU",
}

// bootstrapJSONURLs GitHub JSON 文件 URL (主 URL + 镜像)
var bootstrapJSONURLs = []string{
	"https://raw.githubusercontent.com/binn-yang/TokenGo/master/bootstrap.json",
	"https://cdn.jsdelivr.net/gh/binn-yang/TokenGo@master/bootstrap.json",
}

// BootstrapPeerList bootstrap.json 的结构
type BootstrapPeerList struct {
	Version int      `json:"version"`
	Peers   []string `json:"peers"`
}

// ProtocolPrefix 私有 DHT 协议前缀
const ProtocolPrefix = "/tokengo"

// ResolveBootstrapPeers 解析 bootstrap peers
// 从三个来源获取并合并：硬编码默认值、GitHub JSON、配置文件
func ResolveBootstrapPeers(ctx context.Context, configPeers []string) []peer.AddrInfo {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	resultChan := make(chan []peer.AddrInfo, 3)
	var wg sync.WaitGroup

	// Layer 1: 硬编码默认 peers
	wg.Add(1)
	go func() {
		defer wg.Done()
		peers := parseMultiaddrs(DefaultBootstrapPeers, "hardcoded")
		resultChan <- peers
	}()

	// Layer 2: 从 GitHub 获取 bootstrap.json
	wg.Add(1)
	go func() {
		defer wg.Done()
		peers := fetchBootstrapPeers(ctx)
		resultChan <- peers
	}()

	// Layer 3: 配置文件中的 peers (最高优先级)
	wg.Add(1)
	go func() {
		defer wg.Done()
		peers := parseMultiaddrs(configPeers, "config")
		resultChan <- peers
	}()

	// 等待所有 goroutine 完成
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// 合并去重 (按 PeerID)
	peerMap := make(map[peer.ID]peer.AddrInfo)
	for peers := range resultChan {
		for _, p := range peers {
			if p.ID != "" && len(p.Addrs) > 0 {
				existing, ok := peerMap[p.ID]
				if ok {
					// 合并地址
					for _, addr := range p.Addrs {
						existing.Addrs = append(existing.Addrs, addr)
					}
					peerMap[p.ID] = existing
				} else {
					peerMap[p.ID] = p
				}
			}
		}
	}

	// 转换为切片
	result := make([]peer.AddrInfo, 0, len(peerMap))
	for _, p := range peerMap {
		result = append(result, p)
	}

	if len(result) > 0 {
		log.Printf("解析到 %d 个 bootstrap peers", len(result))
	}

	return result
}

// parseMultiaddrs 解析 multiaddr 字符串列表
func parseMultiaddrs(addrs []string, source string) []peer.AddrInfo {
	if len(addrs) == 0 {
		return nil
	}

	var result []peer.AddrInfo
	for _, addrStr := range addrs {
		if addrStr == "" {
			continue
		}
		ma, err := multiaddr.NewMultiaddr(addrStr)
		if err != nil {
			log.Printf("警告: 解析 %s multiaddr 失败 %s: %v", source, addrStr, err)
			continue
		}

		addrInfo, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			log.Printf("警告: 解析 %s AddrInfo 失败 %s: %v", source, addrStr, err)
			continue
		}

		result = append(result, *addrInfo)
	}

	return result
}

// fetchBootstrapPeers 从 GitHub 获取 bootstrap.json
func fetchBootstrapPeers(ctx context.Context) []peer.AddrInfo {
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	for _, url := range bootstrapJSONURLs {
		peers, err := fetchFromURL(ctx, client, url)
		if err != nil {
			log.Printf("从 %s 获取 bootstrap.json 失败: %v", url, err)
			continue
		}
		if len(peers) > 0 {
			log.Printf("从 %s 获取到 %d 个 peers", url, len(peers))
			return peers
		}
	}

	return nil
}

// fetchFromURL 从指定 URL 获取并解析 bootstrap.json
func fetchFromURL(ctx context.Context, client *http.Client, url string) ([]peer.AddrInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP 状态码: %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	var peerList BootstrapPeerList
	if err := json.Unmarshal(data, &peerList); err != nil {
		return nil, fmt.Errorf("解析 JSON 失败: %w", err)
	}

	if peerList.Version != 1 {
		return nil, fmt.Errorf("不支持的 bootstrap.json 版本: %d", peerList.Version)
	}

	return parseMultiaddrs(peerList.Peers, "github"), nil
}
