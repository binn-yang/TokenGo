# TokenGo 私有 DHT 网络实现方案

## 目标

将 TokenGo 从公共 IPFS DHT 转换为私有 DHT 网络，实现：
- **更快的节点发现**：从 5-30s 降低到 <3s
- **零配置启动**：`tokengo client` 无需任何参数
- **更好的用户体验**：启动过程有进度反馈
- **无外部依赖**：不再依赖公共 IPFS 基础设施

## 核心设计

### 架构变更

```
当前架构:
  Client → 公共 IPFS DHT → 发现 Relay → 连接 Relay → 查询 Exit 公钥
           (依赖外部节点，延迟高)

新架构:
  Client → Relay (即 DHT Bootstrap) → 发现其他 Relay → 连接最优 Relay → 查询 Exit 公钥
           (自有节点，延迟低)
```

### 关键设计决策

1. **Relay = DHT Bootstrap**
   - Relay 节点天然是系统的最小必要基础设施
   - 无需单独部署 bootstrap 节点
   - 第一个 Relay 自举启动，后续节点通过已知 Relay 加入

2. **Bootstrap Peers 双层发现**
   - **Layer 1**：硬编码在二进制中的默认 peers（保底）
   - **Layer 2**：从 GitHub 仓库获取 `bootstrap.json`（动态更新）
   - 两层并发执行，合并去重

3. **私有 DHT 协议**
   - 使用 `dht.ProtocolPrefix("/tokengo")` 隔离网络
   - 与公共 IPFS DHT 完全独立

---

## 文件变更清单

### 新增文件

| 文件 | 说明 |
|------|------|
| `internal/dht/peers.go` | Bootstrap peers 解析模块（硬编码 + GitHub fetch） |
| `internal/client/progress.go` | 启动进度反馈接口实现 |
| `bootstrap.json` | 仓库根目录的动态 peers 列表 |

### 修改文件

| 文件 | 变更说明 |
|------|---------|
| `internal/dht/node.go` | 添加 ProtocolPrefix，替换 Sleep(5s) 为轮询，使用 ResolveBootstrapPeers |
| `internal/config/config.go` | 移除 `Enabled`、`UseIPFSBootstrap`、`BootstrapAPI`、`BootstrapConfig` |
| `internal/client/proxy.go` | DHT 始终启用，移除 Bootstrap API 代码，集成进度反馈 |
| `internal/dht/discovery.go` | 移除 Exit DHT 发现（Exit 通过 Relay QueryExitKeys 获取） |
| `internal/relay/relay.go` | 移除 Enabled/UseIPFSBootstrap 检查 |
| `internal/exit/exit.go` | 移除 Enabled/UseIPFSBootstrap 检查 |
| `cmd/tokengo/main.go` | 重写 clientCmd 零配置路径，移除 bootstrapCmd |
| `configs/client.yaml` | 精简为最小配置 |
| `configs/relay-dht.yaml` | 移除 IPFS 相关配置 |
| `configs/exit-dht.yaml` | 移除 IPFS 相关配置 |

### 删除文件

| 文件 | 原因 |
|------|------|
| `internal/bootstrap/client.go` | Bootstrap API 被私有 DHT 替代 |
| `internal/dht/bootstrap.go` | Relay 节点即 bootstrap，无需单独类型 |
| `internal/bootstrap/` 目录 | 整个目录移除 |

---

## 详细实现

### 1. Bootstrap Peers 模块 (`internal/dht/peers.go`)

```go
// 硬编码默认 peers (编译进二进制)
var DefaultBootstrapPeers = []string{
    "/ip4/43.156.60.67/udp/4433/p2p/<PeerID>",  // 主服务器
}

// GitHub JSON URL (主 + 镜像)
var bootstrapJSONURLs = []string{
    "https://raw.githubusercontent.com/binn-yang/TokenGo/master/bootstrap.json",
    "https://cdn.jsdelivr.net/gh/binn-yang/TokenGo@master/bootstrap.json",
}

// 解析并合并 bootstrap peers
func ResolveBootstrapPeers(ctx context.Context, configPeers []string) []peer.AddrInfo
```

**逻辑流程**：
1. 并发启动：解析硬编码 peers + fetch GitHub JSON
2. 合并去重（按 PeerID）
3. 如有 configPeers 则优先合并
4. 总超时 8s，GitHub 失败不影响硬编码 peers

