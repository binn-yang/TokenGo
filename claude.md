# TokenGo 项目文档

## 项目概述

TokenGo 是一个去中心化 AI API 网关，通过 OHTTP (Oblivious HTTP, RFC 9458) 协议实现端到端加密，保护用户隐私。

## 当前状态

- **版本**: 0.1.0
- **状态**: MVP 可用

## 架构设计

### 核心组件

```
┌─────────┐     QUIC      ┌─────────┐  QUIC 反向隧道  ┌─────────┐    HTTP     ┌──────────┐
│ Client  │──────────────>│  Relay  │<───────────────│  Exit   │───────────>│ AI 后端  │
│ :8080   │   加密请求     │  :4433  │   Exit 主动连接  │ (无监听) │   明文请求  │ :11434   │
└─────────┘               └─────────┘                └─────────┘            └──────────┘
     │                         │                          │
     │ 持有 Exit 公钥          │ 无法解密                  │ 持有私钥，可解密
     │ 从 Relay 查询公钥       │ 只知道来源 IP             │ 不知道来源 IP
```

**关键设计**: Exit 无需公网 IP，通过 QUIC 反向隧道主动连接 Relay。Client 从 Relay 查询 Exit 公钥，请求中指定目标 Exit 的 pubKeyHash，Relay 做盲转发。

### 隐私保护 (盲转发架构)

| 节点 | 知道的信息 | 不知道的信息 |
|------|-----------|-------------|
| Relay | Client IP、Exit 连接 | 请求内容（OHTTP 加密） |
| Exit | 请求内容 | Client IP |

### 端口分配

| 服务 | 端口 | 协议 | 说明 |
|------|------|------|------|
| Client (本地代理) | 8080 | HTTP | 监听本地 |
| Relay (中继) | 4433 | QUIC | 接受 Client 和 Exit 连接 |
| Exit (出口) | 无监听端口 | QUIC (反向) | 主动连接 Relay |
| AI 后端 | 11434 | HTTP | 如 Ollama |

### ALPN 协议区分

Relay 通过 TLS ALPN 区分连接类型：
- `tokengo-exit`: Exit 反向隧道连接
- `tokengo-relay` (默认): Client 连接

## 核心模块

### internal/crypto

OHTTP 加密实现：
- `ohttp.go` - OHTTP 请求/响应加解密
- 使用 HPKE (X25519 + HKDF-SHA256 + AES-128-GCM)
- KeyID 用于匹配客户端公钥和服务端私钥
- `EncodeKeyConfig` / `LoadPublicKeyConfig` - KeyConfig 编解码 (RFC 9458)
- `PubKeyHash` - 计算公钥哈希（用于标识 Exit）

### internal/client

本地 HTTP 代理：
- 监听本地端口，接收 OpenAI 兼容 API 请求
- `NewClient` - 静态模式，需提供 relayAddr、keyID、exitPublicKey
- `NewClientDynamic` - 动态发现模式，仅需 insecureSkipVerify
- 通过 DHT 发现 Relay，连接后从 Relay 查询 Exit 公钥
- 使用 Exit 公钥加密请求，通过 QUIC 发送到 Relay

### internal/relay

QUIC 中继节点 (盲转发模式)：
- 接收 Client/Exit QUIC 连接（通过 ALPN 区分）
- Exit 注册: 接收 Register 消息，提取 pubKeyHash 和 KeyConfig，存入 Registry
- Client 请求: 根据消息中的 Target (pubKeyHash) 查找已注册的 Exit 连接并转发
- 支持 QueryExitKeys: 返回所有已注册 Exit 的 KeyConfig 列表
- Registry 带心跳超时清理

### internal/exit

OHTTP 出口节点 (反向隧道)：
- 通过 DHT 发现 Relay 节点（或使用静态地址）
- 主动连接 Relay，使用 ALPN `tokengo-exit`
- 注册时发送 pubKeyHash + KeyConfig
- 维持心跳保活（15s 间隔）
- 接收 Relay 转发的加密请求，解密后转发到 AI 后端

### internal/dht

基于 libp2p Kademlia 的服务发现：
- `Provider` - 服务注册（Relay/Exit 注册自己到 DHT）
- `Discovery` - 服务发现（带缓存，2分钟刷新）
- 命名空间: `/tokengo/relay/v1`, `/tokengo/exit/v1`
- 使用 CID-based Provider Records

