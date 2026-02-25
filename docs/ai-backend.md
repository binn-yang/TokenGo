exit节点的ai backend有不同的协议类型如claude、gemini、openai等，需要让client能主动选择并使用正确的协议，同时将来ai backend还会提供增值能力如不同sla有不同的定价，你搜索下相关服务的最佳实践，如sn64网络等，给我一些建议方案

基于对 TokenGo 代码库和行业项目的深入研究,以下是我的分析和建议:

  ---
  现状分析

  当前 TokenGo 的 Exit 注册信息只包含 pubKeyHash + KeyConfig,没有任何关于 AI 后端类型、能力、定价的元数据。Client 选择 Exit 时只能盲选第一个可用节点。

  ---
  行业参考

  1. OpenRouter / AnyInt 模式 (中心化聚合网关)

  - 统一 API 端点:一个接口调用 400+ 模型,兼容 OpenAI 协议格式
  - 智能路由:根据任务类型 (coding/writing/general) 自动选最优模型
  - 多层降级:Subscription → API Key → Cheap → Free,零停机
  - 语义路由:不仅做传统延迟/价格路由,还做语义层面的模型匹配 (10ms 内完成决策)
  - 供应商评估:持续评估供应商是否"降智",自动降权

  2. Bittensor SN64 (Chutes) 模式 (去中心化推理市场)

  - 矿工注册能力:矿工注册时声明支持的模型列表和 GPU 配置
  - 验证者评分:验证者持续评估矿工推理质量,分配排放奖励
  - 竞争市场:矿工之间竞争提供最优的推理服务,质量差的自动被淘汰
  - 按用量计费:每次 API 调用计费,平台抽成 + 回购代币

  3. SingularityNET 模式 (AI 服务市场)

  - Agent 能力描述:每个 AI Agent 注册时声明能力、输入输出格式
  - Agent 发现:通过语义搜索发现合适的 Agent
  - Agent 组合:多个 Agent 可以串联协作完成复杂任务

  4. Agent Registry 最新研究 (A2A / MCP / NANDA)

  - 能力清单 (Agent Card / Capability Manifest):标准化 JSON 描述 Agent 能力
  - 语义分类发现:基于 DHT + 语义分类的去中心化发现
  - 可验证身份:Sigstore 签名验证 Agent 完整性

  ---
  TokenGo 建议方案

  方案概述:服务目录 + 能力注册

  ┌──────────┐                    ┌──────────┐                    ┌──────────┐
  │  Client  │ ──QueryExitKeys──> │  Relay   │ <──Register──────  │  Exit    │
  │          │ <─ExitKeysResp───  │ (Registry│   含 ServiceInfo   │          │
  │ 按协议   │                    │  +目录)  │                    │ 声明能力  │
  │ 筛选Exit │                    └──────────┘                    │ 协议/模型 │
  └──────────┘                                                    │ SLA/定价  │

  核心设计:ExitServiceInfo

  // Exit 注册时携带的服务元数据
  type ExitServiceInfo struct {
      // 基础标识
      PubKeyHash  string   `json:"pub_key_hash"`
      KeyConfig   string   `json:"key_config"`

      // === 新增:服务能力描述 ===

      // 支持的 AI 协议类型
      Protocols   []string `json:"protocols"`    // ["openai", "claude", "gemini", "ollama"]

      // 支持的模型列表 (可选,Exit 可选择公开)
      Models      []ModelInfo `json:"models,omitempty"`

      // 服务等级
      Tier        string   `json:"tier,omitempty"`  // "free", "standard", "premium"

      // 自定义标签 (用于未来扩展)
      Tags        map[string]string `json:"tags,omitempty"`

      // 版本 (协议演进用)
      Version     int      `json:"version"`
  }

  type ModelInfo struct {
      ID       string  `json:"id"`        // "gpt-4o", "claude-sonnet-4"
      MaxCtx   int     `json:"max_ctx,omitempty"`   // 最大上下文窗口
      Pricing  *Price  `json:"pricing,omitempty"`   // 定价信息
  }

  type Price struct {
      InputPer1M  float64 `json:"input_per_1m"`   // 每百万 input token 价格
      OutputPer1M float64 `json:"output_per_1m"`  // 每百万 output token 价格
      Currency    string  `json:"currency"`        // "USD", "TAO", etc.
  }

  实施路径 (分 3 期)

  P1: 协议类型注册与筛选 (最小可用)

  改动范围:
  1. Exit 注册:Register 消息 Payload 从纯 KeyConfig 扩展为 KeyConfig + ServiceInfo
  2. Relay Registry:存储 ServiceInfo,在 ExitKeysResponse 中返回
  3. Client 筛选:Client 根据用户请求的目标协议筛选 Exit

  协议变更:
  Register Payload (v2):
  [KeyConfigLen(4)] [KeyConfig(N)] [ServiceInfoLen(4)] [ServiceInfo(JSON)]

  Client 使用方式:
  # 用户指定使用 OpenAI 协议的 Exit
  tokengo client --protocol openai

  # 或在配置文件中指定
  protocol_preference: ["claude", "openai"]  # 按优先级

  P2: 模型元数据与智能路由

  - Exit 注册时声明支持的模型列表
  - Client 可以按模型名筛选 Exit (如 "我要用 claude-sonnet-4")
  - Relay 可做简单的负载均衡 (同协议多 Exit 时选延迟最低的)

  P3: 定价与 SLA (增值服务基础)

  - Exit 注册定价信息和 SLA 等级
  - Client 可按价格/SLA 偏好选择 Exit
  - 引入可选的用量计量和结算机制 (参考 Bittensor 的代币激励或简单的 API Key 预付费)

  ---
  关键设计选择建议

  ┌──────────────┬─────────────────────────────────────────────────┬──────────────────────────────────────────────┐
  │     维度     │                      建议                       │                     理由                     │
  ├──────────────┼─────────────────────────────────────────────────┼──────────────────────────────────────────────┤
  │ 协议标准化   │ Client 统一用 OpenAI 兼容格式发请求             │ OpenAI 协议已成事实标准,降低 Client 复杂度   │
  ├──────────────┼─────────────────────────────────────────────────┼──────────────────────────────────────────────┤
  │ 协议转换位置 │ 在 Exit 侧做协议转换                            │ 符合现有盲转发架构,Client 不需要知道后端细节 │
  ├──────────────┼─────────────────────────────────────────────────┼──────────────────────────────────────────────┤
  │ 能力发现     │ 通过 Relay 查询 (扩展 QueryExitKeys)            │ 复用现有消息通道,无需额外基础设施            │
  ├──────────────┼─────────────────────────────────────────────────┼──────────────────────────────────────────────┤
  │ 定价模型     │ 初期简单固定价,后期按 token 计量                │ 降低初期复杂度,保持灵活性                    │
  ├──────────────┼─────────────────────────────────────────────────┼──────────────────────────────────────────────┤
  │ SLA 验证     │ 参考 Bittensor 验证者模式,Relay 做心跳+延迟探测 │ Relay 已有心跳机制,自然扩展                  │
  └──────────────┴─────────────────────────────────────────────────┴──────────────────────────────────────────────┘

  与 Bittensor/SN64 的差异化定位

  ┌──────────┬───────────────────┬─────────────────────────────┐
  │   特性   │   Chutes (SN64)   │           TokenGo           │
  ├──────────┼───────────────────┼─────────────────────────────┤
  │ 核心价值 │ 去中心化 GPU 算力 │ 隐私保护 (OHTTP 端到端加密) │
  ├──────────┼───────────────────┼─────────────────────────────┤
  │ 信任模型 │ 公开验证+质押惩罚 │ 盲转发,密码学保证           │
  ├──────────┼───────────────────┼─────────────────────────────┤
  │ 目标用户 │ 模型部署者        │ 最终用户 (隐私敏感)         │
  ├──────────┼───────────────────┼─────────────────────────────┤
  │ 激励机制 │ TAO 代币排放      │ 可选,按需引入               │
  ├──────────┼───────────────────┼─────────────────────────────┤
  │ 协议转换 │ 无 (原生模型)     │ Exit 侧协议翻译             │
  └──────────┴───────────────────┴─────────────────────────────┘

  TokenGo 的独特价值在于隐私保护 + 多协议访问:用户通过一个统一入口,以加密方式访问任何 AI 后端,而 Relay 无法知道请求内容,Exit 无法知道用户身份。这是 Chutes/OpenRouter 等公开网络做不到的。

  ---
  下一步建议

  推荐从 P1 开始:扩展 Register 消息携带 protocols 字段,让 Client 能按协议类型筛选 Exit。这个改动范围可控 (主要改 protocol/messages.go、relay/registry.go、client/proxy.go),且为后续的模型发现和定价打下基础。

  需要我基于这个方案做详细的实施规划吗?
