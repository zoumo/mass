# Event Consumer Guide — `runtime/watch_event` 最佳实践

本文档面向所有通过 `runtime/watch_event` 消费 AgentRunEvent 事件流的第三方 UI 或事件消费方。
它描述每种事件的语义、字段读取方式、状态管理策略，以及 late-join（中途接入）场景下的容错模式。

> **参考实现**：TUI chat client 是目前最完整的事件消费方参考。

---

## 1. 连接与订阅

### 1.1 建立连接

```
1. 通过 Unix socket 建立 JSON-RPC 连接
2. 调用 runtime/watch_event（fromSeq 可选）
3. 开始消费 runtime/event_update notification 流
4. 可选：调用 runtime/status 获取当前 agent 状态
```

### 1.2 fromSeq 语义

| fromSeq | 行为 |
|---------|------|
| 省略 | Live-only：只接收后续新事件 |
| `0` | 全量 replay + live（相当于 K8s List + Watch） |
| `N` | 从 seq N 开始 replay + live（断线恢复用） |

### 1.3 断线重连

使用客户端收到的最后一个 `event.seq + 1` 作为 `fromSeq` 重连，即可无缝补齐丢失的事件段。
**不要使用** response 中的 `nextSeq` 作为重连 seq——它只用于诊断。

---

## 2. 事件信封（AgentRunEvent）

每个 `runtime/event_update` notification 的 `params` 都是一个 AgentRunEvent 信封：

```json
{
  "runId":     "codex",
  "sessionId": "acp-xxx",
  "seq":       42,
  "time":      "2026-04-07T10:00:02Z",
  "type":      "agent_message",
  "turnId":    "turn-001",
  "payload":   { ... }
}
```

| 字段 | 说明 |
|------|------|
| `seq` | 全局唯一递增序列号，用于排序、去重、断线恢复 |
| `type` | 事件类型标识符（见第 3 节） |
| `turnId` | 仅 active turn 内的事件携带；turn 外事件（如 `runtime_update`）不携带 |

### 排序规则

- **Turn 内排序**：同一 `turnId` 下按 `seq` 排序
- **跨 Turn 排序**：回退到全局 `seq` 排序
- **去重**：全局 `seq` 是唯一 dedup key

---

## 3. 事件类型速查

| type | payload 类型 | 说明 |
|------|-------------|------|
| `turn_start` | `{}` | Turn 开始 |
| `agent_message` | ContentEvent | Agent 输出文本 chunk |
| `agent_thinking` | ContentEvent | Agent 思考 / 推理 chunk |
| `user_message` | ContentEvent | 用户输入回显 |
| `tool_call` | ToolCallEvent | Tool 调用（已执行） |
| `tool_result` | ToolResultEvent | Tool 执行结果 |
| `plan` | PlanEvent | 执行计划更新 |
| `turn_end` | TurnEndEvent | Turn 结束 |
| `error` | ErrorEvent | 翻译失败 / runtime 异常 |
| `runtime_update` | RuntimeUpdateEvent | 运行时状态与 session 元数据更新 |

---

## 4. Content 事件：`agent_message` / `agent_thinking` / `user_message`

### 4.1 Wire Shape

```json
{
  "status": "streaming",
  "content": { "type": "text", "text": "Refactoring the auth module..." }
}
```

### 4.2 Block Status 语义

| status | 含义 |
|--------|------|
| `start` | Content block 的第一个 chunk |
| `streaming` | Block 中间的 chunk |
| `end` | Block 结束信号（synthetic，content 为空） |

### 4.3 聚合策略

Content 事件是**连续输出的 chunk**，消费方必须自行聚合：

```
收到 agent_message → 将 content.text 追加到当前 assistant message 的文本缓冲区
收到 agent_thinking → 将 content.text 追加到当前 assistant message 的 thinking 缓冲区
```

**关键设计模式**：

1. **维护一个"当前消息"(currentMsg) 引用**：所有同一 turn 内的连续 `agent_message` / `agent_thinking` chunk 追加到同一条消息上。
2. **thinking → text 切换**：收到第一个 `agent_message` 时，将 thinking 状态标记为结束，开始累积 text。thinking 和 text 共存于同一条消息。
3. **Text 提取**：从 `content` 字段提取文本：`content.text.text`（当 content.type == "text" 时）。

### 4.4 容错：不要强依赖 status

`start` / `streaming` / `end` 提供了块边界信号，但消费方**不能强依赖这些状态**：

