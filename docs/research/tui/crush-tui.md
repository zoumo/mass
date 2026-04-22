# Charmbracelet Crush TUI 深度调研

> 仓库: https://github.com/charmbracelet/crush
> 调研日期: 2026-04-22
> 目的: 分析 Crush 的 UI 实现优势，为 MASS chat TUI 改进提供参考

## 1. 项目概览

Crush 是 Charmbracelet 开发的终端 AI 聊天客户端，基于 Bubble Tea v2 构建。核心结构：

```
internal/
├── ui/
│   ├── model/        # 主 TUI model、键绑定、布局
│   │   ├── ui.go     # 主状态机 (3662 行)
│   │   ├── keys.go   # KeyMap 定义
│   │   ├── chat.go   # Chat 消息处理
│   │   ├── header.go # Header 渲染
│   │   └── status.go # Status bar
│   ├── chat/         # 消息组件
│   │   ├── messages.go # MessageItem 接口与缓存
│   │   └── tools.go    # 工具调用渲染
│   ├── anim/         # 动画系统
│   ├── list/         # 懒加载列表
│   ├── styles/       # 样式系统
│   ├── diffview/     # Diff 渲染
│   ├── completions/  # @ 补全弹窗
│   └── dialog/       # 模态对话框
├── commands/         # Slash command 加载
└── app/              # 应用入口
```

## 2. Slash Command 系统

### 2.1 触发方式

两种入口：
- **`/` 键** — textarea 为空时按 `/` 触发 (`keys.go:132`)
- **`ctrl+p`** — 全局快捷键 (`keys.go:79`)

```go
// ui.go:1826
key.Matches(msg, m.keyMap.Editor.Commands) && m.textarea.Value() == ""
```

### 2.2 命令类型

三个 Tab 页签，通过 `tab`/`shift+tab` 切换：

| 类型 | 来源 | 前缀 |
|------|------|------|
| System | 内置硬编码 | 无 |
| User | `~/.config/crush/commands/*.md` 或 `.crush/commands/*.md` | `user:` / `project:` |
| MCP | MCP Server prompts | `{mcpName}:{promptName}` |

### 2.3 自定义命令格式

用户命令是普通 Markdown 文件，通过 `$VARIABLE_NAME` 模板变量定义参数：

```go
// commands/commands.go:17
var namedArgPattern = regexp.MustCompile(`\$([A-Z][A-Z0-9_]*)`)
```

加载路径优先级：
1. `$XDG_CONFIG_HOME/crush/commands/` → `user:` 前缀
2. `~/.crush/commands/` → `user:` 前缀
3. `{project}/.crush/commands/` → `project:` 前缀

文件结构直接映射为命令 ID：`commands/review/pr.md` → `user:review:pr`

### 2.4 对话框 UI

`dialog/commands.go` (562 行) 实现为 Dialog 接口：

```go
type Commands struct {
    selected       CommandType        // System/User/MCP tab
    input          textinput.Model    // 过滤输入框
    list           *list.FilterableList
    customCommands []commands.CustomCommand
    mcpPrompts     []commands.MCPPrompt
}
```

交互：
- 输入字符实时过滤命令列表
- `enter` 执行选中命令
- `esc` 关闭
- 支持 1 字符快捷键直接执行 (如 `n` → New Session)

### 2.5 System Commands 列表

动态生成，根据上下文条件显示/隐藏：

| 命令 | 条件 |
|------|------|
| New Session | 始终 |
| Switch Session | 始终 |
| Switch Model | 始终 |
| Summarize Session | 有 session |
| Toggle Thinking | 始终 |
| Toggle Sidebar | window width >= 120 |
| Open File Picker | 始终 |
| External Editor | 始终 |
| Toggle To-Dos | 有 todos |
| Toggle Yolo Mode | 始终 |
| Initialize Project | 始终 |
| Quit | 始终 |

### 2.6 执行流程

