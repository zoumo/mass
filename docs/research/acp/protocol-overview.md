# ACP (Agent Client Protocol) 协议详细调研

> 基于官方文档 https://agentclientprotocol.com/protocol 全站 14 个页面整理，协议版本 v1
> 调研日期：2026-04-07

---

## 目录

1. [协议概述](#1-协议概述)
2. [传输层（Transports）](#2-传输层transports)
3. [初始化（Initialization）](#3-初始化initialization)
4. [会话创建与恢复（Session Setup）](#4-会话创建与恢复session-setup)
5. [会话列表（Session List）](#5-会话列表session-list)
6. [提示轮次（Prompt Turn）](#6-提示轮次prompt-turn)
7. [内容块（Content）](#7-内容块content)
8. [工具调用（Tool Calls）](#8-工具调用tool-calls)
9. [文件系统（File System）](#9-文件系统file-system)
10. [终端（Terminals）](#10-终端terminals)
11. [执行计划（Agent Plan）](#11-执行计划agent-plan)
12. [会话模式（Session Modes）【已废弃】](#12-会话模式session-modes已废弃)
13. [会话配置选项（Session Config Options）](#13-会话配置选项session-config-options)
14. [斜杠命令（Slash Commands）](#14-斜杠命令slash-commands)
15. [扩展性（Extensibility）](#15-扩展性extensibility)
16. [完整协议生命周期](#16-完整协议生命周期)

---

## 1. 协议概述

ACP（Agent Client Protocol）是一个基于 **JSON-RPC 2.0** 的双向协议，定义了 **Client（客户端）** 与 **Agent（智能体）** 之间的通信方式。

### 核心角色

| 角色 | 职责 |
|------|------|
| **Client** | 启动 Agent 子进程，发起 prompt，管理会话，提供文件系统和终端能力，处理权限请求 |
| **Agent** | 处理 prompt，调用语言模型，执行工具调用，流式输出结果，主动请求文件/终端/权限操作 |

### 双向性（Bidirectionality）

ACP 不是单向的 Client → Agent 协议。Agent 在处理 prompt 期间，可以主动向 Client 发起请求：

- **`fs/read_text_file`** / **`fs/write_text_file`** — 读写 Client 环境中的文件（包括编辑器未保存的内容）
- **`session/request_permission`** — 请求用户授权工具执行
- **`terminal/create`** 等 — 在 Client 环境中执行终端命令

这种双向能力使 Agent 既能访问 Client 本地资源，也能通过 Client 将操作可见化给用户。

---

## 2. 传输层（Transports）

### stdio（标准输入输出）

这是 ACP 的主要传输机制，**Agent 和 Client 都应尽可能支持 stdio**。

规则：

- Client 将 Agent 作为子进程启动
- Agent 从 `stdin` 读取 JSON-RPC 消息，向 `stdout` 写入消息
- 消息格式：逐行 JSON（line-delimited JSON），每条消息独占一行，以 `\n` 分隔
- 消息**不得**包含嵌入的换行符
- Agent 可以向 `stderr` 写入 UTF-8 日志，Client 可捕获、转发或忽略
- Agent 的 `stdout` **只能**输出合法的 ACP 消息
- Client 写入 Agent `stdin` 的内容**只能**是合法的 ACP 消息

### Streamable HTTP

草案阶段，尚未定稿，不在当前规范范围内。

### 自定义传输

实现方可以自定义传输机制，但**必须**保留 JSON-RPC 消息格式和生命周期语义。

---

## 3. 初始化（Initialization）

在创建任何 Session 之前，Client **必须**先完成初始化握手，协商协议版本和能力。

### 3.1 初始化请求（Client → Agent）

```json
{
  "jsonrpc": "2.0",
  "id": 0,
  "method": "initialize",
  "params": {
    "protocolVersion": 1,
    "clientCapabilities": {
      "fs": {
        "readTextFile": true,
        "writeTextFile": true
      },
      "terminal": true
    },
    "clientInfo": {
      "name": "my-client",
      "title": "My Client",
      "version": "1.0.0"
    }
  }
}
```

### 3.2 初始化响应（Agent → Client）

```json
{
  "jsonrpc": "2.0",
  "id": 0,
  "result": {
    "protocolVersion": 1,
    "agentCapabilities": {
      "loadSession": true,
      "promptCapabilities": {
        "image": true,
        "audio": true,
        "embeddedContext": true
      },
      "mcpCapabilities": {
        "http": true,
        "sse": true
      },
      "sessionCapabilities": {
        "list": {}
      }
    },
    "agentInfo": {
      "name": "my-agent",
      "title": "My Agent",
      "version": "1.0.0"
    },
    "authMethods": []
  }
}
```

### 3.3 协议版本协商

- `protocolVersion` 是一个整数，仅在**破坏性变更**时递增
- Client 在请求中携带其支持的**最新版本**
- 如果 Agent 支持该版本，**必须**在响应中回送相同版本号；否则，**必须**回送其支持的最新版本
- 如果 Client 不支持 Agent 返回的版本，**应**关闭连接并告知用户
- 非破坏性新功能通过 Capabilities 机制引入，不触发版本号变更

### 3.4 Client 能力（Client Capabilities）

| 能力字段 | 类型 | 说明 |
|---------|------|------|
| `fs.readTextFile` | boolean | 支持 `fs/read_text_file` 方法 |
| `fs.writeTextFile` | boolean | 支持 `fs/write_text_file` 方法 |
| `terminal` | boolean | 支持所有 `terminal/*` 方法 |

### 3.5 Agent 能力（Agent Capabilities）

| 能力字段 | 类型 | 说明 |
|---------|------|------|
| `loadSession` | boolean（默认 false） | 支持 `session/load` 方法 |
| `promptCapabilities.image` | boolean（默认 false） | prompt 可包含图片内容块 |
| `promptCapabilities.audio` | boolean（默认 false） | prompt 可包含音频内容块 |
| `promptCapabilities.embeddedContext` | boolean（默认 false） | prompt 可包含嵌入资源内容块 |
| `mcpCapabilities.http` | boolean（默认 false） | 支持通过 HTTP 连接 MCP 服务器 |
| `mcpCapabilities.sse` | boolean（默认 false） | 支持通过 SSE 连接 MCP 服务器（MCP 规范已废弃此传输） |
| `sessionCapabilities.list` | object | 支持 `session/list` 方法 |

**重要规则**：
- 所有能力字段均**可选**。双方**必须**将对方未声明的能力视为不支持
- 新能力的引入不是破坏性变更
- 实现方可用 `_meta` 字段在能力对象中声明自定义扩展能力

### 3.6 实现信息（Implementation Info）

`clientInfo` / `agentInfo` 均包含三个字段：

| 字段 | 说明 |
|------|------|
| `name` | 程序名，可用于展示（fallback） |
| `title` | 面向用户的显示名（优先展示） |
| `version` | 版本号，可展示给用户或用于调试/监控 |

---

## 4. 会话创建与恢复（Session Setup）

Session 代表 Client 与 Agent 之间的一次独立对话，维护自己的上下文、对话历史和状态。**初始化完成后**才能创建 Session。

会话建立有两种**平行**路径（根据场景二选一）：

```
initialize 完成
    ├── session/new   → 创建全新会话（Agent 生成 sessionId）
    └── session/load  → 恢复已有会话（Client 提供已知 sessionId，需 loadSession 能力）
```

### 4.1 创建新会话 `session/new`

**请求（Client → Agent）**：

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "session/new",
  "params": {
    "cwd": "/home/user/project",
    "mcpServers": [
      {
        "name": "filesystem",
        "command": "/path/to/mcp-server",
        "args": ["--stdio"],
        "env": [
          { "name": "API_KEY", "value": "secret123" }
        ]
      }
    ]
  }
}
```

**响应（Agent → Client）**：

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "sessionId": "sess_abc123def456",
    "configOptions": [...],
    "modes": {
      "currentModeId": "ask",
      "availableModes": [...]
    }
  }
}
```

- Agent 生成并返回唯一的 `sessionId`
- 可选返回 `configOptions`（新版配置选项）和 `modes`（旧版模式，保持向后兼容）

### 4.2 恢复已有会话 `session/load`

**前提**：Agent 在 `initialize` 响应中声明 `agentCapabilities.loadSession: true`。Client 在调用前**必须**先检查此能力。

**请求（Client → Agent）**：

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "session/load",
  "params": {
    "sessionId": "sess_789xyz",
    "cwd": "/home/user/project",
    "mcpServers": [
      {
        "name": "filesystem",
        "command": "/path/to/mcp-server",
        "args": ["--mode", "filesystem"],
        "env": []
      }
    ]
  }
}
```

**核心机制**：Agent 收到 `session/load` 后，**必须**通过 `session/update` 通知将整个对话历史重放给 Client，例如：

```json
{
  "jsonrpc": "2.0",
  "method": "session/update",
  "params": {
    "sessionId": "sess_789xyz",
    "update": {
      "sessionUpdate": "user_message_chunk",
      "content": { "type": "text", "text": "法国的首都是哪里？" }
    }
  }
}
```

```json
{
  "jsonrpc": "2.0",
  "method": "session/update",
  "params": {
    "sessionId": "sess_789xyz",
    "update": {
      "sessionUpdate": "agent_message_chunk",
      "content": { "type": "text", "text": "法国的首都是巴黎。" }
    }
  }
}
```

历史重放完成后，Agent **必须**响应原始请求：

```json
{ "jsonrpc": "2.0", "id": 1, "result": null }
```

Client 此后可以像全新会话一样继续发送 prompt。

### 4.3 工作目录（cwd）

- **必须**是绝对路径
- 无论 Agent 子进程在哪个目录启动，都**必须**以此路径作为会话的文件系统上下文
- **应**作为工具对文件系统操作的边界

### 4.4 MCP 服务器配置

Client 在创建会话时，MAY 指定 Agent 应连接的 MCP 服务器列表。支持三种传输：

#### stdio 传输（所有 Agent 必须支持）

```json
{
  "name": "filesystem",
  "command": "/path/to/mcp-server",
  "args": ["--stdio"],
  "env": [{ "name": "API_KEY", "value": "secret123" }]
}
```

| 字段 | 必须 | 说明 |
|------|------|------|
| `name` | ✅ | 服务器的可读标识符 |
| `command` | ✅ | MCP 服务器可执行文件的绝对路径 |
| `args` | ✅ | 命令行参数数组 |
| `env` | ❌ | 环境变量数组，每项包含 `name` 和 `value` |

#### HTTP 传输（需要 `mcpCapabilities.http: true`）

```json
{
  "type": "http",
  "name": "api-server",
  "url": "https://api.example.com/mcp",
  "headers": [
    { "name": "Authorization", "value": "Bearer token123" },
    { "name": "Content-Type", "value": "application/json" }
  ]
}
```

#### SSE 传输（需要 `mcpCapabilities.sse: true`，MCP 规范已废弃）

```json
{
  "type": "sse",
  "name": "event-stream",
  "url": "https://events.example.com/mcp",
  "headers": [{ "name": "X-API-Key", "value": "apikey456" }]
}
```

Client 可以通过包含自己的 MCP 服务器，直接向底层语言模型提供工具。

---

## 5. 会话列表（Session List）

`session/list` 是一个可选能力，允许 Client 发现 Agent 已知的所有会话，用于展示历史记录、切换会话等 UI 功能。

### 5.1 能力检查

使用前必须确认 Agent 支持：

```json
{
  "agentCapabilities": {
    "sessionCapabilities": {
      "list": {}
    }
  }
}
```

### 5.2 列出会话

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "session/list",
  "params": {
    "cwd": "/home/user/project",
    "cursor": "eyJwYWdlIjogMn0="
  }
}
```

参数均可选。空参数返回第一页。

| 参数 | 说明 |
|------|------|
| `cwd` | 按工作目录过滤，必须是绝对路径 |
| `cursor` | 分页游标，来自上次响应的 `nextCursor` |

**响应**：

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "sessions": [
      {
        "sessionId": "sess_abc123def456",
        "cwd": "/home/user/project",
        "title": "实现 session list API",
        "updatedAt": "2025-10-29T14:22:15Z",
        "_meta": { "messageCount": 12, "hasErrors": false }
      },
      {
        "sessionId": "sess_uvw345rst678",
        "cwd": "/home/user/project",
        "updatedAt": "2025-10-27T15:30:00Z"
      }
    ],
    "nextCursor": "eyJwYWdlIjogM30="
  }
}
```

**SessionInfo 字段**：

| 字段 | 必须 | 说明 |
|------|------|------|
| `sessionId` | ✅ | 会话唯一标识符 |
| `cwd` | ✅ | 工作目录（绝对路径） |
| `title` | ❌ | 可读标题，通常由第一条 prompt 自动生成 |
| `updatedAt` | ❌ | ISO 8601 格式的最后活动时间 |
| `_meta` | ❌ | Agent 特定元数据 |

### 5.3 分页规则

- 使用**游标分页**（cursor-based pagination）
- Client **必须**将无 `nextCursor` 视为结果结束
- Client **必须**将 cursor 视为不透明 token，不得解析、修改或持久化
- Agent 应在 cursor 无效时返回错误
- Agent 应在内部控制合理的页面大小

### 5.4 实时元数据更新

Agent 可以通过 `session/update` 通知实时推送会话元数据更新（如自动生成标题）：

```json
{
  "jsonrpc": "2.0",
  "method": "session/update",
  "params": {
    "sessionId": "sess_abc123def456",
    "update": {
      "sessionUpdate": "session_info_update",
      "title": "实现用户认证",
      "_meta": { "tags": ["feature", "auth"], "priority": "high" }
    }
  }
}
```

- 所有字段可选，只需包含变更的字段
- `sessionId` 已在 params 中，`cwd` 不可变，均不在 update 中
- `title` 或 `updatedAt` 设为 `null` 表示清空该字段

### 5.5 与其他方法的关系

`session/list` 仅用于**发现**会话，不恢复或修改会话：

1. Client 调用 `session/list` 发现可用会话
2. 用户从列表中选择
3. Client 调用 `session/load` 恢复选中会话

---

## 6. 提示轮次（Prompt Turn）

Prompt Turn 是协议的**核心交互单元**，代表从用户发送消息到 Agent 完成响应的完整周期，期间可能包含多次 LLM 调用和工具调用。

### 6.1 完整生命周期（6步）

```
Client                              Agent
  │                                   │
  │ 1. session/prompt ───────────────>│
  │    (请求阻塞，等待 turn 完成)     │
  │                                   │
  │                   2. Agent 处理消息│
  │                      调用语言模型 │
  │                                   │
  │ 3. session/update (plan) <────────│ 推送执行计划（可选）
  │ 3. session/update (agent_message_chunk) <─│ 推送文本块
  │ 3. session/update (tool_call) <───│ 推送工具调用创建
  │                                   │
  │    4. 检查是否有 pending 工具调用 │
  │    ├── 无 → 结束，返回 PromptResponse
  │    └── 有 → 继续步骤 5           │
  │                                   │
  │ ←── session/request_permission ───│ 5. 请求权限（可选）
  │ ──► permission response ─────────>│
  │                                   │
  │ 5. session/update (tool_call_update in_progress) <─│
  │ 5. session/update (tool_call_update completed) <───│
  │                                   │
  │                   6. 将工具结果返回给 LLM，回到步骤 2
  │                                   │
  │ <── PromptResponse (stopReason) ──│ turn 结束
```

### 6.2 发送 Prompt

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "session/prompt",
  "params": {
    "sessionId": "sess_abc123def456",
    "prompt": [
      {
        "type": "text",
        "text": "帮我分析这段代码是否有问题？"
      },
      {
        "type": "resource",
        "resource": {
          "uri": "file:///home/user/project/main.py",
          "mimeType": "text/x-python",
          "text": "def process_data(items):\n    for item in items:\n        print(item)"
        }
      }
    ]
  }
}
```

- `session/prompt` 是**阻塞请求**，直到整个 turn 完成才返回
- `prompt` 数组支持多种 ContentBlock 类型（详见第7节）
- Client **必须**根据初始化时协商的 promptCapabilities 限制内容类型

### 6.3 Stop Reasons（结束原因）

Turn 结束时，Agent **必须**在 PromptResponse 中携带 `stopReason`：

```json
{ "jsonrpc": "2.0", "id": 2, "result": { "stopReason": "end_turn" } }
```

| stopReason | 说明 |
|-----------|------|
| `end_turn` | 语言模型正常完成，无更多工具调用 |
| `max_tokens` | 达到 token 上限 |
| `max_turn_requests` | 单次 turn 内模型请求数超限 |
| `refusal` | Agent 拒绝继续执行 |
| `cancelled` | 被 Client 取消 |

### 6.4 取消（Cancellation）

Client 在任何时候都可以通过 notification 取消进行中的 prompt turn：

```json
{
  "jsonrpc": "2.0",
  "method": "session/cancel",
  "params": { "sessionId": "sess_abc123def456" }
}
```

取消语义：

1. Client 发送 `session/cancel` 后，应**立即**将当前 turn 所有未完成的工具调用标记为 cancelled
2. Client **必须**对所有 pending 的 `session/request_permission` 请求响应 `{ "outcome": "cancelled" }`
3. Agent 收到取消通知后，**应**尽快停止 LLM 请求和工具调用
4. Agent 在终止所有操作后，**必须**以 `stopReason: "cancelled"` 响应原 prompt 请求
5. Agent 在取消过程中仍可能发送 `session/update` 通知（最终状态），Client **应**继续接受
6. **重要**：Agent **必须**捕获工具调用异常并转换为 `cancelled`（而非 error），避免 Client 向用户显示不友好的错误

---

## 7. 内容块（Content）

内容块（ContentBlock）是协议中信息流动的基本单元，出现在：

- 用户 prompt（`session/prompt` 的 `prompt` 数组）
- Agent 消息块（`agent_message_chunk` 的 `content`）
- 工具调用内容（`tool_call` / `tool_call_update` 的 `content` 数组中的 `content` 类型项）

ACP 的 ContentBlock 结构**与 MCP（Model Context Protocol）相同**，使 Agent 可以直接转发 MCP 工具输出，无需转换。

### 7.1 文本内容（text）

所有 Agent **必须**支持。

```json
{
  "type": "text",
  "text": "请分析以下代码...",
  "annotations": { ... }
}
```

| 字段 | 必须 | 说明 |
|------|------|------|
| `text` | ✅ | 文本内容 |
| `annotations` | ❌ | 内容展示/使用的元数据 |

### 7.2 图片内容（image）

需要 `promptCapabilities.image: true`（在 prompt 中使用时）。

```json
{
  "type": "image",
  "mimeType": "image/png",
  "data": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB...",
  "uri": "https://example.com/image.png",
  "annotations": { ... }
}
```

| 字段 | 必须 | 说明 |
|------|------|------|
| `data` | ✅ | Base64 编码的图片数据 |
| `mimeType` | ✅ | MIME 类型（如 `image/png`、`image/jpeg`） |
| `uri` | ❌ | 可选的图片源 URI |
| `annotations` | ❌ | 展示元数据 |

### 7.3 音频内容（audio）

需要 `promptCapabilities.audio: true`（在 prompt 中使用时）。

```json
{
  "type": "audio",
  "mimeType": "audio/wav",
  "data": "UklGRiQAAABXQVZFZm10IBAAAAABAAEAQB8AAAB...",
  "annotations": { ... }
}
```

| 字段 | 必须 | 说明 |
|------|------|------|
| `data` | ✅ | Base64 编码的音频数据 |
| `mimeType` | ✅ | MIME 类型（如 `audio/wav`、`audio/mp3`） |
| `annotations` | ❌ | 展示元数据 |

### 7.4 嵌入资源（resource）

需要 `promptCapabilities.embeddedContext: true`（在 prompt 中使用时）。

**推荐**：这是在 prompt 中包含文件等上下文的首选方式，Client 可通过 @-mention 等 UI 交互触发。

```json
{
  "type": "resource",
  "resource": {
    "uri": "file:///home/user/script.py",
    "mimeType": "text/x-python",
    "text": "def hello():\n    print('Hello, world!')"
  },
  "annotations": { ... }
}
```

`resource` 字段是一个联合类型：

**文本资源（Text Resource）**：

| 字段 | 必须 | 说明 |
|------|------|------|
| `uri` | ✅ | 资源标识符 URI |
| `text` | ✅ | 文本内容 |
| `mimeType` | ❌ | MIME 类型 |

**二进制资源（Blob Resource）**：

| 字段 | 必须 | 说明 |
|------|------|------|
| `uri` | ✅ | 资源标识符 URI |
| `blob` | ✅ | Base64 编码的二进制数据 |
| `mimeType` | ❌ | MIME 类型 |

嵌入资源的优势：将内容直接内联在请求中，Agent 无需有独立的访问权限也能获取上下文。

### 7.5 资源链接（resource_link）

**所有 Agent 必须**在 prompt 中支持此类型（基线能力）。

```json
{
  "type": "resource_link",
  "uri": "file:///home/user/document.pdf",
  "name": "document.pdf",
  "mimeType": "application/pdf",
  "title": "项目文档",
  "description": "包含完整的项目设计说明",
  "size": 1024000,
  "annotations": { ... }
}
```

| 字段 | 必须 | 说明 |
|------|------|------|
| `uri` | ✅ | 资源 URI |
| `name` | ✅ | 可读名称 |
| `mimeType` | ❌ | MIME 类型 |
| `title` | ❌ | 展示标题 |
| `description` | ❌ | 资源内容描述 |
| `size` | ❌ | 资源大小（字节） |
| `annotations` | ❌ | 展示元数据 |

---

## 8. 工具调用（Tool Calls）

工具调用是语言模型请求 Agent 执行外部操作时的通知机制。Agent 通过 `session/update` 通知向 Client 报告实时进度。

### 8.1 创建工具调用

当 LLM 请求工具调用时，Agent **应**立即通知 Client：

```json
{
  "jsonrpc": "2.0",
  "method": "session/update",
  "params": {
    "sessionId": "sess_abc123def456",
    "update": {
      "sessionUpdate": "tool_call",
      "toolCallId": "call_001",
      "title": "读取配置文件",
      "kind": "read",
      "status": "pending"
    }
  }
}
```

**工具调用字段**：

| 字段 | 必须 | 说明 |
|------|------|------|
| `toolCallId` | ✅ | 会话内唯一标识符 |
| `title` | ✅ | 工具正在执行的可读描述 |
| `kind` | ❌ | 工具分类（见 ToolKind） |
| `status` | ❌ | 执行状态（默认 pending） |
| `content` | ❌ | 工具产生的内容 |
| `locations` | ❌ | 关联的文件位置 |
| `rawInput` | ❌ | 发送给工具的原始参数 |
| `rawOutput` | ❌ | 工具返回的原始结果 |

### 8.2 ToolKind（工具类型）

| 值 | 说明 | 用途 |
|----|------|------|
| `read` | 读取文件或数据 | Client 选择合适的图标 |
| `edit` | 修改文件或内容 | |
| `delete` | 删除文件或数据 | |
| `move` | 移动或重命名文件 | |
| `search` | 搜索信息 | |
| `execute` | 运行命令或代码 | |
| `think` | 内部推理或规划 | |
| `fetch` | 获取外部数据 | |
| `switch_mode` | 切换会话模式 | |
| `other` | 其他类型（默认） | |

### 8.3 ToolCallStatus（工具调用状态）

| 值 | 说明 |
|----|------|
| `pending` | 待执行（输入流式传输中或等待审批） |
| `in_progress` | 执行中 |
| `completed` | 成功完成 |
| `failed` | 执行失败 |

### 8.4 更新工具调用

工具执行过程中，通过 `tool_call_update` 报告进度：

```json
{
  "jsonrpc": "2.0",
  "method": "session/update",
  "params": {
    "sessionId": "sess_abc123def456",
    "update": {
      "sessionUpdate": "tool_call_update",
      "toolCallId": "call_001",
      "status": "in_progress",
      "content": [
        {
          "type": "content",
          "content": { "type": "text", "text": "找到 3 个配置文件..." }
        }
      ]
    }
  }
}
```

`tool_call_update` 中除 `toolCallId` 外所有字段均**可选**，只需包含变更的字段（增量更新语义）。

### 8.5 工具内容类型（ToolCallContent）

工具调用可产生三种类型的内容：

#### 普通内容（content）

标准内容块：

```json
{
  "type": "content",
  "content": { "type": "text", "text": "分析完成，发现 3 个问题。" }
}
```

#### 文件差异（diff）

文件修改以 diff 形式展示：

```json
{
  "type": "diff",
  "path": "/home/user/project/src/config.json",
  "oldText": "{\n  \"debug\": false\n}",
  "newText": "{\n  \"debug\": true\n}"
}
```

| 字段 | 必须 | 说明 |
|------|------|------|
| `path` | ✅ | 被修改文件的绝对路径 |
| `oldText` | ❌ | 原始内容（新建文件时为 null） |
| `newText` | ✅ | 修改后的新内容 |

#### 终端（terminal）

嵌入实时终端输出：

```json
{
  "type": "terminal",
  "terminalId": "term_xyz789"
}
```

当终端嵌入到工具调用时，Client 实时展示输出，并在终端 release 后**继续**展示已有输出。

### 8.6 工具位置（ToolCallLocation）

用于 Client 的 "follow-along" 功能，跟踪 Agent 正在访问的文件：

```json
{
  "path": "/home/user/project/src/main.py",
  "line": 42
}
```

### 8.7 请求权限（Permission Request）

Agent 在执行敏感工具前，可请求用户授权：

```json
{
  "jsonrpc": "2.0",
  "id": 5,
  "method": "session/request_permission",
  "params": {
    "sessionId": "sess_abc123def456",
    "toolCall": {
      "toolCallId": "call_001"
    },
    "options": [
      { "optionId": "allow-once", "name": "允许一次", "kind": "allow_once" },
      { "optionId": "allow-always", "name": "始终允许", "kind": "allow_always" },
      { "optionId": "reject-once", "name": "拒绝", "kind": "reject_once" }
    ]
  }
}
```

Client 响应用户决定：

```json
{
  "jsonrpc": "2.0",
  "id": 5,
  "result": {
    "outcome": { "outcome": "selected", "optionId": "allow-once" }
  }
}
```

**PermissionOption 字段**：

| 字段 | 必须 | 说明 |
|------|------|------|
| `optionId` | ✅ | 选项唯一标识符 |
| `name` | ✅ | 向用户展示的标签 |
| `kind` | ✅ | 权限选项类型（提示 Client 选择图标/UI） |

**PermissionOptionKind**：

| 值 | 说明 |
|----|------|
| `allow_once` | 仅允许本次操作 |
| `allow_always` | 允许并记住选择 |
| `reject_once` | 仅拒绝本次操作 |
| `reject_always` | 拒绝并记住选择 |

**取消时**：如果 prompt turn 被取消，Client **必须**对所有 pending 的权限请求响应：

```json
{ "outcome": { "outcome": "cancelled" } }
```

Client 可以根据用户设置自动允许或拒绝权限请求，无需弹出交互。

---

## 9. 文件系统（File System）

文件系统方法允许 Agent 读写 Client 环境中的文件，包括获取编辑器中**未保存的最新内容**，以及让 Client 追踪 Agent 的文件修改。

### 9.1 能力检查

Agent 调用前**必须**检查 Client 能力：

```json
{
  "clientCapabilities": {
    "fs": {
      "readTextFile": true,
      "writeTextFile": true
    }
  }
}
```

### 9.2 读取文件 `fs/read_text_file`

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "fs/read_text_file",
  "params": {
    "sessionId": "sess_abc123def456",
    "path": "/home/user/project/src/main.py",
    "line": 10,
    "limit": 50
  }
}
```

| 参数 | 必须 | 说明 |
|------|------|------|
| `sessionId` | ✅ | 会话 ID |
| `path` | ✅ | 文件绝对路径 |
| `line` | ❌ | 从第几行开始读取（1-based） |
| `limit` | ❌ | 最多读取的行数 |

**响应**：

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "result": {
    "content": "def hello_world():\n    print('Hello, world!')\n"
  }
}
```

### 9.3 写入文件 `fs/write_text_file`

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "method": "fs/write_text_file",
  "params": {
    "sessionId": "sess_abc123def456",
    "path": "/home/user/project/config.json",
    "content": "{\n  \"debug\": true,\n  \"version\": \"1.0.0\"\n}"
  }
}
```

| 参数 | 必须 | 说明 |
|------|------|------|
| `sessionId` | ✅ | 会话 ID |
| `path` | ✅ | 文件绝对路径（文件不存在时 Client **必须**创建） |
| `content` | ✅ | 写入的文本内容 |

**响应**：成功时返回 `null`。

---

## 10. 终端（Terminals）

终端方法允许 Agent 在 Client 环境中执行 shell 命令，并获取实时输出流和进程控制。

### 10.1 能力检查

```json
{ "clientCapabilities": { "terminal": true } }
```

### 10.2 创建终端 `terminal/create`

```json
{
  "jsonrpc": "2.0",
  "id": 5,
  "method": "terminal/create",
  "params": {
    "sessionId": "sess_abc123def456",
    "command": "npm",
    "args": ["test", "--coverage"],
    "env": [{ "name": "NODE_ENV", "value": "test" }],
    "cwd": "/home/user/project",
    "outputByteLimit": 1048576
  }
}
```

| 参数 | 必须 | 说明 |
|------|------|------|
| `sessionId` | ✅ | 会话 ID |
| `command` | ✅ | 要执行的命令 |
| `args` | ❌ | 命令行参数数组 |
| `env` | ❌ | 环境变量数组 |
| `cwd` | ❌ | 命令的工作目录（绝对路径） |
| `outputByteLimit` | ❌ | 最大保留输出字节数；超过时从头部截断，保证截断在字符边界 |

Client **立即**返回（不等待命令完成）：

```json
{ "jsonrpc": "2.0", "id": 5, "result": { "terminalId": "term_xyz789" } }
```

### 10.3 嵌入工具调用（实时输出展示）

推荐在创建工具调用时同时嵌入终端，使用户可以看到实时输出：

```json
{
  "sessionUpdate": "tool_call",
  "toolCallId": "call_002",
  "title": "运行测试",
  "kind": "execute",
  "status": "in_progress",
  "content": [{ "type": "terminal", "terminalId": "term_xyz789" }]
}
```

### 10.4 获取输出 `terminal/output`

不等待命令完成，立即返回当前输出：

```json
{
  "jsonrpc": "2.0",
  "id": 6,
  "method": "terminal/output",
  "params": {
    "sessionId": "sess_abc123def456",
    "terminalId": "term_xyz789"
  }
}
```

**响应**：

```json
{
  "jsonrpc": "2.0",
  "id": 6,
  "result": {
    "output": "Running tests...\n✓ All tests passed (42 total)\n",
    "truncated": false,
    "exitStatus": {
      "exitCode": 0,
      "signal": null
    }
  }
}
```

| 字段 | 必须 | 说明 |
|------|------|------|
| `output` | ✅ | 当前已捕获的输出 |
| `truncated` | ✅ | 是否因超过 outputByteLimit 而截断 |
| `exitStatus` | ❌ | 仅在命令已退出时存在，包含 `exitCode` 和 `signal` |

### 10.5 等待退出 `terminal/wait_for_exit`

阻塞直到命令退出：

```json
{
  "jsonrpc": "2.0",
  "id": 7,
  "method": "terminal/wait_for_exit",
  "params": {
    "sessionId": "sess_abc123def456",
    "terminalId": "term_xyz789"
  }
}
```

**响应**：

```json
{
  "jsonrpc": "2.0",
  "id": 7,
  "result": { "exitCode": 0, "signal": null }
}
```

### 10.6 终止命令 `terminal/kill`

终止命令但**保留** terminalId（仍可读取输出和等待退出状态）：

```json
{
  "method": "terminal/kill",
  "params": { "sessionId": "...", "terminalId": "term_xyz789" }
}
```

杀死后仍需调用 `terminal/release` 释放资源。

### 10.7 释放终端 `terminal/release`

释放所有资源（如命令仍在运行则同时终止）：

```json
{
  "method": "terminal/release",
  "params": { "sessionId": "...", "terminalId": "term_xyz789" }
}
```

释放后 terminalId 对所有 `terminal/*` 方法均**无效**。若终端已嵌入工具调用，Client **应**在 release 后继续展示已有输出。

### 10.8 完整生命周期

```
terminal/create
    └── [并行执行]
        ├── terminal/output     (多次，非阻塞，轮询输出)
        ├── terminal/wait_for_exit  (阻塞等待)
        └── terminal/kill       (可选，超时时终止)
    └── terminal/release        (必须调用，释放资源)
```

### 10.9 超时模式（Timeout Pattern）

Agent 实现命令超时的推荐方式：

1. 调用 `terminal/create` 创建终端
2. 启动定时器
3. 并发等待：定时器到期 OR `terminal/wait_for_exit` 返回
4. 若定时器先到期：
   - 调用 `terminal/kill` 终止命令
   - 调用 `terminal/output` 获取最终输出
   - 将输出返回给模型
5. 调用 `terminal/release` 释放资源

---

## 11. 执行计划（Agent Plan）

Agent 可以通过 `session/update` 通知将执行计划（Plan）实时推送给 Client，提供任务进度的可视化。

### 11.1 创建计划

```json
{
  "jsonrpc": "2.0",
  "method": "session/update",
  "params": {
    "sessionId": "sess_abc123def456",
    "update": {
      "sessionUpdate": "plan",
      "entries": [
        { "content": "分析现有代码库结构", "priority": "high", "status": "pending" },
        { "content": "识别需要重构的组件", "priority": "high", "status": "pending" },
        { "content": "为关键函数创建单元测试", "priority": "medium", "status": "pending" }
      ]
    }
  }
}
```

### 11.2 PlanEntry 字段

| 字段 | 必须 | 说明 |
|------|------|------|
| `content` | ✅ | 任务的可读描述 |
| `priority` | ✅ | 优先级：`high` / `medium` / `low` |
| `status` | ✅ | 当前状态：`pending` / `in_progress` / `completed` |

### 11.3 更新计划

Agent 在推进计划时，**应**发送包含所有条目的完整更新（非差量）：

```json
{
  "sessionUpdate": "plan",
  "entries": [
    { "content": "分析现有代码库结构", "priority": "high", "status": "completed" },
    { "content": "识别需要重构的组件", "priority": "high", "status": "in_progress" },
    { "content": "为关键函数创建单元测试", "priority": "medium", "status": "pending" }
  ]
}
```

**关键规则**：
- Agent **必须**在每次更新中发送**完整的** entries 列表
- Client **必须**用新列表**完整替换**当前计划（不是增量 merge）
- Agent 可以在执行中**动态增删**条目（发现新需求或完成任务时）

---

## 12. 会话模式（Session Modes）【已废弃】

> ⚠️ **已废弃**：Session Modes 将在协议未来版本中移除，推荐使用 [Session Config Options](#13-会话配置选项session-config-options)。
> 在过渡期间，支持 configOptions 的 Agent **应**同时提供 modes 以保持向后兼容。

### 12.1 初始状态

创建/恢复会话时，Agent 可返回可用模式列表：

```json
{
  "sessionId": "sess_abc123def456",
  "modes": {
    "currentModeId": "ask",
    "availableModes": [
      { "id": "ask", "name": "Ask", "description": "执行任何操作前请求权限" },
      { "id": "architect", "name": "Architect", "description": "设计和规划系统，不执行实现" },
      { "id": "code", "name": "Code", "description": "具有完整工具访问权限的代码编写" }
    ]
  }
}
```

### 12.2 Client 切换模式

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "session/set_mode",
  "params": { "sessionId": "sess_abc123def456", "modeId": "code" }
}
```

### 12.3 Agent 切换模式

Agent 自身可以切换模式并通知 Client：

```json
{
  "sessionUpdate": "current_mode_update",
  "modeId": "code"
}
```

常见场景：从 architect 模式完成规划后，LLM 调用 "exit mode" 工具切换到 code 模式，通常附带权限请求：

```json
{
  "method": "session/request_permission",
  "params": {
    "toolCall": {
      "toolCallId": "call_switch_mode_001",
      "title": "准备好开始实现",
      "kind": "switch_mode",
      "status": "pending",
      "content": [{ "type": "text", "text": "## 实现计划..." }]
    },
    "options": [
      { "optionId": "code", "name": "是，自动接受所有操作", "kind": "allow_always" },
      { "optionId": "ask", "name": "是，手动接受操作", "kind": "allow_once" },
      { "optionId": "reject", "name": "否，留在 architect 模式", "kind": "reject_once" }
    ]
  }
}
```

---

## 13. 会话配置选项（Session Config Options）

Session Config Options 是**当前推荐的**会话级配置机制，取代旧版 Session Modes。提供更灵活的多维度配置，如模式、模型、推理级别等。

### 13.1 初始状态

创建/恢复会话时，Agent 可返回配置选项列表：

```json
{
  "sessionId": "sess_abc123def456",
  "configOptions": [
    {
      "id": "mode",
      "name": "会话模式",
      "description": "控制 Agent 如何请求权限",
      "category": "mode",
      "type": "select",
      "currentValue": "ask",
      "options": [
        { "value": "ask", "name": "Ask", "description": "执行任何操作前请求权限" },
        { "value": "code", "name": "Code", "description": "具有完整工具访问权限" }
      ]
    },
    {
      "id": "model",
      "name": "模型",
      "category": "model",
      "type": "select",
      "currentValue": "model-1",
      "options": [
        { "value": "model-1", "name": "Model 1", "description": "最快的模型" },
        { "value": "model-2", "name": "Model 2", "description": "最强大的模型" }
      ]
    },
    {
      "id": "thought_level",
      "name": "推理深度",
      "category": "thought_level",
      "type": "select",
      "currentValue": "normal",
      "options": [
        { "value": "minimal", "name": "最小" },
        { "value": "normal", "name": "标准" },
        { "value": "deep", "name": "深度" }
      ]
    }
  ]
}
```

### 13.2 ConfigOption 字段

| 字段 | 必须 | 说明 |
|------|------|------|
| `id` | ✅ | 配置项唯一标识符 |
| `name` | ✅ | 可读标签 |
| `description` | ❌ | 配置项详细说明 |
| `category` | ❌ | 语义分类（帮助 Client 统一 UX） |
| `type` | ✅ | 当前仅支持 `select` |
| `currentValue` | ✅ | 当前选中的值 |
| `options` | ✅ | 可选值列表 |

### 13.3 内置分类（Category）

| category | 说明 |
|---------|------|
| `mode` | 会话模式选择器 |
| `model` | 模型选择器 |
| `thought_level` | 推理/思考深度选择器 |

以 `_` 开头的 category 保留给自定义用途（如 `_my_custom_category`）。不以 `_` 开头的 category 名称由 ACP 规范保留。

**重要**：category 仅用于 UX 提示，Client **必须**容忍未知 category，且**不得**依赖 category 来判断配置项的正确性。

### 13.4 排序规则

- `configOptions` 数组的顺序有意义，Agent **应**将更重要的选项放在前面
- Client **应**按 Agent 提供的顺序展示
- 多个选项共享同一 category 时，Client **应**用数组顺序决定优先级

### 13.5 默认值与降级

- Agent **必须**为每个配置项提供默认值，即使 Client 不支持配置选项
- Client 遇到未知类型的配置项，**应**忽略该项（Agent 继续使用默认值）

### 13.6 Client 设置配置

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "session/set_config_option",
  "params": {
    "sessionId": "sess_abc123def456",
    "configId": "mode",
    "value": "code"
  }
}
```

Agent **必须**在响应中返回**完整的** `configOptions` 列表（反映所有关联变更，如修改模型可能影响可用的推理级别选项）：

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "configOptions": [ /* 完整列表 */ ]
  }
}
```

### 13.7 Agent 更新配置

Agent 可主动通过 `config_option_update` 通知更新配置（同样包含完整状态）：

```json
{
  "sessionUpdate": "config_option_update",
  "configOptions": [ /* 完整列表 */ ]
}
```

常见触发场景：
- 规划阶段完成后自动切换到 code 模式
- 达到限速/错误时降级到备用模型
- 根据执行中发现的上下文调整可用选项

### 13.8 与 Session Modes 的共存

过渡期间，同时支持两者的 Agent **应**：
- 在 session 响应中同时返回 `configOptions`（含 `category: "mode"`）和 `modes`
- 支持 configOptions 的 Client 应使用 `configOptions` 并**忽略** `modes`
- 不支持 configOptions 的 Client 应使用 `modes`
- Agent 应保持两者同步

---

## 14. 斜杠命令（Slash Commands）

Agent 可以通过 `available_commands_update` 通知向 Client 公告可用的斜杠命令，让用户通过输入 `/命令名` 快速触发特定功能。

### 14.1 公告命令

会话创建后，Agent MAY 推送可用命令列表：

```json
{
  "jsonrpc": "2.0",
  "method": "session/update",
  "params": {
    "sessionId": "sess_abc123def456",
    "update": {
      "sessionUpdate": "available_commands_update",
      "availableCommands": [
        {
          "name": "web",
          "description": "在网络上搜索信息",
          "input": { "hint": "搜索查询词" }
        },
        {
          "name": "test",
          "description": "运行当前项目的测试"
        },
        {
          "name": "plan",
          "description": "创建详细的实现计划",
          "input": { "hint": "需要规划的内容描述" }
        }
      ]
    }
  }
}
```

**AvailableCommand 字段**：

| 字段 | 必须 | 说明 |
|------|------|------|
| `name` | ✅ | 命令名称（如 `web`、`test`、`plan`） |
| `description` | ✅ | 命令功能的可读描述 |
| `input` | ❌ | 输入规范（目前仅支持 `hint` 字段） |

### 14.2 动态更新

Agent 可以在会话期间随时发送新的 `available_commands_update` 通知，动态增删或修改命令（根据上下文调整可用能力）。

### 14.3 执行命令

命令通过普通 prompt 请求执行，Client 将命令文本作为 prompt 内容发送：

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "session/prompt",
  "params": {
    "sessionId": "sess_abc123def456",
    "prompt": [{ "type": "text", "text": "/web agent client protocol" }]
  }
}
```

Agent 识别 `/` 前缀并按命令处理。命令可以与其他内容块（图片、音频等）一起出现在同一 prompt 数组中。

---

## 15. 扩展性（Extensibility）

ACP 提供内置扩展机制，允许实现方添加自定义功能，同时保持与核心协议的兼容性。

### 15.1 `_meta` 字段

协议中**所有类型**都包含可选的 `_meta` 字段，类型为 `{ [key: string]: unknown }`，用于附加自定义元数据。适用范围：

- 请求（requests）
- 响应（responses）
- 通知（notifications）
- 嵌套类型（ContentBlock、ToolCall、PlanEntry、能力对象等）

示例：在 `session/prompt` 中附加追踪上下文

```json
{
  "method": "session/prompt",
  "params": {
    "sessionId": "sess_abc123def456",
    "prompt": [{ "type": "text", "text": "Hello, world!" }],
    "_meta": {
      "traceparent": "00-80e1afed08e019fc1110464cfa66635c-7a085853722dc6d2-01",
      "zed.dev/debugMode": true
    }
  }
}
```

**W3C 追踪上下文保留字段**（根级别 `_meta` 中保留，保证与 MCP 和 OpenTelemetry 的互操作）：

- `traceparent`
- `tracestate`
- `baggage`

**规则**：实现方**不得**在规范定义类型的根级别添加任何自定义字段（所有名称均保留给未来版本）。

### 15.2 Extension Methods（扩展方法）

协议保留所有以 `_` 开头的方法名用于自定义扩展，不会与未来的协议版本冲突。

#### 自定义请求（Custom Requests）

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "_zed.dev/workspace/buffers",
  "params": { "language": "rust" }
}
```

响应（成功）：

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "buffers": [
      { "id": 0, "path": "/home/user/project/src/main.rs" },
      { "id": 1, "path": "/home/user/project/src/editor.rs" }
    ]
  }
}
```

响应（不支持）：

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "error": { "code": -32601, "message": "Method not found" }
}
```

#### 自定义通知（Custom Notifications）

```json
{
  "jsonrpc": "2.0",
  "method": "_zed.dev/file_opened",
  "params": { "path": "/home/user/project/src/editor.rs" }
}
```

接收方**应**忽略未识别的自定义通知。

### 15.3 公告自定义能力

实现方**应**在能力对象的 `_meta` 字段中公告自定义扩展，便于 peer 在初始化时检查并适配：

```json
{
  "agentCapabilities": {
    "loadSession": true,
    "_meta": {
      "zed.dev": {
        "workspace": true,
        "fileNotifications": true
      }
    }
  }
}
```

---

## 16. 完整协议生命周期

```
Client                                    Agent
  │                                         │
  │ ──── initialize ───────────────────────>│
  │ <─── InitializeResponse ────────────────│  版本 + 能力协商
  │                                         │
  │ ──── [authenticate] ───────────────────>│  (可选) 身份认证
  │ <─── AuthenticateResponse ──────────────│
  │                                         │
  │ ┌── session/new ──────────────────────>│  创建新会话
  │ │   <── { sessionId, configOptions } ──│
  │ │                                       │
  │ └── session/load ───────────────────── >│  OR 恢复会话（需 loadSession 能力）
  │     <── session/update (历史重放) ──×N─│
  │     <── null ───────────────────────────│
  │                                         │
  │ ┌── session/list ────────────────────>  │  (可选) 发现会话列表
  │ └── <─ { sessions, nextCursor } ───────│
  │                                         │
  │ ──── session/prompt ───────────────────>│  发送用户消息（阻塞）
  │                                         │
  │         [Agent 处理，可能多轮]          │
  │                                         │
  │ <─── session/update (plan) ─────────────│  推送执行计划（可选）
  │ <─── session/update (agent_msg_chunk) ──│  推送文本块
  │ <─── session/update (tool_call) ────────│  推送工具调用
  │                                         │
  │         [需要文件访问时]                │
  │ <─── fs/read_text_file ────────────────│  Agent 请求读文件
  │ ──── { content } ──────────────────────>│
  │                                         │
  │         [需要终端时]                    │
  │ <─── terminal/create ──────────────────│  Agent 请求创建终端
  │ ──── { terminalId } ────────────────── >│
  │ <─── terminal/wait_for_exit ────────────│  Agent 等待命令退出
  │ ──── { exitCode } ─────────────────────>│
  │ <─── terminal/release ─────────────────│  Agent 释放终端
  │ ──── null ─────────────────────────────>│
  │                                         │
  │         [需要用户授权时]                │
  │ <─── session/request_permission ────────│  Agent 请求权限
  │ ──── { outcome } ──────────────────────>│
  │                                         │
  │ <─── session/update (tool_call_update)  │  工具进度/结果更新
  │ <─── session/update (config_option_update)│ 配置变更（可选）
  │                                         │
  │ <─── PromptResponse (stopReason) ───────│  turn 结束
  │                                         │
  │ ──── [session/cancel] ─────────────────>│  (可选) 取消进行中的 turn
  │                                         │
  │     [循环：继续发送 session/prompt]     │
```

---

## 附录：SessionUpdate 变体完整列表

`session/update` 通知的 `update` 字段是一个 discriminated union，通过 `sessionUpdate` 字段区分：

| sessionUpdate | 说明 |
|---------------|------|
| `user_message_chunk` | 回显用户消息块（`session/load` 历史重放时使用） |
| `agent_message_chunk` | Agent 回复文本块（流式） |
| `agent_thought_chunk` | Agent 内部推理块（流式） |
| `tool_call` | 工具调用创建 |
| `tool_call_update` | 工具调用状态/内容更新（增量） |
| `plan` | 执行计划更新（全量替换） |
| `available_commands_update` | 可用斜杠命令列表变更 |
| `current_mode_update` | 当前模式变更（旧版） |
| `config_option_update` | 配置选项变更（新版，全量） |
| `session_info_update` | 会话元数据更新（title、updatedAt 等） |

---

## 附录：方法汇总表

### Client → Agent

| 方法 | 类型 | 说明 |
|------|------|------|
| `initialize` | request | 协议握手，协商版本和能力 |
| `authenticate` | request | 身份认证（可选） |
| `session/new` | request | 创建新会话 |
| `session/load` | request | 恢复已有会话（需 loadSession 能力） |
| `session/list` | request | 列出已知会话（需 sessionCapabilities.list） |
| `session/prompt` | request | 发送用户消息（阻塞到 turn 结束） |
| `session/cancel` | notification | 取消进行中的 turn |
| `session/set_mode` | request | 切换会话模式（旧版） |
| `session/set_config_option` | request | 设置配置选项（新版） |
| `session/set_model` | request | 切换模型（实验性） |

### Agent → Client

| 方法 | 类型 | 说明 |
|------|------|------|
| `session/update` | notification | 流式推送会话更新（所有变体） |
| `session/request_permission` | request | 请求用户授权工具调用 |
| `fs/read_text_file` | request | 读取 Client 文件系统中的文件 |
| `fs/write_text_file` | request | 写入 Client 文件系统中的文件 |
| `terminal/create` | request | 创建终端执行命令 |
| `terminal/output` | request | 获取终端当前输出（非阻塞） |
| `terminal/wait_for_exit` | request | 等待命令退出（阻塞） |
| `terminal/kill` | request | 终止命令（保留 terminalId） |
| `terminal/release` | request | 释放终端资源 |
