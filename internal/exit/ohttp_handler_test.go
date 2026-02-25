package exit

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/binn/tokengo/internal/crypto"
	"github.com/binn/tokengo/internal/protocol"
)

// setupTestHandler 创建匹配的密钥对 + 测试后端 + OHTTPHandler
func setupTestHandler(t *testing.T, backendHandler http.HandlerFunc) (*OHTTPHandler, *crypto.OHTTPClient, *httptest.Server) {
	t.Helper()

	// 生成密钥对
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}

	// 创建测试 AI 后端
	backend := httptest.NewServer(backendHandler)
	t.Cleanup(backend.Close)

	// 创建 AIClient 和 OHTTPHandler
	aiClient := NewAIClient(backend.URL, "", nil)
	handler, err := NewOHTTPHandler(kp.KeyID, kp.PrivateKey, kp.PublicKey, aiClient)
	if err != nil {
		t.Fatalf("NewOHTTPHandler failed: %v", err)
	}

	// 创建 OHTTPClient
	ohttpClient, err := crypto.NewOHTTPClient(kp.KeyID, kp.PublicKey)
	if err != nil {
		t.Fatalf("NewOHTTPClient failed: %v", err)
	}

	return handler, ohttpClient, backend
}

// encryptRequest 用 OHTTPClient 加密 HTTP 请求
func encryptRequest(t *testing.T, client *crypto.OHTTPClient, method, path string, body []byte) ([]byte, *crypto.ClientContext) {
	t.Helper()

	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, "http://ai-backend"+path, bodyReader)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.ContentLength = int64(len(body))
	}

	ohttpReq, ctx, err := client.EncapsulateRequest(req)
	if err != nil {
		t.Fatalf("EncapsulateRequest failed: %v", err)
	}

	return ohttpReq, ctx
}

