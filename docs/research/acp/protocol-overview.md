# ACP (Agent Client Protocol) 协议调研

> 基于 acp-go-sdk v0.6.3 源码分析，协议版本 v1
> 参考：https://agentclientprotocol.com/protocol

## 1. 协议概述

ACP 是一个基于 **JSON-RPC 2.0** 的双向协议，定义了 **Client（客户端）** 与 **Agent（智能体）** 之间的通信方式。通信通过 **行分隔的 JSON (line-delimited JSON)** 在 stdio 上进行。

### 核心角色

| 角色 | 职责 |
|------|------|
| **Client** | 发起 prompt、管理 session、提供文件系统和终端能力、处理权限请求 |
| **Agent** | 处理 prompt、执行工具调用、流式输出结果、请求文件/终端/权限操作 |

### 传输层

- 基于 **JSON-RPC 2.0**，支持 request/response 和 notification 两种消息模式
- 使用 stdio（stdin/stdout）+ line-delimited JSON 作为传输
- 每条消息一行 JSON，由 `\n` 分隔
- 缓冲区上限 10MB

## 2. 协议生命周期

```
Client                          Agent
  │                               │
  │──── initialize ──────────────>│  协商版本和能力
  │<─── InitializeResponse ───────│
  │                               │
  │──── authenticate ────────────>│  (可选) 身份认证
  │<─── AuthenticateResponse ─────│
  │                               │
  │ ┌─ session/new ──────────────>│  创建新会话（二选一）
  │ │  <── NewSessionResponse ────│
  │ │                             │
  │ └─ session/load ─────────────>│  恢复已有会话（二选一，需 loadSession 能力）
  │    <── LoadSessionResponse ───│
  │                               │
  │──── session/prompt ──────────>│  发送用户消息
  │     (阻塞等待)                │
  │<─── session/update ──────────│  N 次流式通知
  │<─── session/update ──────────│
  │<─── ...                      │
  │<─── PromptResponse ──────────│  turn 结束
  │                               │
  │──── session/cancel ──────────>│  (可选) 取消当前 turn
```

## 3. 方法清单

### 3.1 Client → Agent 方法 (请求)

| 方法 | 类型 | 说明 |
|------|------|------|
| `initialize` | request | 协议握手，协商版本号和双方能力 |
| `authenticate` | request | 身份认证（如果 agent 在 initialize 响应中声明了 authMethods） |
| `session/new` | request | 创建新会话，指定工作目录和 MCP 服务器列表 |
| `session/load` | request | 恢复已有会话（需 agent 声明 loadSession 能力） |
| `session/prompt` | request | 向会话发送用户消息，阻塞直到 turn 结束 |
| `session/cancel` | notification | 取消正在进行的 prompt turn |
| `session/set_mode` | request | 切换会话模式 |
| `session/set_model` | request | (实验性) 切换模型 |

### 3.2 Agent → Client 方法 (请求 + 通知)

| 方法 | 类型 | 说明 |
|------|------|------|
| `session/update` | notification | 流式推送会话更新（消息块、工具调用、计划等） |
| `fs/read_text_file` | request | 请求读取文件内容 |
| `fs/write_text_file` | request | 请求写入文件内容 |
| `session/request_permission` | request | 请求用户授权工具调用 |
| `terminal/create` | request | 创建终端执行命令 |
| `terminal/output` | request | 获取终端当前输出 |
| `terminal/wait_for_exit` | request | 等待终端命令退出 |
| `terminal/kill` | request | 终止终端命令（不释放终端） |
| `terminal/release` | request | 释放终端资源 |

## 4. 核心类型详解

### 4.1 初始化 (Initialize)

#### InitializeRequest

```json
{
  "protocolVersion": 1,
  "clientInfo": { "name": "...", "version": "..." },
  "clientCapabilities": {
    "fs": {
      "readTextFile": true,
      "writeTextFile": true
    },
    "terminal": true
  }
}
```

#### InitializeResponse

