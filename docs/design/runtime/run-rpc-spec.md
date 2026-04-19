---
last_updated: 2026-04-17
---

# Shim RPC Specification

## 概述

本文档定义 agent-run 对上暴露的 **shim RPC 目标契约**。
它是 agentd 与单个 runtime session 之间的本地控制面：

- `session/*` 负责 turn 级控制与订阅；
- `runtime/*` 负责进程真相、历史回放与停止；
- ACP 保持为 agent-run 内部实现细节，不直接暴露给 agentd。

**协议**：JSON-RPC 2.0 over Unix socket

**相关 authority**：

- [runtime-spec.md](runtime-spec.md) 负责 runtime 状态模型、state dir 布局、socket 路径约定；
- 本文档负责 shim RPC 方法名、notification 名、回放 / 重连语义；
- [agent-run.md](agent-run.md) 只描述组件职责与边界，不重复定义规范字段。

## 设计原则

1. **clean-break surface** —— request/response surface 统一使用 `session/*` + `runtime/*`；
   notification surface 统一使用 `runtime/event_update`，不再把 legacy PascalCase 方法或 `$/event`
   notification 当作当前契约。
2. **ACP 不穿透** —— agent-run 是唯一理解 ACP 的组件；上层只看到翻译后的
   runtime/session 语义。
3. **重连可证实** —— 断线恢复依赖 socket 发现、`runtime/status` 状态检查、
   `runtime/watch_event` 一步完成历史补齐 + live 流恢复。
4. **一个序列空间** —— live notification 与历史回放共享单调递增 `seq`，
   使断线补齐、去重、诊断有可比对依据。
5. **payload not content** —— AgentRunEvent envelope 使用 `payload` 字段携带事件数据，
   避免与事件内部的 `content` 字段（如 ContentBlock）产生命名混淆。

## Socket 发现与恢复语义

socket 路径和 state dir 布局由 [runtime-spec.md](runtime-spec.md) 定义。
In agentd-managed deployments, bundle/state/socket are co-located:

```text
<bundleRoot>/<workspace>-<name>/
├── config.json
├── workspace -> <workspace-dir>
├── state.json
├── agent-run.sock
└── events.jsonl
```

恢复调用方（通常是 agentd）在重启后应执行以下顺序：

1. 使用已持久化的 AgentRun 元数据（`ShimSocketPath`）找到 socket；
2. 连接每个 shim socket；
3. 调用 `runtime/status` 获取当前 runtime truth 与 `lastSeq`；
4. 调用 `runtime/watch_event(fromSeq=0)` 完成历史 replay + live 流恢复。

**规范要求**：

- `runtime/watch_event` 的历史 replay 与 live notification 共享同一个 `seq` 空间；
- 历史事件通过 `runtime/event_update` notification 流式推送（非 response body），避免大 payload；
- 调用方通过追踪收到事件的 `seq` 实现断线重连（K8s reflector 模式）。

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

- 调用期间，shim 会持续产出 `runtime/event_update` notification（包含 session events 与 runtime_update events）；
- 调用完成表示这一轮工作已结束，不表示 runtime 被销毁；
- 若 session 尚未处于可接收 prompt 的状态，必须返回错误。

### `session/cancel`

取消当前活跃 turn。agent-run 将此翻译为内部 ACP `session/cancel` 或等价控制。

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

### `runtime/watch_event`

建立 K8s List-Watch 风格的事件订阅。历史事件和 live 事件统一通过
`runtime/event_update` notification 流式推送，response body 只含 `nextSeq`。

**Request**:

```json
{
  “jsonrpc”: “2.0”,
  “id”: 3,
  “method”: “runtime/watch_event”,
  “params”: {
    “fromSeq”: 0
  }
}
```

`fromSeq` 为 OPTIONAL：

