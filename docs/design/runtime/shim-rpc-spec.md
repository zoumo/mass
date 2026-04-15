# Shim RPC Specification

## 概述

本文档定义 agent-shim 对上暴露的 **shim RPC 目标契约**。
它是 agentd 与单个 runtime session 之间的本地控制面：

- `session/*` 负责 turn 级控制与订阅；
- `runtime/*` 负责进程真相、历史回放与停止；
- ACP 保持为 agent-shim 内部实现细节，不直接暴露给 agentd。

**协议**：JSON-RPC 2.0 over Unix socket

**相关 authority**：

- [runtime-spec.md](runtime-spec.md) 负责 runtime 状态模型、state dir 布局、socket 路径约定；
- 本文档负责 shim RPC 方法名、notification 名、回放 / 重连语义；
- [agent-shim.md](agent-shim.md) 只描述组件职责与边界，不重复定义规范字段。

## 设计原则

1. **clean-break surface** —— request/response surface 统一使用 `session/*` + `runtime/*`；
   notification surface 统一使用 `shim/event`，不再把 legacy PascalCase 方法或 `$/event`
   notification 当作当前契约。
2. **ACP 不穿透** —— agent-shim 是唯一理解 ACP 的组件；上层只看到翻译后的
   runtime/session 语义。
3. **重连可证实** —— 断线恢复依赖 socket 发现、`runtime/status` 状态检查、
   `runtime/history` 历史补齐、`session/subscribe` 恢复 live 流。
4. **一个序列空间** —— live notification 与历史回放共享单调递增 `seq`，
   使断线补齐、去重、诊断有可比对依据。
5. **payload not content** —— ShimEvent envelope 使用 `payload` 字段携带事件数据，
   避免与事件内部的 `content` 字段（如 ContentBlock）产生命名混淆。

## Socket 发现与恢复语义

socket 路径和 state dir 布局由 [runtime-spec.md](runtime-spec.md) 定义。
In agentd-managed deployments, bundle/state/socket are co-located:

```text
<bundleRoot>/<workspace>-<name>/
├── config.json
├── workspace -> <workspace-dir>
├── state.json
├── agent-shim.sock
└── events.jsonl
```

恢复调用方（通常是 agentd）在重启后应执行以下顺序：

1. 使用已持久化的 AgentRun 元数据（`ShimSocketPath`）找到 socket；
2. 连接每个 shim socket；
3. 调用 `runtime/status` 获取当前 runtime truth 与 `lastSeq`；
4. 调用 `runtime/history`，从调用方最后成功处理的 `seq + 1` 开始补齐；
5. 调用 `session/subscribe`，传入 `afterSeq =` 已成功处理的最后一个序列号，恢复 live 流。

**规范要求**：

- `runtime/history` 返回的记录必须与 live notification 共享同一个 `seq` 空间；
- `session/subscribe(afterSeq)` 建立后，shim 只能投递 `seq > afterSeq` 的 live `shim/event` notification；
- 调用方负责按 `seq` 去重；shim 不负责跨连接去重状态。

## 方法

### `session/prompt`

向已 bootstrap 完成的 session 发送一个工作 turn。
这是 shim 边界上的 work-entry path，对应上层 ARI `agentrun/prompt` 的 runtime 侧落点。

**Request**:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "session/prompt",
  "params": {
    "prompt": "Refactor the auth module to use JWT tokens."
  }
}
```

**Response**:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "stopReason": "end_turn"
  }
}
```

**说明**：

- 调用期间，shim 会持续产出 `shim/event` notification（包含 session events 与 state_change events）；
- 调用完成表示这一轮工作已结束，不表示 runtime 被销毁；
- 若 session 尚未处于可接收 prompt 的状态，必须返回错误。

### `session/cancel`

取消当前活跃 turn。agent-shim 将此翻译为内部 ACP `session/cancel` 或等价控制。