- **Late join**：你可能在 `streaming` 中间接入，永远看不到 `start`
- **Early disconnect**：你可能在 `end` 之前断开
- **Replay 模式**：历史回放时所有状态都会重放，但中途连入的 live 流没有补齐

**正确做法**：

```
收到 agent_message 且没有 currentMsg：
  → 创建新的 assistant message（ensureCurrentMsg 模式）
  → 开始追加文本

收到 agent_thinking 且没有 currentMsg：
  → 同上，创建新消息并开始追加 thinking

status == "end"：
  → 可选：标记当前 block 完成（用于 UI 渲染优化）
  → 但不要以此作为创建新消息的唯一信号
```

### 4.5 `user_message` 去重

如果消费方自身发送了 prompt（通过 `session/prompt`），会广播一条 `user_message` 事件。
消费方需要用一个 `sentPrompt` flag 去重：

```
发送 prompt 时 → sentPrompt = true
收到 user_message 时：
  if sentPrompt:
    sentPrompt = false  // 跳过，已经自行显示过
  else:
    显示这条 user_message  // 来自其他 client
```

---

## 5. Tool Call 事件

### 5.1 事件模型（重要）

我们的 `tool_call` 事件语义是**"工具已执行"的通知**，不是"请求执行"。
这与 crush/Claude Code 的模型不同：

| | crush 模型（请求-响应） | 我们的模型（已执行通知） |
|---|---|---|
| tool_call 到达时 | 工具尚未执行 | 工具已执行完成 |
| 初始 UI 状态 | pending / running | success |
| 后续 tool_result | 必须等待 | 可能有，用于补充/覆盖数据 |

### 5.2 ToolCallEvent Wire Shape

```json
{
  "id": "toolu_abc123",
  "kind": "file",
  "title": "Read File",
  "status": "completed",
  "content": [
    { "type": "content", "content": { "type": "text", "text": "file contents..." } },
    { "type": "diff", "path": "src/main.go", "oldText": "...", "newText": "..." }
  ],
  "locations": [
    { "path": "src/main.go", "line": 42 }
  ],
  "rawInput": { ... },
  "rawOutput": { ... }
}
```

### 5.3 字段读取优先级

#### Tool 名称 / 类型（用于 UI 标签显示）

```
1. 如果 kind 非空 → 使用 kind（如 "read", "write", "bash"）
2. 如果 title 以 "Tool: " 开头 → 提取后缀（如 "Tool: workspace/send" → "workspace/send"）
3. 否则 → 使用 title 本身
4. 都为空 → fallback "tool"
```

#### Tool 显示标题（副标题 / 参数区）

```
如果 kind 非空 → title 作为显示标题（如 kind="read", title="Read File"）
如果 kind 为空 → title 已被用作名称，不再重复显示
```

#### Tool Input（参数展示）

从 `title` 和 `locations` 构建：

```
- title（去除 kind 占用的部分）作为描述
- locations[].path + locations[].line 作为文件位置
- rawInput 作为原始输入参数（JSON）
```

#### Tool Output（结果展示）

**优先级从高到低**：

```
1. content[] 结构化内容块（最优先）：
   - type="content" → 提取 content.text.text 作为文本
   - type="diff" → 提取 path/oldText/newText 用于 diff 视图渲染
   - type="terminal" → 提取 terminalId
2. rawOutput（fallback）：
   - 字符串 → 直接使用
   - map → 尝试提取 aggregated_output 或 content[].text
   - 其他 → JSON 序列化
```

### 5.4 Tool ID 管理

`tool_call.id` 是 ACP 分配的全局唯一标识符（如 `"toolu_abc123"`），用于：

1. **关联 tool_call 和 tool_result**：`tool_result.id == tool_call.id`
2. **UI 查找**：消费方维护 `toolItemIDs[tool_call.id] → UI item ID` 映射

```
收到 tool_call：
  → 创建 UI item
  → toolItemIDs[event.id] = uiItemID

收到 tool_result：
  → 从 toolItemIDs[event.id] 查找 UI item
  → 找到 → 更新该 item 的状态和内容
  → 未找到 → 静默跳过（late join 场景）
```

### 5.5 Tool 状态管理

```
收到 tool_call：
  → 初始状态设为 success（工具已执行完成）
  → 如果 content/rawOutput 非空，立即填充结果数据

收到 tool_result：
  → 根据 status 字段更新：
     "completed" → success
     "error"     → error
  → 合并内容：只在新事件有实际内容时更新，避免覆盖已有数据
  → 合并 diff：如果新事件无 diff 但旧数据有 diff，保留旧 diff
```

