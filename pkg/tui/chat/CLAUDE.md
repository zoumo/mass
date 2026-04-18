# pkg/tui/chat — 设计约定

> **事件消费参考**：本包是 `runtime/watch_event` 的参考实现。
> 事件类型定义、数据流处理、内容提取优先级、late-join 容错等通用协议见
> [Event Consumer Guide](../../../docs/develop/event-consumer-guide.md)。
> 本文档仅记录 chat TUI 实现层面的设计决策和 Bubbletea 特有约束。

## 架构

```
connectCmd (chat.go)
    │
    ├── runclient.Dial → *runclient.Client
    ├── sc.WatchEvent  → *runclient.Watcher (typed event stream)
    │
    ▼
waitNotif(watcher) (tea.Cmd)
    │  读 watcher.ResultChan()
    │  Watcher 内部处理 JSON-RPC 反序列化和 watchID 过滤
    │
    ├── turn_end event     → turnEndMsg
    ├── runtime_update (含 Status) → stateChangeMsg → 更新状态栏
    ├── 其他事件            → notifMsg     → handleNotif()
    └── channel closed      → connClosedMsg
    │
    ▼
chatModel (chat.go)
    ├── chat: *component.Chat    ← pkg/tui/component 的 crush 风格渲染
    ├── StreamingMessage         ← 可变 Message 实现 (streaming.go)
    ├── toolItemIDs              ← tool_call ID → chat item ID 映射
    └── agentStatus              ← 实时 agent 状态
```

## Late Join（中途接入）

chat 可以随时连接到正在运行的 agent。这意味着：

1. **text/thinking 无 currentMsg**：`ensureCurrentMsg()` 自动创建 assistant message 开始接收
2. **tool_result 无对应 tool_call**：静默跳过——这是历史事件，创建独立行只会产生大量 `↳ pending` 噪音
3. **agent 已在 running**：`fetchStatusCmd` 查询初始状态，`stateChangeMsg` 处理 `running` → 自动进入 waiting 模式

## Fire-and-Forget Prompt

`sendPromptCmd` 使用 `sc.SendPrompt()`（内部调 `CallAsync`，不等回复）而非阻塞式 `Call`。原因：

- `session/prompt` RPC 阻塞到 turn 结束——如果用阻塞式 `Call` 会卡住 tea.Cmd goroutine
- Turn 结束通过 `turn_end` 通知知道，不需要 RPC 回复
- `CallAsync` 发完立即返回，不注册 pending response handler
- 这样 `session/cancel` 的阻塞式 `Call` 不会和 prompt 的 encoder 竞争

## RPC 事件流

chat 通过 `runclient.Watcher` 接收事件，Watcher 内部封装了：

- NDJSON 流的读取和 JSON 反序列化
- watchID 过滤（只返回本次 watch 订阅的事件）
- 类型化的 `AgentRunEvent` 输出（通过 `ResultChan()`）

chat 不直接接触 NDJSON 流或 JSON 解析。

## waitNotif 绝不能返回 nil

`waitNotif` 读取 `w.ResultChan()` 并返回对应的 `tea.Msg`，**绝不能返回 nil**。
原因：返回 nil 的 tea.Cmd 不会触发 Bubbletea 的 Update，导致 waitNotif 不会被重新调度，
**整个通知链永久断裂**。

Watcher 已在内部完成 watchID 过滤和 JSON 解析，所以到达 `waitNotif` 的事件都是有效的，
每个分支都必须 return 一个非 nil 的 `tea.Msg`。

## 状态栏

底部等待区域显示实时 agent 状态：

- `● running` (绿色) — ctrl+x to cancel
- `● idle` (蓝色) — ready for input
- `● error` (红色) — agent error, check logs
- `● stopped` (灰色) — agent stopped

状态来源：
1. 连接时 `runtime/status` RPC 查询初始状态 → 返回 `initialStatusMsg`（不触发 waitNotif 重调度）
2. 之后通过 `runtime_update`（含 Status）通知实时更新 → 返回 `stateChangeMsg`（触发 waitNotif 重调度）

**关键：** `fetchStatusCmd` 必须返回 `initialStatusMsg` 而非 `stateChangeMsg`。
如果返回 `stateChangeMsg`，其 handler 会调 `waitNotif(m.watcher)` 产生第二条通知链，
两个 goroutine 竞争同一个 channel → 随机事件丢失。

## 键盘操作

