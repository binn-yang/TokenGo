package identity

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// Identity 节点身份
type Identity struct {
	PrivKey crypto.PrivKey
	PubKey  crypto.PubKey
	PeerID  peer.ID
}

// Generate 生成新的节点身份
func Generate() (*Identity, error) {
	privKey, pubKey, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成密钥失败: %w", err)
	}

	peerID, err := peer.IDFromPublicKey(pubKey)
	if err != nil {
		return nil, fmt.Errorf("生成 PeerID 失败: %w", err)
	}

	return &Identity{
		PrivKey: privKey,
		PubKey:  pubKey,
		PeerID:  peerID,
	}, nil
}

// LoadOrGenerate 加载或生成节点身份
func LoadOrGenerate(keyPath string) (*Identity, error) {
	// 尝试加载现有密钥
	if keyPath != "" {
		if _, err := os.Stat(keyPath); err == nil {
			return Load(keyPath)
		}
	}

	// 生成新密钥
	identity, err := Generate()
	if err != nil {
		return nil, err
	}

	// 如果指定了路径，保存密钥
	if keyPath != "" {
		if err := identity.Save(keyPath); err != nil {
			return nil, fmt.Errorf("保存密钥失败: %w", err)
		}
	}

	return identity, nil
}

// Load 从文件加载节点身份
func Load(keyPath string) (*Identity, error) {
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("读取密钥文件失败: %w", err)
	}

	// Base64 解码
	keyBytes, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil {
		return nil, fmt.Errorf("解码密钥失败: %w", err)
	}

	privKey, err := crypto.UnmarshalPrivateKey(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("解析私钥失败: %w", err)
	}

	pubKey := privKey.GetPublic()
	peerID, err := peer.IDFromPublicKey(pubKey)
	if err != nil {
		return nil, fmt.Errorf("生成 PeerID 失败: %w", err)
	}

	return &Identity{
		PrivKey: privKey,
		PubKey:  pubKey,
		PeerID:  peerID,
	}, nil
}

// Save 保存节点身份到文件
func (i *Identity) Save(keyPath string) error {
	keyBytes, err := crypto.MarshalPrivateKey(i.PrivKey)
	if err != nil {
		return fmt.Errorf("序列化私钥失败: %w", err)
	}

	// Base64 编码
	encoded := base64.StdEncoding.EncodeToString(keyBytes)

	if err := os.WriteFile(keyPath, []byte(encoded), 0600); err != nil {
		return fmt.Errorf("写入密钥文件失败: %w", err)
	}

	return nil
}

// String 返回 PeerID 字符串
func (i *Identity) String() string {
	return i.PeerID.String()
}