### 2. `bootstrap.json` Schema

```json
{
  "version": 1,
  "peers": [
    "/ip4/43.156.60.67/udp/4433/p2p/12D3KooW..."
  ]
}
```

位置：仓库根目录 `/Users/binn/ZedProjects/token-run-workspace/TokenGo/bootstrap.json`

### 3. DHT Node 改造 (`internal/dht/node.go`)

**Config 结构体变更**：
- 移除 `UseIPFSBootstrap` 字段
- `ProtocolPrefix` 已存在，启用它

**Start() 方法变更**：
```go
// 添加私有协议前缀
dhtOpts = append(dhtOpts, dht.ProtocolPrefix(protocol.ID("/tokengo")))

// 替换 Sleep(5s) 为轮询路由表
func (n *Node) waitForRoutingTable(ctx context.Context) error {
    ticker := time.NewTicker(500 * time.Millisecond)
    timeout := time.After(10 * time.Second)
    for {
        select {
        case <-timeout:
            // 超时也继续，只要有节点就连得上
            return nil
        case <-ticker.C:
            if n.dht.RoutingTable().Size() > 0 {
                return nil
            }
        }
    }
}

// 使用 ResolveBootstrapPeers
func (n *Node) connectBootstrapPeers(ctx context.Context) (int, error) {
    peers := ResolveBootstrapPeers(ctx, n.config.BootstrapPeers)
    // 并发连接所有 peers...
}
```

### 4. Config 精简 (`internal/config/config.go`)

**DHTConfig 变更**：
```go
// 之前
type DHTConfig struct {
    Enabled          bool     `yaml:"enabled"`
    BootstrapPeers   []string `yaml:"bootstrap_peers"`
    ListenAddrs      []string `yaml:"listen_addrs"`
    ExternalAddrs    []string `yaml:"external_addrs,omitempty"`
    PrivateKeyFile   string   `yaml:"private_key_file"`
    Mode             string   `yaml:"mode"`
    UseIPFSBootstrap bool     `yaml:"use_ipfs_bootstrap,omitempty"`
}

// 之后
type DHTConfig struct {
    BootstrapPeers   []string `yaml:"bootstrap_peers,omitempty"`
    ListenAddrs      []string `yaml:"listen_addrs,omitempty"`
    ExternalAddrs    []string `yaml:"external_addrs,omitempty"`
    PrivateKeyFile   string   `yaml:"private_key_file,omitempty"`
    Mode             string   `yaml:"mode,omitempty"`
}
```

**ClientConfig 变更**：
```go
// 之前
type ClientConfig struct {
    Listen             string        `yaml:"listen"`
    Timeout            time.Duration `yaml:"timeout"`
    InsecureSkipVerify bool          `yaml:"insecure_skip_verify"`
    DHT                DHTConfig     `yaml:"dht,omitempty"`
    Bootstrap          BootstrapAPI  `yaml:"bootstrap,omitempty"`
}

// 之后
type ClientConfig struct {
    Listen             string        `yaml:"listen"`
    Timeout            time.Duration `yaml:"timeout"`
    InsecureSkipVerify bool          `yaml:"insecure_skip_verify"`
    BootstrapPeers     []string      `yaml:"bootstrap_peers,omitempty"` // 可选覆盖
}
```

**移除**：
- `BootstrapAPI` 结构体
- `BootstrapConfig` 结构体
- `LoadBootstrapConfig` 函数

### 5. 启动 UX (`internal/client/progress.go`)

```go
type ProgressReporter interface {
    OnBootstrapConnecting()
    OnBootstrapConnected(connected, total int)
    OnDiscoveringRelays()
    OnRelaysDiscovered(count int)
    OnRelayProbed(addr string, latency time.Duration, selected bool)
    OnRelaySelected(addr string, latency time.Duration)
    OnFetchingExitKeys()
    OnExitKeyFetched(pubKeyHash string)
    OnReady(listenAddr string)
}
```

**预期输出**：
```
$ tokengo client

TokenGo Client v0.2.0
正在连接 DHT 网络...
已连接到 2 个引导节点
正在发现 Relay 节点...
发现 3 个 Relay 节点，正在测量延迟...
  relay-tokyo: 23ms [selected]
  relay-eu: 187ms
已选择最佳 Relay (23ms)
正在获取 Exit 公钥...
已获取 Exit 公钥 (Hash: abc123...)
就绪! 监听 127.0.0.1:8080
```

