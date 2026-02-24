package relay

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/binn/tokengo/internal/protocol"
	"github.com/quic-go/quic-go"
)

// QUICServer QUIC 服务器
type QUICServer struct {
	listener  *quic.Listener
	registry  *Registry
	addr      string
	tlsConfig *tls.Config
	wg        sync.WaitGroup // 追踪所有 goroutine
	ready     chan struct{}
	readyOnce sync.Once
}

// NewQUICServer 创建 QUIC 服务器
func NewQUICServer(addr string, tlsConfig *tls.Config, registry *Registry) *QUICServer {
	return &QUICServer{
		addr:      addr,
		tlsConfig: tlsConfig,
		registry:  registry,
		ready:     make(chan struct{}),
	}
}

// Start 启动 QUIC 服务器
func (s *QUICServer) Start(ctx context.Context) error {
	// QUIC 配置
	quicConfig := &quic.Config{
		MaxIdleTimeout:  120_000_000_000, // 120 秒 (纳秒)
		KeepAlivePeriod: 30_000_000_000,  // 30 秒
	}

	// 启动监听
	listener, err := quic.ListenAddr(s.addr, s.tlsConfig, quicConfig)
	if err != nil {
		return fmt.Errorf("启动 QUIC 监听失败: %w", err)
	}
	s.listener = listener

	// 标记为就绪
	s.readyOnce.Do(func() { close(s.ready) })

	log.Printf("QUIC 服务器启动，监听 %s", s.addr)

	// 接受连接
	for {
		select {
		case <-ctx.Done():
			// 等待所有 goroutine 完成
			s.wg.Wait()
			return nil
		default:
			conn, err := listener.Accept(ctx)
			if err != nil {
				if ctx.Err() != nil {
					s.wg.Wait()
					return nil
				}
				log.Printf("接受连接失败: %v", err)
				continue
			}

			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.handleConnection(ctx, conn)
			}()
		}
	}
}

// handleConnection 处理单个 QUIC 连接，根据 ALPN 区分 Client 和 Exit
func (s *QUICServer) handleConnection(ctx context.Context, conn quic.Connection) {
	alpn := conn.ConnectionState().TLS.NegotiatedProtocol
	log.Printf("新连接: %s, ALPN: %s", conn.RemoteAddr(), alpn)

	switch alpn {
	case "tokengo-exit":
		s.handleExitConnection(ctx, conn)
	default:
		// 包括 "tokengo-relay" 和其他协议，按 Client 处理
		s.handleClientConnection(ctx, conn)
	}
}

// handleClientConnection 处理 Client 连接（原有逻辑）
func (s *QUICServer) handleClientConnection(ctx context.Context, conn quic.Connection) {
	defer conn.CloseWithError(0, "connection closed")

	var streamWg sync.WaitGroup
	defer streamWg.Wait() // 确保所有流处理完成

	for {
		stream, err := conn.AcceptStream(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// 检查是否是连接关闭导致的错误
			select {
			case <-conn.Context().Done():
				return
			default:
				log.Printf("接受流失败: %v", err)
				return
			}
		}

		streamWg.Add(1)
		go func(stream quic.Stream) {
			defer streamWg.Done()
			s.handleStream(stream)
		}(stream)
	}
}