**Request**:

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "session/cancel",
  "params": null
}
```

**Response**:

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "result": null
}
```

### `session/subscribe`

建立 live notification 订阅。
订阅成功后，shim 会在同一连接上异步发送 `shim/event` notification。

**Request**:

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "session/subscribe",
  "params": {
    "afterSeq": 41
  }
}
```

`afterSeq` 为 OPTIONAL：

- 省略时，表示”从当前 head 之后的 live 流开始”；
- 指定时，表示”只接收 `seq > afterSeq` 的 live notification”。

此外也支持 `fromSeq`（OPTIONAL）参数：当指定时，shim 在建立订阅前先返回
`seq >= fromSeq` 的历史 entries（原子 backfill），然后继续投递 live notification。
这允许调用方在一次调用中完成断线补齐和恢复 live 流。

**Response**:

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "result": {
    "nextSeq": 42
  }
}
```

`nextSeq` 表示订阅建立后下一条可能投递的序列号下界。
它用于辅助调用方校验 recovery 流程是否从正确边界恢复。

### `runtime/status`

返回当前 runtime truth 与回放所需的最小恢复元数据。

**Request**:

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "method": "runtime/status",
  "params": null
}
```

**Response**:

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "result": {
    "state": {
      "massVersion": "0.1.0",
      "id": "session-abc123",
      "status": "idle",
      "pid": 12345,
      "bundle": "/var/lib/agentd/bundles/session-abc123",
      "annotations": {
        "org.openagents.task": "PROJ-1234"
      },
      "updatedAt": "2026-04-07T10:00:00.123456789Z",
      "session": {
        "agentInfo": { "name": "claude-code", "version": "1.0.0" },
        "capabilities": { "promptCapabilities": { "image": true } }
      },
      "eventCounts": {
        "agent_message": 42,
        "tool_call": 7,
        "tool_result": 7,
        "turn_start": 3,
        "turn_end": 2
      }
    },
    "recovery": {
      "lastSeq": 41
    }
  }
}
```

`state` 字段的结构与 [runtime-spec.md](runtime-spec.md) 的 State 定义一致。

> **注意**：`runtime/status` 响应中的 `eventCounts` 来自 Translator 内存中的实时累计值，
> 并非直接读取 state.json 文件。因此在两次 state 持久化之间，`eventCounts` 可能比
> state.json 中的值更新。这保证了调用方始终获取最准确的事件统计。
`recovery.lastSeq` 是当前 `events.jsonl` / notification 流中最后一个已分配的序列号。

### `runtime/history`

读取历史 notification 记录，用于断线补齐或诊断。

**Request**:

```json
{
  "jsonrpc": "2.0",
  "id": 5,
  "method": "runtime/history",
  "params": {
    "fromSeq": 39
  }
}
```

`fromSeq` 为 OPTIONAL，默认 `0`。
返回结果中的每一项都使用和 live notification 相同的 envelope。

**Response**:

```json
{
  "jsonrpc": "2.0",
  "id": 5,
  "result": {
    "entries": [
      {
        "runId": "codex",
        "sessionId": "acp-xxx",
        "seq": 39,
        "time": "2026-04-07T10:00:00Z",
        "category": "runtime",
        "type": "state_change",
        "payload": {
          "previousStatus": "idle",
          "status": "running",
          "pid": 12345
        }
      },
      {
        "runId": "codex",
        "sessionId": "acp-xxx",
        "seq": 40,
        "time": "2026-04-07T10:00:01Z",
        "category": "session",
        "type": "agent_message",
        "turnId": "turn-001",
        "payload": {
          "status": "streaming",
          "content": { "type": "text", "text": "I found the auth handler." }
        }
      }
    ]
  }
}
```

### `runtime/stop`

优雅停止 runtime，并关闭 shim RPC server。
这是 runtime 生命周期控制，不是当前 turn 的取消。

**Request**:

