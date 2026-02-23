package exit

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	"github.com/binn/tokengo/internal/protocol"
	"github.com/quic-go/quic-go"
)

// TunnelClient Exit 侧反向隧道客户端
// Exit 主动连接 Relay，注册自己，然后接收 Relay 转发过来的请求
type TunnelClient struct {
	relayAddrs      []string
	pubKeyHash      string
	tlsConfig       *tls.Config
	ohttpHandler    *OHTTPHandler
	conn            quic.Connection
	connMu          sync.Mutex
	ctx             context.Context
	cancel          context.CancelFunc
	activeRelayAddr string
}

// NewTunnelClient 创建反向隧道客户端
func NewTunnelClient(relayAddrs []string, pubKeyHash string, ohttpHandler *OHTTPHandler, insecureSkipVerify bool) *TunnelClient {
	tlsConfig := &tls.Config{
		NextProtos:         []string{"tokengo-exit"},
		InsecureSkipVerify: insecureSkipVerify,
		MinVersion:         tls.VersionTLS13,
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &TunnelClient{
		relayAddrs:   relayAddrs,
		pubKeyHash:   pubKeyHash,
		tlsConfig:    tlsConfig,
		ohttpHandler: ohttpHandler,
		ctx:          ctx,
		cancel:       cancel,
	}
}

// Start 启动反向隧道
func (t *TunnelClient) Start(ctx context.Context) error {
	// 1. 选择最佳 Relay
	addr, err := t.selectRelay(ctx)
	if err != nil {
		return fmt.Errorf("选择 Relay 失败: %w", err)
	}
	log.Printf("选择 Relay: %s", addr)

	// 2. 连接并注册
	if err := t.connectAndRegister(ctx, addr); err != nil {
		return fmt.Errorf("连接 Relay 失败: %w", err)
	}
	log.Printf("已注册到 Relay %s (pubKeyHash=%s)", addr, t.pubKeyHash)

	// 3. 启动心跳和流接收 goroutine
	connCtx, connCancel := context.WithCancel(ctx)
	go func() {
		// 当 QUIC 连接断开时取消 connCtx
		<-t.conn.Context().Done()
		connCancel()
	}()
	go t.heartbeatLoop(connCtx)
	go t.acceptStreams(connCtx)

	// 4. 启动重连循环 (阻塞)
	t.reconnectLoop(ctx)
	return nil
}

// selectRelay 选择延迟最低的 Relay 节点
func (t *TunnelClient) selectRelay(ctx context.Context) (string, error) {
	if len(t.relayAddrs) == 0 {
		return "", fmt.Errorf("没有可用的 Relay 地址")
	}

	// 只有一个地址，直接返回
	if len(t.relayAddrs) == 1 {
		return t.relayAddrs[0], nil
	}

	// 并发探测所有 Relay 的 RTT
	type probeResult struct {
		addr string
		rtt  time.Duration
		err  error
	}

	results := make(chan probeResult, len(t.relayAddrs))
	probeCtx, probeCancel := context.WithTimeout(ctx, 10*time.Second)
	defer probeCancel()

	for _, addr := range t.relayAddrs {
		go func(a string) {
			rtt, err := t.probeRelay(probeCtx, a)
			results <- probeResult{addr: a, rtt: rtt, err: err}
		}(addr)
	}

	var bestAddr string
	var bestRTT time.Duration

	for i := 0; i < len(t.relayAddrs); i++ {
		r := <-results
		if r.err != nil {
			log.Printf("探测 Relay %s 失败: %v", r.addr, r.err)
			continue
		}
		log.Printf("Relay %s RTT: %v", r.addr, r.rtt)
		if bestAddr == "" || r.rtt < bestRTT {
			bestAddr = r.addr
			bestRTT = r.rtt
		}
	}

	if bestAddr == "" {
		return "", fmt.Errorf("所有 Relay 地址均不可达")
	}

	return bestAddr, nil
}

// probeRelay 探测 Relay 的 RTT (QUIC 握手时间)
func (t *TunnelClient) probeRelay(ctx context.Context, addr string) (time.Duration, error) {
	start := time.Now()
	probeConn, err := quic.DialAddr(ctx, addr, t.tlsConfig, &quic.Config{})
	rtt := time.Since(start)
	if err != nil {
		return 0, err
	}
	probeConn.CloseWithError(0, "probe")
	return rtt, nil
}

// connectAndRegister 连接到 Relay 并发送注册消息
func (t *TunnelClient) connectAndRegister(ctx context.Context, addr string) error {
	// 1. 建立 QUIC 连接
	conn, err := quic.DialAddr(ctx, addr, t.tlsConfig, &quic.Config{
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

	// 3. 发送注册消息
	regMsg := protocol.NewRegisterMessage(t.pubKeyHash, nil)
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
func (t *TunnelClient) acceptStreams(ctx context.Context) {
	for {
		stream, err := t.conn.AcceptStream(ctx)
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

			time.Sleep(backoff)

			// 重新选择 Relay
			addr, err := t.selectRelay(ctx)
			if err != nil {
				log.Printf("选择 Relay 失败: %v", err)
				backoff = nextBackoff(backoff, maxBackoff)
				continue
			}

			// 连接并注册
			if err := t.connectAndRegister(ctx, addr); err != nil {
				log.Printf("重连 Relay %s 失败: %v", addr, err)
				backoff = nextBackoff(backoff, maxBackoff)
				continue
			}

			log.Printf("重连成功，已重新注册到 Relay %s", addr)

			// 重连成功，重新启动心跳和流接收
			connCtx, connCancel := context.WithCancel(ctx)
			go func() {
				<-t.conn.Context().Done()
				connCancel()
			}()
			go t.heartbeatLoop(connCtx)
			go t.acceptStreams(connCtx)

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

// nextBackoff 计算下一次退避时间 (指数退避，上限 maxBackoff)
func nextBackoff(current, max time.Duration) time.Duration {
	next := time.Duration(math.Min(float64(current*2), float64(max)))
	return next
}
