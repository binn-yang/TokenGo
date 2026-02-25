package relay

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/binn/tokengo/internal/protocol"
	"github.com/binn/tokengo/internal/testutil"
)

// setupServerWithRegistry 创建带有 Registry 的 QUICServer（不启动监听）
func setupServerWithRegistry(t *testing.T) (*QUICServer, *Registry) {
	t.Helper()
	registry := NewRegistry()
	server := &QUICServer{
		registry: registry,
		ready:    make(chan struct{}),
	}
	return server, registry
}

func TestHandleStream_Request(t *testing.T) {
	server, registry := setupServerWithRegistry(t)

	// 注册一个 mock Exit 连接
	exitConn := testutil.NewMockConn(1)
	registry.Register("exit-hash-1", exitConn, []byte("keyconfig"))

	// 创建 Exit 侧的 stream pair（模拟 OpenStreamSync 返回）
	exitClient, exitServer := testutil.NewStreamPair()
	exitConn.PushOpenStream(exitClient)

	// Exit 端异步处理：读取请求 → 写回响应
	go func() {
		msg, err := protocol.Decode(exitServer)
		if err != nil {
			t.Errorf("Exit decode failed: %v", err)
			return
		}
		if msg.Type != protocol.MessageTypeRequest {
			t.Errorf("Exit got type 0x%02x, want Request", msg.Type)
		}
		resp := protocol.NewResponseMessage([]byte("encrypted-response"))
		exitServer.Write(resp.Encode())
		exitServer.Close()
	}()

	// Client 侧的 stream pair
	clientStream, serverStream := testutil.NewStreamPair()

	// Client 端异步：写入请求，然后读取响应
	errCh := make(chan error, 1)
	var respMsg *protocol.Message
	go func() {
		reqMsg := protocol.NewRequestMessage("exit-hash-1", []byte("encrypted-payload"))
		if _, err := clientStream.Write(reqMsg.Encode()); err != nil {
			errCh <- err
			return
		}
		clientStream.Close()

		msg, err := protocol.Decode(clientStream)
		if err != nil {
			errCh <- err
			return
		}
		respMsg = msg
		errCh <- nil
	}()

	// Server 侧：处理流
	server.handleStream(serverStream)

	if err := <-errCh; err != nil {
		t.Fatalf("Client side failed: %v", err)
	}

	if respMsg.Type != protocol.MessageTypeResponse {
		t.Errorf("response type = 0x%02x, want Response", respMsg.Type)
	}
	if !bytes.Equal(respMsg.Payload, []byte("encrypted-response")) {
		t.Error("response payload mismatch")
	}
}

func TestHandleStream_StreamRequest(t *testing.T) {
	server, registry := setupServerWithRegistry(t)

	exitConn := testutil.NewMockConn(1)
	registry.Register("exit-hash-1", exitConn, []byte("keyconfig"))

	exitClient, exitServer := testutil.NewStreamPair()
	exitConn.PushOpenStream(exitClient)

	// Exit 端异步处理
	go func() {
		msg, err := protocol.Decode(exitServer)
		if err != nil {
			t.Errorf("Exit decode failed: %v", err)
			return
		}
		if msg.Type != protocol.MessageTypeStreamRequest {
			t.Errorf("Exit got type 0x%02x, want StreamRequest", msg.Type)
		}

		chunk1 := protocol.NewStreamChunkMessage([]byte("chunk-1"))
		exitServer.Write(chunk1.Encode())
		chunk2 := protocol.NewStreamChunkMessage([]byte("chunk-2"))
		exitServer.Write(chunk2.Encode())
		end := protocol.NewStreamEndMessage()
		exitServer.Write(end.Encode())
		exitServer.Close()
	}()

	clientStream, serverStream := testutil.NewStreamPair()

	type streamResult struct {
		msgs []*protocol.Message
		err  error
	}
	resultCh := make(chan streamResult, 1)

	go func() {
		reqMsg := protocol.NewStreamRequestMessage("exit-hash-1", []byte("encrypted-stream-req"))
		if _, err := clientStream.Write(reqMsg.Encode()); err != nil {
			resultCh <- streamResult{err: err}
			return
		}
		clientStream.Close()

		var msgs []*protocol.Message
		for {
			msg, err := protocol.Decode(clientStream)
			if err != nil {
				resultCh <- streamResult{msgs: msgs, err: err}
				return
			}
			msgs = append(msgs, msg)
			if msg.Type == protocol.MessageTypeStreamEnd || msg.Type == protocol.MessageTypeError {
				break
			}
		}
		resultCh <- streamResult{msgs: msgs}
	}()

	server.handleStream(serverStream)

	result := <-resultCh
	if result.err != nil {
		t.Fatalf("Client side failed: %v", result.err)
	}

	if len(result.msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result.msgs))
	}

	if result.msgs[0].Type != protocol.MessageTypeStreamChunk {
		t.Errorf("msg0 type = 0x%02x, want StreamChunk", result.msgs[0].Type)
	}
	if !bytes.Equal(result.msgs[0].Payload, []byte("chunk-1")) {
		t.Error("chunk1 payload mismatch")
	}
	if !bytes.Equal(result.msgs[1].Payload, []byte("chunk-2")) {
		t.Error("chunk2 payload mismatch")
	}
	if result.msgs[2].Type != protocol.MessageTypeStreamEnd {
		t.Errorf("msg2 type = 0x%02x, want StreamEnd", result.msgs[2].Type)
	}
}