```json
{
  "protocolVersion": 1,
  "agentInfo": { "name": "...", "version": "..." },
  "agentCapabilities": {
    "loadSession": false,
    "mcpCapabilities": { "http": false, "sse": false },
    "promptCapabilities": {
      "audio": false,
      "embeddedContext": false,
      "image": false
    }
  },
  "authMethods": []
}
```

**能力协商机制**：
- Client 声明自己支持的能力（fs 读写、terminal）
- Agent 声明自己支持的能力（loadSession、MCP 传输、prompt 内容类型）
- 双方根据对方能力决定可用的方法集

### 4.2 会话管理 (Session)

会话建立有两种**平行**的方式，不是先后关系：

```
initialize
    ├── session/new   → 创建新会话，agent 生成 sessionId 返回
    └── session/load  → 恢复已有会话，client 提供已知的 sessionId
```

Client 根据场景选择其一：
- 全新对话 → `session/new`
- 恢复之前的对话 → `session/load`（前提：agent 在 InitializeResponse 中声明 `agentCapabilities.loadSession: true`）

#### session/new — 创建新会话

**NewSessionRequest**:

```json
{
  "cwd": "/absolute/path/to/workspace",
  "mcpServers": [
    { "type": "stdio", "command": "...", "args": [...] },
    { "type": "http", "name": "...", "url": "...", "headers": {...} },
    { "type": "sse", "name": "...", "url": "..." }
  ]
}
```

**NewSessionResponse**:

```json
{
  "sessionId": "sess_abc123",
  "modes": {
    "currentModeId": "default",
    "modes": [{ "id": "default", "name": "Default" }]
  }
}
```

- Agent 生成并返回 `sessionId`
- 可选返回 `modes`（会话模式）和 `models`（实验性，模型状态）

#### session/load — 恢复已有会话

需要 agent 声明 `agentCapabilities.loadSession: true`，否则返回 MethodNotFound。

**LoadSessionRequest**:

```json
{
  "sessionId": "sess_abc123",
  "cwd": "/absolute/path/to/workspace",
  "mcpServers": [...]
}
```

**LoadSessionResponse**:

```json
{
  "modes": { ... },
  "models": { ... }
}
```

- Client 提供之前保存的 `sessionId`
- 同样需要提供 `cwd` 和 `mcpServers`（agent 恢复会话时可能需要重新建立工作环境）
- 响应中**不再返回 sessionId**（因为 client 已知）
- Agent 负责根据 sessionId 恢复对话历史和上下文

#### 通用说明

- **SessionId**: 字符串类型，唯一标识一个会话
- **MCP 服务器**: 支持 stdio（必须）、http（可选）、sse（可选）三种传输
- 无论哪种方式建立会话，后续的 prompt/cancel/set_mode 等操作都通过 sessionId 关联

### 4.3 Prompt Turn（提示轮次）

这是协议最核心的交互流程：

#### PromptRequest

```json
{
  "sessionId": "sess_abc123",
  "prompt": [
    { "type": "text", "text": "帮我重构这个函数" },
    { "type": "resource_link", "uri": "file:///path/to/file.go", "name": "file.go" }
  ]
}
```

**ContentBlock 类型**（prompt 内容块）：

| 类型 | 说明 | 是否必须支持 |
|------|------|-------------|
| `text` | 纯文本/Markdown | 必须 |
| `resource_link` | 资源引用链接 | 必须 |
| `resource` | 嵌入式资源内容 | 需要 embeddedContext 能力 |
| `image` | 图片（base64） | 需要 image 能力 |
| `audio` | 音频（base64） | 需要 audio 能力 |

#### PromptResponse

```json
{
  "stopReason": "end_turn"
}
```

**StopReason 枚举**：

| 值 | 说明 |
|----|------|
| `end_turn` | agent 正常完成 |
| `max_tokens` | 达到 token 上限 |
| `max_turn_requests` | 达到 turn 内最大请求数 |
| `refusal` | agent 拒绝执行 |
| `cancelled` | 被 client 取消 |

### 4.4 会话更新通知 (SessionUpdate)

