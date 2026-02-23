package crypto

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

func TestGenerateKeyPair(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}

	if len(kp.PublicKey) == 0 {
		t.Error("PublicKey is empty")
	}
	if len(kp.PrivateKey) == 0 {
		t.Error("PrivateKey is empty")
	}

	// X25519 公钥应该是 32 字节
	if len(kp.PublicKey) != 32 {
		t.Errorf("PublicKey length = %d, want 32", len(kp.PublicKey))
	}
}

func TestEncodeDecodeKeyConfig(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}

	// 编码
	encoded := EncodeKeyConfig(kp.KeyID, kp.PublicKey)
	if len(encoded) == 0 {
		t.Error("EncodeKeyConfig returned empty")
	}

	// 解码
	keyID, pubKey, err := DecodeKeyConfig(encoded)
	if err != nil {
		t.Fatalf("DecodeKeyConfig failed: %v", err)
	}

	if keyID != kp.KeyID {
		t.Errorf("KeyID = %d, want %d", keyID, kp.KeyID)
	}
	if !bytes.Equal(pubKey, kp.PublicKey) {
		t.Error("PublicKey mismatch")
	}
}

func TestOHTTPEncryptDecrypt(t *testing.T) {
	// 生成密钥对
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}

	// 创建客户端和服务端
	client, err := NewOHTTPClient(kp.KeyID, kp.PublicKey)
	if err != nil {
		t.Fatalf("NewOHTTPClient failed: %v", err)
	}

	server, err := NewOHTTPServer(kp.KeyID, kp.PrivateKey)
	if err != nil {
		t.Fatalf("NewOHTTPServer failed: %v", err)
	}

	// 创建测试请求
	body := `{"message": "hello"}`
	req, err := http.NewRequest("POST", "http://example.com/test", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))

	// 客户端加密请求
	encryptedReq, clientCtx, err := client.EncapsulateRequest(req)
	if err != nil {
		t.Fatalf("EncapsulateRequest failed: %v", err)
	}

	if len(encryptedReq) == 0 {
		t.Error("Encrypted request is empty")
	}

	// 服务端解密请求
	decryptedReq, serverCtx, err := server.DecapsulateRequest(encryptedReq)
	if err != nil {
		t.Fatalf("DecapsulateRequest failed: %v", err)
	}

	if decryptedReq.Method != "POST" {
		t.Errorf("Method = %s, want POST", decryptedReq.Method)
	}

	// 创建测试响应
	respBody := `{"response": "world"}`
	resp := &http.Response{
		StatusCode: 200,
		Status:     "200 OK",
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
		Body:       nil,
	}
	resp.Header.Set("Content-Type", "application/json")
	resp.Body = newReadCloser([]byte(respBody))
	resp.ContentLength = int64(len(respBody))

	// 服务端加密响应
	encryptedResp, err := serverCtx.EncapsulateResponse(resp)
	if err != nil {
		t.Fatalf("EncapsulateResponse failed: %v", err)
	}

	if len(encryptedResp) == 0 {
		t.Error("Encrypted response is empty")
	}

	// 客户端解密响应
	decryptedResp, err := clientCtx.DecapsulateResponse(encryptedResp)
	if err != nil {
		t.Fatalf("DecapsulateResponse failed: %v", err)
	}

	if decryptedResp.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", decryptedResp.StatusCode)
	}
}

func TestOHTTPKeyIDMismatch(t *testing.T) {
	// 生成密钥对
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}

	// 创建客户端和服务端（使用不同的 KeyID）
	client, err := NewOHTTPClient(kp.KeyID, kp.PublicKey)
	if err != nil {
		t.Fatalf("NewOHTTPClient failed: %v", err)
	}

	differentKeyID := kp.KeyID + 1
	server, err := NewOHTTPServer(differentKeyID, kp.PrivateKey)
	if err != nil {
		t.Fatalf("NewOHTTPServer failed: %v", err)
	}

	// 创建测试请求
	req, _ := http.NewRequest("GET", "http://example.com/test", nil)

	// 客户端加密请求
	encryptedReq, _, err := client.EncapsulateRequest(req)
	if err != nil {
		t.Fatalf("EncapsulateRequest failed: %v", err)
	}

	// 服务端解密应该失败（KeyID 不匹配）
	_, _, err = server.DecapsulateRequest(encryptedReq)
	if err == nil {
		t.Error("DecapsulateRequest should fail with mismatched KeyID")
	}
}

