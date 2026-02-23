package exit

import (
	"bufio"
	"bytes"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/binn/tokengo/internal/crypto"
	"github.com/binn/tokengo/internal/protocol"
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

// HandleOHTTPStream 处理流式 OHTTP 请求
func (h *OHTTPHandler) HandleOHTTPStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

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

	// 从 HPKE 上下文派生流加密器
	encryptor, err := ctx.NewStreamEncryptor()
	if err != nil {
		log.Printf("创建流加密器失败: %v", err)
		http.Error(w, "Failed to create stream encryptor", http.StatusInternalServerError)
		return
	}

	// 使用流式客户端转发到 AI 后端
	innerResp, err := h.aiClient.ForwardStream(innerReq)
	if err != nil {
		log.Printf("转发请求失败: %v", err)
		http.Error(w, "AI backend unavailable", http.StatusBadGateway)
		return
	}
	defer innerResp.Body.Close()

	// 检查 AI 后端是否返回错误
	if innerResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(innerResp.Body)
		log.Printf("AI 后端返回错误: %d - %s", innerResp.StatusCode, string(body))
		http.Error(w, "AI backend error", innerResp.StatusCode)
		return
	}

	// 设置响应头，使用 Flusher 实现分块写入
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/ohttp-chunked-res")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// 逐行读取 AI 后端 SSE 响应，累积完整事件后加密发送
	scanner := bufio.NewScanner(innerResp.Body)
	var eventBuf strings.Builder

	for scanner.Scan() {
		line := scanner.Text()
		eventBuf.WriteString(line)
		eventBuf.WriteString("\n")

		// SSE 事件以空行分隔
		if line == "" && eventBuf.Len() > 1 {
			event := eventBuf.String()
			eventBuf.Reset()

			// 加密该事件
			encrypted, err := encryptor.EncryptChunk([]byte(event))
			if err != nil {
				log.Printf("加密流式块失败: %v", err)
				break
			}

			// 写入 StreamChunk 协议消息
			msg := protocol.NewStreamChunkMessage(encrypted)
			if _, err := w.Write(msg.Encode()); err != nil {
				log.Printf("写入流式块失败: %v", err)
				break
			}
			flusher.Flush()
		}
	}

	// 写入 StreamEnd 标记
	endMsg := protocol.NewStreamEndMessage()
	w.Write(endMsg.Encode())
	flusher.Flush()
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
