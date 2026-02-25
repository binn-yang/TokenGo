package exit

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestAIClient(t *testing.T, handler http.HandlerFunc) (*AIClient, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := NewAIClient(server.URL, "", nil)
	return client, server
}

func TestAIClient_Forward_Success(t *testing.T) {
	client, _ := newTestAIClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"hello"}}]}`))
	})

	req, _ := http.NewRequest("POST", "http://dummy/v1/chat/completions", bytes.NewReader([]byte(`{"model":"test"}`)))
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Forward(req)
	if err != nil {
		t.Fatalf("Forward failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "hello") {
		t.Errorf("response body missing 'hello': %s", string(body))
	}
}

func TestAIClient_Forward_WithAPIKey(t *testing.T) {
	var gotAuth string
	client, _ := newTestAIClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	})
	client.apiKey = "sk-test-key"

	req, _ := http.NewRequest("POST", "http://dummy/v1/chat/completions", nil)
	_, err := client.Forward(req)
	if err != nil {
		t.Fatalf("Forward failed: %v", err)
	}

	if gotAuth != "Bearer sk-test-key" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer sk-test-key")
	}
}

func TestAIClient_Forward_WithCustomHeaders(t *testing.T) {
	var gotHeaders http.Header
	client, _ := newTestAIClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	})
	client.apiKey = "sk-should-be-ignored"
	client.headers = map[string]string{
		"X-Custom-Auth": "custom-token",
		"X-Region":      "us-east-1",
	}

	req, _ := http.NewRequest("POST", "http://dummy/v1/chat/completions", nil)
	_, err := client.Forward(req)
	if err != nil {
		t.Fatalf("Forward failed: %v", err)
	}

	// 自定义 headers 应该覆盖 apiKey
	if gotHeaders.Get("X-Custom-Auth") != "custom-token" {
		t.Errorf("X-Custom-Auth = %q, want %q", gotHeaders.Get("X-Custom-Auth"), "custom-token")
	}
	if gotHeaders.Get("X-Region") != "us-east-1" {
		t.Errorf("X-Region = %q, want %q", gotHeaders.Get("X-Region"), "us-east-1")
	}
	// 当有自定义 headers 时，apiKey 不应设置
	if gotHeaders.Get("Authorization") != "" {
		t.Errorf("Authorization should be empty when custom headers are set, got %q", gotHeaders.Get("Authorization"))
	}
}

func TestAIClient_Forward_HopByHopHeadersFiltered(t *testing.T) {
	var gotHeaders http.Header
	client, _ := newTestAIClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	})

	req, _ := http.NewRequest("POST", "http://dummy/v1/chat/completions", nil)
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Transfer-Encoding", "chunked")
	req.Header.Set("Keep-Alive", "timeout=5")
	req.Header.Set("X-Custom", "should-pass")

	_, err := client.Forward(req)
	if err != nil {
		t.Fatalf("Forward failed: %v", err)
	}

	// Hop-by-hop headers 应被过滤
	if gotHeaders.Get("Connection") != "" {
		t.Error("Connection header should be filtered")
	}
	if gotHeaders.Get("Transfer-Encoding") != "" {
		t.Error("Transfer-Encoding header should be filtered")
	}
	if gotHeaders.Get("Keep-Alive") != "" {
		t.Error("Keep-Alive header should be filtered")
	}
	// 非 hop-by-hop header 应通过
	if gotHeaders.Get("X-Custom") != "should-pass" {
		t.Errorf("X-Custom = %q, want %q", gotHeaders.Get("X-Custom"), "should-pass")
	}
}

func TestAIClient_Forward_ServerError(t *testing.T) {
	client, _ := newTestAIClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal"}`))
	})

	req, _ := http.NewRequest("POST", "http://dummy/v1/chat/completions", nil)
	resp, err := client.Forward(req)
	if err != nil {
		t.Fatalf("Forward should not error on 500: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500", resp.StatusCode)
	}
}

func TestAIClient_Forward_URLPath(t *testing.T) {
	var gotPath string
	client, _ := newTestAIClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	})

	req, _ := http.NewRequest("GET", "http://dummy/v1/models", nil)
	_, err := client.Forward(req)
	if err != nil {
		t.Fatalf("Forward failed: %v", err)
	}

	if gotPath != "/v1/models" {
		t.Errorf("path = %q, want %q", gotPath, "/v1/models")
	}
}

func TestAIClient_ForwardStream_SSEResponse(t *testing.T) {
	client, _ := newTestAIClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		w.Write([]byte("data: {\"chunk\":1}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: {\"chunk\":2}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	})

	req, _ := http.NewRequest("POST", "http://dummy/v1/chat/completions", bytes.NewReader([]byte(`{"stream":true}`)))
	resp, err := client.ForwardStream(req)
	if err != nil {
		t.Fatalf("ForwardStream failed: %v", err)
	}
	defer resp.Body.Close()

	if !IsSSEResponse(resp) {
		t.Error("expected SSE response")
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "chunk") {
		t.Error("SSE body should contain chunk data")
	}
}

func TestIsSSEResponse(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		want        bool
	}{
		{"SSE", "text/event-stream", true},
		{"SSE with charset", "text/event-stream; charset=utf-8", true},
		{"JSON", "application/json", false},
		{"empty", "", false},
		{"partial match", "text/event", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{Header: http.Header{}}
			resp.Header.Set("Content-Type", tt.contentType)
			got := IsSSEResponse(resp)
			if got != tt.want {
				t.Errorf("IsSSEResponse(%q) = %v, want %v", tt.contentType, got, tt.want)
			}
		})
	}
}

func TestIsHopByHopHeader(t *testing.T) {
	tests := []struct {
		header string
		want   bool
	}{
		{"Connection", true},
		{"Keep-Alive", true},
		{"Transfer-Encoding", true},
		{"Upgrade", true},
		{"Proxy-Authenticate", true},
		{"Content-Type", false},
		{"Authorization", false},
		{"X-Custom", false},
	}

	for _, tt := range tests {
		t.Run(tt.header, func(t *testing.T) {
			got := isHopByHopHeader(tt.header)
			if got != tt.want {
				t.Errorf("isHopByHopHeader(%q) = %v, want %v", tt.header, got, tt.want)
			}
		})
	}
}
