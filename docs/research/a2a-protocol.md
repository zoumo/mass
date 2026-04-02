# A2A (Agent2Agent Protocol) - 3W2H 深度调研

> 调研日期: 2026-04-01
> 协议版本: v1.0.0 (2026-03-12 发布)
> 官网: https://a2a-protocol.org/latest/
> GitHub: https://github.com/a2aproject/A2A
> 许可证: Apache License 2.0
> 归属: Google 发起，已捐赠给 Linux Foundation

---

## 目录

- [What - 是什么](#what---是什么)
- [Why - 为什么](#why---为什么)
- [Who - 谁在做](#who---谁在做)
- [How - 怎么做](#how---怎么做)
- [How Well - 做得怎么样](#how-well---做得怎么样)
- [参考链接](#参考链接)

---

## What - 是什么

### 一句话定义

A2A 是一个开放协议，标准化 **AI Agent 之间**的通信与协作方式。它解决的是 agent-to-agent 的互操作问题——让不同框架、不同厂商构建的 agent 能够发现彼此、委托任务、交换结果。

### 核心思想

```
没有 A2A:                              有了 A2A:
  Agent A ─┬─ 自定义 API ─ Agent B       Agent A ─┐
  Agent A ─┼─ 自定义 API ─ Agent C       Agent B ─┤
  Agent B ─┼─ 自定义 API ─ Agent C       Agent C ─┼── A2A ──┬─ Agent D
  Agent D ─┼─ 自定义 API ─ Agent A       Agent D ─┤         ├─ Agent E
  Agent D ─┴─ 自定义 API ─ Agent E       Agent E ─┘         └─ Agent F
  (每个组合都要定制集成)                  (实现一次协议即可互通)
```

### 关键特征

| 特征 | 描述 |
|---|---|
| **Agent-to-Agent** | 不是 agent-to-tool (MCP) 或 editor-to-agent (ACP)，而是 agent 之间的对等通信 |
| **多协议绑定** | 同一规范支持 JSON-RPC 2.0、gRPC、HTTP+JSON/REST 三种传输方式 |
| **异步优先** | 为长时间运行任务设计，支持流式更新、推送通知、人在环路 (HITL) |
| **Agent Card 发现** | 通过 Well-Known URI (`/.well-known/agent-card.json`) 自动发现 agent 能力 |
| **模态无关** | 支持文本、图片、音视频、结构化数据等任意内容类型 |
| **不透明执行** | Agent 之间基于声明的能力协作，不需要暴露内部实现细节 |
| **企业就绪** | OAuth 2.0 + PKCE、mTLS、多租户、OpenTelemetry 追踪、审计 |

### 核心抽象

```
┌─────────────────────────────────────────────────────────────────┐
│                         Context                                   │
│  (逻辑分组标识符，关联多个 Task 和 Message)                         │
│                                                                   │
│  ┌──────────────────────────────────────────────────────────┐    │
│  │                      Task                                 │    │
│  │  (有状态的工作单元，具有生命周期)                           │    │
│  │                                                           │    │
│  │  ┌──────────┐  ┌──────────┐  ┌──────────┐               │    │
│  │  │ Message  │  │ Message  │  │ Message  │  ...           │    │
│  │  │ (user)   │  │ (agent)  │  │ (user)   │               │    │
│  │  └──────────┘  └──────────┘  └──────────┘               │    │
│  │                                                           │    │
│  │  ┌──────────┐  ┌──────────┐                              │    │
│  │  │ Artifact │  │ Artifact │  ...                         │    │
│  │  │ (输出物) │  │ (输出物) │                              │    │
│  │  └──────────┘  └──────────┘                              │    │
│  └──────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────┘
```

- **Context** -- 服务端生成的标识符，逻辑分组多个相关 Task 和 Message
- **Task** -- 有状态的工作单元，有唯一 ID 和定义的生命周期状态机
- **Message** -- 客户端和 agent 之间的单轮通信，包含角色（user / agent）和若干 Part
- **Part** -- Message 和 Artifact 的最小内容单元（text / raw / url / data）
- **Artifact** -- agent 在任务处理期间生成的有形输出（文档、图像、结构化数据等）

### Agent Card

Agent Card 是 agent 的"数字名片"，JSON 格式，通过 Well-Known URI 发现：

```json
{
  "name": "code-reviewer",
  "description": "Code review agent",
  "protocolVersion": "1.0.0",
  "supportedInterfaces": [
    {
      "url": "https://agent.example.com/a2a",
      "protocolBinding": "PROTOCOL_BINDING_JSONRPC_OVER_HTTP",
      "protocolVersion": "1.0.0"
    }
  ],
  "capabilities": {
    "streaming": true,
    "pushNotifications": true,
    "contextSupport": true
  },
  "skills": [
    {
      "id": "review-code",
      "name": "Code Review",
      "description": "Review pull requests and provide feedback"
    }
  ],
  "securitySchemes": { ... }
}
```

内容包括：身份、能力声明、技能列表、支持的传输方式、安全认证方案。
v1.0 支持 Agent Card 签名验证（JWS + JSON Canonicalization RFC 8785）。

### Task 状态机

```
                SendMessage
                    │
                    ▼
            ┌──────────────┐
            │   SUBMITTED   │
            └──────┬───────┘
                   │
                   ▼
            ┌──────────────┐
     ┌───── │   WORKING     │ ─────┐
     │      └──────┬───────┘      │
     │             │               │
     │     ┌───────┴────────┐     │
     │     ▼                ▼     │
┌─────────────┐    ┌──────────────┐
│INPUT_REQUIRED│    │AUTH_REQUIRED  │
│  (需要输入)  │    │ (需要认证)    │
└──────┬──────┘    └──────┬───────┘
       │                   │
       │   SendMessage     │   认证后重发
       └───────┬───────────┘
               ▼
        ┌──────────────┐
        │   WORKING     │
        └──────┬───────┘
               │
    ┌──────────┼──────────┬──────────┐
    ▼          ▼          ▼          ▼
┌────────┐ ┌────────┐ ┌────────┐ ┌────────┐
│COMPLETED│ │ FAILED │ │CANCELED│ │REJECTED│
│ (终态)  │ │ (终态) │ │ (终态) │ │ (终态) │
└────────┘ └────────┘ └────────┘ └────────┘
```

v1.0 使用 SCREAMING_SNAKE_CASE 枚举：
`TASK_STATE_SUBMITTED` → `TASK_STATE_WORKING` → `TASK_STATE_COMPLETED` / `TASK_STATE_FAILED` / `TASK_STATE_CANCELED` / `TASK_STATE_REJECTED`

中断态：`TASK_STATE_INPUT_REQUIRED`（需要用户输入）、`TASK_STATE_AUTH_REQUIRED`（需要认证）

**终态不可变**：Task 达到终态后，后续交互必须在同一 contextId 下创建新 Task。

### Agent 响应模式

| 模式 | 行为 | 适用场景 |
|------|------|---------|
| **Message-only** | 始终返回 Message，无状态 | 简单问答、翻译等即时响应 |
| **Task-generating** | 始终返回 Task 对象 | 长时间运行、需要状态追踪的任务 |
| **Hybrid** | 根据情况返回 Message 或 Task | 灵活场景 |

### 规范架构（三层）

```
┌─────────────────────────────────────────┐
│  Layer 3: Protocol Bindings              │  JSON-RPC / gRPC / HTTP+JSON
├─────────────────────────────────────────┤
│  Layer 2: Abstract Operations            │  SendMessage, GetTask, CancelTask ...
├─────────────────────────────────────────┤
│  Layer 1: Canonical Data Model           │  Protocol Buffers (spec/a2a.proto)
└─────────────────────────────────────────┘

spec/a2a.proto 是唯一权威规范源
```

---

## Why - 为什么

### 解决的核心问题

1. **Agent 被降格为 Tool** -- 开发者常将 agent 包装为 tool（通过 MCP 等）暴露给其他 agent，但这限制了 agent 的自主性和完整能力
2. **N x M 定制集成** -- 每对 agent 之间都需要定制通信方案，工程开销巨大
3. **互操作性缺失** -- 不同框架（LangChain、CrewAI、Semantic Kernel 等）构建的 agent 无法互通
4. **安全一致性差** -- 临时通信缺乏标准化的认证、授权、加密措施
5. **可扩展性瓶颈** -- 随着 agent 数量增长，系统难以扩展和维护

### 在协议栈中的定位

```
┌────────────────────────────────────────────┐
│         Agent ↔ Agent 协作层                │
│                                             │
│    ┌─────────────────────────────────┐     │
│    │        A2A (本协议)              │     │  发现、委托、任务管理
│    │    agent 间发现与协作             │     │  流式结果、推送通知
│    └─────────────────────────────────┘     │
│                                             │
├────────────────────────────────────────────┤
│         Agent ↔ Tool/Data 交互层            │
│                                             │
│    ┌─────────────────────────────────┐     │
│    │           MCP                    │     │  工具调用、资源访问
│    │    agent 连接工具和数据源         │     │  上下文获取
│    └─────────────────────────────────┘     │
│                                             │
├────────────────────────────────────────────┤
│         Editor ↔ Agent 交互层               │
│                                             │
│    ┌─────────────────────────────────┐     │
│    │           ACP                    │     │  会话管理、流式输出
│    │    编辑器与 agent 通信            │     │  权限控制
│    └─────────────────────────────────┘     │
│                                             │
├────────────────────────────────────────────┤
│         Agent ↔ Model 交互层                │
│                                             │
│    ┌─────────────────────────────────┐     │
│    │      LLM API (各厂商)            │     │
│    └─────────────────────────────────┘     │
└────────────────────────────────────────────┘
```

**A2A 与 MCP 是互补关系：**
- MCP = agent 如何使用工具和获取数据（agent-to-tool）
- A2A = agent 之间如何发现和协作（agent-to-agent）
- 一个 agent 可以同时使用 MCP 连接工具，使用 A2A 与其他 agent 协作

**Tool vs Agent 的本质区别：**
- **Tool (MCP)** -- 无状态原语，明确输入输出，执行特定功能
- **Agent (A2A)** -- 自主系统，能推理、规划、使用多个工具、维护长期交互状态

### 核心设计原则

| 原则 | 说明 |
|------|------|
| **Simple (简单)** | 复用已有标准：HTTP、JSON-RPC 2.0、SSE、Protocol Buffers |
| **Enterprise Ready (企业就绪)** | 对齐企业实践：OAuth 2.0、mTLS、审计、多租户、分布式追踪 |
| **Async First (异步优先)** | 为长时间运行任务设计，支持流式更新和推送通知 |
| **Modality Agnostic (模态无关)** | Part 支持任意内容类型：text、raw bytes、URL、structured data |
| **Opaque Execution (不透明执行)** | Agent 基于声明能力协作，不需暴露内部状态或工具链 |

---

## Who - 谁在做

### 发起方

A2A 由 **Google** 于 2025 年 3 月发起，后捐赠给 **Linux Foundation** 管理。

### 核心贡献者

| 贡献者 | 贡献数 | 背景 |
|--------|--------|------|
| holtskinner | 174 | 主要维护者 |
| kthota-g | 27 | Google |
| amye | 22 | Linux Foundation |
| madankumarpichamuthu | 13 | - |
| pstephengoogle | 13 | Google |
| zeroasterisk | 13 | - |
| darrelmiller | 12 | - |
| didier-durand | 12 | - |
| herczyn | 11 | - |
| DJ-os | 10 | - |

### 合作伙伴生态（166 家）

**科技巨头：**
Adobe, AWS, Atlassian, Autodesk, Cisco, Salesforce, SAP, Workday, Zoom, Box, Bloomberg

**AI 公司：**
Cohere, AI21 Labs, DataRobot, Writer, Contextual AI

**咨询/系统集成：**
Accenture, BCG, Capgemini, Cognizant, Deloitte, Wipro

**云/基础设施：**
AliCloud, Confluent, DataStax, Datadog, Chronosphere

**其他重要参与者：**
Microsoft（通过 Semantic Kernel）、UiPath（RPA 领域）

### SDK 维护

| 语言 | 仓库 | 状态 | 维护方 |
|------|------|------|--------|
| Python | a2aproject/a2a-python | **Stable** | 官方 |
| Go | a2aproject/a2a-go | **Stable** | 官方 |
| Java | a2aproject/a2a-java | **Stable** | 官方 |
| JavaScript | a2aproject/a2a-js | **Stable** | 官方 |
| C# / .NET | a2aproject/a2a-dotnet | **Stable** | 官方 |
| Rust | tomtom215/a2a-rust | v1.0.0 | 社区 |
| Swift | tolgaki/a2a-client-swift | v1.0.0 | 社区 |
| Elixir | actioncard/a2a-elixir | v0.2.0 | 社区 |

### 治理模式

- 归属 **Linux Foundation**，开放治理
- Apache 2.0 许可证
- 规范源：`spec/a2a.proto`（Protocol Buffers）
- GitHub 上公开讨论、Issue 和 PR
- IANA 注册：媒体类型 `application/a2a+json`、Well-Known URI `/.well-known/agent-card.json`

---

## How - 怎么做

### 传输层

A2A 支持三种协议绑定，共享同一套数据模型：

| 绑定 | 传输 | 流式 | 适用场景 |
|------|------|------|---------|
| **JSON-RPC 2.0** | HTTP POST | SSE | Web、通用场景 |
| **gRPC** | HTTP/2 + Protobuf | 双向流 | 高性能、微服务 |
| **HTTP+JSON/REST** | HTTP GET/POST | SSE | 简单集成、REST 风格 |

所有绑定在生产环境必须使用 **HTTPS**（TLS 1.2+），支持 mTLS。

### 核心操作

| 操作 | 方向 | 用途 |
|------|------|------|
| `SendMessage` | Client → Agent | 发送消息，同步等待响应或返回 Task |
| `SendStreamingMessage` | Client → Agent | 发送消息，通过 SSE 流式返回增量更新 |
| `GetTask` | Client → Agent | 查询任务状态和结果 |
| `ListTasks` | Client → Agent | 列出任务（v1.0 新增，支持游标分页） |
| `CancelTask` | Client → Agent | 取消正在执行的任务 |
| `SubscribeToTask` | Client → Agent | 重新订阅任务的流式更新 |
| `CreateTaskPushNotificationConfig` | Client → Agent | 注册 webhook 推送端点 |
| `GetTaskPushNotificationConfig` | Client → Agent | 查询推送配置 |
| `ListTaskPushNotificationConfigs` | Client → Agent | 列出推送配置 |
| `DeleteTaskPushNotificationConfig` | Client → Agent | 删除推送配置 |
| `GetExtendedAgentCard` | Client → Agent | 获取带认证的扩展 Agent Card |

### 典型交互流程

```
Client Agent                              Server Agent
   │                                           │
   │  1. GET /.well-known/agent-card.json      │
   │ ─────────────────────────────────────────▶ │
   │ ◀───────────────────────────────────────── │  Agent Card (能力、技能、认证)
   │                                           │
   │  2. SendMessage { message, contextId }    │
   │ ─────────────────────────────────────────▶ │
   │ ◀───────────────────────────────────────── │  Task { status: WORKING }
   │                                           │
   │  3. GetTask / SubscribeToTask             │
   │ ─────────────────────────────────────────▶ │
   │ ◀── SSE: TaskStatusUpdateEvent ────────── │  status: WORKING → INPUT_REQUIRED
   │                                           │
   │  4. SendMessage { 补充信息, taskId }       │
   │ ─────────────────────────────────────────▶ │
   │ ◀── SSE: TaskArtifactUpdateEvent ──────── │  artifact: { 生成的代码 }
   │ ◀── SSE: TaskStatusUpdateEvent ────────── │  status: COMPLETED
   │                                           │
```

### 流式模型

SSE (Server-Sent Events) 用于实时增量更新：

| 事件类型 | 说明 |
|---------|------|
| `TaskStatusUpdateEvent` | 任务状态变化（WORKING → COMPLETED 等） |
| `TaskArtifactUpdateEvent` | 产物更新（增量输出，v1.0 新增 index 字段标识位置） |

v1.0 移除了 `kind` 鉴别器，改用 JSON 成员名（member name）区分事件类型。

### 推送通知机制

适用于长时间运行任务（分钟/小时级）：

```
Client Agent                              Server Agent
   │                                           │
   │  CreateTaskPushNotificationConfig         │
   │  { taskId, webhook: "https://...", auth } │
   │ ─────────────────────────────────────────▶ │
   │                                           │
   │     (任务异步执行中...)                     │
   │                                           │
   │ ◀── POST webhook: TaskStatusUpdateEvent ── │  推送状态更新
   │ ◀── POST webhook: TaskArtifactUpdateEvent  │  推送产物更新
   │                                           │
```

支持每个任务多个推送配置，推送通知包含认证信息（AuthenticationInfo）。

### Agent Card 发现

```
1. 自动发现：GET https://agent.example.com/.well-known/agent-card.json
2. 认证后扩展：GetExtendedAgentCard（获取需要认证才能看到的额外能力）
3. 签名验证：JWS (RFC 7515) + JSON Canonicalization (RFC 8785)
4. 缓存：支持标准 HTTP 缓存机制
```

### 认证与安全

| 认证方案 | 说明 |
|---------|------|
| `APIKeySecurityScheme` | API Key |
| `HTTPAuthSecurityScheme` | HTTP Basic / Bearer |
| `OAuth2SecurityScheme` | Authorization Code + PKCE, Client Credentials, Device Code |
| `OpenIdConnectSecurityScheme` | OpenID Connect |
| `MutualTlsSecurityScheme` | 双向 TLS（v1.0 新增） |

v1.0 移除了已废弃的 OAuth Implicit 和 Password flow，新增 PKCE 支持。

### 扩展机制

- Agent 可声明自定义协议扩展
- 扩展有版本控制和需求声明（required / optional）
- 通过 `A2A-Extensions` HTTP Header 传递
- 不需要修改核心协议即可添加新能力

### v1.0 关键架构变化

| 变化 | v0.x | v1.0 |
|------|------|------|
| 规范源 | JSON Schema | **`spec/a2a.proto` (Protocol Buffers)** |
| 类型区分 | `kind` 鉴别器 | **JSON member name 多态** |
| 枚举命名 | kebab-case | **SCREAMING_SNAKE_CASE** |
| Part 类型 | TextPart / FilePart / DataPart | **统一 Part 结构** |
| Agent Card | `url` + `preferredTransport` | **`supportedInterfaces[]`** |
| 分页 | 页码分页 | **游标分页 (cursor-based)** |
| 错误处理 | 自定义 | **google.rpc.Status** |
| ID 格式 | 复合 ID | **简单 UUID** |
| 多租户 | 无 | **`tenant` 字段** |

---

## How Well - 做得怎么样

### 采纳状况（截至 2026-04-01）

**GitHub 数据：**

| 指标 | 数值 |
|------|------|
| Stars | **22,933** |
| Forks | 2,330 |
| Watchers | 230 |
| Open Issues | 217 |
| 合作伙伴 | **166 家** |

**框架集成（12 个内建支持）：**

| 框架 | 维护方 |
|------|--------|
| Agent Development Kit (ADK) | Google |
| Agno | Agno |
| AG2 | Microsoft |
| BeeAI Framework | IBM |
| CrewAI | CrewAI |
| Hector | - |
| LangGraph | LangChain |
| LiteLLM | LiteLLM |
| Microsoft Agent Framework | Microsoft (Semantic Kernel) |
| Pydantic AI | Pydantic |
| Slide (Tyler) | - |
| Strands Agents | AWS |

**SDK 生态：**

| 语言 | 类型 | 状态 |
|------|------|------|
| Python | 官方 | Stable |
| Go | 官方 | Stable |
| Java | 官方 | Stable |
| JavaScript | 官方 | Stable |
| C# / .NET | 官方 | Stable |
| Rust | 社区 | v1.0.0 |
| Swift | 社区 | v1.0.0 |
| Elixir | 社区 | v0.2.0 |

### 成熟度评估

| 维度 | 评级 | 说明 |
|------|------|------|
| 规范完整度 | **高** | Proto 定义权威规范，三种协议绑定，IANA 注册 |
| 企业采纳 | **高** | 166 家合作伙伴，覆盖科技巨头、咨询公司、AI 厂商 |
| 框架集成 | **高** | 12 个主流 agent 框架内建支持 |
| SDK 覆盖 | **高** | 5 种官方 SDK 均 Stable + 3 种社区 SDK |
| 社区活跃度 | **高** | 22,933 stars，活跃开发，频繁发版 |
| 规范稳定性 | **高** | v1.0.0 已发布，有正式版本协商和迁移指南 |
| 治理成熟度 | **高** | Linux Foundation 管理，Apache 2.0，开放治理 |

### 发展速度

| 时间 | 里程碑 |
|------|--------|
| 2025-03 | 仓库创建，Google 发起 |
| 2025-06 | v0.1.0 ~ v0.2.x 快速迭代 |
| 2025-07 | v0.3.0，mTLS、扩展卡片 |
| 2026-03 | **v1.0.0 发布**，Proto 升级为规范源，企业就绪 |

**约一年内**从 0 到 v1.0 + 166 家合作伙伴 + 12 个框架集成 + 22,000+ stars。

### 与其他协议对比

| 维度 | A2A | MCP | ACP |
|------|-----|-----|-----|
| **定位** | Agent ↔ Agent | Agent ↔ Tool/Data | Editor ↔ Agent |
| **类比** | 微服务间 RPC | DB Driver | LSP |
| **发起方** | Google → Linux Foundation | Anthropic | Zed + JetBrains |
| **传输** | HTTP / gRPC / REST | stdio / HTTP (JSON-RPC) | stdio (JSON-RPC) |
| **核心能力** | Task lifecycle, Agent Card 发现, Artifact, 推送通知 | 工具调用, 资源访问, Prompt | 会话管理, 流式输出, 权限控制, Diff |
| **HITL** | INPUT_REQUIRED 状态 | 无原生支持 | request_permission |
| **异步支持** | 原生（异步优先设计） | 无 | 无 |
| **版本** | v1.0.0 (2026-03) | 活跃迭代中 | v0.11.4 (2026-03) |
| **Stars** | 22,933 | - | 2,633 |
| **关系** | 与 MCP 互补 | 被 A2A/ACP 引用 | 与 MCP 互补 |

---

## 参考链接

### 官方资源

| 资源 | URL |
|------|-----|
| 官网 | https://a2a-protocol.org/latest/ |
| GitHub | https://github.com/a2aproject/A2A |
| 规范文档 | https://a2a-protocol.org/latest/specification/ |
| 迁移指南 | v0.3.0 → v1.0 迁移指南（规范附录） |
| 示例代码 | https://github.com/a2aproject/a2a-samples |

### SDK

| SDK | 仓库 |
|-----|------|
| Python | https://github.com/a2aproject/a2a-python |
| Go | https://github.com/a2aproject/a2a-go |
| Java | https://github.com/a2aproject/a2a-java |
| JavaScript | https://github.com/a2aproject/a2a-js |
| C# / .NET | https://github.com/a2aproject/a2a-dotnet |
| Rust (社区) | https://github.com/tomtom215/a2a-rust |
| Swift (社区) | https://github.com/tolgaki/a2a-client-swift |

### 学习资源

| 资源 | 说明 |
|------|------|
| DeepLearning.AI 短课程 | "A2A: The Agent2Agent Protocol"（免费） |
| Python Quickstart | 官方入门教程 |
| a2a-samples | 多框架示例（LlamaIndex, Autogen, AG2+MCP, PydanticAI） |