```json
{
  "jsonrpc": "2.0",
  "id": 6,
  "method": "runtime/stop",
  "params": null
}
```

**Response**:

```json
{
  "jsonrpc": "2.0",
  "id": 6,
  "result": null
}
```

**规范要求**：

- shim 必须先回复成功，再执行关闭流程；
- 关闭流程可以是 `SIGTERM → grace period → SIGKILL` 或等价策略；
- 当 runtime 已经退出时，调用方仍应得到可诊断的状态或幂等结果，而不是无上下文断连。

## Turn-Aware Event Ordering

### 目标

单调递增 `seq` 足以用于全局回放与去重，但不足以在 chat/replay 中可靠地重建单次 turn 的内部顺序。
Turn-Aware Event Ordering 为 `shim/event` notification envelope 增加可选字段，
使 agentd 可以在 turn 级精确排序，而不必把完整的时间戳或索引外部化到调用方。

### Turn-aware 字段

`shim/event` 顶层携带以下可选字段（仅 session category、active turn 内的事件携带）：

| 字段 | 类型 | 说明 |
|------|------|------|
| `turnId` | `string` | 当前 prompt turn 的标识符，由 shim 在 `turn_start` 事件时分配，在 `turn_end` 事件后清除 |

### 排序规则

1. **`seq` 继续作为全局唯一序列号**，用于跨 turn 排序、断线补齐、去重。  
   任何 `shim/event` notification 都携带 `seq`。

2. **`turnId` 在 `turn_start` 事件时由 shim 分配**，并在该 turn 内所有 session category 的 `shim/event` notification 中携带相同值（包括 metadata 事件）；
   `turn_end` 事件后，后续 notification 不再携带 `turnId`（字段缺失或为 `null`）。

3. **runtime category 事件（`state_change`）不参与 turn 排序**：该事件只携带全局 `seq`，不携带 `turnId`。

4. **Turn 字段反映时间上下文**：active turn 内到达的 session category 事件（包括 session_info、usage 等 metadata 事件）都携带 `turnId`；turn 外到达的事件不携带。

### Replay 语义

- **turn 内排序**：chat/replay 在同一 `turnId` 下按 `seq` 排序事件；
- **跨 turn 排序**：在 `turnId` 缺失或跨 turn 边界时，回退到全局 `seq` 排序；
- **去重**：全局 `seq` 是唯一 dedup key。

### `shim/event` 示例

content 事件（turn 内，携带 turn 字段和 block status）：

```json
{
  "jsonrpc": "2.0",
  "method": "shim/event",
  "params": {
    "runId": "codex",
    "sessionId": "acp-xxx",
    "seq": 42,
    "time": "2026-04-07T10:00:02Z",
    "category": "session",
    "type": "agent_message",
    "turnId": "turn-001",
    "payload": {
      "status": "streaming",
      "content": { "type": "text", "text": "Refactoring the auth module..." }
    }
  }
}
```

`turn_start` 事件：

```json
{
  "jsonrpc": "2.0",
  "method": "shim/event",
  "params": {
    "runId": "codex",
    "sessionId": "acp-xxx",
    "seq": 40,
    "time": "2026-04-07T10:00:00Z",
    "category": "session",
    "type": "turn_start",
    "turnId": "turn-001",
    "payload": {}
  }
}
```

`state_change` 事件（runtime category，不携带 turn 字段）：

```json
{
  "jsonrpc": "2.0",
  "method": "shim/event",
  "params": {
    "runId": "codex",
    "sessionId": "acp-xxx",
    "seq": 39,
    "time": "2026-04-07T10:00:00Z",
    "category": "runtime",
    "type": "state_change",
    "payload": {
      "previousStatus": "idle",
      "status": "running",
      "pid": 12345,
      "reason": "prompt-started"
    }
  }
}
```

仅元数据变更的 `state_change`（`previousStatus == status`，`sessionChanged` 列出被更新的 session 字段）：

