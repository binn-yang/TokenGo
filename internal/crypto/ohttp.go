package crypto

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"

	"github.com/cloudflare/circl/hpke"
)

// OHTTPClient 客户端 OHTTP 处理器
type OHTTPClient struct {
	keyID     uint8
	pubKeyRaw []byte
	suite     hpke.Suite
}

// NewOHTTPClient 创建 OHTTP 客户端
func NewOHTTPClient(keyID uint8, publicKeyBytes []byte) (*OHTTPClient, error) {
	suite := hpke.NewSuite(KEMID, KDFID, AEADID)

	return &OHTTPClient{
		keyID:     keyID,
		pubKeyRaw: publicKeyBytes,
		suite:     suite,
	}, nil
}

// EncapsulateRequest 封装 HTTP 请求为 OHTTP 格式
// 返回加密后的 OHTTP 请求和用于解密响应的上下文
func (c *OHTTPClient) EncapsulateRequest(req *http.Request) ([]byte, *ClientContext, error) {
	// 确保 Content-Length 被设置（http.ReadRequest 需要这个来正确读取 body）
	if req.Body != nil && req.ContentLength > 0 {
		req.Header.Set("Content-Length", fmt.Sprintf("%d", req.ContentLength))
	}

	// 1. 将 HTTP 请求序列化为 Binary HTTP (简化版: 使用 HTTP/1.1 格式)
	reqBytes, err := httputil.DumpRequest(req, true)
	if err != nil {
		return nil, nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	// 2. HPKE 加密
	kemScheme := GetKEMScheme()
	pubKey, err := kemScheme.UnmarshalBinaryPublicKey(c.pubKeyRaw)
	if err != nil {
		return nil, nil, fmt.Errorf("解析公钥失败: %w", err)
	}

	sender, err := c.suite.NewSender(pubKey, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("创建 HPKE sender 失败: %w", err)
	}

	enc, sealer, err := sender.Setup(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("HPKE setup 失败: %w", err)
	}

	// 3. 构建 AAD (Associated Authenticated Data)
	// AAD = KeyID || KEM_ID || KDF_ID || AEAD_ID
	aad := make([]byte, 7)
	aad[0] = c.keyID
	binary.BigEndian.PutUint16(aad[1:3], uint16(KEMID))
	binary.BigEndian.PutUint16(aad[3:5], uint16(KDFID))
	binary.BigEndian.PutUint16(aad[5:7], uint16(AEADID))

	// 4. 加密请求
	ct, err := sealer.Seal(reqBytes, aad)
	if err != nil {
		return nil, nil, fmt.Errorf("加密请求失败: %w", err)
	}

	// 5. 构建 OHTTP 请求
	// 格式: KeyID(1) || KEM_ID(2) || KDF_ID(2) || AEAD_ID(2) || enc(Nenc) || ct(N)
	ohttpReq := make([]byte, 0, 7+len(enc)+len(ct))
	ohttpReq = append(ohttpReq, aad...)
	ohttpReq = append(ohttpReq, enc...)
	ohttpReq = append(ohttpReq, ct...)

	// 保存上下文用于解密响应
	ctx := &ClientContext{
		sealer: sealer,
	}

	return ohttpReq, ctx, nil
}

// ClientContext 客户端上下文，用于解密响应
type ClientContext struct {
	sealer hpke.Sealer
}

// DecapsulateResponse 解密 OHTTP 响应
func (ctx *ClientContext) DecapsulateResponse(data []byte) (*http.Response, error) {
	// OHTTP 响应格式: nonce(16) || ct(N)
	// 使用 AES-128-GCM，nonce 为 16 字节
	const nonceLen = 16
	if len(data) < nonceLen+16 { // 至少需要 nonce + tag
		return nil, fmt.Errorf("响应数据太短")
	}

	nonce := data[:nonceLen]
	ct := data[nonceLen:]

	// 使用 Export 导出响应密钥
	secret := ctx.sealer.Export([]byte("message/bhttp response"), 16)

	// 使用 XOR 生成响应密钥
	respKey := make([]byte, 16)
	for i := range respKey {
		respKey[i] = secret[i] ^ nonce[i]
	}

	// 创建 AES-GCM 解密器
	block, err := aes.NewCipher(respKey)
	if err != nil {
		return nil, fmt.Errorf("创建 AES 失败: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("创建 GCM 失败: %w", err)
	}

	// 检查密文长度（需要包含 GCM nonce）
	gcmNonceSize := aead.NonceSize()
	if len(ct) < gcmNonceSize+aead.Overhead() {
		return nil, fmt.Errorf("密文太短")
	}

	// 提取 GCM nonce 和实际密文
	gcmNonce := ct[:gcmNonceSize]
	actualCt := ct[gcmNonceSize:]

	// 解密响应
	respBytes, err := aead.Open(nil, gcmNonce, actualCt, nil)
	if err != nil {
		return nil, fmt.Errorf("解密响应失败: %w", err)
	}

	// 解析 HTTP 响应
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(respBytes)), nil)
	if err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}

	return resp, nil
}