| 按键 | 上下文 | 行为 |
|------|--------|------|
| Enter | editor | 发送消息 |
| Shift+Enter | editor | 插入换行 |
| Tab | 全局 | 切换 editor ↔ chat 焦点 |
| Ctrl+X | waiting | 取消当前 turn (session/cancel) |
| Esc | chat focused | 切回 editor |
| Ctrl+C | 全局 | 退出 |
| j/k | chat focused | 上下滚动 1 行 |
| d/u | chat focused | 上下半页 |
| f/b | chat focused | 上下整页 |
| g/G | chat focused | 顶部/底部 |
| PgUp/PgDn | editor | 滚动 chat |
| 鼠标滚轮 | 全局 | 滚动 chat（3 行/次） |
| Shift+点击 | 全局 | 选择文本（终端原生行为） |

## Tool 事件模型

我们的 tool_call 事件是"工具已执行"的通知，不是"请求执行"。这和 crush 的模型不同：

| | crush 模型 | 我们的模型 |
|---|---|---|
| tool_call | 请求执行（pending → running → done） | 已执行（直接 done） |
| 初始 status | ToolStatusRunning | ToolStatusSuccess |
| ToolCall.Finished | false（等待结果） | true（已完成） |
| "Waiting for tool response..." | 正常（等结果） | **不应出现** |

**关键：** 创建 ToolMessageItem 后必须设置：
- `ToolCall.Finished = true` — 防止 `isSpinning()` 返回 true（避免乱码动画）
- `SetStatus(ToolStatusSuccess)` — 防止 `toolEarlyStateContent` 显示 "Waiting for tool response..."

```
tool_call{id, kind, title, content?, rawOutput?}
    → finish current assistant msg
    → create ToolMessageItem (Finished=true, status=Success)
    → if content/rawOutput present: pre-populate ToolResult for immediate display
    → track toolItemIDs[id] = itemID
    → create new AssistantMessageItem for post-tool text

tool_result{id, status, content?, rawOutput?}
    → lookup toolItemIDs[id]
    → found: update ToolMessageItem status (success/error) + result content
    → not found (late join): skip silently
```

### 结果内容提取优先级

`BuildResultContent` 按以下顺序提取工具结果：

1. **结构化 Content blocks** — Text 取全文，Diff 取 path + newText，Terminal 取 ID
2. **RawOutput** — fallback，支持 string/JSON 任意类型

## user_message 事件

`handlePrompt` 在 `NotifyTurnStart` 之后、`mgr.Prompt` 之前广播 `user_message` 事件。
这样所有订阅者（包括中途接入的 chat）都能看到 user prompt，且记录在 event log 中。

chat 处理 `user_message` 时通过 `sentPrompt` flag 去重：
- 自己发的 prompt → `sentPrompt=true` → 收到 `user_message` 时跳过（已经显示了）
- 别人发的 prompt（CLI/其他 client）→ 正常显示

## waitNotif 链必须不间断

`waitNotif` 是一个自调度链：读一个通知 → 返回 tea.Msg → Update 处理 → 在 cmds 里加 `waitNotif` 继续读下一个。

**Update 中每个消费 notifs channel 的 case 都必须重新调度 `waitNotif(m.watcher)`：**

```go
case notifMsg:       cmds = append(cmds, waitNotif(m.watcher))  // ✓
case turnEndMsg:     cmds = append(cmds, waitNotif(m.watcher))  // ✓
case stateChangeMsg: cmds = append(cmds, waitNotif(m.watcher))  // ✓ 曾经遗漏导致消息全丢
case connClosedMsg:  // 不需要（连接已断）
```

如果某个 case 漏了 `waitNotif`，链就断了，**后续所有通知（text、thinking、tool_call 等）都会被静默丢弃**。症状：状态栏显示 running 但看不到任何过程消息。

## 不要做的事

- 不要用 `c.call("session/prompt")` —— 会阻塞到 turn 结束
- 不要给没有 tool_call 的 tool_result 创建独立行 —— late join 场景会刷屏
- 不要忘记在创建 AssistantMessageItem 后调用 `StartAnimation()` + 处理 `anim.StepMsg`
- 不要忘记 `ensureCurrentMsg()` —— 中途接入时第一个 text/thinking 可能没有 currentMsg
- **不要在任何消费 notifs 的 case 中遗漏 `waitNotif` 重新调度** —— 会导致整个通知流中断
- **不要在 `waitNotif` 内部返回 nil** —— 会导致 Bubbletea 跳过 Update，通知链永久断裂
- **不要给 tool item 设 ToolStatusRunning** —— 我们的 tool_call 是已执行通知，Running 会显示 "Waiting for tool response..."
- **不要设 ToolCall.Finished=false** —— 会触发 isSpinning()=true，显示乱码动画字符
- **不要让 `fetchStatusCmd` 返回 `stateChangeMsg`** —— 必须用 `initialStatusMsg`，否则产生双 waitNotif 链
- **不要用 `ScrollBy()` / `ScrollToBottom()` 等非 Animate 方法** —— 会导致翻页后动画冻结，必须用 `*AndAnimate` 变体
