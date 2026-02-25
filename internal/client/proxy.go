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

	"github.com/binn/tokengo/internal/config"
	"github.com/binn/tokengo/internal/crypto"
	"github.com/binn/tokengo/internal/dht"
)

// LocalProxy 本地 HTTP 代理服务器
type LocalProxy struct {
	cfg      *config.ClientConfig
	client   *Client
	server   *http.Server
	dhtNode  *dht.Node
	discovery *dht.Discovery
	progress ProgressReporter
}

// NewLocalProxy 创建本地代理
func NewLocalProxy(cfg *config.ClientConfig) (*LocalProxy, error) {
	// 警告：如果启用了不安全模式
	if cfg.InsecureSkipVerify {
		log.Println("警告: TLS 证书验证已禁用，仅用于开发环境！")
	}

	proxy := &LocalProxy{
		cfg:      cfg,
		progress: NewConsoleProgress(),
	}

	// DHT 始终启用（私有网络）
	dhtCfg := &dht.Config{
		BootstrapPeers: cfg.BootstrapPeers, // 可选覆盖
		ListenAddrs:    []string{"/ip4/0.0.0.0/tcp/0"},
		Mode:           "client",
		ServiceType:    "client",
	}

	dhtNode, err := dht.NewNode(dhtCfg)
	if err != nil {
		return nil, fmt.Errorf("创建 DHT 节点失败: %w", err)
	}
	proxy.dhtNode = dhtNode

	// 创建 Client（不预设 Relay/Exit，后续动态发现）
	client, err := NewClientDynamic(cfg.InsecureSkipVerify)
	if err != nil {
		proxy.dhtNode.Stop()
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
		cfg:      cfg,
		client:   client,
		progress: NewSilentProgress(), // 静态模式静默
	}, nil
}

// Start 启动本地代理服务器
func (p *LocalProxy) Start() error {
	ctx := context.Background()

	// 动态发现模式
	if p.client.GetRelayAddr() == "" {
		// 启动 DHT 节点
		if p.dhtNode != nil {
			p.progress.OnBootstrapConnecting()
			if err := p.dhtNode.Start(ctx); err != nil {
				return fmt.Errorf("启动 DHT 节点失败: %w", err)
			}
			p.progress.OnBootstrapConnected(1, 1) // 简化处理
		}

		// 动态发现并连接
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

	p.progress.OnReady(p.cfg.Listen)
	return p.server.ListenAndServe()
}

// discoverAndConnect 发现节点并连接
// 新架构：先连接 Relay，再从 Relay 查询 Exit 公钥
func (p *LocalProxy) discoverAndConnect(ctx context.Context) error {
	// 1. 创建 Discovery 并持久化
	if p.dhtNode != nil {
		p.progress.OnDiscoveringRelays()
		p.discovery = dht.NewDiscovery(p.dhtNode)
		p.discovery.Start()
		p.client.SetDiscovery(p.discovery)
	}

	// 2. 连接 Relay（Client 内部走 connectWithDiscovery）
	if err := p.client.Connect(ctx); err != nil {
		return fmt.Errorf("连接 Relay 失败: %w", err)
	}
	log.Printf("已连接到 Relay: %s", p.client.GetRelayAddr())

	// 3. 从 Relay 查询 Exit 公钥
	keyID, publicKey, err := p.discoverExit(ctx)
	if err != nil {
		return fmt.Errorf("发现 Exit 失败: %w", err)
	}

	// 4. 设置 Exit
	if err := p.client.SetExit(keyID, publicKey); err != nil {
		return fmt.Errorf("设置 Exit 失败: %w", err)
	}
	log.Printf("已选择 Exit 公钥哈希: %s", p.client.GetExitPubKeyHash())

	return nil
}

// discoverExit 从 Relay 查询 Exit 公钥
func (p *LocalProxy) discoverExit(ctx context.Context) (keyID uint8, publicKey []byte, err error) {
	p.progress.OnFetchingExitKeys()

	// 从已连接的 Relay 查询 Exit 公钥列表
	entries, queryErr := p.client.QueryExitKeys(ctx)
	if queryErr != nil {
		return 0, nil, fmt.Errorf("从 Relay 查询 Exit 公钥失败: %w", queryErr)
	}

	if len(entries) == 0 {
		return 0, nil, fmt.Errorf("Relay 没有已注册的 Exit 节点")
	}

	entry := entries[0]
	kid, pubKey, decodeErr := crypto.DecodeKeyConfig(entry.KeyConfig)
	if decodeErr != nil {
		return 0, nil, fmt.Errorf("解析 Exit KeyConfig 失败: %w", decodeErr)
	}

	p.progress.OnExitKeyFetched(entry.PubKeyHash)
	log.Printf("从 Relay 获取 Exit 公钥 (KeyID: %d, Hash: %s)", kid, entry.PubKeyHash)
	return kid, pubKey, nil
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
	// 1. JSON body 中精确匹配 "stream" 字段
	if len(body) > 0 {
		var partial struct {
			Stream *bool `json:"stream"`
		}
		if json.Unmarshal(body, &partial) == nil && partial.Stream != nil && *partial.Stream {
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
	// 停止 Discovery（在关闭 Client 前）
	if p.discovery != nil {
		p.discovery.Stop()
	}

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
