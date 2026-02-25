package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"

	"github.com/binn/tokengo/internal/cert"
	"github.com/binn/tokengo/internal/crypto"
	"github.com/binn/tokengo/internal/dht"
	"github.com/binn/tokengo/internal/loadbalancer"
	"github.com/binn/tokengo/internal/netutil"
	"github.com/binn/tokengo/internal/protocol"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/quic-go/quic-go"
)

// Client 客户端核心逻辑
type Client struct {
	relayAddr      string
	exitPubKeyHash string // Exit 公钥哈希 (由 Client 指定，Relay 盲转发)
	ohttpClient    *crypto.OHTTPClient
	conn           quic.Connection
	connMu         sync.Mutex
	dhtNode        *dht.Node
	discovery      *dht.Discovery
	selector       loadbalancer.Selector
	currentRelayID peer.ID
}

// NewClient 创建客户端 (静态模式，跳过 PeerID 验证)
func NewClient(relayAddr string, keyID uint8, exitPublicKey []byte) (*Client, error) {
	ohttpClient, err := crypto.NewOHTTPClient(keyID, exitPublicKey)
	if err != nil {
		return nil, fmt.Errorf("创建 OHTTP 客户端失败: %w", err)
	}

	return &Client{
		relayAddr:      relayAddr,
		exitPubKeyHash: crypto.PubKeyHash(exitPublicKey),
		ohttpClient:    ohttpClient,
		selector:       loadbalancer.NewWeightedSelector(),
	}, nil
}

// NewClientDynamic 创建动态发现模式的客户端（不预设 Relay/Exit）
func NewClientDynamic() (*Client, error) {
	return &Client{
		selector: loadbalancer.NewWeightedSelector(),
	}, nil
}

// connect 连接到 Relay 节点
func (c *Client) connect(ctx context.Context) error {
	// 关闭旧连接（如果存在）
	if c.conn != nil {
		c.conn.CloseWithError(0, "reconnecting")
		c.conn = nil
	}

	// 如果启用了 DHT 发现
	if c.discovery != nil {
		return c.connectWithDiscovery(ctx)
	}

	// 静态模式
	return c.connectToAddr(ctx, c.relayAddr, peer.ID(""))
}

// connectWithDiscovery 使用 DHT 发现连接
func (c *Client) connectWithDiscovery(ctx context.Context) error {
	// 从 DHT 发现 Relay 节点
	relays, err := c.discovery.DiscoverRelays(ctx)
	if err != nil {
		return fmt.Errorf("DHT 发现 Relay 失败: %w", err)
	}
	if len(relays) == 0 {
		return fmt.Errorf("DHT 未发现任何 Relay 节点")
	}

	log.Printf("从 DHT 发现 %d 个 Relay 节点", len(relays))

	// 选择一个 Relay 节点
	selected, err := c.selector.Select(ctx, relays)
	if err != nil {
		return fmt.Errorf("选择 Relay 失败: %w", err)
	}

	// 提取地址
	relayAddr := netutil.ExtractQUICAddress(selected.Addrs)
	if relayAddr == "" {
		c.selector.ReportFailure(selected.ID)
		return fmt.Errorf("无法提取 Relay 地址")
	}

	// 尝试连接
	if err := c.connectToAddr(ctx, relayAddr, selected.ID); err != nil {
		c.selector.ReportFailure(selected.ID)
		return fmt.Errorf("连接 Relay 失败: %w", err)
	}

	c.selector.ReportSuccess(selected.ID)
	c.currentRelayID = selected.ID
	return nil
}

// SetDiscovery 设置 Discovery 实例（供 proxy 持久化 discovery 到 Client）
func (c *Client) SetDiscovery(d *dht.Discovery) {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	c.discovery = d
}

// connectToAddr 连接到指定地址
// 如果peerID 不为空，则验证证书中的 PeerID
func (c *Client) connectToAddr(ctx context.Context, addr string, peerID peer.ID) error {
	if addr == "" {
		return fmt.Errorf("Relay 地址为空")
	}

	quicConfig := &quic.Config{
		MaxIdleTimeout:  120_000_000_000, // 120 秒
		KeepAlivePeriod: 30_000_000_000,  // 30 秒
	}

	// 根据是否有 PeerID 选择 TLS 配置
	var tlsConfig *tls.Config
	if peerID != "" {
		// 使用 PeerID 验证证书
		tlsConfig = cert.CreatePeerIDVerifyTLSConfig(peerID)
	} else {
		// 静态模式，跳过证书验证
		tlsConfig = &tls.Config{
			InsecureSkipVerify: true,
			NextProtos:         []string{"tokengo-relay"},
			MinVersion:         tls.VersionTLS13,
		}
	}

	conn, err := quic.DialAddr(ctx, addr, tlsConfig, quicConfig)
	if err != nil {
		return fmt.Errorf("连接 Relay 失败: %w", err)
	}

	c.conn = conn
	c.relayAddr = addr
	c.currentRelayID = peerID
	return nil
}

