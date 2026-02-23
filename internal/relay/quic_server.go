package relay

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"sync"

	"github.com/binn/tokengo/internal/protocol"
	"github.com/quic-go/quic-go"
)

// QUICServer QUIC 服务器
type QUICServer struct {
	listener  *quic.Listener
	forwarder *Forwarder
	addr      string
	tlsConfig *tls.Config
	wg        sync.WaitGroup // 追踪所有 goroutine
}

// NewQUICServer 创建 QUIC 服务器
func NewQUICServer(addr string, tlsConfig *tls.Config, forwarder *Forwarder) *QUICServer {
	return &QUICServer{
		addr:      addr,
		tlsConfig: tlsConfig,
		forwarder: forwarder,
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

// handleConnection 处理单个 QUIC 连接
func (s *QUICServer) handleConnection(ctx context.Context, conn quic.Connection) {
	defer conn.CloseWithError(0, "connection closed")

	log.Printf("新连接: %s", conn.RemoteAddr())

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
	default:
		log.Printf("无效的消息类型: %d", msg.Type)
		errMsg := protocol.NewErrorMessage("invalid message type")
		stream.Write(errMsg.Encode())
	}
}

// handleForwardRequest 处理转发请求
func (s *QUICServer) handleForwardRequest(stream quic.Stream, msg *protocol.Message) {
	// 验证目标地址
	if msg.Target == "" {
		log.Printf("请求缺少目标地址")
		errMsg := protocol.NewErrorMessage("missing target address")
		stream.Write(errMsg.Encode())
		return
	}

	// 转发到 Client 指定的 Exit 节点 (盲转发)
	ohttpResp, err := s.forwarder.Forward(msg.Target, msg.Payload)
	if err != nil {
		log.Printf("转发失败: %v", err)
		// 返回通用错误消息，不泄露内部信息
		errMsg := protocol.NewErrorMessage("request forwarding failed")
		stream.Write(errMsg.Encode())
		return
	}

	// 返回响应
	respMsg := protocol.NewResponseMessage(ohttpResp)
	if _, err := stream.Write(respMsg.Encode()); err != nil {
		log.Printf("写入响应失败: %v", err)
	}
}

// handleStreamForwardRequest 处理流式转发请求
func (s *QUICServer) handleStreamForwardRequest(stream quic.Stream, msg *protocol.Message) {
	if msg.Target == "" {
		log.Printf("流式请求缺少目标地址")
		errMsg := protocol.NewErrorMessage("missing target address")
		stream.Write(errMsg.Encode())
		return
	}

	// 流式转发：Exit 的响应直接管道到 QUIC stream
	if err := s.forwarder.ForwardStream(msg.Target, msg.Payload, stream); err != nil {
		log.Printf("流式转发失败: %v", err)
		errMsg := protocol.NewErrorMessage("stream forwarding failed")
		stream.Write(errMsg.Encode())
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