### 6. CLI 变更 (`cmd/tokengo/main.go`)

**clientCmd**：
- 零配置模式下直接创建 DHT client 节点
- 添加 `--bootstrap-peer` 标志用于手动覆盖
- 移除所有 `DHT.Enabled`、`UseIPFSBootstrap` 相关代码

**exitCmd**：
- 移除 `DHT.Enabled` 检查（DHT 始终启用）
- 移除 `UseIPFSBootstrap` 引用

**移除 bootstrapCmd**：
- 删除整个 `bootstrapCmd()` 函数
- 从 `main()` 中移除 `rootCmd.AddCommand(bootstrapCmd())`

### 7. Discovery 简化 (`internal/dht/discovery.go`)

**移除 Exit 相关**：
- 删除 `exits`、`exitTTL` 字段
- 删除 `refreshExits()` 方法
- 删除 `DiscoverExits()` 方法
- 删除 `GetCachedExits()` 方法
- 删除 `ExitCount()` 方法

**原因**：Exit 公钥通过 Relay 的 `QueryExitKeys` 获取，不走 DHT。

---

## 配置文件示例

### `configs/client.yaml` (精简版)
```yaml
# TokenGo Client 配置
# 私有 DHT 网络自动发现节点，通常无需配置

listen: "127.0.0.1:8080"
timeout: 30s

# 自定义引导节点 (可选，覆盖内置默认值)
# bootstrap_peers:
#   - "/ip4/1.2.3.4/udp/4433/p2p/12D3Koo..."
```

### `configs/relay-dht.yaml`
```yaml
listen: ":4433"
tls:
  cert_file: "./certs/cert.pem"
  key_file: "./certs/key.pem"
dht:
  listen_addrs:
    - "/ip4/0.0.0.0/tcp/4003"
  external_addrs:
    - "/ip4/43.156.60.67/udp/4433"
  private_key_file: "./keys/relay_identity.key/identity.key"
  mode: "server"
  # bootstrap_peers 可选，默认使用内置列表
```

### `configs/exit-dht.yaml`
```yaml
ohttp_private_key_file: "./keys/ohttp_private.key"
ai_backend:
  url: "http://localhost:11434"
dht:
  listen_addrs:
    - "/ip4/0.0.0.0/tcp/4002"
  private_key_file: "./keys/exit_identity.key/identity.key"
  mode: "server"
```

---

## 确认决策

- **向后兼容性**：不需要
- **GitHub 仓库**：`github.com/binn-yang/TokenGo` ✓
- **主服务器 IP**：`43.156.60.67` ✓
- **版本号**：保持 0.1.0（开发阶段）
- **`serve` 命令**：保持不变（静态模式）

---

## 实施顺序

1. **Phase 1**: 创建 `internal/dht/peers.go` (无依赖，可独立测试)
2. **Phase 2**: 修改 `internal/dht/node.go` (依赖 Phase 1)
3. **Phase 3**: 精简 `internal/config/config.go` (无依赖)
4. **Phase 4**: 修改 `internal/relay/relay.go` 和 `internal/exit/exit.go` (依赖 Phase 3)
5. **Phase 5**: 简化 `internal/dht/discovery.go` (无依赖)
6. **Phase 6**: 创建 `internal/client/progress.go` (无依赖)
7. **Phase 7**: 修改 `internal/client/proxy.go` (依赖 Phase 1-6)
8. **Phase 8**: 修改 `cmd/tokengo/main.go` (依赖 Phase 3, 7)
9. **Phase 9**: 更新配置文件和创建 `bootstrap.json`
10. **Phase 10**: 删除废弃文件，测试验证

---

## 风险与缓解

| 风险 | 缓解措施 |
|------|---------|
| GitHub 访问不稳定（国内） | 硬编码 peers 保底 + jsDelivr 镜像 |
| 第一个 Relay 自举问题 | 允许空路由表启动，接受直接连接 |
| 旧配置不兼容 | YAML 忽略未知字段，静默兼容 |

---

## 验收标准

1. `tokengo client` 零配置启动成功
2. 启动时间 < 5s（有 Relay 可用时）
3. 启动过程显示进度反馈
4. `tokengo serve` 命令不受影响（静态模式）
5. 旧配置文件不会导致启动失败

---

## 待实施

所有决策已确认，可以开始实施。