func TestStreamEncryptDecrypt(t *testing.T) {
	// 生成密钥对
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}

	// 创建客户端和服务端
	client, err := NewOHTTPClient(kp.KeyID, kp.PublicKey)
	if err != nil {
		t.Fatalf("NewOHTTPClient failed: %v", err)
	}

	server, err := NewOHTTPServer(kp.KeyID, kp.PrivateKey)
	if err != nil {
		t.Fatalf("NewOHTTPServer failed: %v", err)
	}

	// 执行一次 OHTTP 握手以建立 HPKE 会话
	body := `{"model":"test","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req, err := http.NewRequest("POST", "http://example.com/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))

	encryptedReq, clientCtx, err := client.EncapsulateRequest(req)
	if err != nil {
		t.Fatalf("EncapsulateRequest failed: %v", err)
	}

	_, serverCtx, err := server.DecapsulateRequest(encryptedReq)
	if err != nil {
		t.Fatalf("DecapsulateRequest failed: %v", err)
	}

	// 从会话派生流加密器和解密器
	encryptor, err := serverCtx.NewStreamEncryptor()
	if err != nil {
		t.Fatalf("NewStreamEncryptor failed: %v", err)
	}

	decryptor, err := clientCtx.NewStreamDecryptor()
	if err != nil {
		t.Fatalf("NewStreamDecryptor failed: %v", err)
	}

	// 测试多个 SSE 事件加解密
	events := []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\" world\"}}]}\n\n",
		"data: [DONE]\n\n",
	}

	for i, event := range events {
		encrypted, err := encryptor.EncryptChunk([]byte(event))
		if err != nil {
			t.Fatalf("EncryptChunk[%d] failed: %v", i, err)
		}

		decrypted, err := decryptor.DecryptChunk(encrypted)
		if err != nil {
			t.Fatalf("DecryptChunk[%d] failed: %v", i, err)
		}

		if string(decrypted) != event {
			t.Errorf("Chunk[%d]: got %q, want %q", i, string(decrypted), event)
		}
	}
}

func TestStreamEncryptorDifferentNonces(t *testing.T) {
	// 验证每个 chunk 使用不同的 nonce
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}

	client, err := NewOHTTPClient(kp.KeyID, kp.PublicKey)
	if err != nil {
		t.Fatalf("NewOHTTPClient failed: %v", err)
	}

	server, err := NewOHTTPServer(kp.KeyID, kp.PrivateKey)
	if err != nil {
		t.Fatalf("NewOHTTPServer failed: %v", err)
	}

	body := `{"test":"stream"}`
	req, _ := http.NewRequest("POST", "http://example.com/test", strings.NewReader(body))
	req.ContentLength = int64(len(body))

	encryptedReq, _, err := client.EncapsulateRequest(req)
	if err != nil {
		t.Fatalf("EncapsulateRequest failed: %v", err)
	}

	_, serverCtx, err := server.DecapsulateRequest(encryptedReq)
	if err != nil {
		t.Fatalf("DecapsulateRequest failed: %v", err)
	}

	encryptor, err := serverCtx.NewStreamEncryptor()
	if err != nil {
		t.Fatalf("NewStreamEncryptor failed: %v", err)
	}

	// 加密相同数据两次，密文应不同（因为随机 nonce）
	data := []byte("same data")
	ct1, err := encryptor.EncryptChunk(data)
	if err != nil {
		t.Fatalf("EncryptChunk 1 failed: %v", err)
	}
	ct2, err := encryptor.EncryptChunk(data)
	if err != nil {
		t.Fatalf("EncryptChunk 2 failed: %v", err)
	}

	if bytes.Equal(ct1, ct2) {
		t.Error("Two encryptions of the same data produced identical ciphertext (nonce reuse)")
	}
}

func TestDecodeKeyConfigInvalid(t *testing.T) {
	// 测试空数据
	_, _, err := DecodeKeyConfig([]byte{})
	if err == nil {
		t.Error("DecodeKeyConfig should fail with empty data")
	}

	// 测试数据太短
	_, _, err = DecodeKeyConfig([]byte{1, 2, 3})
	if err == nil {
		t.Error("DecodeKeyConfig should fail with short data")
	}
}

// readCloser 辅助类型
type readCloser struct {
	*bytes.Reader
}

func newReadCloser(data []byte) *readCloser {
	return &readCloser{bytes.NewReader(data)}
}

func (r *readCloser) Close() error {
	return nil
}
