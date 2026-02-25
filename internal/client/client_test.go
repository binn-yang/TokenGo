package client

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/binn/tokengo/internal/crypto"
	"github.com/binn/tokengo/internal/protocol"
	"github.com/binn/tokengo/internal/testutil"
)

func createDummyHTTPRequest() (*http.Request, error) {
	body := []byte(`{"model":"test"}`)
	req, err := http.NewRequest("POST", "http://ai-backend/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))
	return req, err
}

func TestClient_SetExit(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}

	c, err := NewClientDynamic()
	if err != nil {
		t.Fatalf("NewClientDynamic failed: %v", err)
	}

	err = c.SetExit(kp.KeyID, kp.PublicKey)
	if err != nil {
		t.Fatalf("SetExit failed: %v", err)
	}

	// pubKeyHash 应该被设置
	hash := c.GetExitPubKeyHash()
	if hash == "" {
		t.Fatal("exitPubKeyHash should not be empty")
	}

	// hash 应该与 PubKeyHash 一致
	expectedHash := crypto.PubKeyHash(kp.PublicKey)
	if hash != expectedHash {
		t.Errorf("hash = %q, want %q", hash, expectedHash)
	}

	// ohttpClient 应该被创建
	if c.ohttpClient == nil {
		t.Fatal("ohttpClient should not be nil")
	}
}

func TestClient_SetRelay_GetRelayAddr(t *testing.T) {
	c, err := NewClientDynamic()
	if err != nil {
		t.Fatalf("NewClientDynamic failed: %v", err)
	}

	if c.GetRelayAddr() != "" {
		t.Errorf("initial relayAddr should be empty")
	}

	c.SetRelay("127.0.0.1:4433")
	if c.GetRelayAddr() != "127.0.0.1:4433" {
		t.Errorf("GetRelayAddr() = %q, want %q", c.GetRelayAddr(), "127.0.0.1:4433")
	}

	c.SetRelay("10.0.0.1:5555")
	if c.GetRelayAddr() != "10.0.0.1:5555" {
		t.Errorf("GetRelayAddr() = %q, want %q", c.GetRelayAddr(), "10.0.0.1:5555")
	}
}

func TestClient_SetRelay_ThreadSafe(t *testing.T) {
	c, err := NewClientDynamic()
	if err != nil {
		t.Fatalf("NewClientDynamic failed: %v", err)
	}

	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			c.SetRelay("addr-writer")
		}
		close(done)
	}()

	for i := 0; i < 1000; i++ {
		_ = c.GetRelayAddr()
	}
	<-done
}

// --- StreamResponse 测试 ---

func newTestStreamResponse(t *testing.T) (*StreamResponse, *testutil.MockPipeStream, *crypto.KeyPair) {
	t.Helper()

	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}

	// 创建 stream pair
	clientStream, serverStream := testutil.NewStreamPair()

	// 创建 OHTTP 客户端和加密上下文
	ohttpClient, err := crypto.NewOHTTPClient(kp.KeyID, kp.PublicKey)
	if err != nil {
		t.Fatalf("NewOHTTPClient failed: %v", err)
	}

	ohttpServer, err := crypto.NewOHTTPServer(kp.KeyID, kp.PrivateKey)
	if err != nil {
		t.Fatalf("NewOHTTPServer failed: %v", err)
	}

	// 做一次加密/解密握手来建立会话密钥
	dummyReq, _ := createDummyHTTPRequest()
	ohttpReq, clientCtx, err := ohttpClient.EncapsulateRequest(dummyReq)
	if err != nil {
		t.Fatalf("EncapsulateRequest failed: %v", err)
	}

	_, serverCtx, err := ohttpServer.DecapsulateRequest(ohttpReq)
	if err != nil {
		t.Fatalf("DecapsulateRequest failed: %v", err)
	}

	// 创建 server 端的 encryptor 和 client 端的 decryptor
	encryptor, err := serverCtx.NewStreamEncryptor()
	if err != nil {
		t.Fatalf("NewStreamEncryptor failed: %v", err)
	}

	decryptor, err := clientCtx.NewStreamDecryptor()
	if err != nil {
		t.Fatalf("NewStreamDecryptor failed: %v", err)
	}

	sr := &StreamResponse{
		stream:    clientStream,
		decryptor: decryptor,
	}

	// 返回 serverStream 用于写入数据，encryptor 用于加密
	_ = encryptor

	// 我们把 encryptor 和 serverStream 放到 helper 闭包中
	// 但由于 Go 的限制，我们直接在调用端使用它们
	// 用一个更简洁的方式：把 serverStream 返回，让测试自己通过 encryptor 写

	// 其实为了简化测试，直接返回并在测试中使用
	t.Cleanup(func() {
		clientStream.Close()
		serverStream.Close()
	})

	// 通过 serverStream 写入加密的 chunks
	go func() {
		// 写入 chunks
		for _, data := range []string{"data: hello\n\n", "data: world\n\n"} {
			encrypted, err := encryptor.EncryptChunk([]byte(data))
			if err != nil {
				return
			}
			msg := protocol.NewStreamChunkMessage(encrypted)
			serverStream.Write(msg.Encode())
		}
		// 写入 end
		endMsg := protocol.NewStreamEndMessage()
		serverStream.Write(endMsg.Encode())
	}()

	return sr, serverStream, kp
}

func TestStreamResponse_ReadChunk_StreamChunk(t *testing.T) {
	sr, _, _ := newTestStreamResponse(t)

	chunk, err := sr.ReadChunk()
	if err != nil {
		t.Fatalf("ReadChunk failed: %v", err)
	}
	if string(chunk) != "data: hello\n\n" {
		t.Errorf("chunk = %q, want %q", string(chunk), "data: hello\n\n")
	}

	chunk2, err := sr.ReadChunk()
	if err != nil {
		t.Fatalf("ReadChunk 2 failed: %v", err)
	}
	if string(chunk2) != "data: world\n\n" {
		t.Errorf("chunk2 = %q, want %q", string(chunk2), "data: world\n\n")
	}
}

func TestStreamResponse_ReadChunk_StreamEnd(t *testing.T) {
	sr, _, _ := newTestStreamResponse(t)

	// 读取所有 chunks
	sr.ReadChunk()
	sr.ReadChunk()

	// 第三个应该是 StreamEnd → io.EOF
	_, err := sr.ReadChunk()
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

func TestStreamResponse_ReadChunk_Error(t *testing.T) {
	clientStream, serverStream := testutil.NewStreamPair()
	defer clientStream.Close()
	defer serverStream.Close()

	sr := &StreamResponse{
		stream:    clientStream,
		decryptor: nil, // 不需要，因为消息类型是 Error
	}

	go func() {
		errMsg := protocol.NewErrorMessage("something went wrong")
		serverStream.Write(errMsg.Encode())
	}()

	_, err := sr.ReadChunk()
	if err == nil {
		t.Fatal("ReadChunk should return error for Error message")
	}
}

func TestStreamResponse_ReadChunk_UnknownType(t *testing.T) {
	clientStream, serverStream := testutil.NewStreamPair()
	defer clientStream.Close()
	defer serverStream.Close()

	sr := &StreamResponse{
		stream:    clientStream,
		decryptor: nil,
	}

	go func() {
		// 发送一个非预期类型
		msg := protocol.NewRegisterAckMessage(nil)
		serverStream.Write(msg.Encode())
	}()

	_, err := sr.ReadChunk()
	if err == nil {
		t.Fatal("ReadChunk should return error for unknown type")
	}
}