Agent 在处理 prompt 期间，通过 `session/update` 通知流式推送进度。`SessionUpdate` 是一个 discriminated union，通过 `sessionUpdate` 字段区分变体：

#### 变体一览

| sessionUpdate 值 | 类型 | 说明 |
|------------------|------|------|
| `user_message_chunk` | SessionUpdateUserMessageChunk | 回显用户消息块 |
| `agent_message_chunk` | SessionUpdateAgentMessageChunk | agent 回复文本块 |
| `agent_thought_chunk` | SessionUpdateAgentThoughtChunk | agent 内部推理块 |
| `tool_call` | SessionUpdateToolCall | 工具调用创建 |
| `tool_call_update` | SessionToolCallUpdate | 工具调用状态/结果更新 |
| `plan` | SessionUpdatePlan | 执行计划更新 |
| `available_commands_update` | SessionAvailableCommandsUpdate | 可用命令列表变更 |
| `current_mode_update` | SessionCurrentModeUpdate | 当前模式变更 |

#### SessionUpdateAgentMessageChunk

```json
{
  "sessionUpdate": "agent_message_chunk",
  "content": { "type": "text", "text": "这段代码..." }
}
```

#### SessionUpdateToolCall（工具调用创建）

```json
{
  "sessionUpdate": "tool_call",
  "toolCallId": "tc_001",
  "title": "Reading file.go",
  "kind": "read",
  "status": "in_progress",
  "locations": [{ "path": "/path/to/file.go", "line": 42 }],
  "content": [
    { "type": "content", "content": { "type": "text", "text": "..." } },
    { "type": "diff", "path": "/path/to/file.go", "oldText": "...", "newText": "..." },
    { "type": "terminal", "terminalId": "term_001" }
  ],
  "rawInput": { ... },
  "rawOutput": { ... }
}
```

**ToolKind 枚举**：

| 值 | 说明 |
|----|------|
| `read` | 读取操作 |
| `edit` | 编辑操作 |
| `delete` | 删除操作 |
| `move` | 移动操作 |
| `search` | 搜索操作 |
| `execute` | 执行命令 |
| `think` | 思考/推理 |
| `fetch` | 获取远程资源 |
| `switch_mode` | 切换模式 |
| `other` | 其他 |

**ToolCallStatus 枚举**：

| 值 | 说明 |
|----|------|
| `pending` | 待执行 |
| `in_progress` | 执行中 |
| `completed` | 已完成 |
| `failed` | 执行失败 |

**ToolCallContent**（工具调用内容，discriminated union）：

| type 值 | 说明 |
|---------|------|
| `content` | 标准内容块（text/image/resource） |
| `diff` | 文件修改的 diff（oldText → newText） |
| `terminal` | 嵌入终端输出（通过 terminalId 关联） |

**ToolCallLocation**（工具调用关联的文件位置）：
- `path`: 文件路径
- `line`: 可选的行号
- 用于 client 的 "follow-along" 功能

#### SessionToolCallUpdate（工具调用更新）

与 ToolCall 结构类似，但所有字段都是可选的（增量更新语义）。通过 `toolCallId` 关联到之前的 ToolCall。

#### SessionUpdatePlan（执行计划）

```json
{
  "sessionUpdate": "plan",
  "entries": [
    { "content": "分析代码结构", "status": "completed", "priority": "high" },
    { "content": "重构函数", "status": "in_progress", "priority": "high" },
    { "content": "更新测试", "status": "pending", "priority": "medium" }
  ]
}
```

**PlanEntry**：
- `content`: 任务描述
- `status`: `pending` | `in_progress` | `completed`
- `priority`: `high` | `medium` | `low`

注意：每次更新发送**完整的 entries 列表**，client 整体替换。

### 4.5 权限请求 (Permission)

Agent 执行敏感操作前，通过 `session/request_permission` 请求用户授权。

#### RequestPermissionRequest

