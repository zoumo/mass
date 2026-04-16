# pkg/tui/component — 设计约定

## 来源

本包复刻自 [charmbracelet/crush](https://github.com/charmbracelet/crush) 的 `internal/ui/chat/` 和 `internal/ui/model/chat.go`，适配了我们自己的 `Message` 接口。crush 的 UI 基础设施（styles、anim、list、common）在 `third_party/charmbracelet/crush/` 中。

## 架构

```
Message 接口 (message.go)     ← 调用方实现，提供数据
    │
    ▼
MessageItem 类型 (messages.go) ← 包装 Message，实现 list.Item
    │
    ├── UserMessageItem (user.go)
    ├── AssistantMessageItem (assistant.go)    ← 支持流式 SetMessage()
    ├── GenericToolMessageItem (generic.go)
    └── SystemItem (simple_items.go)           ← 轻量系统消息
    │
    ▼
Chat wrapper (chat.go)        ← 包装 list.List，提供 follow 模式、ID 查找、滚动
```

## 关键设计决策

### isSpinning() 判定

`AssistantMessageItem.isSpinning()` 决定是否显示动画 spinner 而非实际内容：

```go
return (isThinking || !isFinished) && !hasContent && !hasThinking && !hasToolCalls
```

**必须检查 `hasThinking`**：否则 thinking 阶段有了文本内容但仍显示 spinner 乱码（`=$@14dd%3=`）。这是 crush 原版的 bug 边界——crush 中 thinking 到达时 `IsThinking()=true` 且内容为空会短暂显示 spinner，但因为 crush 的 token 更新频率高所以不明显；我们的场景中 thinking 内容累积在同一个 message 上，必须立即显示。

### 动画必须接线

`AssistantMessageItem` 创建后如果 `isSpinning()=true`，必须调用 `StartAnimation()` 并在 Update 循环中处理 `anim.StepMsg`。否则动画卡在初始随机字符状态。

```go
// 创建时
item := component.NewAssistantMessageItem(&sty, msg)
if a, ok := item.(*chat.AssistantMessageItem); ok {
    cmd = a.StartAnimation()  // 返回的 cmd 必须传给 tea
}

// Update 中
case anim.StepMsg:
    cmd = component.Animate(msg)   // 转发给 chat 管理可见性优化
```

### 渲染缓存（cachedMessageItem）

每种 MessageItem 都缓存渲染结果，key 是宽度。当 `SetMessage()` 或 `AppendText()` 修改内容时必须调用 `clearCache()`。忘记清缓存会导致内容不更新。

### Follow 模式

- `follow=false` 初始状态
- `ScrollToBottom()` → `follow=true`
- `ScrollBy(负数)` → `follow=false`
- `ScrollBy(正数)` 且到底 → `follow=true`
- `AppendMessages` 时如果 `follow=true` → 自动 `ScrollToBottom()`
- `SetSize` 时如果已经在底部 → 自动保持

### Message 接口与 StreamingMessage

`Message` 是只读接口，但 `StreamingMessage`（在 `pkg/tui/chat` 中）是可变实现。流式更新模式：

1. 创建 `StreamingMessage`（空内容）
2. 创建 `AssistantMessageItem(msg)` 并加入 chat
3. 每次 token 到达 → 修改 `StreamingMessage` 字段 → 调用 `item.SetMessage(msg)` 触发重渲染

**不要在 pkg/tui/component 中直接修改 Message 内容**——这是调用方的职责。

## 已知问题

### CJK 文本换行异常

`ansi.Wordwrap` 和 `glamour` markdown 渲染在处理中日韩(CJK)字符与 ASCII 混合文本时，可能在错误位置断行，导致视觉上看起来像插入了中文标点（如文件路径 `.md` 前出现中文逗号 `，`）。这是上游 `charmbracelet/x/ansi` 的 CJK word-wrap 问题，不是我们的代码 bug。数据层（events.jsonl）中的文本是正确的。

## 不要做的事

- 不要在 `isSpinning()` 中只检查 `hasContent`——必须同时检查 `hasThinking`
- 不要忘记 `StartAnimation()` 的返回 cmd
- 不要在 `SetMessage` 后忘记在 follow 模式下调 `ScrollToBottom()`
- 不要在 Message 接口中加 setter 方法——它是只读的
