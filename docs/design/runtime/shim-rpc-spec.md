# Shim RPC Specification

## 概述

Shim RPC 是 agent-shim 对上暴露的管理接口——agentd 通过它控制和观测单个 agent session。

对标 containerd-shim 的 ttrpc 接口。区别在于：
- containerd-shim 使用 ttrpc（protobuf over Unix socket）
- agent-shim 使用 JSON-RPC 2.0 over Unix socket

**协议**：JSON-RPC 2.0 over Unix socket（流式传输）

**Socket 路径约定**：`/run/agentd/shim/<session-id>/agent-shim.sock`

Socket 与 `state.json`、`events.jsonl` 同在一个 state dir 下，
agentd 无需额外记录 socket 路径——知道 session-id 就能算出来。

## 设计原则

1. **Shim RPC 是 agentd 唯一的 agent 管理接口**——agentd 不直接接触 agent 进程，
   不持有 stdio，不理解 ACP。所有交互通过 shim RPC 中转。
2. **ACP 不穿透**——agent-shim 内部使用 ACP 与 agent 通信，
   但 shim RPC 暴露的是翻译后的 typed events 和高层命令，消费方无需感知 ACP。
3. **重连友好**——agentd 重启后通过扫描 socket 文件重新连接，
   调用 `GetState` 恢复元数据，调用 `Subscribe` 重新订阅事件流。

## 方法

### `Prompt`

向 agent 发送 prompt，阻塞直到 agent turn 完成。

**Request**:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "Prompt",
  "params": {
    "text": "string — user-supplied prompt text"
  }
}
```

**Response**:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "stopReason": "string — mirrors agent stop reason (e.g. end_turn, tool_use)"
  }
}
```

**Error codes**:
- `-32603` (Internal Error) — agent 进程异常或 ACP 通信失败

### `Cancel`

取消当前 agent turn。agent-shim 将此转发为 ACP `session/cancel`。

**Request**:

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "Cancel",
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

### `Subscribe`

订阅 typed event stream。方法立即返回空 result；
事件作为 `$/event` notification 异步送达，直到客户端断开连接。

**Request**:

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "Subscribe",
  "params": null
}
```

**Response**:

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "result": null
}
```

订阅建立后，事件通过 notification 送达（见下方 [事件](#事件json-rpc-notification) 部分）。

### `GetState`

查询 agent 进程的当前状态。

**Request**:

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "method": "GetState",
  "params": null
}
```

**Response**:

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "result": {
    "oarVersion": "string — OAR Runtime Spec version",
    "id": "string — session ID",
    "status": "string — creating | created | running | stopped",
    "pid": 12345,
    "bundle": "string — bundle directory path",
    "annotations": {}
  }
}
```

`status` 值对应 [Runtime Spec](runtime-spec.md) 定义的生命周期状态。

### `GetHistory`

读取 event log，从指定偏移开始返回所有条目。
用于 agentd 重连后补全在断开期间错过的事件。

**Request**:

```json
{
  "jsonrpc": "2.0",
  "id": 5,
  "method": "GetHistory",
  "params": {
    "fromSeq": 0
  }
}
```

`fromSeq` 为 0 时返回完整历史。参数可省略，默认 `fromSeq=0`。

**Response**:

```json
{
  "jsonrpc": "2.0",
  "id": 5,
  "result": {
    "entries": [
      {
        "seq": 1,
        "timestamp": "2025-01-15T10:30:00Z",
        "type": "text",
        "payload": { "text": "..." }
      }
    ]
  }
}
```

### `Shutdown`

优雅关闭 agent 进程和 shim RPC server。
agent-shim 先回复确认，然后执行 SIGTERM → 等待 → SIGKILL 序列。

**Request**:

```json
{
  "jsonrpc": "2.0",
  "id": 6,
  "method": "Shutdown",
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

响应在 agent 进程终止之前发送——调用方收到响应即可释放连接。

## 事件（JSON-RPC Notification）

Subscribe 建立后，agent-shim 通过 `$/event` notification 推送 typed events。
Notification 没有 `id` 字段，不需要回复。

```json
{
  "jsonrpc": "2.0",
  "method": "$/event",
  "params": {
    "type": "string — event type discriminator",
    "payload": {}
  }
}
```

### 事件类型

| type | payload 字段 | 说明 |
|------|------------|------|
| `text` | `text: string` | agent 输出的文本片段（流式） |
| `thinking` | `text: string` | agent 思考/推理片段（流式） |
| `user_message` | `text: string` | 用户 prompt 的回显片段 |
| `tool_call` | `id: string`, `kind: string`, `title: string` | agent 调用了一个 tool |
| `tool_result` | `id: string`, `status: string` | tool 调用结果 |
| `file_write` | `path: string`, `allowed: bool` | 文件写入事件 |
| `file_read` | `path: string`, `allowed: bool` | 文件读取事件 |
| `command` | `command: string`, `allowed: bool` | shell 命令执行事件 |
| `plan` | `entries: PlanEntry[]` | agent 更新了执行计划 |
| `turn_start` | _(empty)_ | agent turn 开始 |
| `turn_end` | `stopReason: string` | agent turn 结束 |
| `error` | `msg: string` | 未知或畸形事件 |

### 事件与 ACP 的关系

这些事件是 agent-shim 从 ACP 协议翻译而来的，**不是** raw ACP notification 的透传。
agentd 作为消费者只需理解上述事件类型，不需要感知 ACP 协议细节。

```
agentd ↔ agent-shim:  typed events ($/event)   ← 本规范定义
agent-shim ↔ agent:   ACP over stdio            ← agent-shim 内部实现细节
```

如果未来某个 agent 使用 gRPC 而非 ACP，只需编写新的 shim 实现，
上层 typed event 接口不变。

## 方法速查

| 方法 | 方向 | 阻塞 | 说明 |
|------|------|------|------|
| `Prompt` | 请求/响应 | 是（等 turn 完成） | 发送 prompt |
| `Cancel` | 请求/响应 | 否 | 取消当前 turn |
| `Subscribe` | 请求/响应 + 异步 notification | 否（立即回复，事件异步） | 订阅事件流 |
| `GetState` | 请求/响应 | 否 | 查询进程状态 |
| `GetHistory` | 请求/响应 | 否 | 读取历史事件 |
| `Shutdown` | 请求/响应 | 否（回复先于进程退出） | 关闭 agent 和 shim |

## 错误码

遵循 JSON-RPC 2.0 标准错误码：

| 码 | 含义 | 场景 |
|----|------|------|
| `-32601` | Method not found | 未知方法名 |
| `-32602` | Invalid params | 参数缺失或格式错误 |
| `-32603` | Internal error | agent 进程异常、ACP 通信失败 |
