package exit

import (
	"bufio"
	"bytes"
	"fmt"
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

// decryptAndForward 核心逻辑: 解密 OHTTP → 转发到 AI → 加密响应
func (h *OHTTPHandler) decryptAndForward(ohttpReqData []byte) ([]byte, error) {
	innerReq, ctx, err := h.ohttpServer.DecapsulateRequest(ohttpReqData)
	if err != nil {
		return nil, fmt.Errorf("解密请求失败: %w", err)
	}

	innerResp, err := h.aiClient.Forward(innerReq)
	if err != nil {
		log.Printf("转发请求失败: %v", err)
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

	ohttpResp, err := ctx.EncapsulateResponse(innerResp)
	if err != nil {
		return nil, fmt.Errorf("加密响应失败: %w", err)
	}

	return ohttpResp, nil
}

// streamContext 流式处理上下文，由 prepareStream 创建，writeStreamChunks 消费
type streamContext struct {
	encryptor *crypto.StreamEncryptor
	resp      *http.Response
}

// prepareStream 解密请求并建立流式转发连接
func (h *OHTTPHandler) prepareStream(ohttpReqData []byte) (*streamContext, error) {
	innerReq, ctx, err := h.ohttpServer.DecapsulateRequest(ohttpReqData)
	if err != nil {
		return nil, fmt.Errorf("解密请求失败: %w", err)
	}

	encryptor, err := ctx.NewStreamEncryptor()
	if err != nil {
		return nil, fmt.Errorf("创建流加密器失败: %w", err)
	}

	innerResp, err := h.aiClient.ForwardStream(innerReq)
	if err != nil {
		return nil, fmt.Errorf("转发请求失败: %w", err)
	}

	if innerResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(innerResp.Body)
		innerResp.Body.Close()
		return nil, fmt.Errorf("AI 后端返回错误: %d - %s", innerResp.StatusCode, string(body))
	}

	return &streamContext{encryptor: encryptor, resp: innerResp}, nil
}

// writeStreamChunks 从 AI 响应读取 SSE 事件，加密并写入 StreamChunk/StreamEnd
func (h *OHTTPHandler) writeStreamChunks(sc *streamContext, writer io.Writer) error {
	defer sc.resp.Body.Close()

	scanner := bufio.NewScanner(sc.resp.Body)
	var eventBuf strings.Builder

	for scanner.Scan() {
		line := scanner.Text()
		eventBuf.WriteString(line)
		eventBuf.WriteString("\n")

		// SSE 事件以空行分隔
		if line == "" && eventBuf.Len() > 1 {
			event := eventBuf.String()
			eventBuf.Reset()

			encrypted, err := sc.encryptor.EncryptChunk([]byte(event))
			if err != nil {
				log.Printf("加密流式块失败: %v", err)
				break
			}

			msg := protocol.NewStreamChunkMessage(encrypted)
			if _, err := writer.Write(msg.Encode()); err != nil {
				log.Printf("写入流式块失败: %v", err)
				break
			}
		}
	}

	endMsg := protocol.NewStreamEndMessage()
	if _, err := writer.Write(endMsg.Encode()); err != nil {
		return fmt.Errorf("写入流式结束标记失败: %w", err)
	}

	return nil
}

// flushWriter 自动 flush 的 io.Writer 包装器 (HTTP 流式响应使用)
type flushWriter struct {
	w io.Writer
	f http.Flusher
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	fw.f.Flush()
	return n, err
}

// HandleOHTTP 处理 OHTTP 请求 (HTTP 模式)
func (h *OHTTPHandler) HandleOHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	contentType := r.Header.Get("Content-Type")
	if contentType != "message/ohttp-req" {
		http.Error(w, "Invalid content type", http.StatusBadRequest)
		return
	}

	ohttpReq, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("读取请求体失败: %v", err)
		http.Error(w, "Failed to read request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	ohttpResp, err := h.decryptAndForward(ohttpReq)
	if err != nil {
		log.Printf("%v", err)
		http.Error(w, "Failed to process request", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "message/ohttp-res")
	w.WriteHeader(http.StatusOK)
	w.Write(ohttpResp)
}

// HandleOHTTPStream 处理流式 OHTTP 请求 (HTTP 模式)
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

	ohttpReq, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("读取请求体失败: %v", err)
		http.Error(w, "Failed to read request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	sc, err := h.prepareStream(ohttpReq)
	if err != nil {
		log.Printf("%v", err)
		http.Error(w, "Failed to process stream request", http.StatusBadGateway)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		sc.resp.Body.Close()
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/ohttp-chunked-res")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	if err := h.writeStreamChunks(sc, &flushWriter{w: w, f: flusher}); err != nil {
		log.Printf("流式处理失败: %v", err)
	}
}

// ProcessRequest 处理 OHTTP 请求 (隧道模式)
func (h *OHTTPHandler) ProcessRequest(ohttpReq []byte) ([]byte, error) {
	return h.decryptAndForward(ohttpReq)
}

// ProcessStreamRequest 处理流式 OHTTP 请求 (隧道模式)
func (h *OHTTPHandler) ProcessStreamRequest(ohttpReq []byte, writer io.Writer) error {
	sc, err := h.prepareStream(ohttpReq)
	if err != nil {
		return err
	}
	return h.writeStreamChunks(sc, writer)
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