```json
{
  "jsonrpc": "2.0",
  "method": "shim/event",
  "params": {
    "runId": "codex",
    "sessionId": "acp-xxx",
    "seq": 40,
    "time": "2026-04-07T10:00:01Z",
    "category": "runtime",
    "type": "state_change",
    "payload": {
      "previousStatus": "idle",
      "status": "idle",
      "sessionChanged": ["configOptions"]
    }
  }
}
```

> `sessionChanged` 仅在元数据变更（`previousStatus == status`）时出现，列出本次更新涉及的
> session 子字段。可能的取值：`agentInfo`、`capabilities`、`availableCommands`、
> `configOptions`、`sessionInfo`、`currentMode`。

## Notifications

### `shim/event`

这是 shim 对外暴露的统一 notification。所有来自 ACP 的文本流、工具调用、
文件 / 命令副作用等（`session` category），以及 runtime 进程状态变化
（`runtime` category），统一通过此 notification 暴露。

**Category 划分**：
- `session`：所有 ACP SessionUpdate 翻译产出的事件（text, thinking, tool_call, tool_result,
  file_write, file_read, command, plan, user_message, turn_start, turn_end, error,
  session_info, config_option, available_commands, current_mode, usage）
- `runtime`：仅 `state_change`（runtime 进程自身产生的生命周期事件）

```json
{
  "jsonrpc": "2.0",
  "method": "shim/event",
  "params": {
    "runId": "codex",
    "sessionId": "acp-xxx",
    "seq": 40,
    "time": "2026-04-07T10:00:01Z",
    "category": "session",
    "type": "agent_message",
    "turnId": "turn-001",
    "payload": {
      "status": "streaming",
      "content": { "type": "text", "text": "I found the auth handler." }
    }
  }
}
```

## Typed Event 类型

`session/update.params.event.type` 的目标集合如下：

| type | payload 字段 | 说明 |
|------|-------------|------|
| `agent_message` | `status: string`, `content: ContentBlock` | agent 输出片段，携带 content block streaming status |
| `agent_thinking` | `status: string`, `content: ContentBlock` | agent 思考 / 推理片段，携带 content block streaming status |
| `user_message` | `status: string`, `content: ContentBlock` | 用户输入回显，携带 content block streaming status |
| `tool_call` | `id`, `kind`, `title`, `status`, `content[]`, `locations[]`, `rawInput`, `rawOutput`, `_meta` | tool 调用开始（完整 ACP 字段） |
| `tool_result` | `id`, `status`, `kind`, `title`, `content[]`, `locations[]`, `rawInput`, `rawOutput`, `_meta` | tool 调用完成 / 失败（完整 ACP 字段） |
| `file_write` | `path: string`, `allowed: bool` | 文件写入及权限结果 |
| `file_read` | `path: string`, `allowed: bool` | 文件读取及权限结果 |
| `command` | `command: string`, `allowed: bool` | shell / terminal 操作及权限结果 |
| `plan` | `entries: PlanEntry[]`, `_meta?` | 执行计划更新 |
| `turn_start` | _(empty)_ | 一个 turn 开始 |
| `turn_end` | `stopReason: string` | 一个 turn 结束 |
| `error` | `message: string` | ACP 翻译失败、runtime 异常、畸形事件 |
| `available_commands` | `commands: AvailableCommand[]`, `_meta?` | 可用命令/工具列表更新 |
| `current_mode` | `modeId: string`, `_meta?` | 当前操作模式变更 |
| `config_option` | `configOptions: ConfigOption[]`, `_meta?` | 配置选项变更 |
| `session_info` | `title?: string`, `updatedAt?: string`, `_meta?` | 会话元数据更新 |
| `usage` | `size: int`, `used: int`, `cost?: Cost`, `_meta?` | Token/API 用量和费用统计 |

**约束**：