```
用户按 / 或 ctrl+p
    ↓
openCommandsDialog() → Dialog 入栈
    ↓
Commands.HandleMsg() 处理键盘事件
    ↓
选中命令 → 返回 ActionItem
    ↓
UI.Update() 分发 Action:
  - ActionRunCustomCommand  → 模板替换 → 作为 prompt 发送
  - ActionRunMCPPrompt      → GetMCPPrompt() → 作为 prompt 发送
  - ActionNewSession        → 创建新 session
  - ActionToggleThinking    → 切换思考模式
  ...
    ↓
dialog.CloseDialog()
```

## 3. @ 文件补全系统

### 3.1 触发

`@` 字符，且光标在行首或空白后：

```go
// ui.go:1840
msg.String() == "@" && !m.completionsOpen &&
    (curIdx == 0 || isWhitespace(curValue[curIdx-1]))
```

### 3.2 数据源

并行加载：
- **文件**: 递归扫描当前目录
- **MCP 资源**: 所有注册 MCP Server 的 resources

### 3.3 匹配排序

四级优先级：
1. `tierExactName` — 文件名精确匹配
2. `tierPrefixName` — 文件名前缀匹配
3. `tierPathSegment` — 路径段匹配
4. `tierFallback` — 模糊匹配

### 3.4 弹窗渲染

- 位置：编辑器上方，向上弹出
- 尺寸：宽 10-100，高 1-10，自适应
- 模糊匹配高亮显示
- 选中后替换 `@query` 并自动读取文件内容作为 attachment

## 4. 样式系统

### 4.1 语义色彩

`styles/styles.go` (44KB) 定义完整主题系统：

```
颜色层次:
- Primary / Secondary / Tertiary
- BgBase / BgBaseLighter / BgSubtle / BgOverlay
- FgBase / FgMuted / FgHalfMuted / FgSubtle
- Error / Warning / Info / Success
```

自动检测终端亮/暗背景 (`lipgloss.HasDarkBackground()`)，切换配色。

### 4.2 工具状态样式

```go
ToolCallPending   // 动画中
ToolCallSuccess   // 绿色
ToolCallError     // 红色
ToolCallCancelled // 灰色
```

每个工具调用独立 `anim.Anim` 实例，pending 时旋转，完成后停止。

### 4.3 响应式布局

```go
// 紧凑模式触发条件
width < 120 || height < 30
```

紧凑模式下：
- Sidebar 折叠
- Header 缩为单行
- Tool 输出单行摘要
- Detail 面板变为 overlay

## 5. 动画系统

`anim/anim.go` 实现 gradient spinner：

- **帧率**: 20 FPS (50ms/帧)
- **省略号动画**: 8 帧一周期 (400ms)
- **渐变色循环**: 两色之间线性插值
- **错开入场**: 字符按序延迟出现 (最多 1 秒偏移)
- **预渲染**: 10 帧缓存后循环
- **视口优化**: 只有可见 item 执行动画，滚出视口暂停

## 6. 列表与滚动

`list/list.go` — 懒加载列表：

- **按需渲染**: 只渲染可见 item
- **缓存**: 按 width 缓存渲染结果
- **Follow 模式**: 新消息自动滚到底部
- **滚动优化**: 支持行级、item 级、半页、全页滚动
- **鼠标**: 滚轮 5 行阈值，拖选，triple-click

## 7. Header / Status Bar

### Header

```
┌─────────────────────────────────────────────┐
│ CRUSH logo  ////  LSP ● Token 45%  cwd      │
└─────────────────────────────────────────────┘
```

- 紧凑模式单行，完整模式含 Sidebar (30 列宽)
- 显示 LSP 错误数、Token 用量百分比、工作目录

### Status Bar

底部单行，显示快捷键帮助 + 临时消息 overlay：

```go
type InfoMsg struct {
    Type    InfoType  // Error/Warn/Info/Success/Update
    Content string
}
// TTL: 5 秒后自动清除
```

## 8. Dialog 系统

栈式管理，支持多个对话框叠加：