// Connect 公开的连接方法
func (c *Client) Connect(ctx context.Context) error {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	return c.connect(ctx)
}

// getConnection 获取或建立连接（已持有锁）
func (c *Client) getConnection(ctx context.Context) (quic.Connection, error) {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	// 检查连接是否有效
	if c.conn != nil {
		select {
		case <-c.conn.Context().Done():
			// 连接已关闭，需要重新连接
		default:
			return c.conn, nil
		}
	}

	// 重新连接
	if err := c.connect(ctx); err != nil {
		return nil, err
	}

	return c.conn, nil
}

// SendRequest 发送 HTTP 请求
func (c *Client) SendRequest(ctx context.Context, req *http.Request) (*http.Response, error) {
	// 获取连接
	conn, err := c.getConnection(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取连接失败: %w", err)
	}

	// 创建新流
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, fmt.Errorf("创建流失败: %w", err)
	}

	// OHTTP 加密请求
	ohttpReq, clientCtx, err := c.ohttpClient.EncapsulateRequest(req)
	if err != nil {
		stream.Close()
		return nil, fmt.Errorf("加密请求失败: %w", err)
	}

	// 构建协议消息 (包含 Exit 公钥哈希)
	msg := protocol.NewRequestMessage(c.exitPubKeyHash, ohttpReq)

	// 发送请求
	if _, err := stream.Write(msg.Encode()); err != nil {
		stream.Close()
		return nil, fmt.Errorf("发送请求失败: %w", err)
	}

	// 关闭写入端，表示请求发送完成（但保持读取端开放）
	if err := stream.Close(); err != nil {
		return nil, fmt.Errorf("关闭写入端失败: %w", err)
	}

	// 读取响应
	respMsg, err := protocol.Decode(stream)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	// 检查响应类型
	if respMsg.Type == protocol.MessageTypeError {
		return nil, fmt.Errorf("服务端错误: %s", string(respMsg.Payload))
	}

	if respMsg.Type != protocol.MessageTypeResponse {
		return nil, fmt.Errorf("无效的响应类型: %d", respMsg.Type)
	}

	// OHTTP 解密响应
	resp, err := clientCtx.DecapsulateResponse(respMsg.Payload)
	if err != nil {
		return nil, fmt.Errorf("解密响应失败: %w", err)
	}

	return resp, nil
}

// StreamResponse 封装流式响应读取
type StreamResponse struct {
	stream    quic.Stream
	decryptor *crypto.StreamDecryptor
}

// ReadChunk 读取并解密下一个 SSE 事件，返回 io.EOF 表示流结束
func (sr *StreamResponse) ReadChunk() ([]byte, error) {
	msg, err := protocol.Decode(sr.stream)
	if err != nil {
		return nil, fmt.Errorf("读取流式响应失败: %w", err)
	}

	switch msg.Type {
	case protocol.MessageTypeStreamChunk:
		return sr.decryptor.DecryptChunk(msg.Payload)
	case protocol.MessageTypeStreamEnd:
		return nil, io.EOF
	case protocol.MessageTypeError:
		return nil, fmt.Errorf("服务端错误: %s", string(msg.Payload))
	default:
		return nil, fmt.Errorf("无效的流式响应类型: %d", msg.Type)
	}
}

// Close 关闭流式响应
func (sr *StreamResponse) Close() error {
	sr.stream.CancelRead(0)
	return nil
}

