package exit

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	"github.com/binn/tokengo/internal/cert"
	"github.com/binn/tokengo/internal/dht"
	"github.com/binn/tokengo/internal/netutil"
	"github.com/binn/tokengo/internal/protocol"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/quic-go/quic-go"
)

// TunnelClient Exit 侧反向隧道客户端
// Exit 主动连接 Relay，注册自己，然后接收 Relay 转发过来的请求
type TunnelClient struct {
	discovery       *dht.Discovery
	staticRelayAddr string // 静态 Relay 地址（用于 serve 命令）
	pubKeyHash      string
	keyConfig       []byte // OHTTP KeyConfig (注册时发送给 Relay)
	ohttpHandler    *OHTTPHandler
	conn            quic.Connection
	connMu          sync.Mutex
	ctx             context.Context
	cancel          context.CancelFunc
	activeRelayAddr string
	currentRelayID  peer.ID
	ready           chan struct{}
	readyOnce       sync.Once
}

// NewTunnelClient 创建反向隧道客户端（DHT 发现模式）
func NewTunnelClient(discovery *dht.Discovery, pubKeyHash string, keyConfig []byte, ohttpHandler *OHTTPHandler) *TunnelClient {
	ctx, cancel := context.WithCancel(context.Background())
	return &TunnelClient{
		discovery:    discovery,
		pubKeyHash:   pubKeyHash,
		keyConfig:    keyConfig,
		ohttpHandler: ohttpHandler,
		ctx:          ctx,
		cancel:       cancel,
		ready:        make(chan struct{}),
	}
}

// NewTunnelClientStatic 创建反向隧道客户端（静态地址模式，用于 serve 命令）
func NewTunnelClientStatic(relayAddr string, pubKeyHash string, keyConfig []byte, ohttpHandler *OHTTPHandler) *TunnelClient {
	ctx, cancel := context.WithCancel(context.Background())
	return &TunnelClient{
		staticRelayAddr: relayAddr,
		pubKeyHash:      pubKeyHash,
		keyConfig:       keyConfig,
		ohttpHandler:    ohttpHandler,
		ctx:             ctx,
		cancel:          cancel,
		ready:           make(chan struct{}),
	}
}

// Start 启动反向隧道
func (t *TunnelClient) Start(ctx context.Context) error {
	// 1. 带重试的初始连接
	backoff := 3 * time.Second
	const maxBackoff = 60 * time.Second

	for {
		select {
		case <-t.ctx.Done():
			return fmt.Errorf("隧道已停止")
		default:
		}

		// 选择最佳 Relay
		addr, peerID, err := t.selectRelay(ctx)
		if err != nil {
			log.Printf("选择 Relay 失败: %v，%v 后重试...", err, backoff)
			select {
			case <-time.After(backoff):
				backoff = nextBackoff(backoff, maxBackoff)
				continue
			case <-t.ctx.Done():
				return fmt.Errorf("隧道已停止")
			}
		}

		log.Printf("选择 Relay: %s", addr)

		// 连接并注册
		if err := t.connectAndRegister(ctx, addr, peerID); err != nil {
			log.Printf("连接 Relay %s 失败: %v，%v 后重试...", addr, err, backoff)
			select {
			case <-time.After(backoff):
				backoff = nextBackoff(backoff, maxBackoff)
				continue
			case <-t.ctx.Done():
				return fmt.Errorf("隧道已停止")
			}
		}

		log.Printf("已注册到 Relay %s (pubKeyHash=%s)", addr, t.pubKeyHash)
		t.currentRelayID = peerID
		t.readyOnce.Do(func() { close(t.ready) })
		break
	}

	// 2. 启动心跳和流接收 goroutine
	// 在锁保护下捕获 conn 值，避免数据竞争
	t.connMu.Lock()
	conn := t.conn
	t.connMu.Unlock()

	connCtx, connCancel := context.WithCancel(ctx)
	go func() {
		// 当 QUIC 连接断开或 Stop() 被调用时取消 connCtx
		select {
		case <-conn.Context().Done():
		case <-t.ctx.Done():
		}
		connCancel()
	}()
	go t.heartbeatLoop(connCtx)
	go t.acceptStreams(connCtx, conn)

	// 3. 启动重连循环 (阻塞)
	t.reconnectLoop(ctx)
	return nil
}

