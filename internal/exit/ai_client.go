package exit

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"
)

// AIClient AI 后端客户端
type AIClient struct {
	baseURL      string
	apiKey       string
	httpClient   *http.Client
	streamClient *http.Client // 无全局 Timeout，用于 SSE 流式响应
}

// NewAIClient 创建 AI 客户端
func NewAIClient(baseURL, apiKey string) *AIClient {
	return &AIClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 120 * time.Second, // AI 响应可能较慢
		},
		streamClient: &http.Client{
			// 不设全局 Timeout，SSE 连接持续时间不确定
			Transport: &http.Transport{
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
	}
}

// Forward 转发请求到 AI 后端
func (c *AIClient) Forward(req *http.Request) (*http.Response, error) {
	// 构建目标 URL
	targetURL := c.baseURL + req.URL.Path
	if req.URL.RawQuery != "" {
		targetURL += "?" + req.URL.RawQuery
	}

	// 读取原始请求体
	var bodyBytes []byte
	if req.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("读取请求体失败: %w", err)
		}
		req.Body.Close()
	}

	// 创建新请求
	newReq, err := http.NewRequest(req.Method, targetURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	// 复制必要的 headers
	for key, values := range req.Header {
		// 跳过 hop-by-hop headers
		if isHopByHopHeader(key) {
			continue
		}
		for _, value := range values {
			newReq.Header.Add(key, value)
		}
	}

	// 添加 API Key (如果配置了)
	if c.apiKey != "" {
		newReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	// 发送请求
	resp, err := c.httpClient.Do(newReq)
	if err != nil {
		return nil, fmt.Errorf("请求 AI 后端失败: %w", err)
	}

	return resp, nil
}

// ForwardStream 转发流式请求到 AI 后端 (使用无超时客户端)
func (c *AIClient) ForwardStream(req *http.Request) (*http.Response, error) {
	// 构建目标 URL
	targetURL := c.baseURL + req.URL.Path
	if req.URL.RawQuery != "" {
		targetURL += "?" + req.URL.RawQuery
	}

	// 读取原始请求体
	var bodyBytes []byte
	if req.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("读取请求体失败: %w", err)
		}
		req.Body.Close()
	}

	// 创建新请求
	newReq, err := http.NewRequest(req.Method, targetURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	// 复制必要的 headers
	for key, values := range req.Header {
		if isHopByHopHeader(key) {
			continue
		}
		for _, value := range values {
			newReq.Header.Add(key, value)
		}
	}

	// 添加 API Key (如果配置了)
	if c.apiKey != "" {
		newReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	// 使用无超时的 streamClient
	resp, err := c.streamClient.Do(newReq)
	if err != nil {
		return nil, fmt.Errorf("请求 AI 后端失败: %w", err)
	}

	return resp, nil
}

// IsSSEResponse 检查响应是否为 SSE 流
func IsSSEResponse(resp *http.Response) bool {
	ct := resp.Header.Get("Content-Type")
	return len(ct) >= 17 && ct[:17] == "text/event-stream"
}

// isHopByHopHeader 检查是否是 hop-by-hop header
func isHopByHopHeader(header string) bool {
	hopByHopHeaders := map[string]bool{
		"Connection":          true,
		"Keep-Alive":          true,
		"Proxy-Authenticate":  true,
		"Proxy-Authorization": true,
		"Te":                  true,
		"Trailers":            true,
		"Transfer-Encoding":   true,
		"Upgrade":             true,
	}
	return hopByHopHeaders[header]
}
