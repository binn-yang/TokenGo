package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/binn/tokengo/internal/bootstrap"
	"github.com/binn/tokengo/internal/config"
	"github.com/binn/tokengo/internal/crypto"
	"github.com/binn/tokengo/internal/dht"
	"github.com/libp2p/go-libp2p/core/peer"
)

// LocalProxy 本地 HTTP 代理服务器
type LocalProxy struct {
	cfg             *config.ClientConfig
	client          *Client
	server          *http.Server
	dhtNode         *dht.Node
	bootstrapClient *bootstrap.Client
}

// NewLocalProxy 创建本地代理
func NewLocalProxy(cfg *config.ClientConfig) (*LocalProxy, error) {
	// 警告：如果启用了不安全模式
	if cfg.InsecureSkipVerify {
		log.Println("警告: TLS 证书验证已禁用，仅用于开发环境！")
	}

	proxy := &LocalProxy{
		cfg: cfg,
	}

	// DHT 动态发现模式
	log.Println("模式: DHT 动态发现")

	// 创建 DHT 节点（如果启用）
	if cfg.DHT.Enabled {
		dhtCfg := &dht.Config{
			PrivateKeyPath:   cfg.DHT.PrivateKeyFile,
			BootstrapPeers:   cfg.DHT.BootstrapPeers,
			ListenAddrs:      cfg.DHT.ListenAddrs,
			Mode:             "client",
			ServiceType:      "client",
			UseIPFSBootstrap: cfg.DHT.UseIPFSBootstrap,
		}

		dhtNode, err := dht.NewNode(dhtCfg)
		if err != nil {
			return nil, fmt.Errorf("创建 DHT 节点失败: %w", err)
		}
		proxy.dhtNode = dhtNode
		log.Println("DHT 发现: 已启用")
	}

	// 创建 Bootstrap API 客户端（如果配置）
	if cfg.Bootstrap.URL != "" {
		proxy.bootstrapClient = bootstrap.NewClient(cfg.Bootstrap.URL, cfg.Bootstrap.Interval)
		log.Printf("Bootstrap API: %s", cfg.Bootstrap.URL)
	}

	// 创建 Client（不预设 Relay/Exit，后续动态发现）
	client, err := NewClientDynamic(cfg.InsecureSkipVerify)
	if err != nil {
		if proxy.dhtNode != nil {
			proxy.dhtNode.Stop()
		}
		return nil, fmt.Errorf("创建客户端失败: %w", err)
	}
	proxy.client = client

	return proxy, nil
}

// NewStaticProxy 创建静态模式代理 (用于 serve 命令)
func NewStaticProxy(listen, relayAddr string, keyID uint8, publicKey []byte, insecureSkipVerify bool) (*LocalProxy, error) {
	client, err := NewClient(relayAddr, keyID, publicKey, insecureSkipVerify)
	if err != nil {
		return nil, fmt.Errorf("创建客户端失败: %w", err)
	}

	cfg := &config.ClientConfig{
		Listen:             listen,
		InsecureSkipVerify: insecureSkipVerify,
	}

	return &LocalProxy{
		cfg:    cfg,
		client: client,
	}, nil
}