// handleExitConnection 处理 Exit 节点的反向隧道连接
func (s *QUICServer) handleExitConnection(ctx context.Context, conn quic.Connection) {
	// 不 defer CloseWithError，因为连接需要长期保持

	// 1. AcceptStream 读取第一条消息（注册消息）
	regStream, err := conn.AcceptStream(ctx)
	if err != nil {
		log.Printf("Exit 连接 %s: 接受注册流失败: %v", conn.RemoteAddr(), err)
		conn.CloseWithError(1, "accept register stream failed")
		return
	}

	msg, err := protocol.Decode(regStream)
	if err != nil {
		log.Printf("Exit 连接 %s: 读取注册消息失败: %v", conn.RemoteAddr(), err)
		regStream.Close()
		conn.CloseWithError(1, "read register message failed")
		return
	}

	// 2. 验证是 MessageTypeRegister
	if msg.Type != protocol.MessageTypeRegister {
		log.Printf("Exit 连接 %s: 期望 Register 消息，收到类型 %d", conn.RemoteAddr(), msg.Type)
		errMsg := protocol.NewErrorMessage("expected register message")
		regStream.Write(errMsg.Encode())
		regStream.Close()
		conn.CloseWithError(1, "unexpected message type")
		return
	}

	// 3. 从 msg.Target 取 pubKeyHash
	pubKeyHash := msg.Target
	if pubKeyHash == "" {
		log.Printf("Exit 连接 %s: 注册消息缺少 pubKeyHash", conn.RemoteAddr())
		errMsg := protocol.NewErrorMessage("missing pubKeyHash")
		regStream.Write(errMsg.Encode())
		regStream.Close()
		conn.CloseWithError(1, "missing pubKeyHash")
		return
	}

	// 4. 先发送 RegisterAck，再注册（避免注册窗口期的请求被路由到未就绪的 Exit）
	ackMsg := protocol.NewRegisterAckMessage(nil)
	if _, err := regStream.Write(ackMsg.Encode()); err != nil {
		log.Printf("Exit %s: 发送 RegisterAck 失败: %v", pubKeyHash, err)
		regStream.Close()
		conn.CloseWithError(1, "send register ack failed")
		return
	}
	regStream.Close()

	// 5. 然后注册到 registry (附带 KeyConfig)
	s.registry.Register(pubKeyHash, conn, msg.Payload)

	log.Printf("Exit %s: 注册完成，开始心跳监听", pubKeyHash)

	// 6. 心跳监听循环
	defer func() {
		s.registry.Remove(pubKeyHash)
		conn.CloseWithError(0, "exit connection closed")
		log.Printf("Exit %s: 连接已关闭", pubKeyHash)
	}()

	for {
		hbStream, err := conn.AcceptStream(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			select {
			case <-conn.Context().Done():
				log.Printf("Exit %s: 连接断开", pubKeyHash)
				return
			default:
				log.Printf("Exit %s: 接受心跳流失败: %v", pubKeyHash, err)
				return
			}
		}

		// 读取心跳消息
		hbMsg, err := protocol.Decode(hbStream)
		if err != nil {
			log.Printf("Exit %s: 读取心跳消息失败: %v", pubKeyHash, err)
			hbStream.Close()
			continue
		}

		if hbMsg.Type == protocol.MessageTypeHeartbeat {
			s.registry.UpdateHeartbeat(pubKeyHash)
			ackMsg := protocol.NewHeartbeatAckMessage()
			hbStream.Write(ackMsg.Encode())
		} else {
			log.Printf("Exit %s: 心跳阶段收到非心跳消息类型 %d", pubKeyHash, hbMsg.Type)
		}
		hbStream.Close()
	}
}

// handleStream 处理单个 QUIC 流
func (s *QUICServer) handleStream(stream quic.Stream) {
	defer stream.Close()

	// 读取消息
	msg, err := protocol.Decode(stream)
	if err != nil {
		if err != io.EOF {
			log.Printf("读取消息失败: %v", err)
		}
		return
	}

	// 根据消息类型处理
	switch msg.Type {
	case protocol.MessageTypeRequest:
		s.handleForwardRequest(stream, msg)
	case protocol.MessageTypeStreamRequest:
		s.handleStreamForwardRequest(stream, msg)
	case protocol.MessageTypeQueryExitKeys:
		entries := s.registry.ListExitKeys()
		resp, err := protocol.NewExitKeysResponseMessage(entries)
		if err != nil {
			log.Printf("序列化 Exit 公钥列表失败: %v", err)
			errMsg := protocol.NewErrorMessage("failed to serialize exit keys")
			stream.Write(errMsg.Encode())
			return
		}
		stream.Write(resp.Encode())
	default:
		log.Printf("无效的消息类型: %d", msg.Type)
		errMsg := protocol.NewErrorMessage("invalid message type")
		stream.Write(errMsg.Encode())
	}
}