**注意：ACP 可能为同一个 tool 发送多条 tool_result**（metadata、intermediate、completed）。
只在有实际数据时更新，避免空值覆盖。

### 5.6 tool_call 与 assistant message 的交互

```
收到 tool_call 时：
  1. 结束当前 assistant message（标记为 finish_reason=tool_use）
  2. 创建 tool item
  3. 创建新的空 assistant message（用于 tool 后的文本输出）

如果 tool 后没有新文本 → 新 assistant message 保持为空（UI 不显示空消息）
```

### 5.7 Late Join 下的 Tool 容错

```
收到 tool_result 但没有对应的 tool_call：
  → 静默跳过（不要创建独立的 tool item）
  → 原因：late join 时可能只看到 tool_result 而看不到 tool_call，
    为每个孤立的 tool_result 创建 UI item 会产生大量无上下文的噪音
```

---

## 6. Plan 事件

### 6.1 Wire Shape

```json
{
  "entries": [
    { "content": "Analyze the codebase", "status": "completed" },
    { "content": "Refactor auth module", "status": "in_progress" },
    { "content": "Update tests", "status": "pending" }
  ]
}
```

### 6.2 处理策略

- 每次收到 `plan` 事件时，**替换整个 plan 视图**（不是增量更新）
- `entries` 数组已排序，直接按序渲染
- 每个 entry 的 `status` 可为：`pending`、`in_progress`、`completed`
- 如果 `entries` 为空数组，可忽略该事件

### 6.3 Plan 与 Turn 的关系

Plan 事件可以在 turn 内的任意时刻到达（通常在 agent 决定执行计划后）。
同一个 turn 内可能有多次 plan 更新（每次 agent 完成一步后更新 status）。

---

## 7. Turn 生命周期

### 7.1 正常流程

```
turn_start
  → agent_thinking (0..N chunks)
  → agent_message (0..N chunks)
  → tool_call + tool_result (0..N 组)
  → agent_message (0..N chunks, post-tool)
  → plan (0..N 次)
  → ... (可能多轮 tool + message 交替)
turn_end { stopReason: "end_turn" }
```

### 7.2 消费方状态机

```
                         turn_start / runtime_update(status: running)
                         ┌──────────┐
                ┌───────→│ waiting  │←──────────┐
                │        └────┬─────┘           │
                │             │                 │
                │      events (text/tool/plan)  │
                │             │                 │
                │             ▼                 │
                │        ┌──────────┐    prompt sent
                │        │ waiting  │───────────┘
                │        └────┬─────┘
                │             │
                │      turn_end / runtime_update(status: idle)
                │             │
                │             ▼
                │        ┌──────────┐
                └────────│  idle    │
                         └──────────┘
```

### 7.3 turn_end 处理

```
收到 turn_end：
  1. 结束当前 assistant message（finish_reason=end_turn）
  2. 清空 currentMsg / currentMsgID
  3. 切换为 idle 状态（允许用户输入）
  4. 重置 tool tracking 映射
```

### 7.4 容错：turn_end 可能丢失

在 late join 或断线场景中，可能看不到 `turn_end`。消费方应：

- 使用 `runtime_update` 的 `status.status == "idle"` 作为备用的 turn 结束信号
- 如果 `runtime_update` 报告 `idle` 而消费方仍在 `waiting`，视为隐式 turn_end

---

## 8. `runtime_update` 事件

`runtime_update` 将运行时状态变更与 session 元数据更新合并为单一事件类型。
所有 payload 字段均为可选，一次事件可携带多个字段。