```json
{
  "sessionId": "sess_abc123",
  "toolCall": { ... },
  "options": [
    { "optionId": "allow_once", "name": "允许一次", "kind": "allow_once" },
    { "optionId": "allow_always", "name": "始终允许", "kind": "allow_always" },
    { "optionId": "reject_once", "name": "拒绝", "kind": "reject_once" }
  ]
}
```

#### RequestPermissionResponse

```json
{
  "outcome": { "outcome": "selected", "optionId": "allow_once" }
}
```

**PermissionOptionKind 枚举**：
- `allow_once`: 允许一次
- `allow_always`: 始终允许
- `reject_once`: 拒绝一次
- `reject_always`: 始终拒绝

**取消时**：如果 client 发送了 `session/cancel`，必须响应所有 pending 的 permission 请求为 `{ "outcome": "cancelled" }`。

### 4.6 文件系统操作

需要 client 在 initialize 时声明对应能力。

#### fs/read_text_file

```json
// Request
{ "sessionId": "...", "path": "/absolute/path", "line": 1, "limit": 100 }

// Response
{ "content": "文件内容..." }
```

#### fs/write_text_file

```json
// Request
{ "sessionId": "...", "path": "/absolute/path", "content": "新内容..." }

// Response
{}
```

### 4.7 终端操作

需要 client 在 initialize 时声明 `terminal: true`。

| 方法 | 说明 |
|------|------|
| `terminal/create` | 创建终端，执行命令（command + args + cwd + env），返回 terminalId |
| `terminal/output` | 获取终端当前输出（不等待退出） |
| `terminal/wait_for_exit` | 等待命令退出，返回 exitCode/signal |
| `terminal/kill` | 终止命令但保留 terminalId |
| `terminal/release` | 释放终端资源（也会终止未退出的命令） |

终端生命周期：`create` → `output`/`wait_for_exit`/`kill`（多次） → `release`

### 4.8 会话模式 (Session Modes)

Agent 可以支持多种操作模式（如 plan 模式、code 模式）。

- `session/set_mode`: client 请求切换模式
- `current_mode_update`: agent 通知当前模式变更

### 4.9 可用命令 (Available Commands)

Agent 通过 `available_commands_update` 通知 client 当前可用的命令列表。

```json
{
  "sessionUpdate": "available_commands_update",
  "availableCommands": [
    { "name": "create_plan", "description": "创建执行计划" },
    { "name": "research", "description": "研究代码库", "input": { "hint": "输入搜索关键词" } }
  ]
}
```

## 5. 扩展性

协议内置扩展机制：
- **`_meta` 字段**：所有类型都有可选的 `_meta` 字段，用于传递实现特定的元数据
- **Extension notifications**：Agent 和 Client 都可以发送自定义 notification
- **Extension methods**：Agent 和 Client 都可以发送自定义 request

## 6. 取消机制

1. Client 发送 `session/cancel` notification（包含 sessionId）
2. Agent 收到后应当：
   - 尽快停止 LLM 请求
   - 中止进行中的工具调用
   - 发送所有 pending 的 `session/update` 通知
   - 返回 `stopReason: "cancelled"` 的 PromptResponse
3. Client 收到 cancel 后，必须响应所有 pending 的 permission 请求为 `cancelled`
4. Agent 在取消后仍可能发送 tool_call_update（最终状态），client 应继续接受

## 7. 协议特点总结

| 特点 | 说明 |
|------|------|
| **双向 RPC** | 不是简单的 client→agent，agent 也主动向 client 发请求（fs、terminal、permission） |
| **能力协商** | 通过 initialize 握手，双方声明各自支持的功能 |
| **流式通知** | prompt 期间通过 notification 流式推送进度，prompt 方法本身阻塞到 turn 结束 |
| **会话化** | 所有交互基于 sessionId，支持创建和恢复会话 |
| **Discriminated Union** | 大量使用 discriminator 字段的联合类型（sessionUpdate、type、outcome 等） |
| **可扩展** | `_meta` 字段 + extension notification/method 机制 |
| **stdio 传输** | 基于 stdin/stdout 的 line-delimited JSON，agent 作为子进程运行 |
