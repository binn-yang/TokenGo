package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/binn/tokengo/internal/cert"
	"github.com/binn/tokengo/internal/client"
	"github.com/binn/tokengo/internal/crypto"
	"github.com/binn/tokengo/internal/exit"
	"github.com/binn/tokengo/internal/identity"
	"github.com/binn/tokengo/internal/relay"
)

// testEnv 集成测试环境
type testEnv struct {
	relayAddr  string
	backend    *httptest.Server
	ohttpKeys  *crypto.KeyPair
	keyConfig  []byte
	pubKeyHash string
	registry   *relay.Registry
	cancel     context.CancelFunc
}

func getFreeUDPAddr(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	addr := conn.LocalAddr().String()
	conn.Close()
	return addr
}

// setupIntegrationTest 启动完整的 Client→Relay→Exit 链路
func setupIntegrationTest(t *testing.T, backendHandler http.HandlerFunc) *testEnv {
	t.Helper()

	// 1. AI 后端
	backend := httptest.NewServer(backendHandler)
	t.Cleanup(backend.Close)

	// 2. OHTTP 密钥
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}
	keyConfig := crypto.EncodeKeyConfig(kp.KeyID, kp.PublicKey)
	pubKeyHash := crypto.PubKeyHash(kp.PublicKey)

	// 3. Relay 身份和 TLS 证书
	relayIdentity, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate failed: %v", err)
	}
	tlsCert, err := cert.GeneratePeerIDCert(relayIdentity.PrivKey, "")
	if err != nil {
		t.Fatalf("GeneratePeerIDCert failed: %v", err)
	}
	serverTLSConfig := cert.CreateServerTLSConfig(tlsCert)

	// 4. 获取空闲端口并启动 Relay
	relayAddr := getFreeUDPAddr(t)
	registry := relay.NewRegistry()
	quicServer := relay.NewQUICServer(relayAddr, serverTLSConfig, registry)

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		if err := quicServer.Start(ctx); err != nil {
			// context 取消后 Start 会返回 nil
		}
	}()

	// 等待 Relay 就绪
	select {
	case <-quicServer.Ready():
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("Relay 启动超时")
	}

	// 5. 创建 OHTTPHandler 和 TunnelClient
	aiClient := exit.NewAIClient(backend.URL, "", nil)
	ohttpHandler, err := exit.NewOHTTPHandler(kp.KeyID, kp.PrivateKey, kp.PublicKey, aiClient)
	if err != nil {
		cancel()
		t.Fatalf("NewOHTTPHandler failed: %v", err)
	}

	tunnel := exit.NewTunnelClientStatic(relayAddr, pubKeyHash, keyConfig, ohttpHandler)

	go func() {
		tunnel.Start(ctx)
	}()

	// 等待 Exit 注册完成
	select {
	case <-tunnel.Ready():
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("Exit 注册超时")
	}

	t.Cleanup(func() {
		cancel()
		tunnel.Stop()
		quicServer.Stop()
	})

	return &testEnv{
		relayAddr:  relayAddr,
		backend:    backend,
		ohttpKeys:  kp,
		keyConfig:  keyConfig,
		pubKeyHash: pubKeyHash,
		registry:   registry,
		cancel:     cancel,
	}
}

// newTestClient 创建连接到测试环境的 Client
func newTestClient(t *testing.T, env *testEnv) *client.Client {
	t.Helper()
	c, err := client.NewClient(env.relayAddr, env.ohttpKeys.KeyID, env.ohttpKeys.PublicKey)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Client.Connect failed: %v", err)
	}
	return c
}