### 8.1 Wire Shape

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
  "configOptions": { "options": [{ "type": "select", "id": "model", "name": "Model", "currentValue": "claude-sonnet" }] },
  "sessionInfo": { "title": "Refactor Auth Module", "updatedAt": "2026-04-07T10:05:00Z" },
  "usage": { "size": 200000, "used": 45000, "cost": { "amount": 0.12, "currency": "USD" } }
}
```

### 8.2 Payload 字段

| 字段 | 类型 | 说明 |
|------|------|------|
| `status` | `RuntimeStatus` | 进程生命周期变更 |
| `availableCommands` | `{commands: AvailableCommand[]}` | 可用命令列表更新（nil=未更新，空 commands=清除） |
| `currentMode` | `{modeId: string}` | 操作模式变更 |
| `configOptions` | `{options: ConfigOption[]}` | 配置选项变更（nil=未更新，空 options=清除） |
| `sessionInfo` | `{title?, updatedAt?}` | 会话元数据更新 |
| `usage` | `{size, used, cost?}` | Token/API 用量统计 |

### 8.3 `status` 子字段：进程状态

| status 值 | 含义 |
|-----------|------|
| `running` | Agent 正在执行 turn |
| `idle` | Agent 空闲，可接受 prompt |
| `error` | Agent 发生错误 |
| `stopped` | Agent 已停止 |

### 8.4 处理策略

```
收到 runtime_update：
  if status != nil:
    更新 UI 状态指示器
    status.status == "running" 且消费方不在 waiting：
      → 进入 waiting 模式（可能是其他 client 发起的 prompt）
    status.status == "idle" 且消费方在 waiting：
      → 退出 waiting 模式（turn 已结束的备用信号）

  if availableCommands != nil:
    更新命令补全 / 工具列表 UI

  if currentMode != nil:
    更新 agent 当前操作模式显示

  if configOptions != nil:
    更新设置面板 / 模型切换 UI

  if sessionInfo != nil:
    更新 session 标题或最后活动时间

  if usage != nil:
    更新 token 用量进度条或费用统计
```

---

## 9. Late Join 完整策略

Late join 是指消费方在 agent 已经运行中途才连接。这是常见场景（重连、新开 UI 窗口等）。

### 核心原则

| 场景 | 策略 |
|------|------|
| 收到 text/thinking 但没有 currentMsg | 自动创建 assistant message 开始接收 |
| 收到 tool_result 但没有对应 tool_call | 静默跳过 |
| agent 已在 running | 通过 `runtime/status` 或 `runtime_update` 获知，自动进入 waiting |
| 没有看到 turn_start | 不影响——text/tool 事件可以独立处理 |
| 没有看到 turn_end | 使用 `runtime_update` 的 `status.status == "idle"` 作为备用信号 |

### 完整恢复（fromSeq=0）

如果需要完整历史，使用 `fromSeq=0` 订阅。会先 replay 所有历史事件，
然后无缝切换到 live 流。消费方可以正常处理所有事件，效果等同于从头开始连接。

---

## 10. Content Block 类型参考

### ToolCallContent（union type，JSON 用 `type` 字段区分）

| type | 字段 | 用途 |
|------|------|------|
| `content` | `content: ContentBlock` | 文本内容（工具输出） |
| `diff` | `path`, `oldText?`, `newText` | 文件 diff（用于 diff 视图） |
| `terminal` | `terminalId` | 终端会话引用 |

### ContentBlock（ACP 标准类型，JSON 用 `type` 字段区分）

| type | 字段 | 用途 |
|------|------|------|
| `text` | `text: string` | 纯文本 |
| `image` | `data`, `mimeType` | 图片 |
| `audio` | `data`, `mimeType` | 音频 |
| `resource_link` | `uri`, `name?` | 资源链接 |
| `resource` | `resource` | 嵌入资源 |

---

## 11. 实现清单

消费方实现时，按以下清单逐项检查：

### 连接

- [ ] 建立 JSON-RPC 连接并调用 `runtime/watch_event`，消费 `runtime/event_update` notification
- [ ] 保存最后收到的 `seq` 用于断线重连
- [ ] 调用 `runtime/status` 获取初始 agent 状态

### Content 聚合

- [ ] 维护 currentMsg 引用，将连续 chunk 追加到同一消息
- [ ] 处理 thinking → text 切换
- [ ] `ensureCurrentMsg` 模式：收到 text/thinking 时若无 currentMsg 则自动创建
- [ ] 不强依赖 `status` 字段（start/streaming/end）

### Tool 处理

- [ ] tool_call 到达时结束当前 assistant message
- [ ] tool_call 初始状态设为 success（已执行模型）
- [ ] 维护 `toolItemIDs` 映射关联 tool_call 和 tool_result
- [ ] tool_result 增量合并（不覆盖已有数据）
- [ ] 孤立 tool_result（无对应 tool_call）静默跳过

### Turn 管理

- [ ] turn_end 时清理 currentMsg 并切换到 idle
- [ ] `runtime_update` 的 `status.status == "idle"` 作为备用 turn 结束信号
- [ ] `runtime_update` 的 `status.status == "running"` 时进入 waiting 模式

### user_message 去重

- [ ] 自己发送的 prompt 用 `sentPrompt` flag 去重

### Plan

- [ ] 每次 plan 事件替换整个 plan 视图（非增量）

### runtime_update

- [ ] 按 payload 字段分发处理（status, availableCommands, currentMode 等）
- [ ] `status` 字段用于状态指示器和备用 turn 结束信号