```go
// dialog/dialog.go
type Overlay struct {
    stack []Dialog  // 后进先出
}
```

- 只有栈顶 Dialog 接收事件
- Dialog 实现 `HandleMsg()` / `Draw()` 接口
- 支持 `BringToFront()` 重新激活已有对话框

## 9. 与 MASS Chat TUI 对比

### 9.1 已复用的 Crush 能力

| 能力 | Crush 包 | MASS 使用 |
|------|----------|-----------|
| 动画 spinner | `ui/anim/` | ✅ 完整使用 |
| 懒加载列表 | `ui/list/` | ✅ 完整使用 |
| 样式主题 | `ui/styles/` | ✅ 大部分 |
| Markdown 渲染 | `ui/common/` | ✅ 部分 |
| Diff 查看 | `ui/diffview/` | ✅ 部分 |

### 9.2 Crush 有但 MASS 没有的功能

| 功能 | Crush 实现 | MASS 现状 | 优先级 |
|------|-----------|-----------|--------|
| **Slash commands** | `/` + `ctrl+p` 触发，Dialog 选择 | 无 | 高 |
| **@ 文件补全** | `@` 触发，弹窗选择，自动 attach | 无 | 中 |
| **Dialog 栈系统** | 通用 Dialog interface + Overlay | 无 | 高 |
| **响应式 compact 模式** | 自动 < 120x30 切换 | 无 | 中 |
| **Sidebar** | 30 列宽，显示 session/LSP/MCP 状态 | 无 | 低 |
| **Token 用量显示** | Header 中百分比 | 无 | 低 |
| **Prompt 历史导航** | 上下箭头 | 无 | 中 |
| **文本选择/复制** | 拖选 + triple-click | 无 | 低 |
| **临时信息消息** | Status bar + TTL 5s | 无 | 中 |
| **外部编辑器** | `ctrl+o` 打开 | 无 | 低 |
| **自定义命令** | Markdown 模板 + 变量 | 无 | 中 |
| **MCP prompt 集成** | 命令面板直接调用 | 无 | 中 |

### 9.3 MASS 有但 Crush 不适用的

- **Multi-agent 管理**: workspace/agentrun 概念
- **Permission 审批**: HITL 权限请求
- **Agent 间消息**: workspace send

### 9.4 UI 差距根因分析

Crush UI 更优的核心原因：

1. **层次化交互**: Dialog 栈 + 补全弹窗 + 主界面三层，MASS 只有主界面一层
2. **命令发现性**: `/` 和 `ctrl+p` 让功能可被发现，MASS 依赖用户记住快捷键
3. **响应式设计**: 自动适应终端尺寸，MASS 固定布局
4. **状态反馈**: Header 显示 token/LSP/连接状态，Status bar 显示临时消息，信息密度高
5. **动画精细度**: 按状态切换颜色、按可见性控制动画开销
6. **输入增强**: @ 补全、历史导航、外部编辑器，降低输入摩擦

## 10. 改进建议

### Phase 1: Slash Command (高优先)

参考 Crush 做法，在 `pkg/tui/chat/chat.go` 的 Enter 发送前拦截 `/` 前缀：

```
1. 定义 SlashCommand interface { Name, Description, Execute }
2. 注册内置命令: /help, /clear, /cancel, /exit
3. 输入框为空按 / 时打开命令选择 Dialog
4. 支持输入过滤
5. 后续扩展: 自定义命令、MCP prompt
```

### Phase 2: Dialog 系统 (高优先)

```
1. 实现 Dialog interface + Overlay 栈
2. 命令选择 Dialog
3. 后续: 模型选择、Session 切换等
```

### Phase 3: 输入增强 (中优先)

```
1. Prompt 历史 (上下箭头)
2. @ 文件补全
3. 临时 Info 消息 (Status bar + TTL)
```

### Phase 4: 响应式布局 (中优先)

```
1. Compact 模式检测
2. Header 信息密度提升
3. Sidebar (可选)
```
