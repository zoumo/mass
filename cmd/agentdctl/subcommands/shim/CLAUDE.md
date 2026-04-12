# shim chat TUI — 设计约定

## 架构

```
shim RPC client (command.go)
    │
    ├── readLoop → c.notifs channel (所有通知)
    │
    ▼
waitNotif (tea.Cmd)
    │
    ├── session/update    → notifMsg     → handleNotif()
    ├── runtime/stateChange → stateChangeMsg → 更新状态栏
    ├── turn_end event    → turnEndMsg
    └── channel closed    → connClosedMsg
    │
    ▼
chatModel (chat.go)
    ├── chat: *chat.Chat         ← pkg/tui/chat 的 crush 风格渲染
    ├── shimMessage              ← 可变 Message 实现
    ├── toolItemIDs              ← tool_call ID → chat item ID 映射
    └── agentStatus              ← 实时 agent 状态
```

## Late Join（中途接入）

shim chat 可以随时连接到正在运行的 agent。这意味着：

1. **text/thinking 无 currentMsg**：`ensureCurrentMsg()` 自动创建 assistant message 开始接收
2. **tool_result 无对应 tool_call**：静默跳过——这是历史事件，创建独立行只会产生大量 `↳ pending` 噪音
3. **agent 已在 running**：`fetchStatusCmd` 查询初始状态，`stateChangeMsg` 处理 `running` → 自动进入 waiting 模式

## Fire-and-Forget Prompt

`sendPromptCmd` 使用 `c.send()`（不等回复）而非 `c.call()`（阻塞）。原因：

- `session/prompt` RPC 阻塞到 turn 结束——如果用 `c.call()` 会卡住 tea.Cmd goroutine
- Turn 结束通过 `turn_end` 通知知道，不需要 RPC 回复
- `c.send()` 发完立即返回，不注册 pending handler
- 这样 `session/cancel` 的 `c.call()` 不会和 prompt 的 encoder 竞争

## scanner buffer

`bufio.Scanner` 默认 64KB。agent 的 tool_result 可能很大（代码文件内容）。已增大到 10MB：

```go
sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
```

## 状态栏

底部等待区域显示实时 agent 状态：

- `● running` (绿色) — esc to cancel
- `● idle` (蓝色) — ready for input
- `● error` (红色) — agent error, check logs
- `● stopped` (灰色) — agent stopped

状态来源：
1. 连接时 `runtime/status` RPC 查询初始状态
2. 之后通过 `runtime/stateChange` 通知实时更新

## 键盘操作

| 按键 | 上下文 | 行为 |
|------|--------|------|
| Enter | editor | 发送消息 |
| Shift+Enter | editor | 插入换行 |
| Tab | 全局 | 切换 editor ↔ chat 焦点 |
| Esc | waiting | 取消当前 turn (session/cancel) |
| Esc | chat focused | 切回 editor |
| Ctrl+C | 全局 | 退出 |
| j/k | chat focused | 上下滚动 1 行 |
| d/u | chat focused | 上下半页 |
| f/b | chat focused | 上下整页 |
| g/G | chat focused | 顶部/底部 |
| PgUp/PgDn | editor | 滚动 chat |
| 鼠标滚轮 | 全局 | 滚动 chat（3 行/次） |
| Shift+点击 | 全局 | 选择文本（终端原生行为） |

## Tool 事件流

```
tool_call{id, kind, title}
    → finish current assistant msg
    → create ToolMessageItem (status=running)
    → track toolItemIDs[id] = itemID
    → create new AssistantMessageItem for post-tool text

tool_result{id, status}
    → lookup toolItemIDs[id]
    → found: update ToolMessageItem status (running → success/error)
    → not found (late join): skip silently
```

## 不要做的事

- 不要用 `c.call("session/prompt")` —— 会阻塞到 turn 结束
- 不要给没有 tool_call 的 tool_result 创建独立行 —— late join 场景会刷屏
- 不要忘记在创建 AssistantMessageItem 后调用 `StartAnimation()` + 处理 `anim.StepMsg`
- 不要忘记 `ensureCurrentMsg()` —— 中途接入时第一个 text/thinking 可能没有 currentMsg