- 省略时，表示 live-only：从当前 head 之后开始推送 live 事件；
- 指定为 `0` 时，表示全量 replay + live（类比 K8s List + Watch）；
- 指定为 `N` 时，表示从 seq N 开始 replay + live（类比 K8s Watch from resourceVersion）。

**Response**:

```json
{
  “jsonrpc”: “2.0”,
  “id”: 3,
  “result”: {
    “watchId”: “w-17”,
    “nextSeq”: 42
  }
}
```

| 字段 | 说明 |
|------|------|
| `watchId` | 服务端为本次 watch 分配的唯一标识。后续 `runtime/event_update` notification 的 `params` 中会携带相同的 `watchId`，客户端据此将事件路由到正确的 Watcher 实例（支持单连接多 watch 流复用）。 |
| `nextSeq` | 订阅建立时下一条可能分配的序列号。调用方可用于诊断，但**不应**依赖 `nextSeq` 驱动重连——重连 seq 应来自客户端实际收到的最后一个事件的 `seq + 1`（K8s reflector 模式）。 |

**说明**：

- **WatchID 多路复用**：同一连接可发起多次 `runtime/watch_event` 调用，每次返回不同的
  `watchId`。每条 `runtime/event_update` notification 携带 `watchId` 字段，客户端的
  Watcher 据此过滤，只消费属于自己 watch 流的事件。`watchId` 是 transport-only 字段，
  不持久化到 event log（`events.jsonl`）。
- **两阶段无锁 replay**：shim 先在 Translator mutex 下注册 subscriber channel（O(1)），
  然后在后台 goroutine 中无锁读取 event log 文件，将 `seq < nextSeq` 的历史事件
  通过 `runtime/event_update` notification 流式推送给客户端（每条都携带 `watchId`）。
  随后切换到 live 事件流，跳过 `seq < nextSeq` 的重复事件。这保证了无 gap、无 dup、无大 payload。
- **慢消费者驱逐（K8s 模型）**：subscriber channel buffer = 1024。当 channel 满时，
  shim 关闭该 subscriber 的 channel 并移除订阅，然后 `peer.Close()` 强制断开连接
  （类比 K8s 410 Gone）。客户端检测到断连后（`Watcher.ResultChan()` 关闭），
  用最后收到的 `event.seq + 1` 作为 `fromSeq` 重新 `Dial()` + `WatchEvent()` 重连，
  replay 补齐丢失的事件段，无缝恢复 live 流。

### `session/load`

尝试恢复/加载已有的 ACP session。recovery 时始终调用，agent-run 内部检查 ACP `loadSession` 能力并自动 fallback。

**Request**:

```json
{
  "jsonrpc": "2.0",
  "id": 7,
  "method": "session/load",
  "params": {
    "sessionId": "acp-session-abc123"
  }
}
```

**Response**:

```json
{
  "jsonrpc": "2.0",
  "id": 7,
  "result": null
}
```

**说明**：

- 成功表示 ACP session 已恢复，agent 可继续工作；
- 失败不是致命错误——agent-run 内部自动 fallback 到全新 session；
- 此方法仅在 restart / recovery 路径中使用，不用于正常 prompt 流程。

### `session/set_model`

切换 agent 当前使用的模型。

**Request**:

```json
{
  "jsonrpc": "2.0",
  "id": 8,
  "method": "session/set_model",
  "params": {
    "modelId": "claude-sonnet"
  }
}
```

**Response**:

```json
{
  "jsonrpc": "2.0",
  "id": 8,
  "result": {}
}
```

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
Turn-Aware Event Ordering 为 `runtime/event_update` notification envelope 增加可选字段，
使 agentd 可以在 turn 级精确排序，而不必把完整的时间戳或索引外部化到调用方。

### Turn-aware 字段

`runtime/event_update` 顶层携带以下可选字段（仅 active turn 内的事件携带）：

| 字段 | 类型 | 说明 |
|------|------|------|
| `turnId` | `string` | 当前 prompt turn 的标识符，由 shim 在 `turn_start` 事件时分配，在 `turn_end` 事件后清除 |