// SendStreamRequest 发送流式请求，返回可逐块解密的 StreamResponse
func (c *Client) SendStreamRequest(ctx context.Context, req *http.Request) (*StreamResponse, error) {
	conn, err := c.getConnection(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取连接失败: %w", err)
	}

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, fmt.Errorf("创建流失败: %w", err)
	}

	// OHTTP 加密请求
	ohttpReq, clientCtx, err := c.ohttpClient.EncapsulateRequest(req)
	if err != nil {
		stream.Close()
		return nil, fmt.Errorf("加密请求失败: %w", err)
	}

	// 发送 StreamRequest 消息 (包含 Exit 公钥哈希)
	msg := protocol.NewStreamRequestMessage(c.exitPubKeyHash, ohttpReq)
	if _, err := stream.Write(msg.Encode()); err != nil {
		stream.Close()
		return nil, fmt.Errorf("发送请求失败: %w", err)
	}

	// 关闭写入端，保持读取端开放
	if err := stream.Close(); err != nil {
		return nil, fmt.Errorf("关闭写入端失败: %w", err)
	}

	// 创建流解密器
	decryptor, err := clientCtx.NewStreamDecryptor()
	if err != nil {
		return nil, fmt.Errorf("创建流解密器失败: %w", err)
	}

	return &StreamResponse{
		stream:    stream,
		decryptor: decryptor,
	}, nil
}

// SendRequestRaw 发送原始 HTTP 请求并返回响应体
func (c *Client) SendRequestRaw(ctx context.Context, method, path string, body []byte, headers map[string]string) ([]byte, int, error) {
	// 构建请求
	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, "http://ai-backend"+path, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("创建请求失败: %w", err)
	}

	// 设置 Content-Length
	if len(body) > 0 {
		req.ContentLength = int64(len(body))
	}

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	// 发送请求
	resp, err := c.SendRequest(ctx, req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	// 读取响应
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("读取响应体失败: %w", err)
	}

	return respBody, resp.StatusCode, nil
}

// Close 关闭客户端连接
func (c *Client) Close() error {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	// 停止 DHT 发现
	if c.discovery != nil {
		c.discovery.Stop()
	}

	if c.conn != nil {
		err := c.conn.CloseWithError(0, "client closed")
		c.conn = nil
		return err
	}
	return nil
}

// StartDiscovery 启动后台服务发现
func (c *Client) StartDiscovery() {
	if c.discovery != nil {
		c.discovery.Start()
	}
}

// GetRelayAddr 获取当前连接的 Relay 地址
func (c *Client) GetRelayAddr() string {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	return c.relayAddr
}

// GetCurrentRelayID 获取当前连接的 Relay PeerID
func (c *Client) GetCurrentRelayID() peer.ID {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	return c.currentRelayID
}

// QueryExitKeys 从已连接的 Relay 查询 Exit 公钥列表
func (c *Client) QueryExitKeys(ctx context.Context) ([]protocol.ExitKeyEntry, error) {
	conn, err := c.getConnection(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取连接失败: %w", err)
	}

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, fmt.Errorf("创建流失败: %w", err)
	}
	defer stream.Close()

	// 发送查询消息
	queryMsg := protocol.NewQueryExitKeysMessage()
	if _, err := stream.Write(queryMsg.Encode()); err != nil {
		return nil, fmt.Errorf("发送查询消息失败: %w", err)
	}

	// 读取响应
	respMsg, err := protocol.Decode(stream)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	if respMsg.Type == protocol.MessageTypeError {
		return nil, fmt.Errorf("服务端错误: %s", string(respMsg.Payload))
	}

	if respMsg.Type != protocol.MessageTypeExitKeysResponse {
		return nil, fmt.Errorf("期望 ExitKeysResponse，收到类型 0x%02x", respMsg.Type)
	}

	// 解析 JSON payload
	var entries []protocol.ExitKeyEntry
	if err := json.Unmarshal(respMsg.Payload, &entries); err != nil {
		return nil, fmt.Errorf("解析 Exit 公钥列表失败: %w", err)
	}

	return entries, nil
}

// SetExit 设置 Exit 节点（自动计算公钥哈希）
func (c *Client) SetExit(keyID uint8, publicKey []byte) error {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	c.exitPubKeyHash = crypto.PubKeyHash(publicKey)

	// 重新创建 OHTTP 客户端
	ohttpClient, err := crypto.NewOHTTPClient(keyID, publicKey)
	if err != nil {
		return fmt.Errorf("创建 OHTTP 客户端失败: %w", err)
	}
	c.ohttpClient = ohttpClient

	return nil
}

// GetExitPubKeyHash 获取当前 Exit 公钥哈希
func (c *Client) GetExitPubKeyHash() string {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	return c.exitPubKeyHash
}

// SetRelay 设置 Relay 地址
func (c *Client) SetRelay(relayAddr string) {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	c.relayAddr = relayAddr
}
