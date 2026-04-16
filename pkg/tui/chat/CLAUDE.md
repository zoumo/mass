# pkg/tui/chat — 设计约定

## 架构

```
agent-run RPC client (chat.go)
    │
    ├── readLoop → c.notifs channel (所有通知)
    │
    ▼
waitNotif (tea.Cmd)
    │
    ├── runtime/event_update → notifMsg     → handleNotif()
    ├── runtime_update (含 Status) → stateChangeMsg → 更新状态栏
    ├── turn_end event    → turnEndMsg
    └── channel closed    → connClosedMsg
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

`sendPromptCmd` 使用 `c.send()`（不等回复）而非 `c.call()`（阻塞）。原因：

- `session/prompt` RPC 阻塞到 turn 结束——如果用 `c.call()` 会卡住 tea.Cmd goroutine
- Turn 结束通过 `turn_end` 通知知道，不需要 RPC 回复
- `c.send()` 发完立即返回，不注册 pending handler
- 这样 `session/cancel` 的 `c.call()` 不会和 prompt 的 encoder 竞争

## RPC 读取

使用 `bufio.Reader.ReadBytes('\n')` + `json.Unmarshal` 逐行读取 NDJSON 流：

- **无大小限制**：`ReadBytes` 按需增长，不像 `bufio.Scanner` 有硬上限
- **非 JSON 容错**：遇到非 JSON 行时 skip 并 log warning，不中断连接
- **流状态可恢复**：`json.Decoder` 遇到非 JSON 后流位置损坏无法恢复，`ReadBytes` 按行边界切分，天然可以跳过坏行

## waitNotif 必须内部循环

`waitNotif` 使用 `for` 循环处理无效消息（非 agent-run 通知、解析失败），**绝不能返回 nil**。
原因：返回 nil 的 tea.Cmd 不会触发 Bubbletea 的 Update，导致 waitNotif 不会被重新调度，
**整个通知链永久断裂**。

```go
// ✓ 正确：循环跳过无效消息
for {
    msg, ok := <-ch
    if !ok { return connClosedMsg{} }
    if msg.Method != runapi.MethodRuntimeEventUpdate {
        continue // 不返回 nil！
    }
    // ... process ...
}

// ✗ 错误：返回 nil 会断链
if msg.Method != runapi.MethodRuntimeEventUpdate {
    return nil // BUG: 链断了，后续所有事件丢失
}
```

## 状态栏

底部等待区域显示实时 agent 状态：

- `● running` (绿色) — ctrl+x to cancel
- `● idle` (蓝色) — ready for input
- `● error` (红色) — agent error, check logs
- `● stopped` (灰色) — agent stopped

状态来源：
1. 连接时 `runtime/status` RPC 查询初始状态
2. 之后通过 `runtime_update`（含 Status）通知实时更新

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

**Update 中每个消费 notifs channel 的 case 都必须重新调度 `waitNotif(m.notifs)`：**

```go
case notifMsg:       cmds = append(cmds, waitNotif(m.notifs))  // ✓
case turnEndMsg:     cmds = append(cmds, waitNotif(m.notifs))  // ✓
case stateChangeMsg: cmds = append(cmds, waitNotif(m.notifs))  // ✓ 曾经遗漏导致消息全丢
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