// Start 启动本地代理服务器
func (p *LocalProxy) Start() error {
	ctx := context.Background()

	// 启动 DHT 节点（如果启用）
	if p.dhtNode != nil {
		if err := p.dhtNode.Start(ctx); err != nil {
			return fmt.Errorf("启动 DHT 节点失败: %w", err)
		}
		log.Printf("DHT 节点已启动, PeerID: %s", p.dhtNode.PeerID())
	}

	// 启动 Bootstrap API 客户端（如果配置）
	if p.bootstrapClient != nil {
		p.bootstrapClient.Start()
		log.Println("Bootstrap API 客户端已启动")
	}

	log.Printf("本地代理启动，监听 %s", p.cfg.Listen)

	// 动态发现并连接
	if p.client.GetRelayAddr() == "" {
		// 需要动态发现 Relay
		if err := p.discoverAndConnect(ctx); err != nil {
			log.Printf("警告: 节点发现失败: %v (将在首次请求时重试)", err)
		}
	} else {
		// 静态模式，直接连接
		if err := p.client.Connect(ctx); err != nil {
			log.Printf("警告: 连接 Relay 失败: %v (将在首次请求时重试)", err)
		} else {
			log.Printf("已连接到 Relay: %s", p.client.GetRelayAddr())
			log.Printf("Exit 公钥哈希: %s", p.client.GetExitPubKeyHash())
		}
	}

	mux := http.NewServeMux()

	// 统一路由：协议无关的透明转发
	mux.HandleFunc("/", p.handleRequest)

	p.server = &http.Server{
		Addr:         p.cfg.Listen,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// 处理关闭信号
	go p.handleShutdown()

	return p.server.ListenAndServe()
}

// discoverAndConnect 发现节点并连接 (DHT → Bootstrap API → Fallback)
// 新架构：Exit 公钥直接从 DHT/Bootstrap 获取，不再通过 Relay
func (p *LocalProxy) discoverAndConnect(ctx context.Context) error {
	var relayAddr string
	var exitInfo *dht.ExitNodeInfo

	// 1. 尝试 DHT 发现（Relay + Exit 含公钥）
	if p.dhtNode != nil {
		discovery := dht.NewDiscovery(p.dhtNode)

		// 发现 Relay
		relays, err := discovery.DiscoverRelays(ctx)
		if err == nil && len(relays) > 0 {
			addr := extractRelayAddrFromPeerInfo(relays[0])
			if addr != "" {
				relayAddr = addr
				log.Printf("DHT 发现 Relay: %s", relayAddr)
			}
		} else {
			log.Printf("DHT 发现 Relay 失败: %v", err)
		}

		// 发现 Exit（含公钥）
		exits, err := discovery.DiscoverExitsWithKeys(ctx)
		if err == nil && len(exits) > 0 {
			exitInfo = &exits[0]
			log.Printf("DHT 发现 Exit (含公钥, KeyID: %d)", exitInfo.KeyID)
		} else {
			log.Printf("DHT 发现 Exit 失败: %v", err)
		}
	}

	// 2. 尝试 Bootstrap API（Relay + Exit 含公钥）
	if p.bootstrapClient != nil {
		// 发现 Relay
		if relayAddr == "" {
			relays, err := p.bootstrapClient.DiscoverRelays(ctx)
			if err == nil && len(relays) > 0 {
				relayAddr = relays[0].Address
				log.Printf("Bootstrap API 发现 Relay: %s", relayAddr)
			} else {
				log.Printf("Bootstrap API 发现 Relay 失败: %v", err)
			}
		}

		// 发现 Exit（含公钥）
		if exitInfo == nil {
			exits := p.bootstrapClient.GetExits()
			if len(exits) > 0 && exits[0].PublicKey != nil {
				exitInfo = &dht.ExitNodeInfo{
					PublicKey: exits[0].PublicKey,
				}
				log.Printf("Bootstrap API 发现 Exit (含公钥)")
			}
		}
	}

	// 3. 使用回退地址
	if relayAddr == "" && len(p.cfg.Fallback.RelayAddrs) > 0 {
		relayAddr = p.cfg.Fallback.RelayAddrs[0]
		log.Printf("使用回退 Relay: %s", relayAddr)
	}

	// Exit 回退：如果 DHT/Bootstrap 都没有发现 Exit，使用配置的回退 Exit
	if exitInfo == nil && len(p.cfg.Fallback.Exits) > 0 {
		fallbackExit := p.cfg.Fallback.Exits[0]
		keyID, pubKey, err := crypto.LoadPublicKeyConfig(fallbackExit.PublicKey)
		if err != nil {
			return fmt.Errorf("解码回退 Exit 公钥失败: %w", err)
		}
		exitInfo = &dht.ExitNodeInfo{
			PublicKey: pubKey,
			KeyID:     keyID,
		}
		log.Printf("使用回退 Exit (KeyID: %d)", exitInfo.KeyID)
	}

	// 验证发现结果
	if relayAddr == "" {
		return fmt.Errorf("无法发现 Relay 节点")
	}
	if exitInfo == nil || exitInfo.PublicKey == nil {
		return fmt.Errorf("无法发现 Exit 节点（含公钥）")
	}

	// 设置 Exit（公钥已在发现时获取）
	if err := p.client.SetExit(exitInfo.KeyID, exitInfo.PublicKey); err != nil {
		return fmt.Errorf("设置 Exit 失败: %w", err)
	}
	log.Printf("已选择 Exit 公钥哈希: %s", p.client.GetExitPubKeyHash())

	// 设置 Relay 并连接
	p.client.SetRelay(relayAddr)
	if err := p.client.Connect(ctx); err != nil {
		return fmt.Errorf("连接 Relay 失败: %w", err)
	}
	log.Printf("已连接到 Relay: %s", relayAddr)

	return nil
}

// extractRelayAddrFromPeerInfo 从 peer.AddrInfo 提取 host:port 地址
func extractRelayAddrFromPeerInfo(info peer.AddrInfo) string {
	for _, addr := range info.Addrs {
		addrStr := addr.String()
		// 解析 multiaddr 格式: /ip4/x.x.x.x/tcp/4433 或 /ip4/x.x.x.x/udp/4433/quic
		parts := strings.Split(addrStr, "/")
		var ip, port string
		for i := 0; i < len(parts)-1; i++ {
			if parts[i] == "ip4" || parts[i] == "ip6" {
				ip = parts[i+1]
			}
			if parts[i] == "tcp" || parts[i] == "udp" {
				port = parts[i+1]
			}
		}
		if ip != "" && port != "" {
			return fmt.Sprintf("%s:%s", ip, port)
		}
	}
	return ""
}

// handleRequest 统一请求处理 (协议无关)
func (p *LocalProxy) handleRequest(w http.ResponseWriter, r *http.Request) {
	// 读取请求体
	var body []byte
	var err error
	if r.Body != nil {
		body, err = io.ReadAll(r.Body)
		if err != nil {
			p.writeError(w, "读取请求失败", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()
	}

	// 检测是否为流式请求
	if detectStreaming(body, r) {
		p.handleStreamingRequest(w, r, body)
		return
	}

	// 非流式: 收集 headers 并转发
	headers := make(map[string]string)
	for key := range r.Header {
		headers[key] = r.Header.Get(key)
	}

	ctx, cancel := context.WithTimeout(r.Context(), p.getTimeout())
	defer cancel()

	respBody, statusCode, err := p.client.SendRequestRaw(ctx, r.Method, r.URL.Path, body, headers)
	if err != nil {
		log.Printf("请求失败: %v", err)
		p.writeError(w, "请求转发失败", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	w.Write(respBody)
}

// detectStreaming 协议无关的流式请求检测
func detectStreaming(body []byte, r *http.Request) bool {
	// 1. body 中包含 "stream":true 或 "stream": true (覆盖 OpenAI、Claude)
	if len(body) > 0 {
		// 简单字节匹配，避免解析整个 JSON
		if bytes.Contains(body, []byte(`"stream":true`)) ||
			bytes.Contains(body, []byte(`"stream": true`)) {
			return true
		}
	}

	// 2. URL 路径包含 "stream" (覆盖 Gemini 的 streamGenerateContent)
	if strings.Contains(r.URL.Path, "stream") {
		return true
	}

	// 3. Accept header 包含 text/event-stream (显式声明)
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		return true
	}

	return false
}

// handleStreamingRequest 处理流式请求
func (p *LocalProxy) handleStreamingRequest(w http.ResponseWriter, r *http.Request, body []byte) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		p.writeError(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// 构建 HTTP 请求，使用原始路径透明转发
	httpReq, err := http.NewRequestWithContext(r.Context(), r.Method, "http://ai-backend"+r.URL.Path, bytes.NewReader(body))
	if err != nil {
		p.writeError(w, "创建请求失败", http.StatusInternalServerError)
		return
	}

	// 复制原始请求的 headers
	for key, values := range r.Header {
		for _, value := range values {
			httpReq.Header.Add(key, value)
		}
	}
	httpReq.ContentLength = int64(len(body))

	// 发送流式请求
	streamResp, err := p.client.SendStreamRequest(r.Context(), httpReq)
	if err != nil {
		log.Printf("流式请求失败: %v", err)
		p.writeError(w, "AI 服务请求失败", http.StatusBadGateway)
		return
	}
	defer streamResp.Close()

	// 设置 SSE 响应头
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// 逐块读取并转发
	for {
		chunk, err := streamResp.ReadChunk()
		if err != nil {
			if err == io.EOF {
				break
			}
			log.Printf("读取流式块失败: %v", err)
			break
		}
		if _, err := w.Write(chunk); err != nil {
			log.Printf("写入流式响应失败: %v", err)
			break
		}
		flusher.Flush()
	}
}

// getTimeout 获取请求超时时间
func (p *LocalProxy) getTimeout() time.Duration {
	if p.cfg.Timeout > 0 {
		return p.cfg.Timeout
	}
	return 30 * time.Second
}

// writeError 写入通用错误响应
func (p *LocalProxy) writeError(w http.ResponseWriter, message string, status int) {
	resp := map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"type":    "api_error",
			"code":    fmt.Sprintf("%d", status),
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp)
}

// handleShutdown 处理优雅关闭
func (p *LocalProxy) handleShutdown() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	<-sigChan
	log.Println("收到关闭信号，正在关闭...")
	p.Stop()
}

// Stop 停止代理服务器
func (p *LocalProxy) Stop() error {
	// 关闭客户端 (会停止 discovery)
	if p.client != nil {
		p.client.Close()
	}

	// 停止 DHT 节点
	if p.dhtNode != nil {
		p.dhtNode.Stop()
	}

	// 停止 HTTP 服务器
	if p.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return p.server.Shutdown(ctx)
	}
	return nil
}
