package exit

import (
	"bytes"
	"io"
	"log"
	"net/http"

	"github.com/binn/tokengo/internal/crypto"
)

// OHTTPHandler OHTTP 请求处理器
type OHTTPHandler struct {
	ohttpServer *crypto.OHTTPServer
	aiClient    *AIClient
	keyConfig   []byte // 公钥配置 (用于 /ohttp-keys 端点)
}

// NewOHTTPHandler 创建 OHTTP 处理器
func NewOHTTPHandler(keyID uint8, privateKey, publicKey []byte, aiClient *AIClient) (*OHTTPHandler, error) {
	server, err := crypto.NewOHTTPServer(keyID, privateKey)
	if err != nil {
		return nil, err
	}

	keyConfig := crypto.EncodeKeyConfig(keyID, publicKey)

	return &OHTTPHandler{
		ohttpServer: server,
		aiClient:    aiClient,
		keyConfig:   keyConfig,
	}, nil
}

// HandleOHTTP 处理 OHTTP 请求
func (h *OHTTPHandler) HandleOHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 检查 Content-Type
	contentType := r.Header.Get("Content-Type")
	if contentType != "message/ohttp-req" {
		http.Error(w, "Invalid content type", http.StatusBadRequest)
		return
	}

	// 读取 OHTTP 请求
	ohttpReq, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("读取请求体失败: %v", err)
		http.Error(w, "Failed to read request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// 解密 OHTTP 请求
	innerReq, ctx, err := h.ohttpServer.DecapsulateRequest(ohttpReq)
	if err != nil {
		log.Printf("解密请求失败: %v", err)
		http.Error(w, "Failed to decrypt request", http.StatusBadRequest)
		return
	}

	// 转发到 AI 后端
	innerResp, err := h.aiClient.Forward(innerReq)
	if err != nil {
		log.Printf("转发请求失败: %v", err)
		// 创建错误响应
		innerResp = &http.Response{
			StatusCode: http.StatusBadGateway,
			Status:     "502 Bad Gateway",
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":"AI backend unavailable"}`))),
		}
		innerResp.Header.Set("Content-Type", "application/json")
	}
	defer innerResp.Body.Close()

	// 加密响应
	ohttpResp, err := ctx.EncapsulateResponse(innerResp)
	if err != nil {
		log.Printf("加密响应失败: %v", err)
		http.Error(w, "Failed to encrypt response", http.StatusInternalServerError)
		return
	}

	// 返回 OHTTP 响应
	w.Header().Set("Content-Type", "message/ohttp-res")
	w.WriteHeader(http.StatusOK)
	w.Write(ohttpResp)
}

// HandleKeys 返回 OHTTP 公钥配置
func (h *OHTTPHandler) HandleKeys(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/ohttp-keys")
	w.Header().Set("Cache-Control", "max-age=86400") // 缓存 1 天
	w.WriteHeader(http.StatusOK)
	w.Write(h.keyConfig)
}
