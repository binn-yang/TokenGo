# TokenGo 全面测试覆盖设计

## 背景

项目共 13 个包，仅 3 个有测试（crypto、protocol、relay/registry），覆盖率约 23%。核心通信链路完全没有测试。

## 目标

- 全面覆盖所有未测试模块
- 使用 gomock 处理 QUIC 等复杂接口依赖
- 新增一个 Client→Relay→Exit 端到端集成测试
- DHT 模块使用 libp2p mocknet 模拟网络

## 实施策略：风险驱动（方案 B）

先用简单模块热身，再攻克核心高风险模块，最后集成验证。

## 测试基础设施

### 依赖

- `go.uber.org/mock/mockgen` — Mock 生成
- `testify/assert` — 断言
- libp2p `mocknet` — DHT 测试

### 公共工具 (`internal/testutil/`)

- `GenTestOHTTPKeys()` — 测试用 OHTTP 密钥对
- `GenSelfSignedTLS()` — 测试用 TLS 证书
- `NewTestQUICPair()` — 内存 QUIC 连接对（集成测试）

### 惯例

- 表驱动测试优先
- `-short` 标志跳过慢速集成测试
- Mock 文件放在各模块 `mock_test.go` 或 `internal/testutil/`

## 阶段 1：loadbalancer & netutil（纯单元测试）

### `internal/loadbalancer/selector_test.go`

| 测试用例 | 验证点 |
|---------|-------|
| `TestWeightedSelector_Select_Distribution` | 多次 Select 的结果分布接近权重比例 |
| `TestWeightedSelector_SingleNode` | 单节点始终返回该节点 |
| `TestWeightedSelector_EmptyNodes` | 空列表返回错误 |
| `TestReportFailure_CircuitBreaker` | 连续失败后权重降为 0 |
| `TestReportSuccess_Recovery` | 成功后权重恢复 |
| `TestHealthFilter_Dedup` | 健康过滤去重正确 |

### `internal/netutil/multiaddr_test.go`

| 测试用例 | 验证点 |
|---------|-------|
| `TestExtractQUICAddress_Valid` | 正确提取 QUIC 地址 |
| `TestExtractQUICAddress_MultipleAddrs` | 多地址中选出 QUIC |
| `TestExtractQUICAddress_NoQUIC` | 无 QUIC 时返回错误 |
| `TestExtractQUICAddress_IPv6` | IPv6 正确处理 |

## 阶段 2：exit 模块（gomock QUIC，核心风险）

### `internal/exit/tunnel_test.go`

| 测试用例 | 验证点 |
|---------|-------|
| `TestTunnelClient_ConnectAndRegister` | 连接后发送 Register，收到 Ack |
| `TestTunnelClient_ReconnectLoop_ExponentialBackoff` | 指数退避重连 |
| `TestTunnelClient_HandleIncomingStream_Request` | Request 消息调用 handler |
| `TestTunnelClient_HandleIncomingStream_StreamRequest` | StreamRequest 调用流式处理 |
| `TestTunnelClient_Heartbeat` | 定时发送心跳 |
| `TestTunnelClient_HeartbeatTimeout` | 心跳超时触发重连 |

### `internal/exit/ohttp_handler_test.go`

| 测试用例 | 验证点 |
|---------|-------|
| `TestDecryptAndForward_Success` | 解密→转发→加密响应 |
| `TestDecryptAndForward_DecryptError` | 解密失败返回 Error |
| `TestWriteStreamChunks_SSE` | SSE 分段加密封装 |
| `TestWriteStreamChunks_EmptyResponse` | 空响应正确结束流 |

### `internal/exit/ai_client_test.go`

| 测试用例 | 验证点 |
|---------|-------|
| `TestBuildRequest_HopByHopFiltering` | 去除 hop-by-hop 头 |
| `TestBuildRequest_AuthInjection` | API Key 注入 |
| `TestBuildRequest_CustomHeaders` | 自定义头添加 |
| `TestDoRequest_Timeout` | 超时处理 |

## 阶段 3：client 模块（gomock QUIC）

### `internal/client/client_test.go`

| 测试用例 | 验证点 |
|---------|-------|
| `TestGetConnection_NewConnection` | 创建新连接 |
| `TestGetConnection_ReuseExisting` | 复用已有连接 |
| `TestGetConnection_ReconnectOnFailure` | 失效后重建 |
| `TestSendRequest_EncryptAndSend` | OHTTP 加密并发送 |
| `TestSendRequest_ResponseDecrypt` | 响应正确解密 |
| `TestSendStreamRequest_ChunksReassembly` | 流式块重组 |

### `internal/client/proxy_test.go`

| 测试用例 | 验证点 |
|---------|-------|
| `TestDetectStreaming_OpenAI` | OpenAI 流式识别 |
| `TestDetectStreaming_Gemini` | Gemini 流式识别 |
| `TestDetectStreaming_NonStream` | 非流式识别 |
| `TestProxyHandler_ForwardRequest` | HTTP 代理转发 |
| `TestProxyHandler_StreamResponse` | SSE 写回客户端 |

## 阶段 4：config / identity / bootstrap

### `internal/config/config_test.go`

| 测试用例 | 验证点 |
|---------|-------|
| `TestLoadConfig_Defaults` | 默认值正确 |
| `TestLoadConfig_FromFile` | YAML 解析 |
| `TestLoadConfig_EnvOverride` | 环境变量覆盖 |

### `internal/identity/identity_test.go`

| 测试用例 | 验证点 |
|---------|-------|
| `TestLoadOrGenerateKey_Generate` | 自动生成并保存 |
| `TestLoadOrGenerateKey_Load` | 正确加载已有密钥 |
| `TestLoadOrGenerateKey_InvalidFile` | 损坏文件返回错误 |

### `internal/bootstrap/client_test.go`

| 测试用例 | 验证点 |
|---------|-------|
| `TestFetchExitKeys_Success` | 正确解析 KeyConfig 列表 |
| `TestFetchExitKeys_ServerError` | 错误处理 |

## 阶段 5：DHT（libp2p mocknet）

### `internal/dht/provider_test.go`

| 测试用例 | 验证点 |
|---------|-------|
| `TestProvider_Provide` | mocknet 注册后可被发现 |
| `TestProvider_ProvideMultiple` | 多 Provider 共存 |

### `internal/dht/discovery_test.go`

| 测试用例 | 验证点 |
|---------|-------|
| `TestDiscovery_FindProviders` | 发现已注册 Provider |
| `TestDiscovery_Cache` | TTL 内返回缓存 |
| `TestDiscovery_CacheExpiry` | 过期后重新查询 |
| `TestDiscovery_NoProviders` | 无 Provider 返回空 |

## 阶段 6：端到端集成测试

### `test/integration/e2e_test.go`

使用 build tag `// +build integration`，`httptest.Server` 模拟 AI 后端。

| 测试用例 | 验证点 |
|---------|-------|
| `TestE2E_NonStreamRequest` | 完整非流式链路 |
| `TestE2E_StreamRequest` | 完整流式链路 |
| `TestE2E_ExitReconnect` | Exit 断连后恢复 |
| `TestE2E_MultipleExits` | 多 Exit 选择正确 |

## 预期产出

- 约 50+ 测试用例
- 覆盖全部 13 个包
- 单元测试 + 1 个端到端集成测试套件
