package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"

	"github.com/cloudflare/circl/hpke"
	"github.com/cloudflare/circl/kem"
)

// OHTTP 配置常量 (RFC 9458)
const (
	// HPKE 参数: DHKEM(X25519, HKDF-SHA256), HKDF-SHA256, AES-128-GCM
	KEMID  hpke.KEM  = hpke.KEM_X25519_HKDF_SHA256
	KDFID  hpke.KDF  = hpke.KDF_HKDF_SHA256
	AEADID hpke.AEAD = hpke.AEAD_AES128GCM
)

// KeyPair OHTTP 密钥对
type KeyPair struct {
	PublicKey  []byte
	PrivateKey []byte
	KeyID      uint8
}

// GetKEMScheme 获取 KEM scheme
func GetKEMScheme() kem.Scheme {
	return KEMID.Scheme()
}

// GenerateKeyPair 生成 OHTTP 密钥对
func GenerateKeyPair() (*KeyPair, error) {
	scheme := GetKEMScheme()
	publicKey, privateKey, err := scheme.GenerateKeyPair()
	if err != nil {
		return nil, fmt.Errorf("生成密钥对失败: %w", err)
	}

	pubBytes, err := publicKey.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("序列化公钥失败: %w", err)
	}

	privBytes, err := privateKey.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("序列化私钥失败: %w", err)
	}

	// 生成随机 KeyID
	keyID := make([]byte, 1)
	if _, err := rand.Read(keyID); err != nil {
		return nil, fmt.Errorf("生成 KeyID 失败: %w", err)
	}

	return &KeyPair{
		PublicKey:  pubBytes,
		PrivateKey: privBytes,
		KeyID:      keyID[0],
	}, nil
}

// SaveKeyPair 保存密钥对到文件
func SaveKeyPair(kp *KeyPair, pubPath, privPath string) error {
	// 保存公钥 (包含 KeyConfig 格式)
	pubConfig := EncodeKeyConfig(kp.KeyID, kp.PublicKey)
	pubB64 := base64.StdEncoding.EncodeToString(pubConfig)
	if err := os.WriteFile(pubPath, []byte(pubB64), 0644); err != nil {
		return fmt.Errorf("保存公钥失败: %w", err)
	}

	// 保存私钥
	privB64 := base64.StdEncoding.EncodeToString(kp.PrivateKey)
	if err := os.WriteFile(privPath, []byte(privB64), 0600); err != nil {
		return fmt.Errorf("保存私钥失败: %w", err)
	}

	return nil
}

// LoadPrivateKey 从文件加载私钥
func LoadPrivateKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取私钥文件失败: %w", err)
	}

	privBytes, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil {
		return nil, fmt.Errorf("解码私钥失败: %w", err)
	}

	return privBytes, nil
}

// LoadPublicKeyConfig 从 base64 字符串加载公钥配置
func LoadPublicKeyConfig(b64 string) (keyID uint8, pubKey []byte, err error) {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return 0, nil, fmt.Errorf("解码公钥配置失败: %w", err)
	}

	return DecodeKeyConfig(data)
}

// LoadPublicKeyConfigBytes 从字节数组加载公钥配置
func LoadPublicKeyConfigBytes(data []byte) (keyID uint8, pubKey []byte, err error) {
	return DecodeKeyConfig(data)
}

// EncodeKeyConfig 编码 OHTTP KeyConfig (RFC 9458 Section 3)
// 格式: KeyID(1) || KEM_ID(2) || PublicKey(Npk) || CipherSuites
func EncodeKeyConfig(keyID uint8, publicKey []byte) []byte {
	// CipherSuite: KDF_ID(2) || AEAD_ID(2)
	cipherSuite := make([]byte, 4)
	binary.BigEndian.PutUint16(cipherSuite[0:2], uint16(KDFID))
	binary.BigEndian.PutUint16(cipherSuite[2:4], uint16(AEADID))

	// KeyConfig 长度计算
	// KeyID(1) + KEM_ID(2) + PublicKeyLen(2) + PublicKey(N) + CipherSuiteLen(2) + CipherSuites(4)
	buf := make([]byte, 0, 1+2+2+len(publicKey)+2+4)
	buf = append(buf, keyID)

	kemID := make([]byte, 2)
	binary.BigEndian.PutUint16(kemID, uint16(KEMID))
	buf = append(buf, kemID...)

	pubKeyLen := make([]byte, 2)
	binary.BigEndian.PutUint16(pubKeyLen, uint16(len(publicKey)))
	buf = append(buf, pubKeyLen...)
	buf = append(buf, publicKey...)

	cipherSuiteLen := make([]byte, 2)
	binary.BigEndian.PutUint16(cipherSuiteLen, uint16(len(cipherSuite)))
	buf = append(buf, cipherSuiteLen...)
	buf = append(buf, cipherSuite...)

	return buf
}

// DecodeKeyConfig 解码 OHTTP KeyConfig
func DecodeKeyConfig(data []byte) (keyID uint8, publicKey []byte, err error) {
	if len(data) < 7 {
		return 0, nil, fmt.Errorf("KeyConfig 数据太短")
	}

	keyID = data[0]
	// kemID := binary.BigEndian.Uint16(data[1:3])
	pubKeyLen := binary.BigEndian.Uint16(data[3:5])

	if len(data) < 5+int(pubKeyLen) {
		return 0, nil, fmt.Errorf("公钥数据不完整")
	}

	publicKey = data[5 : 5+pubKeyLen]
	return keyID, publicKey, nil
}