// selectRelay 选择 Relay 节点（静态地址或 DHT 发现）
func (t *TunnelClient) selectRelay(ctx context.Context) (string, peer.ID, error) {
	// 静态模式（用于 serve 命令）
	if t.staticRelayAddr != "" {
		return t.staticRelayAddr, "", nil
	}

	// DHT 发现模式
	if t.discovery == nil {
		return "", "", fmt.Errorf("DHT 未启用，无法发现 Relay 节点")
	}

	relays, err := t.discovery.DiscoverRelays(ctx)
	if err != nil {
		return "", "", fmt.Errorf("DHT 发现 Relay 失败: %w", err)
	}
	if len(relays) == 0 {
		return "", "", fmt.Errorf("DHT 未发现任何 Relay 节点")
	}

	log.Printf("从 DHT 发现 %d 个 Relay 节点", len(relays))

	// 从 peer.AddrInfo 提取地址并探测 RTT
	return t.selectBestRelay(ctx, relays)
}

// selectBestRelay 从 DHT 发现的 Relay 中选择延迟最低的
func (t *TunnelClient) selectBestRelay(ctx context.Context, relays []peer.AddrInfo) (string, peer.ID, error) {
	var bestAddr string
	var bestPeerID peer.ID
	var bestRTT time.Duration

	for _, relay := range relays {
		addr := netutil.ExtractQUICAddress(relay.Addrs)
		if addr == "" {
			continue
		}

		rtt, err := t.probeRelay(ctx, addr, relay.ID)
		if err != nil {
			log.Printf("探测 Relay %s 失败: %v", addr, err)
			continue
		}

		log.Printf("Relay %s RTT: %v", addr, rtt)
		if bestAddr == "" || rtt < bestRTT {
			bestAddr = addr
			bestPeerID = relay.ID
			bestRTT = rtt
		}
	}

	if bestAddr == "" {
		return "", "", fmt.Errorf("所有 Relay 节点均不可达")
	}
	return bestAddr, bestPeerID, nil
}

// probeRelay 探测 Relay 的 RTT (QUIC 握手时间)
func (t *TunnelClient) probeRelay(ctx context.Context, addr string, peerID peer.ID) (time.Duration, error) {
	// 使用 PeerID 验证的 TLS 配置
	tlsConfig := cert.CreatePeerIDVerifyTLSConfig(peerID)

	start := time.Now()
	probeConn, err := quic.DialAddr(ctx, addr, tlsConfig, &quic.Config{})
	rtt := time.Since(start)
	if err != nil {
		return 0, err
	}
	probeConn.CloseWithError(0, "probe")
	return rtt, nil
}

