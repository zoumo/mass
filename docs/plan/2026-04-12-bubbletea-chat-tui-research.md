# Bubbletea TUI 调研：agentdctl shim chat 体验优化

**日期**: 2026-04-12  
**背景**: 评估 charmbracelet/bubbletea 是否值得用于改善 `agentdctl shim chat` 的交互体验

---

## 一、现状分析

### 当前实现 (`cmd/agentdctl/subcommands/shim/command.go`)

`runChat()` 是一个极简 REPL：

```
agentdctl shim chat — type your message, 'exit' to quit

> hello
[thinking seq=1] ...          # 打印到 stderr，ANSI dim
[tool_call seq=2] ...         # 打印到 stderr，ANSI yellow
assistant reply text          # 打印到 stdout，raw
[stop: end_turn]
> 
```

**核心问题清单**：

| 问题 | 影响 |
|------|------|
| stdout/stderr 混输，顺序不稳定 | 文字流和 tool_call 日志互相穿插，难以阅读 |
| `thinking` / `tool_call` / `tool_result` 用 ANSI 裸转义写死 | 不同终端颜色效果不一致，不可配置 |
| 输入框只是 `> ` 前缀，无编辑能力 | 无法上下翻历史、无法在行中间修改输入 |
| agent 响应流式打印，但输入框不消失 | 流式文字直接追加，视觉噪音大 |
| 无滚动，长对话看不回去 | 需手动上滚终端 |
| 无加载状态指示 | agent 思考时终端静止，用户不知道是否卡住 |
| `bufio.Scanner` 单行限制 512KB，不支持多行输入 | 无法粘贴长段落 |

---

## 二、Bubbletea 框架概述