### internal/protocol

自定义二进制消息协议：
- 格式: `[Type(1)][TargetLen(2)][Target(N)][PayloadLen(4)][Payload(N)]`

| 消息类型 | 值 | 方向 | 说明 |
|---------|-----|------|------|
| Request | 0x01 | Client→Relay→Exit | OHTTP 加密请求 |
| Response | 0x02 | Exit→Relay→Client | OHTTP 加密响应 |
| StreamRequest | 0x03 | Client→Relay→Exit | 流式请求 |
| StreamChunk | 0x04 | Exit→Relay→Client | 流式响应块 |
| StreamEnd | 0x05 | Exit→Relay→Client | 流式结束标记 |
| Register | 0x10 | Exit→Relay | 注册（含 KeyConfig） |
| RegisterAck | 0x11 | Relay→Exit | 注册确认 |
| QueryExitKeys | 0x12 | Client→Relay | 查询 Exit 公钥列表 |
| ExitKeysResponse | 0x13 | Relay→Client | 返回 Exit 公钥列表 |
| Heartbeat | 0x20 | Exit→Relay | 心跳 |
| HeartbeatAck | 0x21 | Relay→Exit | 心跳确认 |
| Error | 0xFF | 任意 | 错误消息 |

### pkg/openai

OpenAI API 兼容层：
- 定义 ChatCompletion 请求/响应结构
- 支持流式和非流式响应

## 配置

### 配置结构体

```go
ClientConfig {
    Listen, Timeout, InsecureSkipVerify,
    DHT (DHTConfig), Bootstrap (BootstrapAPI)
}

RelayConfig {
    Listen, TLS (TLSConfig), InsecureSkipVerify,
    DHT (DHTConfig)
}

ExitConfig {
    OHTTPPrivateKeyFile, AIBackend, InsecureSkipVerify,
    DHT (DHTConfig)  // 必需，用于发现 Relay
}

DHTConfig {
    Enabled, BootstrapPeers, ListenAddrs, ExternalAddrs,
    PrivateKeyFile, Mode, UseIPFSBootstrap
}
```

### 配置文件

| 文件 | 用途 |
|------|------|
| `configs/client.yaml` | Client 配置（DHT 发现 + Bootstrap API） |
| `configs/relay-dht.yaml` | Relay 配置（DHT 注册） |
| `configs/exit-dht.yaml` | Exit 配置（DHT 发现 Relay） |

```yaml
# configs/client.yaml
listen: "127.0.0.1:8080"
timeout: 30s
insecure_skip_verify: true
dht:
  enabled: true
  use_ipfs_bootstrap: true
  listen_addrs:
    - "/ip4/0.0.0.0/tcp/0"

# configs/relay-dht.yaml
listen: ":4433"
tls:
  cert_file: "./certs/cert.pem"
  key_file: "./certs/key.pem"
dht:
  enabled: true
  listen_addrs:
    - "/ip4/0.0.0.0/tcp/4003"
  external_addrs:
    - "/ip4/<公网IP>/udp/4433"
  private_key_file: "./keys/relay_identity.key/identity.key"
  mode: "server"

# configs/exit-dht.yaml
ohttp_private_key_file: "./keys/ohttp_private.key"
ai_backend:
  url: "http://localhost:11434"
insecure_skip_verify: true
dht:
  enabled: true
  listen_addrs:
    - "/ip4/0.0.0.0/tcp/4002"
  private_key_file: "./keys/exit_identity.key/identity.key"
  mode: "server"
```

## 密钥管理

### OHTTP 密钥对

```bash
tokengo keygen --type ohttp --output ./keys
# 或
make keygen
```

- `keys/ohttp_private.key` - 私钥 (Exit 节点使用)
- `keys/ohttp_private.key.pub` - 公钥 KeyConfig (Client 使用)

### 节点身份密钥

```bash
tokengo keygen --type identity --output ./keys/exit_identity.key
tokengo keygen --type identity --output ./keys/relay_identity.key
```

用于 DHT 节点身份标识 (libp2p PeerID)。

### TLS 证书

```bash
make certs  # 生成到 certs/ 目录
```