### 排序规则

1. **`seq` 继续作为全局唯一序列号**，用于跨 turn 排序、断线补齐、去重。  
   任何 `runtime/event_update` notification 都携带 `seq`。

2. **`turnId` 在 `turn_start` 事件时由 shim 分配**，并在该 turn 内所有 `runtime/event_update` notification 中携带相同值；
   `turn_end` 事件后，后续 notification 不再携带 `turnId`（字段缺失或为 `null`）。

3. **`runtime_update` 事件不参与 turn 排序**：该事件只携带全局 `seq`，不携带 `turnId`。

4. **Turn 字段反映时间上下文**：active turn 内到达的事件都携带 `turnId`；turn 外到达的事件不携带。

### Replay 语义

- **turn 内排序**：chat/replay 在同一 `turnId` 下按 `seq` 排序事件；
- **跨 turn 排序**：在 `turnId` 缺失或跨 turn 边界时，回退到全局 `seq` 排序；
- **去重**：全局 `seq` 是唯一 dedup key。

### `runtime/event_update` 示例

content 事件（turn 内，携带 turn 字段和 block status）：

```json
{
  "jsonrpc": "2.0",
  "method": "runtime/event_update",
  "params": {
    "runId": "codex",
    "sessionId": "acp-xxx",
    "seq": 42,
    "time": "2026-04-07T10:00:02Z",
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
  "method": "runtime/event_update",
  "params": {
    "runId": "codex",
    "sessionId": "acp-xxx",
    "seq": 40,
    "time": "2026-04-07T10:00:00Z",
    "type": "turn_start",
    "turnId": "turn-001",
    "payload": {}
  }
}
```

`runtime_update` 事件（状态变更，不携带 turn 字段）：

```json
{
  "jsonrpc": "2.0",
  "method": "runtime/event_update",
  "params": {
    "runId": "codex",
    "sessionId": "acp-xxx",
    "seq": 39,
    "time": "2026-04-07T10:00:00Z",
    "type": "runtime_update",
    "payload": {
      "status": {
        "previousStatus": "idle",
        "status": "running",
        "pid": 12345,
        "reason": "prompt-started"
      }
    }
  }
}
```

`runtime_update` 事件（仅元数据变更，携带多个可选字段）：

```json
{
  "jsonrpc": "2.0",
  "method": "runtime/event_update",
  "params": {
    "runId": "codex",
    "sessionId": "acp-xxx",
    "seq": 40,
    "time": "2026-04-07T10:00:01Z",
    "type": "runtime_update",
    "payload": {
      "configOptions": { "options": [{ "type": "select", "id": "model", "name": "Model", "currentValue": "claude-sonnet" }] },
      "usage": { "size": 200000, "used": 45000 }
    }
  }
}
```

## Notifications

### `runtime/event_update`

这是 shim 对外暴露的统一 notification。所有来自 ACP 的文本流、工具调用、
文件 / 命令副作用等，以及 runtime 进程状态变化和 session 元数据更新，
统一通过此 notification 暴露。每个 notification 的 `params` 是一个 AgentRunEvent。

```json
{
  "jsonrpc": "2.0",
  "method": "runtime/event_update",
  "params": {
    "runId": "codex",
    "sessionId": "acp-xxx",
    "seq": 40,
    "time": "2026-04-07T10:00:01Z",
    "type": "agent_message",
    "turnId": "turn-001",
    "payload": {
      "status": "streaming",
      "content": { "type": "text", "text": "I found the auth handler." }
    }
  }
}
```

## AgentRunEvent 类型

`runtime/event_update` notification 的 `params` 是一个 AgentRunEvent 信封。
`type` 字段标识事件类型，`payload` 字段携带该类型的具体数据：

