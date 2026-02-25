package testutil

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/binn/tokengo/internal/crypto"
)

// TestOHTTPKeys 测试用 OHTTP 密钥对
type TestOHTTPKeys struct {
	KeyPair   *crypto.KeyPair
	KeyConfig []byte
}

// GenerateTestOHTTPKeys 生成测试用 OHTTP 密钥对
func GenerateTestOHTTPKeys(t *testing.T) *TestOHTTPKeys {
	t.Helper()
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}
	keyConfig := crypto.EncodeKeyConfig(kp.KeyID, kp.PublicKey)
	return &TestOHTTPKeys{
		KeyPair:   kp,
		KeyConfig: keyConfig,
	}
}

// NewTestAIBackend 创建测试用 AI 后端 httptest.Server
func NewTestAIBackend(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

// JSONAIBackend 返回固定 JSON 响应的 handler
func JSONAIBackend(statusCode int, body interface{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		json.NewEncoder(w).Encode(body)
	}
}

// SSEAIBackend 返回 SSE 事件流的 handler
func SSEAIBackend(events []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		for _, event := range events {
			fmt.Fprintf(w, "data: %s\n\n", event)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}
}

// RawSSEAIBackend 返回原始 SSE 数据的 handler（不加 "data: " 前缀）
func RawSSEAIBackend(chunks [][]byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		for _, chunk := range chunks {
			w.Write(chunk)
			flusher.Flush()
		}
	}
}

// ErrorAIBackend 返回指定错误码的 handler
func ErrorAIBackend(statusCode int) http.HandlerFunc {
	return JSONAIBackend(statusCode, map[string]interface{}{
		"error": map[string]interface{}{
			"message": fmt.Sprintf("error %d", statusCode),
			"type":    "api_error",
		},
	})
}