// handleForwardRequest 处理转发请求（通过反向隧道转发到 Exit）
func (s *QUICServer) handleForwardRequest(stream quic.Stream, msg *protocol.Message) {
	// 验证目标地址（pubKeyHash）
	if msg.Target == "" {
		log.Printf("请求缺少目标地址")
		errMsg := protocol.NewErrorMessage("missing target address")
		stream.Write(errMsg.Encode())
		return
	}

	// 从 registry 查找 Exit 连接
	exitConn, ok := s.registry.Lookup(msg.Target)
	if !ok {
		log.Printf("Exit %s 未注册或已断开", msg.Target)
		errMsg := protocol.NewErrorMessage("exit not found")
		stream.Write(errMsg.Encode())
		return
	}

	// 在 Exit 连接上打开新流（使用带超时的 context，避免客户端断开后阻塞）
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	exitStream, err := exitConn.OpenStreamSync(ctx)
	if err != nil {
		log.Printf("打开 Exit %s 流失败: %v", msg.Target, err)
		// Exit 连接可能已断开，只移除匹配的连接（避免 TOCTOU 竞争）
		s.registry.RemoveIfMatch(msg.Target, exitConn)
		errMsg := protocol.NewErrorMessage("exit connection failed")
		stream.Write(errMsg.Encode())
		return
	}
	defer exitStream.Close()

	// 写入 Request 消息到 Exit（Target 为空，Payload 为 OHTTP 数据）
	reqMsg := protocol.NewRequestMessage("", msg.Payload)
	if _, err := exitStream.Write(reqMsg.Encode()); err != nil {
		log.Printf("写入 Exit %s 请求失败: %v", msg.Target, err)
		errMsg := protocol.NewErrorMessage("write to exit failed")
		stream.Write(errMsg.Encode())
		return
	}

	// 从 Exit 流读取响应消息
	respMsg, err := protocol.Decode(exitStream)
	if err != nil {
		log.Printf("读取 Exit %s 响应失败: %v", msg.Target, err)
		errMsg := protocol.NewErrorMessage("read exit response failed")
		stream.Write(errMsg.Encode())
		return
	}

	// 将响应写回 Client 流
	if _, err := stream.Write(respMsg.Encode()); err != nil {
		log.Printf("写入客户端响应失败: %v", err)
	}
}

// handleStreamForwardRequest 处理流式转发请求（通过反向隧道）
func (s *QUICServer) handleStreamForwardRequest(stream quic.Stream, msg *protocol.Message) {
	if msg.Target == "" {
		log.Printf("流式请求缺少目标地址")
		errMsg := protocol.NewErrorMessage("missing target address")
		stream.Write(errMsg.Encode())
		return
	}

	// 从 registry 查找 Exit 连接
	exitConn, ok := s.registry.Lookup(msg.Target)
	if !ok {
		log.Printf("Exit %s 未注册或已断开", msg.Target)
		errMsg := protocol.NewErrorMessage("exit not found")
		stream.Write(errMsg.Encode())
		return
	}

	// 在 Exit 连接上打开新流（使用带超时的 context，避免客户端断开后阻塞）
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	exitStream, err := exitConn.OpenStreamSync(ctx)
	if err != nil {
		log.Printf("打开 Exit %s 流失败: %v", msg.Target, err)
		// Exit 连接可能已断开，只移除匹配的连接（避免 TOCTOU 竞争）
		s.registry.RemoveIfMatch(msg.Target, exitConn)
		errMsg := protocol.NewErrorMessage("exit connection failed")
		stream.Write(errMsg.Encode())
		return
	}
	defer exitStream.Close()

	// 写入 StreamRequest 消息到 Exit（Target 为空，Payload 为 OHTTP 数据）
	reqMsg := protocol.NewStreamRequestMessage("", msg.Payload)
	if _, err := exitStream.Write(reqMsg.Encode()); err != nil {
		log.Printf("写入 Exit %s 流式请求失败: %v", msg.Target, err)
		errMsg := protocol.NewErrorMessage("write to exit failed")
		stream.Write(errMsg.Encode())
		return
	}

	// 管道式转发：从 Exit 流读取 StreamChunk/StreamEnd，逐个写回 Client 流
	for {
		chunkMsg, err := protocol.Decode(exitStream)
		if err != nil {
			if err != io.EOF {
				log.Printf("读取 Exit %s 流式响应失败: %v", msg.Target, err)
			}
			return
		}

		// 将消息直接写回 Client 流
		if _, err := stream.Write(chunkMsg.Encode()); err != nil {
			log.Printf("写入客户端流式响应失败: %v", err)
			return
		}

		// 如果是 StreamEnd 或 Error，结束转发
		if chunkMsg.Type == protocol.MessageTypeStreamEnd || chunkMsg.Type == protocol.MessageTypeError {
			return
		}
	}
}

// Stop 停止 QUIC 服务器
func (s *QUICServer) Stop() error {
	if s.listener != nil {
		err := s.listener.Close()
		// 等待所有 goroutine 完成
		s.wg.Wait()
		return err
	}
	return nil
}

// Ready 返回就绪信号 channel，当 QUIC 服务器成功启动监听后会关闭该 channel
func (s *QUICServer) Ready() <-chan struct{} {
	return s.ready
}