- 这是 shim 对外的 typed surface，不是 ACP notification 的 1:1 透传；
- 上层消费方不得依赖 ACP 原始事件名；
- 若底层 agent 协议替换为非 ACP，只要 shim 维持此 typed surface，上层 contract 不变。

## Content Block Streaming

### 问题

`agent_message`、`agent_thinking`、`user_message` 是 ACP 流式 chunk 翻译产物，
到达时没有块边界信号。消费方（UI / replay）无法判断一个 content block 何时开始、何时结束。

### 设计

`agent_message`、`agent_thinking`、`user_message` 三种事件的 payload 结构相同，
均携带 `status` 和 `content` 两个字段。`status` 标识当前 chunk 在 content block 中的位置：

| status | 含义 |
|--------|------|
| `start` | content block 的第一个 chunk |
| `streaming` | block 中间的 chunk |
| `end` | block 结束信号（synthetic，content 为空） |

### 关闭规则

Shim 追踪当前 open block 的 event type。只要下一个事件的类型
≠ 当前 block 的类型，shim 会先 emit 一个 `status: "end"` 的空内容事件
关闭当前 block，然后再 emit 新事件。触发关闭的场景包括：

- content 类型切换（`agent_thinking` → `agent_message`）
- 非 content 事件到来（`tool_call`、`plan`、metadata 等）
- turn 结束（`turn_end` 之前自动关闭）

### 示例

```
turn_start
agent_thinking  {status: "start",     content: "Let me analyze..."}
agent_thinking  {status: "streaming", content: "The auth module..."}
agent_thinking  {status: "end"}                                        ← 类型切换
agent_message   {status: "start",     content: "I'll refactor..."}
agent_message   {status: "streaming", content: "the JWT handler..."}
agent_message   {status: "end"}                                        ← tool_call 到来
tool_call       {id: "tc-1", kind: "file", ...}
tool_result     {id: "tc-1", status: "success", ...}
agent_message   {status: "start",     content: "Done. Here's..."}
agent_message   {status: "end"}                                        ← turn 结束
turn_end        {stopReason: "end_turn"}
```

## ACP 边界

shim RPC 与 ACP 的边界如下：

```text
mass ↔ agent-shim:  session/* + runtime/* + typed notifications   ← 本规范定义
agent-shim ↔ agent:   ACP over stdio                                ← 内部实现细节
```

因此：

- agentd 是 runtime/session 语义的消费者，不是 ACP router；
- shim 负责把 ACP `session/update`、tool requests、错误状态翻译成上层可消费事件；
- `fs/*`、`terminal/*`、原始 ACP 握手与兼容性补丁留在 shim 内部，并受 permission posture 约束。

## 方法速查

| 方法 | 方向 | 阻塞 | 说明 |
|------|------|------|------|
| `session/prompt` | 请求 / 响应 | 是（直到 turn 结束） | 发送一个工作 turn |
| `session/cancel` | 请求 / 响应 | 否 | 取消当前 turn |
| `session/subscribe` | 请求 / 响应 + 异步 notification | 否 | 恢复或建立 live `shim/event` 流 |
| `runtime/status` | 请求 / 响应 | 否 | 查询 runtime truth 与恢复边界 |
| `runtime/history` | 请求 / 响应 | 否 | 回放历史（ShimEvent 格式） |
| `runtime/stop` | 请求 / 响应 | 否 | 停止 runtime 与 shim |
| `shim/event` | notification（异步） | — | 统一 notification：session events + state_change |

## 错误码

遵循 JSON-RPC 2.0 标准错误码：

| 码 | 含义 | 场景 |
|----|------|------|
| `-32601` | Method not found | 未知方法名 |
| `-32602` | Invalid params | 参数缺失、`afterSeq` / `fromSeq` 非法、session 未处于允许状态 |
| `-32603` | Internal error | agent 进程异常、ACP 通信失败、事件日志读取失败、shim 内部错误 |