// OHTTPServer 服务端 OHTTP 处理器
type OHTTPServer struct {
	keyID      uint8
	privateKey []byte
	suite      hpke.Suite
}

// NewOHTTPServer 创建 OHTTP 服务端
func NewOHTTPServer(keyID uint8, privateKeyBytes []byte) (*OHTTPServer, error) {
	suite := hpke.NewSuite(KEMID, KDFID, AEADID)

	return &OHTTPServer{
		keyID:      keyID,
		privateKey: privateKeyBytes,
		suite:      suite,
	}, nil
}

// ServerContext 服务端响应上下文
type ServerContext struct {
	opener hpke.Opener
}

// DecapsulateRequest 解密 OHTTP 请求
func (s *OHTTPServer) DecapsulateRequest(data []byte) (*http.Request, *ServerContext, error) {
	if len(data) < 7 {
		return nil, nil, fmt.Errorf("OHTTP 请求数据太短")
	}

	// 解析头部
	keyID := data[0]
	if keyID != s.keyID {
		return nil, nil, fmt.Errorf("KeyID 不匹配: 期望 %d, 收到 %d", s.keyID, keyID)
	}

	kemID := hpke.KEM(binary.BigEndian.Uint16(data[1:3]))
	kdfID := hpke.KDF(binary.BigEndian.Uint16(data[3:5]))
	aeadID := hpke.AEAD(binary.BigEndian.Uint16(data[5:7]))

	// 验证加密套件
	if kemID != KEMID || kdfID != KDFID || aeadID != AEADID {
		return nil, nil, fmt.Errorf("不支持的加密套件")
	}

	// 解析 enc 和密文
	// X25519 的 enc 大小是 32 字节
	kemScheme := GetKEMScheme()
	encSize := kemScheme.CiphertextSize()
	if len(data) < 7+encSize {
		return nil, nil, fmt.Errorf("数据不完整")
	}

	enc := data[7 : 7+encSize]
	ct := data[7+encSize:]
	aad := data[:7]

	// 解密
	privKey, err := kemScheme.UnmarshalBinaryPrivateKey(s.privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("解析私钥失败: %w", err)
	}

	receiver, err := s.suite.NewReceiver(privKey, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("创建 receiver 失败: %w", err)
	}

	opener, err := receiver.Setup(enc)
	if err != nil {
		return nil, nil, fmt.Errorf("HPKE setup 失败: %w", err)
	}

	reqBytes, err := opener.Open(ct, aad)
	if err != nil {
		return nil, nil, fmt.Errorf("解密请求失败: %w", err)
	}

	// 解析 HTTP 请求
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(reqBytes)))
	if err != nil {
		return nil, nil, fmt.Errorf("解析请求失败: %w", err)
	}

	ctx := &ServerContext{
		opener: opener,
	}

	return req, ctx, nil
}

// EncapsulateResponse 加密 HTTP 响应
func (ctx *ServerContext) EncapsulateResponse(resp *http.Response) ([]byte, error) {
	// 序列化响应
	var buf bytes.Buffer
	if err := resp.Write(&buf); err != nil {
		return nil, fmt.Errorf("序列化响应失败: %w", err)
	}
	respBytes := buf.Bytes()

	// 使用 Export 导出响应密钥
	secret := ctx.opener.Export([]byte("message/bhttp response"), 16)

	// 生成随机 nonce
	nonce := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("生成 nonce 失败: %w", err)
	}

	// XOR 生成响应密钥
	respKey := make([]byte, 16)
	for i := range respKey {
		respKey[i] = secret[i] ^ nonce[i]
	}

	// 创建 AES-GCM 加密器
	block, err := aes.NewCipher(respKey)
	if err != nil {
		return nil, fmt.Errorf("创建 AES 失败: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("创建 GCM 失败: %w", err)
	}

	// 生成随机 GCM nonce
	gcmNonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, gcmNonce); err != nil {
		return nil, fmt.Errorf("生成 GCM nonce 失败: %w", err)
	}

	// 加密响应（nonce || 密文）
	ct := aead.Seal(gcmNonce, gcmNonce, respBytes, nil)

	// 返回: nonce || ct
	result := make([]byte, 0, len(nonce)+len(ct))
	result = append(result, nonce...)
	result = append(result, ct...)

	return result, nil
}