// connectAndRegister 连接到 Relay 并发送注册消息
func (t *TunnelClient) connectAndRegister(ctx context.Context, addr string, peerID peer.ID) error {
	// 根据是否有 PeerID 选择 TLS 配置
	var tlsConfig *tls.Config
	if peerID != "" {
		// 使用 PeerID 验证证书
		tlsConfig = cert.CreatePeerIDVerifyTLSConfig(peerID)
	} else {
		// 静态模式，跳过证书验证
		tlsConfig = &tls.Config{
			InsecureSkipVerify: true,
			NextProtos:         []string{"tokengo-exit"},
			MinVersion:         tls.VersionTLS13,
		}
	}

	// 1. 建立 QUIC 连接
	conn, err := quic.DialAddr(ctx, addr, tlsConfig, &quic.Config{
		KeepAlivePeriod: 10 * time.Second,
		MaxIdleTimeout:  60 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("QUIC 连接 Relay 失败: %w", err)
	}

	// 2. 打开注册流
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		conn.CloseWithError(1, "open stream failed")
		return fmt.Errorf("打开注册流失败: %w", err)
	}

	// 3. 发送注册消息 (附带 KeyConfig)
	regMsg := protocol.NewRegisterMessage(t.pubKeyHash, t.keyConfig)
	if _, err := stream.Write(regMsg.Encode()); err != nil {
		stream.Close()
		conn.CloseWithError(1, "write register failed")
		return fmt.Errorf("发送注册消息失败: %w", err)
	}

	// 4. 读取注册确认
	ackMsg, err := protocol.Decode(stream)
	if err != nil {
		stream.Close()
		conn.CloseWithError(1, "read register ack failed")
		return fmt.Errorf("读取注册确认失败: %w", err)
	}

	if ackMsg.Type != protocol.MessageTypeRegisterAck {
		stream.Close()
		conn.CloseWithError(1, "unexpected message type")
		return fmt.Errorf("期望 RegisterAck，收到类型 0x%02x", ackMsg.Type)
	}

	// 5. 关闭注册流
	stream.Close()

	// 6. 保存连接
	t.connMu.Lock()
	t.conn = conn
	t.activeRelayAddr = addr
	t.connMu.Unlock()

	return nil
}

// acceptStreams 循环接收 Relay 转发过来的流
func (t *TunnelClient) acceptStreams(ctx context.Context, conn quic.Connection) {
	for {
		stream, err := conn.AcceptStream(ctx)
		if err != nil {
			if ctx.Err() != nil {
				// 上下文取消，正常退出
				return
			}
			log.Printf("接收流失败 (连接可能已断开): %v", err)
			return
		}

		go t.handleIncomingStream(stream)
	}
}

// handleIncomingStream 处理从 Relay 转发过来的单个流
func (t *TunnelClient) handleIncomingStream(stream quic.Stream) {
	defer stream.Close()

	// 1. 读取消息
	msg, err := protocol.Decode(stream)
	if err != nil {
		log.Printf("解码入站消息失败: %v", err)
		errMsg := protocol.NewErrorMessage(fmt.Sprintf("decode error: %v", err))
		stream.Write(errMsg.Encode())
		return
	}

	// 2. 根据消息类型分发处理
	switch msg.Type {
	case protocol.MessageTypeRequest:
		// 非流式请求
		respBytes, err := t.ohttpHandler.ProcessRequest(msg.Payload)
		if err != nil {
			log.Printf("处理请求失败: %v", err)
			errMsg := protocol.NewErrorMessage(fmt.Sprintf("process error: %v", err))
			stream.Write(errMsg.Encode())
			return
		}
		respMsg := protocol.NewResponseMessage(respBytes)
		if _, err := stream.Write(respMsg.Encode()); err != nil {
			log.Printf("写回响应失败: %v", err)
		}

	case protocol.MessageTypeStreamRequest:
		// 流式请求，直接将加密的流式块写入 stream
		if err := t.ohttpHandler.ProcessStreamRequest(msg.Payload, stream); err != nil {
			log.Printf("处理流式请求失败: %v", err)
			// 尝试写入错误消息 (流可能已经部分写入)
			errMsg := protocol.NewErrorMessage(fmt.Sprintf("stream error: %v", err))
			stream.Write(errMsg.Encode())
		}

	case protocol.MessageTypeHeartbeat:
		// 备选心跳路径: Relay 发起的心跳
		ackMsg := protocol.NewHeartbeatAckMessage()
		if _, err := stream.Write(ackMsg.Encode()); err != nil {
			log.Printf("写回心跳确认失败: %v", err)
		}

	default:
		log.Printf("收到未知消息类型: 0x%02x", msg.Type)
		errMsg := protocol.NewErrorMessage(fmt.Sprintf("unknown message type: 0x%02x", msg.Type))
		stream.Write(errMsg.Encode())
	}
}

// heartbeatLoop 定期发送心跳保持连接活跃
func (t *TunnelClient) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := t.sendHeartbeat(ctx); err != nil {
				log.Printf("发送心跳失败: %v", err)
			}
		}
	}
}

