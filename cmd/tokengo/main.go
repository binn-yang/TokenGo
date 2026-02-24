package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/binn/tokengo/internal/client"
	"github.com/binn/tokengo/internal/config"
	"github.com/binn/tokengo/internal/crypto"
	"github.com/binn/tokengo/internal/dht"
	"github.com/binn/tokengo/internal/exit"
	"github.com/binn/tokengo/internal/identity"
	"github.com/binn/tokengo/internal/relay"
	"github.com/spf13/cobra"
)

var (
	version = "0.1.0"
)

func main() {
	rootCmd := &cobra.Command{
		Use:     "tokengo",
		Short:   "TokenGo - 去中心化 AI API 网关",
		Long:    `TokenGo 是一个去中心化的 AI API 网关，使用 OHTTP + QUIC 实现端到端加密和隐私保护。`,
		Version: version,
	}

	// 添加子命令
	rootCmd.AddCommand(clientCmd())
	rootCmd.AddCommand(relayCmd())
	rootCmd.AddCommand(exitCmd())
	rootCmd.AddCommand(serveCmd())
	rootCmd.AddCommand(bootstrapCmd())
	rootCmd.AddCommand(keygenCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// clientCmd 客户端命令
func clientCmd() *cobra.Command {
	var configPath string
	var listen string
	var insecure bool

	cmd := &cobra.Command{
		Use:   "client",
		Short: "启动客户端 (本地 OpenAI 兼容 API 代理)",
		Long: `启动客户端，提供本地 HTTP API 端点，通过 OHTTP+QUIC 转发请求到 AI 后端。

默认使用公共 IPFS DHT 网络自动发现 Relay 和 Exit 节点，无需任何配置。

示例:
  # 零配置启动 (推荐)
  tokengo client

  # 使用配置文件
  tokengo client --config configs/client.yaml

  # 指定监听地址
  tokengo client --listen :9000`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var cfg *config.ClientConfig

			if cmd.Flags().Changed("config") {
				// 配置文件模式
				var err error
				cfg, err = config.LoadClientConfig(configPath)
				if err != nil {
					return fmt.Errorf("加载配置失败: %w", err)
				}
				if cmd.Flags().Changed("listen") {
					cfg.Listen = listen
				}
				if cmd.Flags().Changed("insecure") {
					cfg.InsecureSkipVerify = insecure
				}
			} else {
				// 默认 DHT 发现模式：使用公共 IPFS Bootstrap
				log.Printf("DHT 发现模式: 使用公共 IPFS Bootstrap 节点")
				cfg = &config.ClientConfig{
					Listen:             listen,
					InsecureSkipVerify: insecure,
					DHT: config.DHTConfig{
						Enabled:          true,
						UseIPFSBootstrap: true,
						ListenAddrs:      []string{"/ip4/0.0.0.0/tcp/0"},
					},
				}
			}

			proxy, err := client.NewLocalProxy(cfg)
			if err != nil {
				return fmt.Errorf("创建代理失败: %w", err)
			}

			return proxy.Start()
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "", "配置文件路径")
	cmd.Flags().StringVarP(&listen, "listen", "l", "127.0.0.1:8080", "监听地址")
	cmd.Flags().BoolVar(&insecure, "insecure", false, "跳过 TLS 证书验证")

	return cmd
}

// relayCmd 中继节点命令
func relayCmd() *cobra.Command {
	var configPath string
	var listen, certFile, keyFile string
	var insecure bool

	cmd := &cobra.Command{
		Use:   "relay",
		Short: "启动中继节点 (QUIC 服务器)",
		Long: `启动中继节点，接收客户端的 QUIC 连接，转发加密流量到 Exit 节点。

Relay 采用盲转发模式：Exit 地址由 Client 在请求中指定，Relay 只负责转发，无需配置 Exit 地址。

示例:
  # 启动 Relay (使用默认证书)
  tokengo relay --listen :4433

  # 指定 TLS 证书
  tokengo relay --cert certs/cert.pem --key certs/key.pem`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var cfg *config.RelayConfig
			var err error

			// 优先使用命令行参数
			if cmd.Flags().Changed("listen") || cmd.Flags().Changed("cert") || cmd.Flags().Changed("insecure") {
				cfg = &config.RelayConfig{
					Listen: listen,
					TLS: config.TLSConfig{
						CertFile: certFile,
						KeyFile:  keyFile,
					},
					InsecureSkipVerify: insecure,
				}
				// 如果没有指定证书，使用默认路径并自动生成
				if certFile == "" {
					cfg.TLS.CertFile = "certs/cert.pem"
					cfg.TLS.KeyFile = "certs/key.pem"
					if err := ensureCerts(cfg.TLS.CertFile, cfg.TLS.KeyFile); err != nil {
						return err
					}
				}
			} else {
				cfg, err = config.LoadRelayConfig(configPath)
				if err != nil {
					return fmt.Errorf("加载配置失败: %w", err)
				}
			}

			r, err := relay.New(cfg)
			if err != nil {
				return fmt.Errorf("创建中继节点失败: %w", err)
			}

			return r.Start()
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "configs/relay.yaml", "配置文件路径")
	cmd.Flags().StringVarP(&listen, "listen", "l", ":4433", "监听地址")
	cmd.Flags().StringVar(&certFile, "cert", "", "TLS 证书文件")
	cmd.Flags().StringVar(&keyFile, "key", "", "TLS 私钥文件")
	cmd.Flags().BoolVar(&insecure, "insecure", false, "跳过 TLS 证书验证")

	return cmd
}

// exitCmd 出口节点命令
func exitCmd() *cobra.Command {
	var configPath string
	var backend, apiKey, privateKeyFile string
	var headers []string
	var insecure bool

	cmd := &cobra.Command{
		Use:   "exit",
		Short: "启动出口节点 (OHTTP 网关)",
		Long: `启动出口节点，通过 DHT 发现 Relay 并建立反向隧道，解密 OHTTP 请求并转发到 AI 后端。

Exit 节点主动连接 Relay（无需公网 IP），通过 QUIC 反向隧道接收请求。
必须启用 DHT 配置以发现 Relay 节点。

示例:
  # 使用配置文件 (推荐)
  tokengo exit --config configs/exit-dht.yaml

  # 指定 AI 后端
  tokengo exit --config configs/exit-dht.yaml --backend https://api.openai.com --api-key sk-xxx`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var cfg *config.ExitConfig
			var err error

			// 优先使用命令行参数
			if backend != "" {
				headerMap := parseHeaders(headers)
				cfg = &config.ExitConfig{
					OHTTPPrivateKeyFile: privateKeyFile,
					AIBackend:           config.AIBackend{URL: backend, APIKey: apiKey, Headers: headerMap},
					InsecureSkipVerify:  insecure,
					DHT: config.DHTConfig{
						Enabled:          true,
						UseIPFSBootstrap: true,
						ListenAddrs:      []string{"/ip4/0.0.0.0/tcp/0"},
					},
				}
				// 如果没有指定密钥，自动生成
				if privateKeyFile == "" {
					cfg.OHTTPPrivateKeyFile = "keys/ohttp_private.key"
					pubKey, err := ensureOHTTPKey(cfg.OHTTPPrivateKeyFile)
					if err != nil {
						return err
					}
					log.Printf("Exit 公钥 (客户端配置用): %s", pubKey)
				}
			} else {
				cfg, err = config.LoadExitConfig(configPath)
				if err != nil {
					return fmt.Errorf("加载配置失败: %w", err)
				}
			}

			// 命令行覆盖
			if cmd.Flags().Changed("insecure") {
				cfg.InsecureSkipVerify = insecure
			}

			// DHT 必须启用 (exit.New 会再次检查，但这里提前给出友好提示)
			if !cfg.DHT.Enabled {
				return fmt.Errorf("必须启用 DHT 配置以发现 Relay 节点，请在配置文件中设置 dht.enabled: true")
			}

			e, err := exit.New(cfg)
			if err != nil {
				return fmt.Errorf("创建出口节点失败: %w", err)
			}

			return e.Start()
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "configs/exit.yaml", "配置文件路径")
	cmd.Flags().StringVarP(&backend, "backend", "b", "", "AI 后端地址 (如: http://localhost:11434)")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "AI 后端 API Key")
	cmd.Flags().StringArrayVar(&headers, "header", nil, "自定义后端请求头 (格式: Key:Value，可多次指定)")
	cmd.Flags().StringVar(&privateKeyFile, "private-key", "", "OHTTP 私钥文件")
	cmd.Flags().BoolVar(&insecure, "insecure", false, "跳过 TLS 证书验证")

	return cmd
}

// serveCmd 一体化服务命令
func serveCmd() *cobra.Command {
	var listen, backend, apiKey string
	var headers []string
	var insecure bool

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "一键启动完整服务 (Relay + Exit)",
		Long: `在单个进程中启动 Relay 和 Exit 节点，简化部署。

示例:
  # 启动服务，连接本地 Ollama
  tokengo serve --backend http://localhost:11434

  # 启动服务，连接 OpenAI API
  tokengo serve --backend https://api.openai.com --api-key sk-xxx

  # 启动服务，连接 Claude API
  tokengo serve --backend https://api.anthropic.com \
    --header "x-api-key:sk-ant-xxx" --header "anthropic-version:2023-06-01"

  # 指定监听端口
  tokengo serve --listen :8080 --backend http://localhost:11434`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if backend == "" {
				return fmt.Errorf("必须指定 --backend 参数")
			}

			// 确保密钥和证书存在
			privateKeyFile := "keys/ohttp_private.key"
			pubKey, err := ensureOHTTPKey(privateKeyFile)
			if err != nil {
				return err
			}

			certFile := "certs/cert.pem"
			keyFile := "certs/key.pem"
			if err := ensureCerts(certFile, keyFile); err != nil {
				return err
			}

			// 配置
			relayListen := ":4433"

			headerMap := parseHeaders(headers)
			exitCfg := &config.ExitConfig{
				OHTTPPrivateKeyFile: privateKeyFile,
				AIBackend:           config.AIBackend{URL: backend, APIKey: apiKey, Headers: headerMap},
				InsecureSkipVerify:  insecure,
			}

			relayCfg := &config.RelayConfig{
				Listen:             relayListen,
				TLS:                config.TLSConfig{CertFile: certFile, KeyFile: keyFile},
				InsecureSkipVerify: insecure,
			}

			// 解析 Exit 公钥
			keyID, publicKey, err := crypto.LoadPublicKeyConfig(pubKey)
			if err != nil {
				return fmt.Errorf("解析 Exit 公钥失败: %w", err)
			}

			// 启动 Relay (必须先启动，Exit 要连接它)
			r, err := relay.New(relayCfg)
			if err != nil {
				return fmt.Errorf("创建 Relay 节点失败: %w", err)
			}
			go func() {
				if err := r.Start(); err != nil {
					log.Fatalf("Relay 节点错误: %v", err)
				}
			}()

			// 等待 Relay 就绪（使用 Ready channel 替代 time.Sleep）
			<-r.Ready()
			log.Printf("Relay 已就绪")

			// 启动 Exit (通过反向隧道连接本地 Relay)
			e, err := exit.NewStatic(exitCfg, "127.0.0.1"+relayListen)
			if err != nil {
				return fmt.Errorf("创建 Exit 节点失败: %w", err)
			}
			go func() {
				if err := e.Start(); err != nil {
					log.Fatalf("Exit 节点错误: %v", err)
				}
			}()

			// 等待 Exit 隧道建立（Exit 尚未实现 Ready，使用较短的超时）
			time.Sleep(100 * time.Millisecond)

			// 启动 Client (静态模式)
			proxy, err := client.NewStaticProxy(
				listen,
				"127.0.0.1"+relayListen,
				keyID,
				publicKey,
				insecure,
			)
			if err != nil {
				return fmt.Errorf("创建 Client 失败: %w", err)
			}
			go func() {
				if err := proxy.Start(); err != nil {
					log.Fatalf("Client 错误: %v", err)
				}
			}()

			log.Printf("TokenGo 服务已启动!")
			log.Printf("  本地 API: http://127.0.0.1%s", listen)
			log.Printf("  AI 后端:  %s", backend)
			log.Printf("")
			log.Printf("测试命令:")
			log.Printf(`  curl http://127.0.0.1%s/v1/chat/completions \`, listen)
			log.Printf(`    -H "Content-Type: application/json" \`)
			log.Printf(`    -d '{"model":"llama3.2:1b","messages":[{"role":"user","content":"hello"}]}'`)

			// 等待信号
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			<-sigCh
			log.Println("收到停止信号，正在关闭...")

			return nil
		},
	}

	cmd.Flags().StringVarP(&listen, "listen", "l", ":8080", "本地 API 监听地址")
	cmd.Flags().StringVarP(&backend, "backend", "b", "", "AI 后端地址 (必需)")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "AI 后端 API Key")
	cmd.Flags().StringArrayVar(&headers, "header", nil, "自定义后端请求头 (格式: Key:Value，可多次指定)")
	cmd.Flags().BoolVar(&insecure, "insecure", false, "跳过 TLS 证书验证 (使用自签名证书时需要)")

	return cmd
}

// bootstrapCmd Bootstrap 节点命令
func bootstrapCmd() *cobra.Command {
	var configPath string
	var printPeerID bool

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "启动 Bootstrap 节点 (DHT 引导节点)",
		Long:  `启动 Bootstrap 节点，为其他节点提供 DHT 网络入口点。`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadBootstrapConfig(configPath)
			if err != nil {
				return fmt.Errorf("加载配置失败: %w", err)
			}

			bootstrap, err := dht.NewBootstrapNode(cfg)
			if err != nil {
				return fmt.Errorf("创建 Bootstrap 节点失败: %w", err)
			}

			if printPeerID {
				fmt.Println(bootstrap.PeerID())
				return nil
			}

			return bootstrap.Run()
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "configs/bootstrap.yaml", "配置文件路径")
	cmd.Flags().BoolVar(&printPeerID, "print-peer-id", false, "仅打印 PeerID 后退出")

	return cmd
}

// keygenCmd 密钥生成命令
func keygenCmd() *cobra.Command {
	var outputDir string
	var keyType string

	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "生成密钥",
		Long:  `生成密钥对。支持 OHTTP 密钥和节点身份密钥。`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := os.MkdirAll(outputDir, 0755); err != nil {
				return fmt.Errorf("创建输出目录失败: %w", err)
			}

			switch keyType {
			case "ohttp":
				return generateOHTTPKey(outputDir)
			case "identity":
				return generateIdentityKey(outputDir)
			default:
				return fmt.Errorf("未知的密钥类型: %s (支持: ohttp, identity)", keyType)
			}
		},
	}

	cmd.Flags().StringVarP(&outputDir, "output", "o", "./keys", "密钥输出目录")
	cmd.Flags().StringVarP(&keyType, "type", "t", "ohttp", "密钥类型 (ohttp 或 identity)")

	return cmd
}

// generateOHTTPKey 生成 OHTTP 密钥
func generateOHTTPKey(outputDir string) error {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		return fmt.Errorf("生成密钥对失败: %w", err)
	}

	privPath := filepath.Join(outputDir, "ohttp_private.key")
	pubPath := filepath.Join(outputDir, "ohttp_private.key.pub")

	if err := crypto.SaveKeyPair(kp, pubPath, privPath); err != nil {
		return fmt.Errorf("保存密钥失败: %w", err)
	}

	log.Printf("OHTTP 密钥对已生成:")
	log.Printf("  私钥: %s", privPath)
	log.Printf("  公钥: %s", pubPath)
	log.Printf("  KeyID: %d", kp.KeyID)

	pubConfig := crypto.EncodeKeyConfig(kp.KeyID, kp.PublicKey)
	log.Printf("\n客户端配置 (exit_public_key):")
	log.Printf("  %s", base64.StdEncoding.EncodeToString(pubConfig))

	return nil
}

// generateIdentityKey 生成节点身份密钥
func generateIdentityKey(outputDir string) error {
	id, err := identity.Generate()
	if err != nil {
		return fmt.Errorf("生成身份失败: %w", err)
	}

	keyPath := filepath.Join(outputDir, "identity.key")
	if err := id.Save(keyPath); err != nil {
		return fmt.Errorf("保存密钥失败: %w", err)
	}

	log.Printf("节点身份密钥已生成:")
	log.Printf("  密钥文件: %s", keyPath)
	log.Printf("  PeerID: %s", id.PeerID)

	return nil
}

// parseHeaders 解析 Key:Value 格式的 headers 列表为 map
func parseHeaders(headers []string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	result := make(map[string]string, len(headers))
	for _, h := range headers {
		idx := strings.IndexByte(h, ':')
		if idx > 0 {
			result[strings.TrimSpace(h[:idx])] = strings.TrimSpace(h[idx+1:])
		}
	}
	return result
}

// ensureOHTTPKey 确保 OHTTP 密钥存在，返回公钥
func ensureOHTTPKey(keyFile string) (string, error) {
	dir := filepath.Dir(keyFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("创建目录失败: %w", err)
	}

	// 检查私钥是否存在
	if _, err := os.Stat(keyFile); os.IsNotExist(err) {
		log.Printf("OHTTP 密钥不存在，自动生成...")
		kp, err := crypto.GenerateKeyPair()
		if err != nil {
			return "", fmt.Errorf("生成密钥对失败: %w", err)
		}

		pubPath := keyFile + ".pub"
		if err := crypto.SaveKeyPair(kp, pubPath, keyFile); err != nil {
			return "", fmt.Errorf("保存密钥失败: %w", err)
		}
		log.Printf("密钥已生成: %s", keyFile)
	}

	// 读取公钥
	pubPath := keyFile + ".pub"
	pubData, err := os.ReadFile(pubPath)
	if err != nil {
		return "", fmt.Errorf("读取公钥失败: %w", err)
	}

	return string(pubData), nil
}

// ensureCerts 确保 TLS 证书存在，不存在则自动生成自签名证书
func ensureCerts(certFile, keyFile string) error {
	// 检查证书是否已存在
	if _, err := os.Stat(certFile); err == nil {
		if _, err := os.Stat(keyFile); err == nil {
			return nil // 证书已存在
		}
	}

	log.Printf("TLS 证书不存在，自动生成自签名证书...")

	// 创建目录
	certDir := filepath.Dir(certFile)
	if err := os.MkdirAll(certDir, 0755); err != nil {
		return fmt.Errorf("创建证书目录失败: %w", err)
	}

	// 生成私钥
	privateKey, err := generateRSAKey()
	if err != nil {
		return fmt.Errorf("生成私钥失败: %w", err)
	}

	// 创建自签名证书
	cert, err := generateSelfSignedCert(privateKey)
	if err != nil {
		return fmt.Errorf("生成证书失败: %w", err)
	}

	// 保存私钥
	keyOut, err := os.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("创建私钥文件失败: %w", err)
	}
	defer keyOut.Close()

	if err := pemEncodeKey(keyOut, privateKey); err != nil {
		return fmt.Errorf("编码私钥失败: %w", err)
	}

	// 保存证书
	certOut, err := os.OpenFile(certFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("创建证书文件失败: %w", err)
	}
	defer certOut.Close()

	if err := pemEncodeCert(certOut, cert); err != nil {
		return fmt.Errorf("编码证书失败: %w", err)
	}

	log.Printf("自签名 TLS 证书已生成:")
	log.Printf("  证书: %s", certFile)
	log.Printf("  私钥: %s", keyFile)

	return nil
}

// generateRSAKey 生成 RSA 私钥
func generateRSAKey() (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, 4096)
}

// generateSelfSignedCert 生成自签名证书
func generateSelfSignedCert(privateKey *rsa.PrivateKey) ([]byte, error) {
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"TokenGo"},
			CommonName:   "localhost",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour), // 1年有效期
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost", "*.localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}

	return x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
}

// pemEncodeKey 将私钥编码为 PEM 格式
func pemEncodeKey(out *os.File, key *rsa.PrivateKey) error {
	return pem.Encode(out, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}

// pemEncodeCert 将证书编码为 PEM 格式
func pemEncodeCert(out *os.File, cert []byte) error {
	return pem.Encode(out, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert,
	})
}
