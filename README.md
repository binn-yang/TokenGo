# TokenGo

去中心化 AI API 网关，使用 OHTTP (RFC 9458) + QUIC (RFC 9000) 实现端到端加密和隐私保护。

## 架构

```
                          QUIC 反向隧道 (Exit 主动连接)
                         ┌──────────────────────────────┐
                         ▼                              │
Client (CLI) ──QUIC──> Relay (中继) <──QUIC── Exit (出口) ──HTTP──> AI服务
     │                    │                      │
     │   (看不到明文)       │      (看不到来源)     │
     └────────────────────┴──────────────────────┘
                      隐私保护链路
```

- **Client**: 本地代理，提供 OpenAI 兼容 API，监听 `localhost:8080`
- **Relay**: 中继节点，接受 Client 和 Exit 连接，盲转发加密流量，监听 `:4433`
- **Exit**: 出口节点，通过反向隧道主动连接 Relay（无需公网 IP），解密请求并调用 AI 后端

## 快速开始

### 一键启动（推荐）

```bash
# 编译
make build

# 一键启动，连接本地 Ollama（自动生成密钥和证书）
./build/tokengo serve --backend http://localhost:11434

# 或连接 OpenAI API
./build/tokengo serve --backend https://api.openai.com --api-key sk-xxx
```

服务启动后，访问 `http://localhost:8080` 即可使用 OpenAI 兼容 API。

### 分布式部署（DHT 发现模式）

Client 零配置，自动通过公共 IPFS DHT 发现节点：

```bash
# 服务器 A: Exit 节点 (通过 DHT 发现 Relay，反向隧道连接)
./build/tokengo exit --config configs/exit-dht.yaml --backend http://localhost:11434

# 服务器 B: Relay 节点 (注册到 DHT，接受 Client 和 Exit 连接)
./build/tokengo relay --config configs/relay-dht.yaml

# 本地: Client（零配置！自动发现 Relay，从 Relay 查询 Exit 公钥）
./build/tokengo client
```

### Docker 一键测试

```bash
# 运行完整的 Docker 集成测试（包含 Ollama）
make docker-test
```

这会自动：
1. 生成 OHTTP 密钥和 TLS 证书
2. 构建 Docker 镜像
3. 启动 Ollama + TokenGo 全链路
4. 下载 llama3.2:1b 模型
5. 运行 API 测试

测试成功后，可以直接使用：

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"llama3.2:1b","messages":[{"role":"user","content":"hello"}]}'
```

### Docker 管理命令

```bash
make docker-up      # 启动服务
make docker-down    # 停止服务
make docker-logs    # 查看日志
make docker-clean   # 清理资源
```

### 本地开发

#### 1. 安装依赖

```bash
# 需要 Go 1.21+
make deps
```

#### 2. 编译

```bash
make build
```

#### 3. 运行（自动生成密钥和证书）

```bash
# 方式一：一键启动（推荐）
./build/tokengo serve --backend http://localhost:11434

# 方式二：分布式启动（DHT 模式）
# 终端 1: Exit 节点
./build/tokengo exit --config configs/exit-dht.yaml --backend http://localhost:11434

# 终端 2: Relay 节点
./build/tokengo relay --config configs/relay-dht.yaml

# 终端 3: Client（零配置，自动发现）
./build/tokengo client
```

#### 4. 配置文件（可选）

如需使用配置文件，编辑 `configs/` 目录：

- `configs/client.yaml`: Client 配置（DHT 发现 + Bootstrap API）
- `configs/relay-dht.yaml`: Relay 配置（QUIC 监听 + DHT 注册）
- `configs/exit-dht.yaml`: Exit 配置（AI 后端 + DHT 发现 Relay）

```bash
make run-client  # 使用 configs/client.yaml
make run-relay   # 使用 configs/relay-dht.yaml
make run-exit    # 使用 configs/exit-dht.yaml
```

#### 5. 手动生成密钥（可选）

```bash
# OHTTP 密钥对（Exit 加解密用）
tokengo keygen --type ohttp --output ./keys

# 节点身份密钥（DHT PeerID 用）
tokengo keygen --type identity --output ./keys/exit_identity.key

# TLS 证书
make certs
```

## 性能测试

在 Docker 环境下的延迟测试结果：

| 方式 | 纯网络延迟 | 含推理延迟 |
|------|-----------|-----------|
| 直接 Ollama | ~10ms | 1-4s |
| TokenGo 管道 | ~10ms | 1-4s |

**结论**: OHTTP 加密 + QUIC 传输的额外开销 **< 1ms**，几乎可以忽略。

## CLI 命令

```bash
# 一键启动 (推荐，适合本地开发)
tokengo serve --backend http://localhost:11434
tokengo serve --backend https://api.openai.com --api-key sk-xxx

# 分布式部署 (DHT 发现模式)
tokengo exit --config configs/exit-dht.yaml --backend http://localhost:11434
tokengo relay --config configs/relay-dht.yaml
tokengo client  # 零配置！自动使用公共 IPFS DHT 发现节点

# 生成密钥
tokengo keygen --type ohttp --output ./keys        # OHTTP 密钥对
tokengo keygen --type identity --output ./keys/id   # 节点身份密钥

# DHT Bootstrap 节点
tokengo bootstrap --config configs/bootstrap.yaml
```

**零配置**: `tokengo client` 默认使用公共 IPFS DHT 网络发现节点，无需任何配置。

**反向隧道**: Exit 主动连接 Relay，无需公网 IP。

**隐私优势**: Relay 采用盲转发模式，根据请求中的 pubKeyHash 转发到对应 Exit。

## 项目结构

```
TokenGo/
├── cmd/tokengo/       # CLI 入口
├── internal/
│   ├── client/        # 客户端代理
│   ├── relay/         # 中继节点 (QUIC 服务 + Exit 注册表)
│   ├── exit/          # 出口节点 (反向隧道 + OHTTP 解密)
│   ├── crypto/        # OHTTP/HPKE 加密
│   ├── protocol/      # 二进制消息协议
│   ├── dht/           # DHT 服务发现 (libp2p Kademlia)
│   ├── config/        # 配置解析
│   └── identity/      # 节点身份
├── pkg/openai/        # OpenAI API 兼容层
├── configs/           # 配置文件
│   └── docker/        # Docker 专用配置
├── scripts/           # 脚本
├── Dockerfile
└── docker-compose.yml
```

## 技术栈

- **Go 1.21+**
- **OHTTP (RFC 9458)** - 端到端加密
- **QUIC (RFC 9000)** - 传输层 (含反向隧道)
- **HPKE** - X25519, HKDF-SHA256, AES-128-GCM
- **libp2p Kademlia DHT** - 去中心化服务发现

## 许可证

MIT
