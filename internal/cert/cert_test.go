package cert

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func generateTestIdentity(t *testing.T) (libp2pcrypto.PrivKey, peer.ID) {
	t.Helper()
	privKey, _, err := libp2pcrypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatalf("GenerateEd25519Key failed: %v", err)
	}
	peerID, err := peer.IDFromPrivateKey(privKey)
	if err != nil {
		t.Fatalf("IDFromPrivateKey failed: %v", err)
	}
	return privKey, peerID
}

func TestGeneratePeerIDCert_InMemory(t *testing.T) {
	privKey, peerID := generateTestIdentity(t)

	cert, err := GeneratePeerIDCert(privKey, "")
	if err != nil {
		t.Fatalf("GeneratePeerIDCert failed: %v", err)
	}

	if cert == nil {
		t.Fatal("cert should not be nil")
	}
	if len(cert.Certificate) == 0 {
		t.Fatal("cert.Certificate should not be empty")
	}

	// 解析证书验证 PeerID
	x509Cert, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("ParseCertificate failed: %v", err)
	}

	if x509Cert.Subject.CommonName != peerID.String() {
		t.Errorf("CommonName = %q, want %q", x509Cert.Subject.CommonName, peerID.String())
	}

	foundInDNS := false
	for _, name := range x509Cert.DNSNames {
		if name == peerID.String() {
			foundInDNS = true
			break
		}
	}
	if !foundInDNS {
		t.Error("PeerID should be in DNSNames")
	}
}

func TestGeneratePeerIDCert_SaveToDir(t *testing.T) {
	privKey, _ := generateTestIdentity(t)

	tmpDir := t.TempDir()
	cert, err := GeneratePeerIDCert(privKey, tmpDir)
	if err != nil {
		t.Fatalf("GeneratePeerIDCert failed: %v", err)
	}

	if cert == nil {
		t.Fatal("cert should not be nil")
	}

	// 验证文件存在
	certPath := filepath.Join(tmpDir, CertFileName)
	keyPath := filepath.Join(tmpDir, KeyFileName)

	if _, err := os.Stat(certPath); os.IsNotExist(err) {
		t.Errorf("cert file not found: %s", certPath)
	}
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		t.Errorf("key file not found: %s", keyPath)
	}

	// 验证可以从文件加载
	loaded, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("LoadX509KeyPair failed: %v", err)
	}
	if len(loaded.Certificate) == 0 {
		t.Error("loaded cert should not be empty")
	}
}

func TestVerifyPeerID_Match(t *testing.T) {
	privKey, peerID := generateTestIdentity(t)

	cert, err := GeneratePeerIDCert(privKey, "")
	if err != nil {
		t.Fatalf("GeneratePeerIDCert failed: %v", err)
	}

	err = VerifyPeerID(cert.Certificate, peerID)
	if err != nil {
		t.Fatalf("VerifyPeerID should pass for matching PeerID: %v", err)
	}
}

func TestVerifyPeerID_Mismatch(t *testing.T) {
	privKey, _ := generateTestIdentity(t)
	_, wrongPeerID := generateTestIdentity(t) // 生成不同的 PeerID

	cert, err := GeneratePeerIDCert(privKey, "")
	if err != nil {
		t.Fatalf("GeneratePeerIDCert failed: %v", err)
	}

	err = VerifyPeerID(cert.Certificate, wrongPeerID)
	if err == nil {
		t.Fatal("VerifyPeerID should fail for mismatching PeerID")
	}
}

func TestVerifyPeerID_NoCerts(t *testing.T) {
	_, peerID := generateTestIdentity(t)

	err := VerifyPeerID(nil, peerID)
	if err == nil {
		t.Fatal("VerifyPeerID should fail with no certs")
	}
}

func TestCreatePeerIDVerifyTLSConfig(t *testing.T) {
	_, peerID := generateTestIdentity(t)

	cfg := CreatePeerIDVerifyTLSConfig(peerID)

	if !cfg.InsecureSkipVerify {
		t.Error("InsecureSkipVerify should be true for custom verification")
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %d, want TLS 1.3", cfg.MinVersion)
	}
	if len(cfg.NextProtos) != 1 || cfg.NextProtos[0] != "tokengo-relay" {
		t.Errorf("NextProtos = %v, want [tokengo-relay]", cfg.NextProtos)
	}
	if cfg.VerifyPeerCertificate == nil {
		t.Error("VerifyPeerCertificate should not be nil")
	}
}

func TestCreateExitTLSConfig(t *testing.T) {
	_, peerID := generateTestIdentity(t)

	cfg := CreateExitTLSConfig(peerID)

	if len(cfg.NextProtos) != 1 || cfg.NextProtos[0] != "tokengo-exit" {
		t.Errorf("NextProtos = %v, want [tokengo-exit]", cfg.NextProtos)
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %d, want TLS 1.3", cfg.MinVersion)
	}
	if cfg.VerifyPeerCertificate == nil {
		t.Error("VerifyPeerCertificate should not be nil")
	}
}

func TestCreateServerTLSConfig(t *testing.T) {
	privKey, _ := generateTestIdentity(t)

	cert, err := GeneratePeerIDCert(privKey, "")
	if err != nil {
		t.Fatalf("GeneratePeerIDCert failed: %v", err)
	}

	cfg := CreateServerTLSConfig(cert)

	if len(cfg.Certificates) != 1 {
		t.Fatalf("Certificates length = %d, want 1", len(cfg.Certificates))
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %d, want TLS 1.3", cfg.MinVersion)
	}

	// 应支持两种 ALPN
	hasRelay := false
	hasExit := false
	for _, proto := range cfg.NextProtos {
		if proto == "tokengo-relay" {
			hasRelay = true
		}
		if proto == "tokengo-exit" {
			hasExit = true
		}
	}
	if !hasRelay {
		t.Error("NextProtos should contain tokengo-relay")
	}
	if !hasExit {
		t.Error("NextProtos should contain tokengo-exit")
	}
}

func TestLoadOrGenerateCert_Generate(t *testing.T) {
	privKey, _ := generateTestIdentity(t)
	tmpDir := t.TempDir()

	cert, err := LoadOrGenerateCert(tmpDir, privKey)
	if err != nil {
		t.Fatalf("LoadOrGenerateCert failed: %v", err)
	}
	if cert == nil {
		t.Fatal("cert should not be nil")
	}

	// 第二次调用应加载已有证书
	cert2, err := LoadOrGenerateCert(tmpDir, privKey)
	if err != nil {
		t.Fatalf("LoadOrGenerateCert (reload) failed: %v", err)
	}
	if cert2 == nil {
		t.Fatal("reloaded cert should not be nil")
	}
}
