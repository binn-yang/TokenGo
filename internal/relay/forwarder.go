package relay

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Forwarder 请求转发器 (盲转发模式：Exit 地址由 Client 指定)
type Forwarder struct {
	httpClient         *http.Client
	insecureSkipVerify bool
}

// NewForwarder 创建转发器 (盲转发模式)
func NewForwarder(insecureSkipVerify bool) *Forwarder {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: insecureSkipVerify,
		},
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
	}

	return &Forwarder{
		insecureSkipVerify: insecureSkipVerify,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   120 * time.Second,
		},
	}
}

// Forward 转发 OHTTP 请求到 Exit 节点 (盲转发模式)
// Exit 地址由 Client 在请求中指定，Relay 只负责转发
func (f *Forwarder) Forward(exitAddr string, ohttpReq []byte) ([]byte, error) {
	if exitAddr == "" {
		return nil, fmt.Errorf("Exit 地址为空")
	}

	exitURL := fmt.Sprintf("https://%s/ohttp", exitAddr)

	// 创建 HTTP 请求
	req, err := http.NewRequest(http.MethodPost, exitURL, bytes.NewReader(ohttpReq))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("Content-Type", "message/ohttp-req")
	req.Header.Set("Accept", "message/ohttp-res")

	// 发送请求
	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 Exit 节点失败: %w", err)
	}
	defer resp.Body.Close()

	// 检查响应状态
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Exit 节点返回错误: %d - %s", resp.StatusCode, string(body))
	}

	// 读取响应
	ohttpResp, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	return ohttpResp, nil
}
