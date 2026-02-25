package testutil

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

// MockPipeStream 使用 io.Pipe 实现 quic.Stream，支持双向读写
type MockPipeStream struct {
	id       quic.StreamID
	reader   *io.PipeReader
	writer   *io.PipeWriter
	ctx      context.Context
	cancel   context.CancelFunc
	closeOnce sync.Once
}

// NewStreamPair 创建一对连接的 mock stream（模拟 QUIC 双向流两端）
// 写入 client 端的数据可以从 server 端读取，反之亦然
func NewStreamPair() (client, server *MockPipeStream) {
	// client→server 管道
	csReader, csWriter := io.Pipe()
	// server→client 管道
	scReader, scWriter := io.Pipe()

	clientCtx, clientCancel := context.WithCancel(context.Background())
	serverCtx, serverCancel := context.WithCancel(context.Background())

	client = &MockPipeStream{
		id:     0,
		reader: scReader, // client 读取 server 写入的数据
		writer: csWriter, // client 写入给 server
		ctx:    clientCtx,
		cancel: clientCancel,
	}

	server = &MockPipeStream{
		id:     1,
		reader: csReader, // server 读取 client 写入的数据
		writer: scWriter, // server 写入给 client
		ctx:    serverCtx,
		cancel: serverCancel,
	}

	return client, server
}

func (s *MockPipeStream) StreamID() quic.StreamID { return s.id }

func (s *MockPipeStream) Read(p []byte) (n int, err error) {
	return s.reader.Read(p)
}

func (s *MockPipeStream) Write(p []byte) (n int, err error) {
	return s.writer.Write(p)
}

func (s *MockPipeStream) Close() error {
	s.closeOnce.Do(func() {
		s.writer.Close()
		s.cancel()
	})
	return nil
}

func (s *MockPipeStream) CancelRead(code quic.StreamErrorCode) {
	s.reader.CloseWithError(io.ErrClosedPipe)
}

func (s *MockPipeStream) CancelWrite(code quic.StreamErrorCode) {
	s.writer.CloseWithError(io.ErrClosedPipe)
}

func (s *MockPipeStream) Context() context.Context {
	return s.ctx
}

func (s *MockPipeStream) SetDeadline(t time.Time) error      { return nil }
func (s *MockPipeStream) SetReadDeadline(t time.Time) error   { return nil }
func (s *MockPipeStream) SetWriteDeadline(t time.Time) error  { return nil }
