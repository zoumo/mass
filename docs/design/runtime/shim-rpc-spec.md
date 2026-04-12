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

1. **clean-break surface** —— 规范 surface 统一使用 `session/*` + `runtime/*`，
   不再把 legacy PascalCase 方法或 `$/event` notification 当作当前契约。
2. **ACP 不穿透** —— agent-shim 是唯一理解 ACP 的组件；上层只看到翻译后的
   runtime/session 语义。
3. **重连可证实** —— 断线恢复依赖 socket 发现、`runtime/status` 状态检查、
   `runtime/history` 历史补齐、`session/subscribe` 恢复 live 流。
4. **一个序列空间** —— live notification 与历史回放共享单调递增 `seq`，
   使断线补齐、去重、诊断有可比对依据。

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
- `session/subscribe(afterSeq)` 建立后，shim 只能投递 `seq > afterSeq` 的 live notification；
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

- 调用期间，shim 会持续产出 `session/update` 与必要的 `runtime/state_change` notification；
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
订阅成功后，shim 会在同一连接上异步发送：

- `session/update`
- `runtime/state_change`

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
      "oarVersion": "0.1.0",
      "id": "session-abc123",
      "status": "idle",
      "pid": 12345,
      "bundle": "/var/lib/agentd/bundles/session-abc123",
      "annotations": {
        "org.openagents.task": "PROJ-1234"
      }
    },
    "recovery": {
      "lastSeq": 41
    }
  }
}
```

`state` 字段的结构与 [runtime-spec.md](runtime-spec.md) 的 State 定义一致。
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
        "method": "runtime/state_change",
        "params": {
          "sessionId": "session-abc123",
          "seq": 39,
          "timestamp": "2026-04-07T10:00:00Z",
          "previousStatus": "idle",
          "status": "running",
          "pid": 12345
        }
      },
      {
        "method": "session/update",
        "params": {
          "sessionId": "session-abc123",
          "seq": 40,
          "timestamp": "2026-04-07T10:00:01Z",
          "event": {
            "type": "text",
            "payload": {
              "text": "I found the auth handler." 
            }
          }
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
Turn-Aware Event Ordering 为 `session/update` notification envelope 增加三个可选字段，
使 agentd 可以在 turn 级精确排序，而不必把完整的时间戳或索引外部化到调用方。

### 新增 envelope 字段

在 `session/update.params` 中追加三个字段（全部可选，仅 turn-bound 事件携带）：

| 字段 | 类型 | 说明 |
|------|------|------|
| `turnId` | `string` | 当前 prompt turn 的标识符，由 shim 在 `turn_start` 事件时分配，在 `turn_end` 事件后清除 |
| `streamSeq` | `int` | turn 内事件的单调递增序号，每个 `turn_start` 重置为 `0`，在 turn 内每次 notification 递增 |
| `phase` | `string` | 事件发生的阶段，可选值：`"thinking"` / `"acting"` / `"tool_call"` |

### 排序规则

1. **`seq` 继续作为全局唯一序列号**，用于跨 turn 排序、断线补齐、去重。  
   任何 shim notification（包括 `runtime/state_change`）都携带 `seq`。

2. **`turnId` 在 `turn_start` 事件时由 shim 分配**，并在该 turn 内所有后续 `session/update` notification 中携带相同值；
   `turn_end` 事件后，后续 notification 不再携带 `turnId`（字段缺失或为 `null`）。

3. **`streamSeq` 在每个 `turn_start` 时重置为 `0`**，并在 turn 内每次 `session/update` notification 递增；
   跨 turn 不延续，reset 是 per-turn 语义。

4. **`runtime/state_change` 不参与 turn 排序**：该 notification 只携带全局 `seq`，不携带 `turnId` / `streamSeq` / `phase`。

### Replay 语义

- **turn 内排序**：chat/replay 在同一 `turnId` 下按 `(turnId, streamSeq)` 排序事件，以保证 turn 内确定性顺序；
- **跨 turn 排序**：在 `turnId` 缺失或跨 turn 边界时，回退到全局 `seq` 排序；
- **去重**：全局 `seq` 仍是唯一 dedup key，`turnId` + `streamSeq` 不作为 dedup key。

### 带 turn 字段的 `session/update` 示例

```json
{
  "jsonrpc": "2.0",
  "method": "session/update",
  "params": {
    "sessionId": "session-abc123",
    "seq": 42,
    "timestamp": "2026-04-07T10:00:02Z",
    "turnId": "turn-001",
    "streamSeq": 3,
    "phase": "acting",
    "event": {
      "type": "text",
      "payload": {
        "text": "Refactoring the auth module..."
      }
    }
  }
}
```

`turn_start` 事件（`streamSeq` 从 `0` 开始）：

```json
{
  "jsonrpc": "2.0",
  "method": "session/update",
  "params": {
    "sessionId": "session-abc123",
    "seq": 40,
    "timestamp": "2026-04-07T10:00:00Z",
    "turnId": "turn-001",
    "streamSeq": 0,
    "phase": "thinking",
    "event": {
      "type": "turn_start",
      "payload": {}
    }
  }
}
```

`runtime/state_change`（不携带 turn 字段）：

```json
{
  "jsonrpc": "2.0",
  "method": "runtime/state_change",
  "params": {
    "sessionId": "session-abc123",
    "seq": 39,
    "timestamp": "2026-04-07T10:00:00Z",
    "previousStatus": "idle",
    "status": "running",
    "pid": 12345,
    "reason": "prompt-started"
  }
}
```

## Notifications

### `session/update`

这是 shim 对外暴露的主要 typed event notification。
所有来自 ACP 的文本流、工具调用、文件 / 命令副作用等，必须先由 shim 翻译，
再通过此 notification 暴露。

```json
{
  "jsonrpc": "2.0",
  "method": "session/update",
  "params": {
    "sessionId": "session-abc123",
    "seq": 40,
    "timestamp": "2026-04-07T10:00:01Z",
    "event": {
      "type": "text",
      "payload": {
        "text": "I found the auth handler."
      }
    }
  }
}
```

### `runtime/state_change`

用于暴露 runtime process truth 的状态变化，例如 `idle → running`、
`running → idle`、`idle/running → stopped`。

```json
{
  "jsonrpc": "2.0",
  "method": "runtime/state_change",
  "params": {
    "sessionId": "session-abc123",
    "seq": 39,
    "timestamp": "2026-04-07T10:00:00Z",
    "previousStatus": "idle",
    "status": "running",
    "pid": 12345,
    "reason": "prompt-started"
  }
}
```

## Typed Event 类型

`session/update.params.event.type` 的目标集合如下：

| type | payload 字段 | 说明 |
|------|-------------|------|
| `text` | `text: string` | agent 输出的文本片段 |
| `thinking` | `text: string` | agent 思考 / 推理片段 |
| `user_message` | `text: string` | 用户输入被 runtime 接收的回显 |
| `tool_call` | `id: string`, `kind: string`, `title: string` | tool 调用开始 |
| `tool_result` | `id: string`, `status: string` | tool 调用完成 / 失败 |
| `file_write` | `path: string`, `allowed: bool` | 文件写入及权限结果 |
| `file_read` | `path: string`, `allowed: bool` | 文件读取及权限结果 |
| `command` | `command: string`, `allowed: bool` | shell / terminal 操作及权限结果 |
| `plan` | `entries: PlanEntry[]` | 执行计划更新 |
| `turn_start` | _(empty)_ | 一个 turn 开始 |
| `turn_end` | `stopReason: string` | 一个 turn 结束 |
| `error` | `message: string` | ACP 翻译失败、runtime 异常、畸形事件 |

**约束**：

- 这是 shim 对外的 typed surface，不是 ACP notification 的 1:1 透传；
- 上层消费方不得依赖 ACP 原始事件名；
- 若底层 agent 协议替换为非 ACP，只要 shim 维持此 typed surface，上层 contract 不变。

## ACP 边界

shim RPC 与 ACP 的边界如下：

```text
agentd ↔ agent-shim:  session/* + runtime/* + typed notifications   ← 本规范定义
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
| `session/subscribe` | 请求 / 响应 + 异步 notification | 否 | 恢复或建立 live notification 流 |
| `runtime/status` | 请求 / 响应 | 否 | 查询 runtime truth 与恢复边界 |
| `runtime/history` | 请求 / 响应 | 否 | 回放历史 notification |
| `runtime/stop` | 请求 / 响应 | 否 | 停止 runtime 与 shim |

## 错误码

遵循 JSON-RPC 2.0 标准错误码：

| 码 | 含义 | 场景 |
|----|------|------|
| `-32601` | Method not found | 未知方法名 |
| `-32602` | Invalid params | 参数缺失、`afterSeq` / `fromSeq` 非法、session 未处于允许状态 |
| `-32603` | Internal error | agent 进程异常、ACP 通信失败、事件日志读取失败、shim 内部错误 |
