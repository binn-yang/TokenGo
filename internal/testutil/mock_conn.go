package testutil

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"github.com/quic-go/quic-go"
)

// MockConn 增强版 mock QUIC 连接
// 支持 AcceptStream/OpenStreamSync 预装流和 ALPN 设置
type MockConn struct {
	ID         int
	CloseCalls atomic.Int32

	ctx    context.Context
	cancel context.CancelFunc

	// 预装的流：AcceptStream 从此 channel 获取
	acceptCh chan quic.Stream
	// OpenStreamSync 返回的流
	openCh chan quic.Stream

	alpn string
	mu   sync.Mutex
}

// NewMockConn 创建新的 mock 连接
func NewMockConn(id int) *MockConn {
	ctx, cancel := context.WithCancel(context.Background())
	return &MockConn{
		ID:       id,
		ctx:      ctx,
		cancel:   cancel,
		acceptCh: make(chan quic.Stream, 16),
		openCh:   make(chan quic.Stream, 16),
	}
}

// NewMockConnWithALPN 创建带 ALPN 的 mock 连接
func NewMockConnWithALPN(id int, alpn string) *MockConn {
	mc := NewMockConn(id)
	mc.alpn = alpn
	return mc
}

// PushAcceptStream 预装一个流供 AcceptStream 返回
func (m *MockConn) PushAcceptStream(stream quic.Stream) {
	m.acceptCh <- stream
}

// PushOpenStream 预装一个流供 OpenStreamSync 返回
func (m *MockConn) PushOpenStream(stream quic.Stream) {
	m.openCh <- stream
}

func (m *MockConn) AcceptStream(ctx context.Context) (quic.Stream, error) {
	select {
	case s := <-m.acceptCh:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-m.ctx.Done():
		return nil, fmt.Errorf("connection closed")
	}
}

func (m *MockConn) AcceptUniStream(ctx context.Context) (quic.ReceiveStream, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockConn) OpenStream() (quic.Stream, error) {
	select {
	case s := <-m.openCh:
		return s, nil
	default:
		// 如果没有预装流，创建一个 pipe pair 并返回 client 端
		client, server := NewStreamPair()
		m.acceptCh <- server // 对端可以 AcceptStream 拿到
		return client, nil
	}
}

func (m *MockConn) OpenStreamSync(ctx context.Context) (quic.Stream, error) {
	select {
	case s := <-m.openCh:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-m.ctx.Done():
		return nil, fmt.Errorf("connection closed")
	}
}

func (m *MockConn) OpenUniStream() (quic.SendStream, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockConn) OpenUniStreamSync(ctx context.Context) (quic.SendStream, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4433}
}

func (m *MockConn) RemoteAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(10, 0, 0, byte(m.ID)), Port: 5000 + m.ID}
}

func (m *MockConn) CloseWithError(_ quic.ApplicationErrorCode, _ string) error {
	m.CloseCalls.Add(1)
	m.cancel()
	return nil
}

func (m *MockConn) Context() context.Context {
	return m.ctx
}

func (m *MockConn) ConnectionState() quic.ConnectionState {
	return quic.ConnectionState{
		TLS: tls.ConnectionState{
			NegotiatedProtocol: m.alpn,
		},
	}
}

func (m *MockConn) SendDatagram(_ []byte) error {
	return fmt.Errorf("not implemented")
}

func (m *MockConn) ReceiveDatagram(_ context.Context) ([]byte, error) {
	return nil, fmt.Errorf("not implemented")
}