用于 Relay 的 QUIC TLS 配置。

## CLI 命令

### 子命令

| 命令 | 说明 | 主要标志 |
|------|------|---------|
| `client` | 启动本地代理 | `--config`, `--listen`, `--insecure` |
| `relay` | 启动中继节点 | `--config`, `--listen`, `--cert`, `--key`, `--insecure` |
| `exit` | 启动出口节点 | `--config`, `--backend`, `--api-key`, `--header`, `--private-key`, `--insecure` |
| `serve` | 单进程启动全部 | `--listen`, `--backend`, `--api-key`, `--header` |
| `bootstrap` | 启动 DHT bootstrap 节点 | `--config`, `--print-peer-id` |
| `keygen` | 生成密钥 | `--type` (ohttp/identity), `--output` |

## 完整发现流程

```
[Exit 启动]
    ↓
DHT.Provide(exit-service-cid)          # 注册自己到 DHT
    ↓
发现 Relay (DHT 或静态地址)
    ↓
QUIC 连接 Relay (ALPN: tokengo-exit)
    ↓
发送 Register 消息 (pubKeyHash + KeyConfig)
    ↓
Relay 存入 Registry，回复 RegisterAck
    ↓
[Exit 心跳保活]

[Relay 启动]
    ↓
DHT.Provide(relay-service-cid)         # 注册自己到 DHT
    ↓
监听 QUIC 端口 :4433

[Client 启动]
    ↓
1. DHT 发现 Relay
   - DHT.FindProviders(relay-service-cid) → Relay 列表
   - 选择延迟最低的 Relay 连接
    ↓
2. 从 Relay 查询 Exit 公钥
   - 发送 QueryExitKeys (0x12) 到 Relay
   - 收到 ExitKeysResponse (0x13)，含所有已注册 Exit 的 KeyConfig
   - 如失败，回退到 Bootstrap API
    ↓
3. 选择 Exit，加密请求，通过 QUIC 发送到 Relay
   - 请求消息的 Target = Exit pubKeyHash
   - Relay 查找 Registry，转发到对应 Exit 连接
```

## 快速部署

### 一键启动 (serve 命令)

```bash
# 本地开发（自动生成密钥和证书）
tokengo serve --backend http://localhost:11434

# 连接 OpenAI
tokengo serve --backend https://api.openai.com --api-key sk-xxx

# 自定义端口
tokengo serve --listen :9000 --backend http://localhost:11434
```

`serve` 命令在单进程中启动 Client + Relay + Exit，适合快速测试和单机部署。

### 分布式部署 (DHT 发现模式)

```bash
# 服务器 A: Exit 节点
tokengo exit --config configs/exit-dht.yaml --backend https://api.openai.com --api-key sk-xxx

# 服务器 B: Relay 节点
tokengo relay --config configs/relay-dht.yaml

# 本地: Client（零配置，自动 DHT 发现）
tokengo client
```

## 远端服务器

| 服务器 | IP | 用户 | 认证方式 | 项目目录 |
|--------|-----|------|---------|---------|
| 主服务器 | 43.156.60.67 | root | 免密 SSH | /root/tokengo/TokenGo |

### 部署命令

```bash
# SSH 登录
ssh root@43.156.60.67

# 在服务器上构建
cd /root/tokengo/TokenGo && make build

# 交叉编译 Linux 版本 (本地)
make build-linux
```

## 常见问题

### KeyID 不匹配

错误: `解密请求失败: KeyID 不匹配`

原因: Client 获取的公钥与 Exit 节点的私钥不匹配。

解决: 重新运行 `make keygen`，重启 Exit 节点以重新注册 KeyConfig。

## 待办事项

- [x] 流式响应支持 (SSE 分块加密)
- [x] DHT 节点发现
- [x] Exit 反向隧道 (QUIC)
- [x] Exit 公钥通过 Relay 分发 (QueryExitKeys)
- [x] TLS 证书自动生成
- [x] Client 默认使用公共 IPFS DHT（零配置启动）
- [x] Exit 启动时打印公钥
- [ ] 多 Exit 节点负载均衡
- [ ] Bootstrap Server 实现
- [ ] exit节点注册ai协议类型到relay，然后由relay注册到dht
- [ ] 监控和指标
- [ ] 生产环境部署文档