// sendHeartbeat 发送单次心跳
func (t *TunnelClient) sendHeartbeat(ctx context.Context) error {
	t.connMu.Lock()
	conn := t.conn
	t.connMu.Unlock()

	if conn == nil {
		return fmt.Errorf("连接不可用")
	}

	hbCtx, hbCancel := context.WithTimeout(ctx, 5*time.Second)
	defer hbCancel()

	stream, err := conn.OpenStreamSync(hbCtx)
	if err != nil {
		return fmt.Errorf("打开心跳流失败: %w", err)
	}
	defer stream.Close()

	// 发送心跳
	hbMsg := protocol.NewHeartbeatMessage()
	if _, err := stream.Write(hbMsg.Encode()); err != nil {
		return fmt.Errorf("写入心跳消息失败: %w", err)
	}

	// 读取心跳确认
	ackMsg, err := protocol.Decode(stream)
	if err != nil {
		return fmt.Errorf("读取心跳确认失败: %w", err)
	}

	if ackMsg.Type != protocol.MessageTypeHeartbeatAck {
		return fmt.Errorf("期望 HeartbeatAck，收到类型 0x%02x", ackMsg.Type)
	}

	return nil
}

// reconnectLoop 等待连接断开后进行指数退避重连
func (t *TunnelClient) reconnectLoop(ctx context.Context) {
	for {
		// 等待当前连接断开
		t.connMu.Lock()
		conn := t.conn
		t.connMu.Unlock()

		if conn != nil {
			select {
			case <-conn.Context().Done():
				log.Printf("与 Relay %s 的连接断开，准备重连...", t.activeRelayAddr)
			case <-t.ctx.Done():
				return
			}
		}

		// 指数退避重连
		backoff := 1 * time.Second
		const maxBackoff = 60 * time.Second

		for {
			select {
			case <-t.ctx.Done():
				return
			default:
			}

			log.Printf("尝试重连 Relay (退避 %v)...", backoff)

			// 使用 select 替换 time.Sleep，以便响应 Stop()
			select {
			case <-time.After(backoff):
				// 继续退避
			case <-t.ctx.Done():
				return // 立即响应 shutdown
			}

			// 重新选择 Relay
			addr, peerID, err := t.selectRelay(ctx)
			if err != nil {
				log.Printf("选择 Relay 失败: %v", err)
				backoff = nextBackoff(backoff, maxBackoff)
				continue
			}

			// 连接并注册
			if err := t.connectAndRegister(ctx, addr, peerID); err != nil {
				log.Printf("重连 Relay %s 失败: %v", addr, err)
				backoff = nextBackoff(backoff, maxBackoff)
				continue
			}

			log.Printf("重连成功，已重新注册到 Relay %s", addr)
			t.currentRelayID = peerID

			// 重连成功，重新启动心跳和流接收
			// 在锁保护下捕获 conn 值，避免数据竞争
			t.connMu.Lock()
			conn := t.conn
			t.connMu.Unlock()

			connCtx, connCancel := context.WithCancel(ctx)
			go func() {
				// 当 QUIC 连接断开或 Stop() 被调用时取消 connCtx
				select {
				case <-conn.Context().Done():
				case <-t.ctx.Done():
				}
				connCancel()
			}()
			go t.heartbeatLoop(connCtx)
			go t.acceptStreams(connCtx, conn)

			break // 退出退避循环，回到外层等待断开
		}
	}
}

// Stop 停止反向隧道客户端
func (t *TunnelClient) Stop() error {
	t.cancel()
	t.connMu.Lock()
	defer t.connMu.Unlock()
	if t.conn != nil {
		return t.conn.CloseWithError(0, "exit shutting down")
	}
	return nil
}

// Ready 返回一个在首次注册成功后关闭的 channel
func (t *TunnelClient) Ready() <-chan struct{} {
	return t.ready
}

// nextBackoff 计算下一次退避时间 (指数退避，上限 maxBackoff)
func nextBackoff(current, max time.Duration) time.Duration {
	next := time.Duration(math.Min(float64(current*2), float64(max)))
	return next
}