[charmbracelet/bubbletea](https://github.com/charmbracelet/bubbletea) 是 Go 生态中最主流的 TUI 框架，基于 **Elm 架构（Model-Update-View）**，2024 年累计超过 28k GitHub stars。

### 架构模型

```
Init() → initial Cmd
         ↓
Update(msg) → new Model + Cmd   ← 所有状态变更入口
         ↓
View() → string                 ← 纯函数渲染
```

- `tea.Cmd` 是异步操作单元，在独立 goroutine 中执行，完成后返回 `tea.Msg`
- `tea.Program` 管理事件循环，UI 刷新默认 ~60fps

### Charm 生态组件（与本需求相关）

| 包 | 用途 |
|----|------|
| `github.com/charmbracelet/bubbles/textarea` | 多行文本输入，支持 Unicode、粘贴、滚动 |
| `github.com/charmbracelet/bubbles/viewport` | 可滚动内容区域，适合展示对话历史 |
| `github.com/charmbracelet/bubbles/spinner` | 旋转加载动画（等待 agent 响应时） |
| `github.com/charmbracelet/lipgloss` | CSS-like 样式：颜色、边框、padding |

---

## 三、可以改善的体验点

### 3.1 分区布局（已有方案）

用 bubbletea + lipgloss 可实现经典聊天布局：

```
┌─────────────────────────────────────┐
│  对话历史（viewport，可滚动）        │
│  You: hello                         │
│  Agent: [thinking...] ⠋             │
│  Agent: here is my answer           │
│  Tool: bash → exit 0                │
│                                     │
├─────────────────────────────────────┤
│  > type your message here...        │  ← textarea
└─────────────────────────────────────┘
```

- `viewport` 持续追加对话内容，自动跟随最新消息
- `textarea` 与对话区分离，输入不被响应流打断

### 3.2 Spinner 加载状态

在 `session/prompt` 发出后、`turn_end` 到来前，显示 spinner：

```
Agent: ⠸ thinking...
```

### 3.3 富文本事件渲染

用 lipgloss 对不同事件类型着色，比现在的裸 ANSI 更可控：

| 事件类型 | 渲染方式 |
|----------|----------|
| `text` | 白色，正常字体 |
| `thinking` | 灰色 dim，缩进 |
| `tool_call` | 黄色，带 `⚙` 前缀 |
| `tool_result` | 灰色，截断长输出 |
| `turn_end` | 绿色分隔线 |

### 3.4 多行输入（`textarea`）

现有的 `bufio.Scanner` 只能输入单行。`textarea` 支持：
- `Enter` 换行，`Ctrl+D` / `Alt+Enter` 发送
- 粘贴多行内容（如代码片段）

### 3.5 历史导航

`viewport` 支持 `PgUp/PgDn`，用户可以在不退出 chat 的情况下回看历史。

---

## 四、接入方案

### 方案 A：最小化接入（推荐）

只替换 `runChat()` 内部实现，不动 RPC client 和事件解析逻辑。

**新增依赖**：
```
github.com/charmbracelet/bubbletea v1.3.x
github.com/charmbracelet/bubbles  v0.20.x
github.com/charmbracelet/lipgloss v1.1.x
```

**架构**：
```
chatModel {
    viewport  viewport.Model   // 对话历史
    textarea  textarea.Model   // 输入框
    spinner   spinner.Model    // 加载动画
    messages  []string         // 已渲染的历史行
    waiting   bool             // 是否等待 agent
}
```

**事件桥接**：将 `c.notifs` channel 转为 `tea.Cmd`：
```go
func waitForNotif(notifs <-chan rpcResponse) tea.Cmd {
    return func() tea.Msg {
        msg, ok := <-notifs
        if !ok {
            return connClosedMsg{}
        }
        return shimNotifMsg{msg}
    }
}
```

每次 `Update` 处理完一个 notif 后，返回下一个 `waitForNotif` 命令，形成持续监听。

**改动范围**：仅 `runChat()` 函数（约 45 行 → 约 200 行），RPC 层零改动。

### 方案 B：完整 TUI（过度设计）

包含多 tab（history / live / logs），鼠标支持，可配置主题。

**结论**：本工具定位是调试/开发用 CLI，方案 B 过于复杂，不推荐。

---

## 五、成本与风险评估

### 成本

| 项目 | 估算 |
|------|------|
| 新增依赖体积 | bubbletea ~1MB, bubbles ~300KB, lipgloss ~200KB，编译后二进制增加约 2-3MB |
| 实现工作量 | 方案 A：约 1-2 天（含测试） |
| 维护成本 | charm 生态活跃，版本兼容性稳定 |

### 风险

| 风险 | 说明 | 缓解 |
|------|------|------|
| Panic 后终端损坏 | bubbletea panic 会留下 raw mode，需手动 `reset` | 加 `defer tea.Program.ReleaseTerminal()` |
| 消息顺序非确定 | goroutine 并发导致 notif 消息可能乱序 | 已有 `seq` 字段，展示层可按 seq 排序 |
| 非 TTY 环境退化 | SSH、pipe、CI 中 TUI 无法正常工作 | 检测 `isatty`，非 TTY 时 fallback 到当前 bufio 实现 |
| Windows 支持 | bubbletea 对 Windows 支持有限 | 本项目目标平台为 macOS/Linux，可接受 |

---

## 六、结论与建议

**推荐引入 bubbletea（方案 A）**，理由：

1. `runChat` 是开发者日常调试的高频入口，体验改善收益明显
2. 方案 A 改动范围小，不影响 RPC 层和生产路径
3. 非 TTY fallback 保证 CI/pipe 场景不受影响
4. bubbletea 生态成熟，在 Go TUI 领域无竞争对手

**不建议**：在 `agentdctl` 其他子命令（`state`, `history`, `prompt`）中引入 TUI，这些命令以 JSON 输出为主，适合 pipe，加 TUI 得不偿失。

---

## 七、参考资料

- [charmbracelet/bubbletea](https://github.com/charmbracelet/bubbletea)
- [charmbracelet/bubbles - textarea](https://charm-docs.vercel.app/docs/bubbles/components/textarea)
- [Building bubbletea programs (深度讲解)](https://leg100.github.io/en/posts/building-bubbletea-programs/)
- [bubbletea pager example (viewport)](https://github.com/charmbracelet/bubbletea/blob/main/examples/pager/main.go)
- [bubbletea performance docs](https://mintlify.com/charmbracelet/bubbletea/advanced/performance)