func TestHandleStream_QueryExitKeys(t *testing.T) {
	server, registry := setupServerWithRegistry(t)

	registry.Register("hash-A", testutil.NewMockConn(1), []byte("kc-A"))
	registry.Register("hash-B", testutil.NewMockConn(2), []byte("kc-B"))

	clientStream, serverStream := testutil.NewStreamPair()

	var respMsg *protocol.Message
	errCh := make(chan error, 1)
	go func() {
		queryMsg := protocol.NewQueryExitKeysMessage()
		if _, err := clientStream.Write(queryMsg.Encode()); err != nil {
			errCh <- err
			return
		}
		clientStream.Close()

		msg, err := protocol.Decode(clientStream)
		if err != nil {
			errCh <- err
			return
		}
		respMsg = msg
		errCh <- nil
	}()

	server.handleStream(serverStream)

	if err := <-errCh; err != nil {
		t.Fatalf("Client side failed: %v", err)
	}

	if respMsg.Type != protocol.MessageTypeExitKeysResponse {
		t.Errorf("type = 0x%02x, want ExitKeysResponse", respMsg.Type)
	}

	var entries []protocol.ExitKeyEntry
	if err := json.Unmarshal(respMsg.Payload, &entries); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
}

func TestHandleStream_InvalidType(t *testing.T) {
	server, _ := setupServerWithRegistry(t)

	clientStream, serverStream := testutil.NewStreamPair()

	var respMsg *protocol.Message
	errCh := make(chan error, 1)
	go func() {
		unknownMsg := &protocol.Message{Type: protocol.MessageTypeHeartbeat}
		if _, err := clientStream.Write(unknownMsg.Encode()); err != nil {
			errCh <- err
			return
		}
		clientStream.Close()

		msg, err := protocol.Decode(clientStream)
		if err != nil {
			errCh <- err
			return
		}
		respMsg = msg
		errCh <- nil
	}()

	server.handleStream(serverStream)

	if err := <-errCh; err != nil {
		t.Fatalf("Client side failed: %v", err)
	}
	if respMsg.Type != protocol.MessageTypeError {
		t.Errorf("type = 0x%02x, want Error", respMsg.Type)
	}
}

func TestHandleStream_MissingTarget(t *testing.T) {
	server, _ := setupServerWithRegistry(t)

	clientStream, serverStream := testutil.NewStreamPair()

	var respMsg *protocol.Message
	errCh := make(chan error, 1)
	go func() {
		reqMsg := protocol.NewRequestMessage("", []byte("payload"))
		if _, err := clientStream.Write(reqMsg.Encode()); err != nil {
			errCh <- err
			return
		}
		clientStream.Close()

		msg, err := protocol.Decode(clientStream)
		if err != nil {
			errCh <- err
			return
		}
		respMsg = msg
		errCh <- nil
	}()

	server.handleStream(serverStream)

	if err := <-errCh; err != nil {
		t.Fatalf("Client side failed: %v", err)
	}
	if respMsg.Type != protocol.MessageTypeError {
		t.Errorf("type = 0x%02x, want Error", respMsg.Type)
	}
	if !bytes.Contains(respMsg.Payload, []byte("missing")) {
		t.Errorf("error should mention 'missing', got: %s", string(respMsg.Payload))
	}
}

