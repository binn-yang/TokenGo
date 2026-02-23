# TokenGo 项目文档

## 项目概述

TokenGo 是一个去中心化 AI API 网关，通过 OHTTP (Oblivious HTTP) 协议实现端到端加密，保护用户隐私。

## 当前状态

- **版本**: 0.1.0
- **状态**: MVP 可用，Docker 集成测试通过
- **测试日期**: 2025-12-13

## 架构设计

### 核心组件

```
┌─────────┐     QUIC      ┌─────────┐    HTTPS     ┌─────────┐    HTTP     ┌─────────┐
│ Client  │──────────────>│  Relay  │─────────────>│  Exit   │────────────>│ Ollama  │
│ :8080   │   加密请求     │  :4433  │   转发密文   │  :8443  │   明文请求  │ :11434  │
└─────────┘               └─────────┘              └─────────┘             └─────────┘
     │                         │                        │
     │ 持有 Exit 公钥          │ 无法解密               │ 持有私钥，可解密
     │ 可加密请求              │ 只知道来源IP           │ 不知道来源IP
```

### 隐私保护 (盲转发架构)

| 节点 | 知道的信息 | 不知道的信息 |
|------|-----------|-------------|
| Relay | Client IP | 请求内容、Exit 地址 (从加密消息中提取转发) |
| Exit | 请求内容 | Client IP |

**关键设计**: Exit 地址由 Client 在请求消息中指定，Relay 只做盲转发，进一步保护隐私。

### 端口分配

| 服务 | 端口 | 协议 |
|------|------|------|
| Client (本地代理) | 8080 | HTTP |
| Relay (中继) | 4433 | QUIC |
| Exit (出口) | 8443 | HTTPS |
| Ollama (AI 后端) | 11434 | HTTP |

## 核心模块

### internal/crypto

OHTTP 加密实现：
- `ohttp.go` - OHTTP 请求/响应加解密
- 使用 HPKE (X25519 + HKDF-SHA256 + AES-128-GCM)
- KeyID 用于匹配客户端公钥和服务端私钥

### internal/client

本地 HTTP 代理：
- 监听本地端口，接收 OpenAI 兼容 API 请求
- 使用 Exit 公钥加密请求
- 通过 QUIC 发送到 Relay

### internal/relay

QUIC 中继节点 (盲转发模式)：
- 接收客户端 QUIC 连接
- 从请求消息中提取目标 Exit 地址
- 盲转发加密数据到 Client 指定的 Exit 节点
- 无法解密任何内容，也不预先配置 Exit 地址

### internal/exit

OHTTP 出口节点：
- 接收加密请求
- 使用私钥解密
- 转发明文到 AI 后端

### pkg/openai

OpenAI API 兼容层：
- 定义 ChatCompletion 请求/响应结构
- 支持流式和非流式响应

## 配置文件

### configs/docker/ (Docker 环境)

```yaml
# client.yaml
listen: "0.0.0.0:8080"
relay: "relay:4433"
exit_public_key: "<base64 编码的公钥>"

# relay.yaml
listen: ":4433"
exit: "exit:8443"

# exit.yaml
listen: ":8443"
ohttp_private_key_file: "/etc/tokengo/keys/ohttp_private.key"
ai_backend:
  url: "http://ollama:11434"
```

## 密钥管理

### OHTTP 密钥对

```bash
make keygen  # 生成到 keys/ 目录
```

- `ohttp_private.key` - 私钥 (Exit 节点使用)
- `ohttp_private.key.pub` - 公钥 (Client 配置使用)

**注意**: 每次 `make keygen` 会生成新密钥，需要同步更新客户端配置中的 `exit_public_key`。Docker 测试脚本会自动处理此同步。

### TLS 证书

```bash
make certs  # 生成到 certs/ 目录
```

用于 Relay → Exit 的 HTTPS 通信。

## Docker 部署

### docker-compose.yml

包含 4 个服务：
1. `ollama` - AI 后端
2. `exit` - 出口节点
3. `relay` - 中继节点
4. `client` - 客户端代理

### 健康检查

Ollama 使用 `ollama list` 命令检查健康状态（容器内无 curl）。

### 测试脚本

`scripts/docker-test.sh`:
1. 同步公钥到客户端配置
2. 构建 Docker 镜像
3. 启动所有服务
4. 等待 Ollama 就绪
5. 下载 llama3.2:1b 模型
6. 执行 API 测试

## 性能数据

测试环境: Docker Desktop on macOS

| 指标 | 数值 |
|------|------|
| 纯网络延迟 (OHTTP + QUIC) | < 1ms |
| 端到端延迟 (含推理) | 1-4s |
| OHTTP 加密开销 | 可忽略 |

## 核心功能

### DHT 节点发现 (internal/dht)