| type | payload 字段 | 说明 |
|------|-------------|------|
| `agent_message` | `status: string`, `content: ContentBlock` | agent 输出片段，携带 content block streaming status |
| `agent_thinking` | `status: string`, `content: ContentBlock` | agent 思考 / 推理片段，携带 content block streaming status |
| `user_message` | `status: string`, `content: ContentBlock` | 用户输入回显，携带 content block streaming status |
| `tool_call` | `id`, `kind`, `title`, `status`, `content[]`, `locations[]`, `rawInput`, `rawOutput`, `_meta` | tool 调用开始（完整 ACP 字段） |
| `tool_result` | `id`, `status`, `kind`, `title`, `content[]`, `locations[]`, `rawInput`, `rawOutput`, `_meta` | tool 调用完成 / 失败（完整 ACP 字段） |
| `plan` | `entries: PlanEntry[]`, `_meta?` | 执行计划更新 |
| `turn_start` | _(empty)_ | 一个 turn 开始 |
| `turn_end` | `stopReason: string` | 一个 turn 结束 |
| `error` | `message: string` | ACP 翻译失败、runtime 异常、畸形事件 |
| `runtime_update` | `status?`, `availableCommands?`, `currentMode?`, `configOptions?`, `sessionInfo?`, `usage?` | 运行时状态与 session 元数据更新（见下方详细说明） |

### `runtime_update` Payload

`runtime_update` 将运行时状态变更与 session 元数据更新合并为单一事件类型。
所有 payload 字段均为可选，一次事件可携带多个字段：

```json
{
  "status": {
    "previousStatus": "idle",
    "status": "running",
    "pid": 12345,
    "reason": "prompt-started"
  },
  "availableCommands": { "commands": [{ "name": "/help", "description": "Show help" }] },
  "currentMode": { "modeId": "plan" },
  "configOptions": { "options": [{ "type": "select", "id": "model", "name": "Model", "currentValue": "claude-sonnet", "options": [] }] },
  "sessionInfo": { "title": "Refactor Auth Module", "updatedAt": "2026-04-07T10:05:00Z" },
  "usage": { "size": 200000, "used": 45000, "cost": { "amount": 0.12, "currency": "USD" } }
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `status` | `RuntimeStatus` | 进程生命周期变更（previousStatus, status, pid, reason） |
| `availableCommands` | `{commands: AvailableCommand[]}` | 可用命令列表更新（nil=未更新，空 commands=清除） |
| `currentMode` | `{modeId: string}` | 操作模式变更 |
| `configOptions` | `{options: ConfigOption[]}` | 配置选项变更（nil=未更新，空 options=清除） |
| `sessionInfo` | `{title?, updatedAt?}` | 会话元数据更新 |
| `usage` | `{size, used, cost?}` | Token/API 用量统计 |

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
mass ↔ agent-run:  session/* + runtime/* + typed notifications   ← 本规范定义
agent-run ↔ agent:   ACP over stdio                                ← 内部实现细节
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
| `runtime/watch_event` | 请求 / 响应 + 异步 notification | 否 | K8s List-Watch 风格：replay + live `runtime/event_update` 流 |
| `session/load` | 请求 / 响应 | 否 | 尝试加载已有 ACP session（best-effort，始终尝试） |
| `session/set_model` | 请求 / 响应 | 否 | 切换 agent 使用的模型 |
| `runtime/status` | 请求 / 响应 | 否 | 查询 runtime truth 与恢复边界 |
| `runtime/stop` | 请求 / 响应 | 否 | 停止 runtime 与 shim |
| `runtime/event_update` | notification（异步） | — | 统一 notification：session events + runtime_update |

## 错误码

遵循 JSON-RPC 2.0 标准错误码：

| 码 | 含义 | 场景 |
|----|------|------|
| `-32601` | Method not found | 未知方法名 |
| `-32602` | Invalid params | 参数缺失、`afterSeq` / `fromSeq` 非法、session 未处于允许状态 |
| `-32603` | Internal error | agent 进程异常、ACP 通信失败、事件日志读取失败、shim 内部错误 |