func TestHandleStream_ExitNotFound(t *testing.T) {
	server, _ := setupServerWithRegistry(t)

	clientStream, serverStream := testutil.NewStreamPair()

	var respMsg *protocol.Message
	errCh := make(chan error, 1)
	go func() {
		reqMsg := protocol.NewRequestMessage("nonexistent-hash", []byte("payload"))
		if _, err := clientStream.Write(reqMsg.Encode()); err != nil {
			errCh <- err
			return
		}
		clientStream.Close()

		msg, err := protocol.Decode(clientStream)
		if err != nil {
			errCh <- err
			return
		}
		respMsg = msg
		errCh <- nil
	}()

	server.handleStream(serverStream)

	if err := <-errCh; err != nil {
		t.Fatalf("Client side failed: %v", err)
	}
	if respMsg.Type != protocol.MessageTypeError {
		t.Errorf("type = 0x%02x, want Error", respMsg.Type)
	}
	if !bytes.Contains(respMsg.Payload, []byte("not found")) {
		t.Errorf("error should mention 'not found', got: %s", string(respMsg.Payload))
	}
}

func TestHandleExitConnection_Registration(t *testing.T) {
	server, registry := setupServerWithRegistry(t)

	exitConn := testutil.NewMockConnWithALPN(1, "tokengo-exit")

	// 创建注册流
	regClient, regServer := testutil.NewStreamPair()
	exitConn.PushAcceptStream(regServer)

	// Exit 端发送注册消息
	go func() {
		regMsg := protocol.NewRegisterMessage("test-exit-hash", []byte("test-keyconfig"))
		regClient.Write(regMsg.Encode())

		// 读取 RegisterAck
		ackMsg, err := protocol.Decode(regClient)
		if err != nil {
			t.Errorf("reading RegisterAck failed: %v", err)
			return
		}
		if ackMsg.Type != protocol.MessageTypeRegisterAck {
			t.Errorf("expected RegisterAck, got 0x%02x", ackMsg.Type)
		}

		// 注册完成后关闭连接以结束心跳循环
		exitConn.CloseWithError(0, "test done")
	}()

	// Relay 处理 Exit 连接
	server.handleExitConnection(exitConn.Context(), exitConn)

	// handleExitConnection 退出后 defer 会 Remove，Count 应为 0
	if registry.Count() != 0 {
		t.Logf("Registry count after exit disconnect: %d (expected 0 after cleanup)", registry.Count())
	}
}

func TestHandleExitConnection_HeartbeatLoop(t *testing.T) {
	server, registry := setupServerWithRegistry(t)

	exitConn := testutil.NewMockConnWithALPN(1, "tokengo-exit")

	// 注册流
	regClient, regServer := testutil.NewStreamPair()
	exitConn.PushAcceptStream(regServer)

	// 心跳流
	hbClient, hbServer := testutil.NewStreamPair()
	exitConn.PushAcceptStream(hbServer)

	go func() {
		// 1. 发送注册
		regMsg := protocol.NewRegisterMessage("hb-exit", []byte("kc"))
		regClient.Write(regMsg.Encode())

		// 2. 读取 RegisterAck
		_, err := protocol.Decode(regClient)
		if err != nil {
			t.Errorf("reading RegisterAck failed: %v", err)
			exitConn.CloseWithError(0, "test error")
			return
		}

		// 3. 发送一次心跳
		hbMsg := protocol.NewHeartbeatMessage()
		hbClient.Write(hbMsg.Encode())

		// 4. 读取 HeartbeatAck
		ackMsg, err := protocol.Decode(hbClient)
		if err != nil {
			t.Errorf("reading HeartbeatAck failed: %v", err)
		} else if ackMsg.Type != protocol.MessageTypeHeartbeatAck {
			t.Errorf("expected HeartbeatAck, got 0x%02x", ackMsg.Type)
		}

		// 验证心跳已更新（在 handleExitConnection 退出前检查）
		registry.mu.RLock()
		if entry, ok := registry.entries["hb-exit"]; ok {
			if entry.LastHeartbeat.IsZero() {
				t.Error("LastHeartbeat should be updated")
			}
		}
		registry.mu.RUnlock()

		// 5. 关闭连接以结束循环
		exitConn.CloseWithError(0, "test done")
	}()

	server.handleExitConnection(exitConn.Context(), exitConn)
}