- 基于 Kademlia 的 P2P 节点发现
- 支持 bootstrap 节点
- Exit 公钥存储: `DHT.PutValue("/tokengo/exit-pubkey/<peerID>")`
- Exit 公钥获取: `DHT.GetValue("/tokengo/exit-pubkey/<peerID>")`
- 配置文件: `configs/*-dht.yaml`

### 负载均衡 (internal/loadbalancer)

- 支持多个 Exit 节点
- 轮询和加权策略

## 常见问题

### KeyID 不匹配

错误: `解密请求失败: KeyID 不匹配`

原因: 客户端配置的公钥与 Exit 节点的私钥不匹配。

解决:
1. 重新运行 `make keygen`
2. 更新客户端配置中的 `exit_public_key`
3. 或使用 `make docker-test` 自动同步

### Ollama 健康检查失败

Ollama 容器内没有 curl，已改用 `ollama list` 命令检查。

## 快速部署

### 一键启动 (serve 命令)

```bash
# 本地开发
tokengo serve --backend http://localhost:11434

# 连接 OpenAI
tokengo serve --backend https://api.openai.com --api-key sk-xxx

# 自定义端口
tokengo serve --listen :9000 --backend http://localhost:11434
```

`serve` 命令会在单进程中启动 Client + Relay + Exit，自动生成密钥和证书，适合快速测试和单机部署。

### 分布式部署 (DHT 发现模式)

**特点**: Client 零配置，自动通过公共 IPFS DHT 网络发现节点

```bash
# 服务器 A: Exit 节点 (DHT 模式)
tokengo exit --config configs/exit-dht.yaml --backend https://api.openai.com --api-key sk-xxx

# 服务器 B: Relay 节点 (DHT 模式)
tokengo relay --config configs/relay-dht.yaml

# 本地: Client (零配置！)
tokengo client
```

**说明**:
- Client 默认使用公共 IPFS DHT bootstrap 节点发现网络中的 Relay/Exit
- Exit/Relay 需要配置 DHT 以注册服务到 DHT 网络
- Exit 公钥存储在 DHT 中，Client 自动获取

### Client 节点发现流程

Client 支持三种发现方式（按优先级）：

1. **DHT 发现** - 通过 libp2p DHT 网络发现 Relay 和 Exit 节点（含公钥）
2. **Bootstrap API** - 通过自建 HTTP API 获取节点列表（含 Exit 公钥）
3. **回退地址** - 使用配置的静态地址（需配置完整 Exit 信息）

```yaml
# configs/client-discovery.yaml
listen: "127.0.0.1:8080"

# DHT 发现 (可选)
dht:
  enabled: true
  bootstrap_peers:
    - "/ip4/x.x.x.x/tcp/4001/p2p/QmXXX"

# Bootstrap API (可选)
bootstrap:
  url: "https://bootstrap.example.com"

# 回退地址 (需配置完整 Exit 信息)
fallback:
  relay_addrs:
    - "relay.example.com:4433"
  exits:
    - address: "exit.example.com:8443"
      public_key: "BASE64_OHTTP_PUBLIC_KEY"
      key_id: 1
```

### 完整发现流程

```
[Exit 启动]
    ↓
DHT.Provide(exit-service-cid)  # 注册服务
DHT.PutValue("/tokengo/exit-pubkey/<peerID>", {keyID, publicKey, address})  # 存储公钥
    ↓
[Client 启动]
    ↓
1. 尝试 DHT 发现 Relay + Exit（含公钥）
   - DHT.FindProviders(relay-service-cid) → Relay 列表
   - DHT.FindProviders(exit-service-cid) → Exit PeerID 列表
   - DHT.GetValue("/tokengo/exit-pubkey/<peerID>") → {keyID, publicKey, address}
2. 否则尝试 Bootstrap API 发现 Relay + Exit（含公钥）
3. 否则使用回退地址（需配置完整 Exit 信息）
    ↓
计算最佳路由 → 选择 Relay + Exit
    ↓
连接 Relay → 使用 Exit 公钥加密 → 转发到 Exit
```

**关键设计变化**:
- Exit 公钥在 DHT/Bootstrap 中直接注册，Client 发现时获取
- Client 不再通过 Relay 获取 Exit 列表
- Relay 只做盲转发，不知道 Exit 地址，也不维护 Exit 列表

**隐私优势**:
- Client 不需要预先配置任何节点地址
- Relay 不知道 Exit 地址，只做盲转发
- Exit 地址由 Client 在请求中动态指定
- Exit 公钥分布式存储在 DHT 中

## 待办事项

- [x] 流式响应支持 (SSE 分块加密)
- [ ] 多 Exit 节点负载均衡
- [x] DHT 节点发现（Exit 公钥存储/获取）
- [x] TLS 证书自动生成
- [x] Client 默认使用公共 IPFS DHT（零配置启动）
- [x] Exit 启动时打印公钥
- [ ] Bootstrap Server 实现
- [ ] 监控和指标
- [ ] 生产环境部署文档