func TestOHTTPHandler_ProcessRequest_Success(t *testing.T) {
	respBody := map[string]interface{}{
		"choices": []map[string]interface{}{
			{"message": map[string]string{"content": "hello world"}},
		},
	}

	handler, ohttpClient, _ := setupTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(respBody)
	})

	// 加密请求
	reqBody := []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}]}`)
	ohttpReq, clientCtx := encryptRequest(t, ohttpClient, "POST", "/v1/chat/completions", reqBody)

	// 处理请求
	ohttpResp, err := handler.ProcessRequest(ohttpReq)
	if err != nil {
		t.Fatalf("ProcessRequest failed: %v", err)
	}

	// 客户端解密响应
	resp, err := clientCtx.DecapsulateResponse(ohttpResp)
	if err != nil {
		t.Fatalf("DecapsulateResponse failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "hello world") {
		t.Errorf("response should contain 'hello world', got: %s", string(body))
	}
}

func TestOHTTPHandler_ProcessRequest_AIBackendError(t *testing.T) {
	handler, ohttpClient, _ := setupTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`{"error":"backend down"}`))
	})

	ohttpReq, clientCtx := encryptRequest(t, ohttpClient, "POST", "/v1/chat/completions", []byte(`{"model":"test"}`))

	// AI 后端返回 502，但 ProcessRequest 应该返回加密的响应（而非 error）
	ohttpResp, err := handler.ProcessRequest(ohttpReq)
	if err != nil {
		t.Fatalf("ProcessRequest should not fail on backend error: %v", err)
	}

	resp, err := clientCtx.DecapsulateResponse(ohttpResp)
	if err != nil {
		t.Fatalf("DecapsulateResponse failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("StatusCode = %d, want 502", resp.StatusCode)
	}
}

func TestOHTTPHandler_ProcessRequest_DecryptionFail(t *testing.T) {
	handler, _, _ := setupTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// 发送垃圾数据
	_, err := handler.ProcessRequest([]byte("garbage-data"))
	if err == nil {
		t.Fatal("ProcessRequest should fail on garbage data")
	}
}

func TestOHTTPHandler_ProcessStreamRequest(t *testing.T) {
	handler, ohttpClient, _ := setupTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)

		events := []string{
			`data: {"id":"1","choices":[{"delta":{"content":"hello"}}]}`,
			`data: {"id":"2","choices":[{"delta":{"content":" world"}}]}`,
			"data: [DONE]",
		}
		for _, event := range events {
			w.Write([]byte(event + "\n\n"))
			flusher.Flush()
		}
	})

	reqBody := []byte(`{"model":"test","stream":true}`)
	ohttpReq, clientCtx := encryptRequest(t, ohttpClient, "POST", "/v1/chat/completions", reqBody)

	// 写入管道
	var buf bytes.Buffer
	err := handler.ProcessStreamRequest(ohttpReq, &buf)
	if err != nil {
		t.Fatalf("ProcessStreamRequest failed: %v", err)
	}

	// 从管道读取 StreamChunk 和 StreamEnd 消息
	reader := bytes.NewReader(buf.Bytes())
	decryptor, err := clientCtx.NewStreamDecryptor()
	if err != nil {
		t.Fatalf("NewStreamDecryptor failed: %v", err)
	}

	var decryptedChunks []string
	for {
		msg, err := protocol.Decode(reader)
		if err != nil {
			t.Fatalf("Decode failed: %v", err)
		}

		if msg.Type == protocol.MessageTypeStreamEnd {
			break
		}

		if msg.Type != protocol.MessageTypeStreamChunk {
			t.Fatalf("unexpected message type: 0x%02x", msg.Type)
		}

		plaintext, err := decryptor.DecryptChunk(msg.Payload)
		if err != nil {
			t.Fatalf("DecryptChunk failed: %v", err)
		}
		decryptedChunks = append(decryptedChunks, string(plaintext))
	}

	if len(decryptedChunks) == 0 {
		t.Fatal("expected at least one decrypted chunk")
	}

	// 验证解密的内容包含原始 SSE 事件
	combined := strings.Join(decryptedChunks, "")
	if !strings.Contains(combined, "hello") {
		t.Errorf("decrypted chunks should contain 'hello', got: %s", combined)
	}
	if !strings.Contains(combined, "world") {
		t.Errorf("decrypted chunks should contain 'world', got: %s", combined)
	}
}

func TestOHTTPHandler_ProcessStreamRequest_VerifyDecryption(t *testing.T) {
	// 验证每个 chunk 独立加解密
	handler, ohttpClient, _ := setupTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		w.Write([]byte("data: chunk-A\n\n"))
		flusher.Flush()
		w.Write([]byte("data: chunk-B\n\n"))
		flusher.Flush()
	})

	ohttpReq, clientCtx := encryptRequest(t, ohttpClient, "POST", "/v1/chat/completions", []byte(`{"stream":true}`))

	var buf bytes.Buffer
	err := handler.ProcessStreamRequest(ohttpReq, &buf)
	if err != nil {
		t.Fatalf("ProcessStreamRequest failed: %v", err)
	}

	reader := bytes.NewReader(buf.Bytes())
	decryptor, err := clientCtx.NewStreamDecryptor()
	if err != nil {
		t.Fatalf("NewStreamDecryptor failed: %v", err)
	}

	chunkCount := 0
	for {
		msg, err := protocol.Decode(reader)
		if err != nil {
			t.Fatalf("Decode failed: %v", err)
		}
		if msg.Type == protocol.MessageTypeStreamEnd {
			break
		}
		if msg.Type == protocol.MessageTypeStreamChunk {
			_, err := decryptor.DecryptChunk(msg.Payload)
			if err != nil {
				t.Fatalf("DecryptChunk %d failed: %v", chunkCount, err)
			}
			chunkCount++
		}
	}

	if chunkCount < 2 {
		t.Errorf("expected at least 2 chunks, got %d", chunkCount)
	}
}

func TestOHTTPHandler_HandleKeys(t *testing.T) {
	handler, _, _ := setupTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/ohttp-keys", nil)
	rec := httptest.NewRecorder()

	handler.HandleKeys(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "application/ohttp-keys" {
		t.Errorf("Content-Type = %q, want %q", rec.Header().Get("Content-Type"), "application/ohttp-keys")
	}
	if rec.Header().Get("Cache-Control") != "max-age=86400" {
		t.Errorf("Cache-Control = %q, want %q", rec.Header().Get("Cache-Control"), "max-age=86400")
	}
	if rec.Body.Len() == 0 {
		t.Error("body should not be empty")
	}

	// 验证返回的 KeyConfig 可以被正确解码
	keyID, pubKey, err := crypto.DecodeKeyConfig(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("DecodeKeyConfig failed: %v", err)
	}
	if len(pubKey) == 0 {
		t.Error("pubKey should not be empty")
	}
	_ = keyID
}

func TestOHTTPHandler_HandleOHTTP_MethodNotAllowed(t *testing.T) {
	handler, _, _ := setupTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/ohttp", nil)
	rec := httptest.NewRecorder()

	handler.HandleOHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("StatusCode = %d, want 405", rec.Code)
	}
}

func TestOHTTPHandler_HandleKeys_MethodNotAllowed(t *testing.T) {
	handler, _, _ := setupTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/ohttp-keys", nil)
	rec := httptest.NewRecorder()

	handler.HandleKeys(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("StatusCode = %d, want 405", rec.Code)
	}
}
