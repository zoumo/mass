# ACP (Agent Client Protocol) - 3W2H 深度调研

> 调研日期: 2026-03-31
> 协议版本: v0.11.4 (2026-03-28 发布)
> 官网: https://agentclientprotocol.com
> GitHub: https://github.com/agentclientprotocol/agent-client-protocol
> 许可证: Apache License 2.0

---

## 目录

- [What - 是什么](#what---是什么)
- [Why - 为什么](#why---为什么)
- [Who - 谁在做](#who---谁在做)
- [How - 怎么做](#how---怎么做)
- [How Well - 做得怎么样](#how-well---做得怎么样)
- [对 MASS 的启示](#对-oai-的启示)
- [参考链接](#参考链接)

---

## What - 是什么

### 一句话定义

ACP 是一个开放协议，标准化**代码编辑器/IDE（客户端）**与 **AI 编程 Agent（服务端）**之间的通信方式。它之于 AI 编程 Agent，就像 LSP（Language Server Protocol）之于语言服务器。

### 核心思想

ACP 解决的是 **N x M** 问题：

```
没有 ACP:                         有了 ACP:
  Editor A ─┬─ Agent 1              Editor A ─┐
  Editor A ─┼─ Agent 2              Editor B ─┤
  Editor B ─┼─ Agent 1              Editor C ─┼── ACP ──┬─ Agent 1
  Editor B ─┼─ Agent 2              Neovim  ─┤         ├─ Agent 2
  Editor C ─┼─ Agent 1              Emacs   ─┘         ├─ Agent 3
  Editor C ─┴─ Agent 2                                  └─ Agent N
  (每个组合都要单独适配)             (实现一次协议即可互通)
```

### 关键特征

| 特征 | 描述 |
|---|---|
| **类 LSP 架构** | JSON-RPC 2.0 双向通信，客户端和 Agent 都可以主动发请求 |
| **MCP 兼容** | 复用 MCP 的 ContentBlock 类型，支持 MCP Server 透传 |
| **UX 优先** | 专门为编辑器中与 AI Agent 交互的 UX 场景设计 |
| **stdio 传输** | 客户端将 Agent 作为子进程启动，通过 stdin/stdout 通信 |
| **流式输出** | 通过 JSON-RPC notification 流式推送 Agent 的思考、输出、工具调用 |
| **权限模型** | Agent 执行工具前可向客户端请求用户许可 |

### 核心抽象

```
┌─────────────────────────────────────────────────────────┐
│                    Connection                            │
│  (一个 stdio 进程 = 一个 JSON-RPC 连接)                   │
│                                                          │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐     │
│  │  Session A   │  │  Session B   │  │  Session C   │     │
│  │  (会话/对话)  │  │  (会话/对话)  │  │  (会话/对话)  │     │
│  │             │  │             │  │             │     │
│  │  Turn 1     │  │  Turn 1     │  │  Turn 1     │     │
│  │  Turn 2     │  │  Turn 2     │  │             │     │
│  │  Turn 3     │  │             │  │             │     │
│  └─────────────┘  └─────────────┘  └─────────────┘     │
└─────────────────────────────────────────────────────────┘
```

- **Connection** -- 一个 Agent 子进程 = 一个 JSON-RPC 连接
- **Session** -- 一次对话/线程，一个连接可以有多个并发 Session
- **Turn** -- 一次 prompt-response 交互轮次，包含流式更新和工具调用

---

## Why - 为什么

### 解决的核心问题

1. **集成开销巨大** -- 每个 Agent-Editor 组合都需要单独适配，重复工作量大
2. **兼容性有限** -- Agent 只能在少数编辑器中使用，开发者被锁定在特定工具链
3. **MCP 不覆盖此层** -- MCP 标准化 LLM 如何连接工具和数据源，但不管 Editor 如何与 Agent 交互
4. **UX 一致性差** -- 不同编辑器中同一 Agent 的交互体验完全不同

### 在协议栈中的定位

```
┌────────────────────────────────────────────┐
│            用户 (开发者)                      │
└──────────────────┬─────────────────────────┘
                   │
         ┌─────────▼─────────┐
         │   ACP (本协议)     │  Editor <-> Agent 交互层
         │   类比: LSP        │  UX、会话、权限、流式输出
         └─────────┬─────────┘
                   │
         ┌─────────▼─────────┐
         │       MCP          │  Agent <-> Tool/Data 交互层
         │   类比: DB Driver   │  工具调用、上下文获取、资源访问
         └─────────┬─────────┘
                   │
         ┌─────────▼─────────┐
         │  LLM API (各厂商)   │  Agent <-> 模型 交互层
         └───────────────────┘
```

**ACP 与 MCP 是互补关系，不是竞争关系：**
- MCP = Agent 内部如何获取工具和上下文
- ACP = Editor 外部如何与 Agent 对话
- ACP 消息中可以携带 MCP Server 配置，Agent 直接连接 MCP Server

### 与 LSP 的类比

| 维度 | LSP | ACP |
|---|---|---|
| 解决的问题 | Editor x Language Server = N x M | Editor x AI Agent = N x M |
| 通信协议 | JSON-RPC 2.0 | JSON-RPC 2.0 |
| 传输层 | stdio / TCP | stdio / (draft: Streamable HTTP) |
| 服务端 | 语言服务器 (gopls, rust-analyzer...) | AI Agent (Claude Code, Copilot...) |
| 客户端 | 编辑器 (VS Code, Neovim...) | 编辑器 (Zed, JetBrains...) |

---

## Who - 谁在做

### 发起方

ACP 由 **Zed Industries**（Zed 编辑器）和 **JetBrains** 联合发起，2025 年 6 月创建仓库。

### 核心维护者

| 姓名 | 组织 | 角色 |
|---|---|---|
| Agus Zubiaga | Zed | 核心维护者，238 commits |
| Ben Brandt | Zed | 核心维护者，225 commits |
| Sergey Ignatov | JetBrains | 核心维护者 |
| Anna Zhdan | JetBrains | 核心维护者 |
| Niko Matsakis | - | 核心维护者 |
| Conrad Irwin | Zed (CEO) | 贡献者，28 commits |
| Max Brunsfeld | Zed (tree-sitter 作者) | 贡献者 |
| Richard Feldman | Zed (Roc 语言作者) | 贡献者 |
| Danilo Leal | Zed (设计) | 贡献者，32 commits |

### SDK 维护者

| SDK | 维护者 | 组织 |
|---|---|---|
| Rust | Agus Zubiaga, Ben Brandt | Zed |
| TypeScript | Ben Brandt | Zed |
| Python | Marcelo Trylesinski | 社区 |
| Kotlin | Anna Zhdan, Sergey Ignatov | JetBrains |
| Java (Spring) | Mark Pollack | Spring/VMware |

### 治理模式

- 开源项目，Apache 2.0 许可证
- GitHub 上公开讨论和 RFC
- Zed + JetBrains 双驱动，社区广泛参与
- GitHub Stars: 2,633（截至 2026-03-31）

---

## How - 怎么做

### 传输层

**主要传输方式: stdio**

```
┌──────────┐    stdin (JSON-RPC)    ┌──────────┐
│  Editor   │ ──────────────────▶  │  Agent    │
│ (Client)  │                       │ (Server)  │
│           │ ◀──────────────────  │           │
└──────────┘    stdout (JSON-RPC)   └──────────┘
```

- 客户端将 Agent 作为子进程 fork/exec
- 消息以换行符分隔的 JSON-RPC 2.0 格式
- UTF-8 编码
- Streamable HTTP 传输方式正在草案中（用于远程 Agent）

### 协议生命周期

```
Phase 1: 初始化          Phase 2: 认证(可选)       Phase 3: 会话建立
  Client ──initialize──▶     Client ──authenticate──▶   Client ──session/new──▶
  Client ◀──response────     Client ◀──response──────   Client ◀──sessionId────
  (版本协商、能力交换)        (OAuth、API Key 等)         (工作目录、MCP Server)

Phase 4: Prompt 交互 (循环)
  Client ──session/prompt──────────▶ Agent
  Client ◀──session/update──────── Agent  (流式: 思考、文本、工具调用)
  Client ◀──session/request_permission── Agent  (请求工具执行许可)
  Client ──permission_response─────▶ Agent
  Client ◀──session/update──────── Agent  (继续流式输出)
  Client ◀──prompt_response──────── Agent  (本轮结束)
```

### 消息类型详解

#### 客户端 → Agent (请求)

| 方法 | 用途 |
|---|---|
| `initialize` | 版本和能力协商 |
| `authenticate` | 认证（可选） |
| `session/new` | 创建新会话，传入 cwd 和 mcpServers |
| `session/load` | 恢复已有会话（需 Agent 支持 `loadSession` 能力） |
| `session/list` | 列出已有会话 |
| `session/prompt` | 发送用户消息 |
| `session/set_config_option` | 设置会话配置（模型、模式、推理级别等） |
| `session/close` | 关闭会话 (unstable) |

#### 客户端 → Agent (通知)

| 方法 | 用途 |
|---|---|
| `session/cancel` | 取消正在进行的 prompt turn |

#### Agent → 客户端 (请求)

| 方法 | 用途 |
|---|---|
| `session/request_permission` | 请求用户许可执行工具 |
| `fs/read_text_file` | 读取客户端文件系统的文件 |
| `fs/write_text_file` | 写入客户端文件系统的文件 |
| `terminal/create` | 执行 shell 命令 |
| `terminal/output` | 获取终端输出 |
| `terminal/wait_for_exit` | 等待命令完成 |
| `terminal/kill` | 终止命令 |
| `terminal/release` | 释放终端资源 |

#### Agent → 客户端 (通知)

| 方法 | 用途 |
|---|---|
| `session/update` | 流式推送各类更新 |

### session/update 事件类型

`session/update` 是 ACP 中最核心的通知，承载所有流式输出：

| 事件类型 | 用途 |
|---|---|
| `agent_message_chunk` | Agent 的回复文本片段（流式） |
| `thought_message_chunk` | Agent 的思考/推理过程 |
| `user_message_chunk` | 用户消息回显 |
| `tool_call` | 工具调用开始 |
| `tool_call_update` | 工具调用状态更新（进行中、完成、失败） |
| `plan` | Agent 的执行计划 |
| `plan_update` | 计划步骤状态更新 |
| `mode_update` | 模式变更 |
| `available_commands_update` | 可用斜杠命令更新 |
| `session_info_update` | 会话元数据更新 |

### 内容类型

复用 MCP 的 ContentBlock 结构：

| 类型 | 描述 | 所需能力 |
|---|---|---|
| `TextContent` | 纯文本（Markdown） | 必须支持 |
| `ImageContent` | Base64 编码图片 | `image` 能力 |
| `AudioContent` | Base64 编码音频 | `audio` 能力 |
| `ResourceContent` | 带 URI 和 MIME 类型的资源 | `embeddedContext` 能力 |

### 工具调用内容类型

| 类型 | 描述 |
|---|---|
| `content` | 标准 ContentBlock |
| `diff` | 文件修改（oldText/newText） |
| `terminal` | 实时终端输出 |

### 权限模型

Agent 执行敏感操作前，通过 `session/request_permission` 请求用户许可：

```json
{
  "method": "session/request_permission",
  "params": {
    "sessionId": "sess-123",
    "toolCall": {
      "kind": "execute",
      "title": "Run npm install",
      "content": [{ "type": "terminal", ... }]
    }
  }
}
```

用户可选择：
- `allow_once` -- 本次允许
- `allow_always` -- 始终允许（同类操作）
- `reject_once` -- 本次拒绝
- `reject_always` -- 始终拒绝

### 工具类别 (Tool Kinds)

```
read | edit | delete | move | search | execute | think | fetch | other
```

### 能力协商

初始化时双方交换能力：

**客户端能力：**
- `fs.readTextFile` / `fs.writeTextFile` -- 文件系统访问
- `terminal` -- 终端/命令执行
- `elicitation` -- 用户交互（unstable）

**Agent 能力：**
- `loadSession` -- 可恢复会话
- `promptCapabilities.image` / `audio` / `embeddedContext` -- 多模态输入
- `mcpCapabilities.http` / `sse` -- 支持的 MCP 传输方式
- `sessionCapabilities.list` -- 可列出会话

### MCP 集成方式

```
┌──────────┐         ACP          ┌──────────┐         MCP          ┌──────────┐
│  Editor   │ ── session/new ──▶ │  Agent    │ ── connect ────────▶ │ MCP Server│
│           │    (mcpServers:[])  │           │                      │           │
└──────────┘                      └──────────┘                      └──────────┘
```

- Editor 在 `session/new` 中传递 MCP Server 配置
- Agent 直接连接 MCP Server
- Editor 也可以将自身工具通过 MCP proxy 暴露给 Agent

### 可扩展性

| 机制 | 描述 |
|---|---|
| `_meta` 字段 | 所有类型都包含 `_meta` 字段，用于自定义元数据 |
| 扩展方法 | 以 `_` 开头的方法名保留给自定义扩展（如 `_zed.dev/workspace/buffers`） |
| W3C Trace Context | `_meta` 中保留 `traceparent`、`tracestate`、`baggage` 键 |

---

## How Well - 做得怎么样

### 采纳状况（截至 2026-03-31）

**Agent 生态（30+ 实现）：**

| Agent | 备注 |
|---|---|
| Claude Code | 通过 adapter |
| GitHub Copilot | Public preview |
| Codex CLI | 通过 adapter |
| Gemini CLI | 原生支持 |
| Cursor | 原生支持 |
| Augment Code | 原生支持 |
| Junie (JetBrains) | 原生支持 |
| Goose | 原生支持 |
| OpenHands | 原生支持 |
| Cline | 原生支持 |
| Qwen Code | 原生支持 |
| Kiro CLI | 原生支持 |
| Mistral Vibe | 原生支持 |
| Docker cagent | 原生支持 |
| Factory Droid | 原生支持 |
| ... | 30+ 总计 |

**编辑器/客户端生态：**

| 客户端 | 支持方式 |
|---|---|
| Zed | 原生支持 |
| JetBrains IDEs | 原生支持 |
| VS Code | 扩展 |
| Neovim | 多个插件 (CodeCompanion, agentic.nvim, avante.nvim) |
| Emacs | 多个插件 (agent-shell.el, acp.el) |
| Obsidian | 插件 |
| Unity | UnityAgentClient |
| Chrome | 扩展 |

**非编辑器客户端：**

| 项目 | Stars | 描述 |
|---|---|---|
| acpx | 1,856 | Headless CLI 客户端 |
| ACP UI | 139 | 跨平台桌面客户端 |
| Harnss | 159 | 开源桌面客户端 |
| Agmente | - | iOS 客户端 |
| Happy | - | iOS/Android/Web |
| Mobvibe | - | iOS/Android/Web |

**消息平台集成：** Discord、Slack、Telegram、WeChat bridges

**框架集成：** LangChain/LangGraph、LlamaIndex、fast-agent、Koog (JetBrains)

### SDK 生态

| 语言 | 类型 | Stars | 发布平台 |
|---|---|---|---|
| Rust | 官方 | 98 | crates.io: `agent-client-protocol` |
| TypeScript | 官方 | 133 | npm: `@agentclientprotocol/sdk` |
| Python | 官方 | 206 | PyPI |
| Kotlin | 官方 | 69 | Maven |
| Java (Spring) | 官方 | 29 | Maven |
| Go | 社区 | 127 | coder/acp-go-sdk |
| Emacs Lisp | 社区 | 141 | xenodium/acp.el |
| React Hooks | 社区 | 48 | marimo-team/use-acp |

### 成熟度评估

| 维度 | 评级 | 说明 |
|---|---|---|
| 规范完整度 | **高** | JSON Schema 定义 100+ 类型，OpenAPI 规范完整 |
| Agent 采纳 | **高** | 30+ Agent 实现，包括 Claude Code、Copilot、Gemini |
| Editor 采纳 | **高** | 两大发起方（Zed、JetBrains）原生支持，VS Code 扩展 |
| SDK 覆盖 | **高** | 5 种官方 SDK + 3 种社区 SDK |
| 社区活跃度 | **高** | 2,633 stars，活跃贡献，频繁发版 |
| 规范稳定性 | **中** | 仍在 v0.x，部分功能标记 unstable/draft |
| 企业采纳 | **中-高** | Zed、JetBrains 原生，GitHub Copilot public preview |

### 发展速度

- 2025-06: 仓库创建
- 2025-12: v0.9.0 ~ v0.10.x，快速迭代
- 2026-03: v0.11.4，协议趋于稳定
- **不到一年内**从 0 到 30+ Agent、8+ Editor 采纳

### 与其他协议对比

| 维度 | ACP | MCP | A2A (Google) |
|---|---|---|---|
| **定位** | Editor ↔ Agent | Agent ↔ Tool/Data | Agent ↔ Agent |
| **类比** | LSP | DB Driver | 微服务间 RPC |
| **传输** | stdio (JSON-RPC) | stdio / SSE (JSON-RPC) | HTTP + SSE |
| **发起方** | Zed + JetBrains | Anthropic | Google |
| **关系** | 复用 MCP 内容类型 | 被 ACP 引用 | 独立层 |
| **核心能力** | 会话、流式、权限、Diff、Plan | 工具、资源、Prompt | Task lifecycle、Artifact |
| **HITL** | request_permission | 无原生支持 | 有 |

---

## 对 MASS 的启示

### MASS 中的 ACP 使用现状

MASS v2 架构中，agent-run 作为 ACP Client 与 Agent 进程通过 stdio JSON-RPC 通信。这与 Agent Client Protocol 的传输方式一致（stdio + JSON-RPC 2.0），但消息格式和语义有差异。

### 对照分析

| 维度 | MASS 当前设计 | Agent Client Protocol |
|---|---|---|
| 传输 | stdio JSON-RPC 2.0 | stdio JSON-RPC 2.0 (一致) |
| 会话管理 | `session/new`, `session/load` | `session/new`, `session/load` (高度相似) |
| Prompt 交互 | `session/prompt` → `session/update` | `session/prompt` → `session/update` (高度相似) |
| 文件操作 | `fs/read_text_file`, `fs/write_text_file` | `fs/read_text_file`, `fs/write_text_file` (一致) |
| 终端操作 | `terminal/execute` | `terminal/create`, `terminal/output`, `terminal/kill` 等 (更细粒度) |
| 取消 | `session/cancel` | `session/cancel` (一致) |
| 权限 | agent-run 策略 (approve-all/reads/deny-all) | `session/request_permission` (更细粒度，per-operation) |
| 能力协商 | 无明确机制 | `initialize` 阶段双向能力交换 |
| MCP 集成 | `session/new` 传 mcpServers | `session/new` 传 mcpServers (一致) |
| 流式事件 | `session/update` | `session/update` 含多种事件类型 (message_chunk, tool_call, plan...) |

### 关键启示

1. **MASS 的 Agent 通信层与 ACP 高度对齐** -- 传输方式、核心消息、会话模型基本一致，说明 MASS 的设计方向符合行业趋势

2. **ACP 的能力协商机制值得借鉴** -- `initialize` 阶段的双向能力交换，比 MASS 当前的静态配置更灵活

3. **ACP 的细粒度权限模型** -- `request_permission` 允许逐操作请求许可，比 MASS 的三级策略（approve-all/reads/deny-all）更精细

4. **ACP 的流式事件分类** -- `session/update` 中区分 message_chunk、thought_chunk、tool_call、plan 等类型，对 UI 展示更友好

5. **ACP 的终端操作更细粒度** -- 分离了 create/output/wait/kill/release，比 MASS 的单一 `terminal/execute` 更适合长时间运行的命令

6. **ACP 的 MCP 集成模式** -- Agent 直接连接 MCP Server 的模式与 MASS 一致，验证了这个设计方向

7. **ACP 30+ Agent 采纳** -- 说明这套协议已经被广泛验证，MASS 如果对齐 ACP 规范可以直接复用整个 Agent 生态

---

## 参考链接

### 官方资源

| 资源 | URL |
|---|---|
| 官网 | https://agentclientprotocol.com |
| GitHub | https://github.com/agentclientprotocol/agent-client-protocol |
| 规范文档 | https://agentclientprotocol.com/specification |
| 快速入门 | https://agentclientprotocol.com/get-started/quickstart |
| 核心概念 | https://agentclientprotocol.com/get-started/concepts |

### SDK

| SDK | URL |
|---|---|
| Rust | crates.io: `agent-client-protocol` |
| TypeScript | npm: `@agentclientprotocol/sdk` |
| Python | PyPI: `agent-client-protocol` |
| Kotlin | Maven |
| Java (Spring) | Maven |
| Go (社区) | github.com/coder/acp-go-sdk |

### 规范子页面

| 页面 | URL |
|---|---|
| Protocol | https://agentclientprotocol.com/specification/protocol |
| Messages | https://agentclientprotocol.com/specification/messages |
| Events | https://agentclientprotocol.com/specification/events |
| Transport | https://agentclientprotocol.com/specification/transport |
| Errors | https://agentclientprotocol.com/specification/errors |
| Extensions | https://agentclientprotocol.com/specification/extensions |