func TestIntegration_NonStreamingRoundTrip(t *testing.T) {
	env := setupIntegrationTest(t, func(w http.ResponseWriter, r *http.Request) {
		// 验证请求到达了 AI 后端
		body, _ := io.ReadAll(r.Body)
		defer r.Body.Close()

		var reqBody map[string]interface{}
		json.Unmarshal(body, &reqBody)

		resp := map[string]interface{}{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"choices": []map[string]interface{}{
				{"message": map[string]string{"role": "assistant", "content": "Hello from AI!"}},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	})

	c := newTestClient(t, env)

	// 发送请求
	reqBody := []byte(`{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`)
	req, _ := http.NewRequest("POST", "http://ai-backend/v1/chat/completions", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(reqBody))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := c.SendRequest(ctx, req)
	if err != nil {
		t.Fatalf("SendRequest failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
	}

	respBody, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(respBody), "Hello from AI!") {
		t.Errorf("response should contain 'Hello from AI!', got: %s", string(respBody))
	}
}

func TestIntegration_StreamingRoundTrip(t *testing.T) {
	env := setupIntegrationTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)

		flusher := w.(http.Flusher)

		events := []string{
			`{"id":"1","choices":[{"delta":{"content":"Hello"}}]}`,
			`{"id":"2","choices":[{"delta":{"content":" World"}}]}`,
			`{"id":"3","choices":[{"delta":{"content":"!"}}]}`,
		}

		for _, event := range events {
			fmt.Fprintf(w, "data: %s\n\n", event)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	})

	c := newTestClient(t, env)

	reqBody := []byte(`{"model":"test","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	req, _ := http.NewRequest("POST", "http://ai-backend/v1/chat/completions", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(reqBody))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	streamResp, err := c.SendStreamRequest(ctx, req)
	if err != nil {
		t.Fatalf("SendStreamRequest failed: %v", err)
	}
	defer streamResp.Close()

	var chunks []string
	for {
		chunk, err := streamResp.ReadChunk()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("ReadChunk failed: %v", err)
		}
		chunks = append(chunks, string(chunk))
	}

	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}

	combined := strings.Join(chunks, "")
	if !strings.Contains(combined, "Hello") {
		t.Errorf("chunks should contain 'Hello', got: %s", combined)
	}
	if !strings.Contains(combined, "World") {
		t.Errorf("chunks should contain 'World', got: %s", combined)
	}
}

func TestIntegration_QueryExitKeys(t *testing.T) {
	env := setupIntegrationTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	c := newTestClient(t, env)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	entries, err := c.QueryExitKeys(ctx)
	if err != nil {
		t.Fatalf("QueryExitKeys failed: %v", err)
	}

	if len(entries) == 0 {
		t.Fatal("expected at least one exit key entry")
	}

	found := false
	for _, entry := range entries {
		if entry.PubKeyHash == env.pubKeyHash {
			found = true
			// 验证 KeyConfig 可以被解码
			keyID, pubKey, err := crypto.DecodeKeyConfig(entry.KeyConfig)
			if err != nil {
				t.Fatalf("DecodeKeyConfig failed: %v", err)
			}
			if keyID != env.ohttpKeys.KeyID {
				t.Errorf("KeyID = %d, want %d", keyID, env.ohttpKeys.KeyID)
			}
			if !bytes.Equal(pubKey, env.ohttpKeys.PublicKey) {
				t.Error("public key mismatch")
			}
			break
		}
	}
	if !found {
		t.Errorf("exit key with hash %s not found in entries", env.pubKeyHash)
	}
}

func TestIntegration_MultipleRequests(t *testing.T) {
	var requestCount atomic.Int32
	env := setupIntegrationTest(t, func(w http.ResponseWriter, r *http.Request) {
		cnt := requestCount.Add(1)
		resp := map[string]interface{}{
			"id": fmt.Sprintf("req-%d", cnt),
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": fmt.Sprintf("response %d", cnt)}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	})

	c := newTestClient(t, env)

	for i := 1; i <= 10; i++ {
		reqBody := []byte(fmt.Sprintf(`{"model":"test","messages":[{"role":"user","content":"request %d"}]}`, i))
		req, _ := http.NewRequest("POST", "http://ai-backend/v1/chat/completions", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		req.ContentLength = int64(len(reqBody))

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

		resp, err := c.SendRequest(ctx, req)
		cancel()
		if err != nil {
			t.Fatalf("Request %d failed: %v", i, err)
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Request %d: StatusCode = %d, want 200", i, resp.StatusCode)
		}

		if !strings.Contains(string(body), "response") {
			t.Errorf("Request %d: body missing 'response': %s", i, string(body))
		}
	}

	if got := requestCount.Load(); got != 10 {
		t.Errorf("AI backend received %d requests, want 10", got)
	}
}
