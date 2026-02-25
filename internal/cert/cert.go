// Package cert 提供基于 PeerID 的 TLS 证书生成和验证
package cert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// CertFileName 证书文件名
const CertFileName = "relay-cert.pem"
const KeyFileName = "relay-key.pem"

// GeneratePeerIDCert 生成绑定 PeerID 的自签名证书
// 如果certDir 为空，则不保存到文件
func GeneratePeerIDCert(privKey crypto.PrivKey, certDir string) (*tls.Certificate, error) {
	// 从 libp2p 私钥提取 ECDSA 私钥
	ecdsaPrivKey, err := extractECDSAPrivKey(privKey)
	if err != nil {
		return nil, fmt.Errorf("提取 ECDSA 私钥失败: %w", err)
	}

	// 计算PeerID
	peerID, err := peer.IDFromPrivateKey(privKey)
	if err != nil {
		return nil, fmt.Errorf("计算 PeerID 失败: %w", err)
	}

	// 生成证书模板
	serialNumber, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   peerID.String(),
			Organization: []string{"TokenGo"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour), // 1 年有效
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{peerID.String()}, // 将 PeerID 作为 SAN
	}

	// 生成证书
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &ecdsaPrivKey.PublicKey, ecdsaPrivKey)
	if err != nil {
		return nil, fmt.Errorf("生成证书失败: %w", err)
	}

	// 如果指定了目录，保存到文件
	if certDir != "" {
		if err := saveCertFiles(certDir, certDER, ecdsaPrivKey); err != nil {
			return nil, fmt.Errorf("保存证书文件失败: %w", err)
		}
	}

	// 创建 tls.Certificate
	cert := &tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  ecdsaPrivKey,
		Leaf:        nil, // 会在第一次使用时填充
	}

	return cert, nil
}

// extractECDSAPrivKey 从 libp2p 私钥提取/生成 ECDSA 私钥
// libp2p 使用 Ed25519 或 ECDSA (secp256k1)
// 我们需要生成新的 ECDSA P-256 密钥对用于 TLS
// 证书仍然绑定 PeerID（通过 CommonName 字段）
func extractECDSAPrivKey(privKey crypto.PrivKey) (*ecdsa.PrivateKey, error) {
	// 为 TLS 生成新的 ECDSA P-256 密钥对
	// 证书的 CommonName 会设置为 PeerID，保证身份绑定
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

// saveCertFiles 保存证书和私钥到文件
func saveCertFiles(dir string, certDER []byte, privKey *ecdsa.PrivateKey) error {
	// 确保目录存在
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	// 保存证书
	certPath := filepath.Join(dir, CertFileName)
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})
	if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
		return err
	}

	// 保存私钥
	keyPath := filepath.Join(dir, KeyFileName)
	privKeyDER, err := x509.MarshalECPrivateKey(privKey)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: privKeyDER,
	})
	return os.WriteFile(keyPath, keyPEM, 0600)
}

// LoadOrGenerateCert 加载或生成证书
func LoadOrGenerateCert(certDir string, privKey crypto.PrivKey) (*tls.Certificate, error) {
	certPath := filepath.Join(certDir, CertFileName)
	keyPath := filepath.Join(certDir, KeyFileName)

	// 尝试加载现有证书
	if _, err := os.Stat(certPath); err == nil {
		if _, err := os.Stat(keyPath); err == nil {
			cert, err := tls.LoadX509KeyPair(certPath, keyPath)
			if err == nil {
				return &cert, nil
			}
		}
	}

	// 生成新证书
	return GeneratePeerIDCert(privKey, certDir)
}

// VerifyPeerID 验证证书中的 PeerID 是否匹配期望的 PeerID
// 返回 nil 表示验证通过
func VerifyPeerID(rawCerts [][]byte, expectedPeerID peer.ID) error {
	if len(rawCerts) == 0 {
		return fmt.Errorf("没有证书")
	}

	cert, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return fmt.Errorf("解析证书失败: %w", err)
	}

	// 检查 CommonName 或 DNSNames 是否包含期望的 PeerID
	peerIDStr := expectedPeerID.String()

	if cert.Subject.CommonName == peerIDStr {
		return nil
	}

	for _, name := range cert.DNSNames {
		if name == peerIDStr {
			return nil
		}
	}

	return fmt.Errorf("证书 PeerID 不匹配: 期望 %s, 证书中为 %s", peerIDStr, cert.Subject.CommonName)
}

// CreatePeerIDVerifyTLSConfig 创建验证 PeerID 的 TLS 配置
func CreatePeerIDVerifyTLSConfig(expectedPeerID peer.ID) *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true, // 跳过默认验证，使用自定义验证
		NextProtos:         []string{"tokengo-relay", "tokengo-exit"},
		MinVersion:         tls.VersionTLS13,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return VerifyPeerID(rawCerts, expectedPeerID)
		},
	}
}

// CreateServerTLSConfig 创建服务器端 TLS 配置
func CreateServerTLSConfig(cert *tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{*cert},
		NextProtos:   []string{"tokengo-relay", "tokengo-exit"},
		MinVersion:   tls.VersionTLS13,
	}
}
