# 减少 Shim 事件翻译过度 — 保留 ACP 原始数据

## 背景与目标

当前 `pkg/events/translator.go` 中的 `translate()` 函数对 ACP 事件做了过度翻译，导致大量有价值的信息被丢弃。上层消费者（TUI、API 客户端等）无法获取完整的事件数据来做自己的加工。

**ACP SDK 版本：** `github.com/coder/acp-go-sdk v0.6.4-0.20260227160919-584abe6abe22`

**问题清单：**

| ACP 原始类型 | 当前输出 | 丢失的数据 |
|---|---|---|
| `SessionUpdateToolCall` (10 个字段) | `ToolCallEvent{ID, Kind, Title}` 仅 3 个字段 | `Meta`、`Content`(diff/terminal)、`Locations`(file:line)、`RawInput`、`RawOutput`、`Status` |
| `SessionToolCallUpdate` (10 个字段) | `ToolResultEvent{ID, Status}` 仅 2 个字段 | `Meta`、`Content`、`Locations`、`RawInput`、`RawOutput`、`Kind`、`Title` |
| `ContentBlock`（5 个变体） | 仅提取 `.Text.Text` 字符串 | `Image`、`Audio`、`ResourceLink`、`Resource` 变体；所有变体的 `Meta`、`Annotations` |
| `AvailableCommandsUpdate` | 静默丢弃 (`return nil`) | 全部命令/工具列表、`Meta` |
| `CurrentModeUpdate` | 静默丢弃 | 当前模式 ID、`Meta` |
| `ConfigOptionUpdate` | 静默丢弃 | 配置选项列表（`SessionConfigOption`）、`Meta` |
| `SessionInfoUpdate` | 静默丢弃 | 会话标题、更新时间、`Meta` |
| `UsageUpdate` | 静默丢弃 | 费用（`Cost`）、`Size`、`Used`、`Meta` |

**目标：** 尽可能保留 ACP 原始数据以便上层加工，同时保留我们自己附加的字段（turnId、streamSeq 等），仅做合理的删减。

## 方案

### 设计原则

1. **镜像 ACP wire shape**：OAR 镜像类型的 JSON 序列化形状必须与 ACP SDK marshal 结果一致（仅省略 `sessionUpdate` 鉴别器字段）。union 类型使用 flat JSON shape + 自定义 `MarshalJSON`/`UnmarshalJSON`，不引入 ACP 不存在的嵌套层级
2. **保留 `_meta` 扩展点**：所有 ACP 原始结构中含 `_meta` 的类型，镜像类型均保留 `Meta map[string]any`
3. **允许增加自定义字段**：turnId、streamSeq 等 OAR 附加字段继续保留
4. **保持 clean-break**：不直接暴露 ACP SDK 类型到 wire surface，而是定义完整的镜像类型，ACP 升级不会破坏外部 API
5. **所有 SessionUpdate 分支都必须翻译**：不允许静默丢弃任何 SDK 已有的事件分支

### ACP union 的 JSON wire shape 参考

经确认，ACP SDK v0.6.4 中所有 union 类型均使用 **flat JSON shape**：

| Union 类型 | 鉴别器 | JSON shape 示例 |
|---|---|---|
| `ContentBlock` | `type` 字段 | `{"type":"text","text":"hello","_meta":{...}}` |
| `ToolCallContent` | `type` 字段 | `{"type":"diff","path":"...","newText":"...","_meta":{...}}` |
| `AvailableCommandInput` | 无（字段模式匹配） | `{"hint":"...","_meta":{...}}` |
| `SessionConfigOption` | `type` 字段 | `{"type":"select","id":"...","name":"...","currentValue":"...",...}` |
| `EmbeddedResourceResource` | 无（`text`/`blob` 字段存在性） | `{"uri":"...","text":"..."}` 或 `{"uri":"...","blob":"..."}` |
| `SessionConfigSelectOptions` | 无（数组元素结构） | `[{"name":"...","value":"..."}]` 或 `[{"group":"...","options":[...]}]` |
| `SessionConfigOptionCategory` | 无（原始字符串） | `"other"` |

OAR 镜像类型必须产出相同的 JSON shape。

### 步骤 1: 定义公共支撑类型

在 `pkg/events/types.go` 中新增所有镜像支撑类型。Go 侧使用多变体 struct + `json:"-"` 标签 + 自定义 `MarshalJSON`/`UnmarshalJSON` 实现 flat JSON shape。

```go
// ── Annotations ──

// Annotations mirrors acp.Annotations.
type Annotations struct {
    Meta         map[string]any `json:"_meta,omitempty"`
    Audience     []string       `json:"audience,omitempty"`     // "user" | "assistant"
    LastModified *string        `json:"lastModified,omitempty"`
    Priority     *float64       `json:"priority,omitempty"`
}

// ── ContentBlock ──

// ContentBlock mirrors acp.ContentBlock — a discriminated union of 5 content types.
// JSON wire shape is FLAT: {"type":"text","text":"hello","_meta":{...}}
// Go side uses variant pointers with json:"-" + custom MarshalJSON/UnmarshalJSON.
type ContentBlock struct {
    Text         *TextContent         `json:"-"`
    Image        *ImageContent        `json:"-"`
    Audio        *AudioContent        `json:"-"`
    ResourceLink *ResourceLinkContent `json:"-"`
    Resource     *ResourceContent     `json:"-"`
}

// MarshalJSON produces the flat ACP wire shape with "type" discriminator.
// UnmarshalJSON reads flat JSON, dispatching by "type" field.
// (implementation details omitted for brevity — see step 7)

// Per-variant structs — NO Type field (added by union MarshalJSON):

type TextContent struct {
    Meta        map[string]any `json:"_meta,omitempty"`
    Text        string         `json:"text"`
    Annotations *Annotations   `json:"annotations,omitempty"`
}

type ImageContent struct {
    Meta        map[string]any `json:"_meta,omitempty"`
    Data        string         `json:"data"`
    MimeType    string         `json:"mimeType"`
    URI         *string        `json:"uri,omitempty"`
    Annotations *Annotations   `json:"annotations,omitempty"`
}

type AudioContent struct {
    Meta        map[string]any `json:"_meta,omitempty"`
    Data        string         `json:"data"`
    MimeType    string         `json:"mimeType"`
    Annotations *Annotations   `json:"annotations,omitempty"`
}

type ResourceLinkContent struct {
    Meta        map[string]any `json:"_meta,omitempty"`
    URI         string         `json:"uri"`
    Name        string         `json:"name"`
    Description *string        `json:"description,omitempty"`
    MimeType    *string        `json:"mimeType,omitempty"`
    Title       *string        `json:"title,omitempty"`
    Size        *int           `json:"size,omitempty"`
    Annotations *Annotations   `json:"annotations,omitempty"`
}

type ResourceContent struct {
    Meta        map[string]any   `json:"_meta,omitempty"`
    Resource    EmbeddedResource `json:"resource"`
    Annotations *Annotations     `json:"annotations,omitempty"`
}

// ── EmbeddedResource ──

// EmbeddedResource mirrors acp.EmbeddedResourceResource — union of text/blob.
// JSON wire shape has NO "type" discriminator — discriminated by text/blob field presence.
// E.g.: {"uri":"...","text":"content"} or {"uri":"...","blob":"base64..."}
type EmbeddedResource struct {
    TextResource *TextResourceContents `json:"-"`
    BlobResource *BlobResourceContents `json:"-"`
}

// MarshalJSON/UnmarshalJSON: flat shape, no type field.

type TextResourceContents struct {
    Meta     map[string]any `json:"_meta,omitempty"`
    URI      string         `json:"uri"`
    MimeType *string        `json:"mimeType,omitempty"`
    Text     string         `json:"text"`
}

type BlobResourceContents struct {
    Meta     map[string]any `json:"_meta,omitempty"`
    URI      string         `json:"uri"`
    MimeType *string        `json:"mimeType,omitempty"`
    Blob     string         `json:"blob"`
}

// ── ToolCall 支撑类型 ──

// ToolCallContent mirrors acp.ToolCallContent — union of content/diff/terminal.
// JSON wire shape is FLAT with "type" discriminator.
type ToolCallContent struct {
    Content  *ToolCallContentContent  `json:"-"`
    Diff     *ToolCallContentDiff     `json:"-"`
    Terminal *ToolCallContentTerminal `json:"-"`
}

// MarshalJSON/UnmarshalJSON: flat shape with "type" field.

type ToolCallContentContent struct {
    Meta    map[string]any `json:"_meta,omitempty"`
    Content ContentBlock   `json:"content"`
}

type ToolCallContentDiff struct {
    Meta    map[string]any `json:"_meta,omitempty"`
    Path    string         `json:"path"`
    OldText *string        `json:"oldText,omitempty"`
    NewText string         `json:"newText"`
}

type ToolCallContentTerminal struct {
    Meta       map[string]any `json:"_meta,omitempty"`
    TerminalID string         `json:"terminalId"`
}

type ToolCallLocation struct {
    Meta map[string]any `json:"_meta,omitempty"`
    Path string         `json:"path"`
    Line *int           `json:"line,omitempty"`
}

// ── AvailableCommand 支撑类型 ──

type AvailableCommand struct {
    Meta        map[string]any        `json:"_meta,omitempty"`
    Name        string                `json:"name"`
    Description string                `json:"description"`
    Input       *AvailableCommandInput `json:"input,omitempty"`
}

// AvailableCommandInput mirrors acp.AvailableCommandInput.
// JSON wire shape has NO "type" — discriminated by field presence.
// Currently single variant: {"hint":"...","_meta":{...}}
type AvailableCommandInput struct {
    Unstructured *UnstructuredCommandInput `json:"-"`
}

// MarshalJSON/UnmarshalJSON: flat shape, delegates to active variant.

type UnstructuredCommandInput struct {
    Meta map[string]any `json:"_meta,omitempty"`
    Hint string         `json:"hint"`
}

// ── ConfigOption 支撑类型 ──

// ConfigOption mirrors acp.SessionConfigOption.
// JSON wire shape is FLAT with "type" discriminator.
// Currently single variant: {"type":"select","id":"...","name":"...","currentValue":"...",...}
type ConfigOption struct {
    Select *ConfigOptionSelect `json:"-"`
}

// MarshalJSON/UnmarshalJSON: flat shape with "type" field.

type ConfigOptionSelect struct {
    Meta         map[string]any        `json:"_meta,omitempty"`
    ID           string                `json:"id"`
    Name         string                `json:"name"`
    CurrentValue string                `json:"currentValue"`
    Description  *string               `json:"description,omitempty"`
    Category     *string               `json:"category,omitempty"` // ACP CategoryOther is a raw string
    Options      ConfigSelectOptions   `json:"options"`
}

// ConfigSelectOptions mirrors acp.SessionConfigSelectOptions — union of ungrouped/grouped.
// JSON wire shape is a bare array — discriminated by element structure.
type ConfigSelectOptions struct {
    Ungrouped []ConfigSelectOption `json:"-"`
    Grouped   []ConfigSelectGroup  `json:"-"`
}

// MarshalJSON/UnmarshalJSON: bare array shape.

type ConfigSelectOption struct {
    Meta        map[string]any `json:"_meta,omitempty"`
    Name        string         `json:"name"`
    Value       string         `json:"value"`
    Description *string        `json:"description,omitempty"`
}

type ConfigSelectGroup struct {
    Meta    map[string]any       `json:"_meta,omitempty"`
    Group   string               `json:"group"`
    Name    string               `json:"name"`
    Options []ConfigSelectOption `json:"options"`
}

// ── Cost 支撑类型 ──

type Cost struct {
    Amount   float64 `json:"amount"`
    Currency string  `json:"currency"`
}
```

### 步骤 2: 丰富 ToolCallEvent

**当前：**
```go
type ToolCallEvent struct {
    ID    string `json:"id"`
    Kind  string `json:"kind"`
    Title string `json:"title"`
}
```

**改为：**
```go
type ToolCallEvent struct {
    Meta      map[string]any     `json:"_meta,omitempty"`
    ID        string             `json:"id"`
    Kind      string             `json:"kind"`
    Title     string             `json:"title"`
    Status    string             `json:"status,omitempty"`
    Content   []ToolCallContent  `json:"content,omitempty"`
    Locations []ToolCallLocation `json:"locations,omitempty"`
    RawInput  any                `json:"rawInput,omitempty"`
    RawOutput any                `json:"rawOutput,omitempty"`
}
```

### 步骤 3: 丰富 ToolResultEvent

**当前：**
```go
type ToolResultEvent struct {
    ID     string `json:"id"`
    Status string `json:"status"`
}
```

**改为：**
```go
type ToolResultEvent struct {
    Meta      map[string]any     `json:"_meta,omitempty"`
    ID        string             `json:"id"`
    Status    string             `json:"status"`
    Kind      string             `json:"kind,omitempty"`
    Title     string             `json:"title,omitempty"`
    Content   []ToolCallContent  `json:"content,omitempty"`
    Locations []ToolCallLocation `json:"locations,omitempty"`
    RawInput  any                `json:"rawInput,omitempty"`
    RawOutput any                `json:"rawOutput,omitempty"`
}
```

### 步骤 4: 丰富 text/thinking/user_message 事件

```go
type TextEvent struct {
    Text    string       `json:"text"`              // 便捷字段：保留向后兼容
    Content *ContentBlock `json:"content,omitempty"` // 完整内容块
}

type ThinkingEvent struct {
    Text    string       `json:"text"`
    Content *ContentBlock `json:"content,omitempty"`
}

type UserMessageEvent struct {
    Text    string       `json:"text"`
    Content *ContentBlock `json:"content,omitempty"`
}
```

`text` 字段保留向后兼容（从 ContentBlock.Text.Text 提取），`content` 字段携带完整数据。

### 步骤 5: 为被丢弃的事件新增类型（全部 5 个分支）

```go
// AvailableCommandsEvent carries the current list of available commands/tools.
type AvailableCommandsEvent struct {
    Meta     map[string]any     `json:"_meta,omitempty"`
    Commands []AvailableCommand `json:"commands"`
}

func (AvailableCommandsEvent) eventType() string { return api.EventTypeAvailableCommands }

// CurrentModeEvent carries mode changes.
type CurrentModeEvent struct {
    Meta   map[string]any `json:"_meta,omitempty"`
    ModeID string         `json:"modeId"`
}

func (CurrentModeEvent) eventType() string { return api.EventTypeCurrentMode }

// ConfigOptionEvent carries config option changes.
type ConfigOptionEvent struct {
    Meta          map[string]any `json:"_meta,omitempty"`
    ConfigOptions []ConfigOption `json:"configOptions"`
}

func (ConfigOptionEvent) eventType() string { return api.EventTypeConfigOption }

// SessionInfoEvent carries session metadata updates.
type SessionInfoEvent struct {
    Meta      map[string]any `json:"_meta,omitempty"`
    Title     *string        `json:"title,omitempty"`
    UpdatedAt *string        `json:"updatedAt,omitempty"`
}

func (SessionInfoEvent) eventType() string { return api.EventTypeSessionInfo }

// UsageEvent carries token/API usage statistics.
type UsageEvent struct {
    Meta map[string]any `json:"_meta,omitempty"`
    Cost *Cost          `json:"cost,omitempty"`
    Size int            `json:"size"`
    Used int            `json:"used"`
}

func (UsageEvent) eventType() string { return api.EventTypeUsage }
```

在 `api/` 包注册新的 event type 常量：
```go
const (
    EventTypeAvailableCommands = "available_commands"
    EventTypeCurrentMode       = "current_mode"
    EventTypeUsage             = "usage"
    EventTypeSessionInfo       = "session_info"
    EventTypeConfigOption      = "config_option"
)
```

### 步骤 6: 更新 translate() 函数

所有 `SessionUpdate` 分支都必须翻译，不允许 `return nil`：

```go
func translate(n acp.SessionNotification) Event {
    u := n.Update
    switch {
    case u.AgentMessageChunk != nil:
        c := u.AgentMessageChunk
        return TextEvent{
            Text:    safeBlockText(c.Content),
            Content: convertContentBlock(c.Content),
        }
    case u.AgentThoughtChunk != nil:
        c := u.AgentThoughtChunk
        return ThinkingEvent{
            Text:    safeBlockText(c.Content),
            Content: convertContentBlock(c.Content),
        }
    case u.UserMessageChunk != nil:
        c := u.UserMessageChunk
        return UserMessageEvent{
            Text:    safeBlockText(c.Content),
            Content: convertContentBlock(c.Content),
        }
    case u.ToolCall != nil:
        tc := u.ToolCall
        return ToolCallEvent{
            Meta:      tc.Meta,
            ID:        string(tc.ToolCallId),
            Kind:      string(tc.Kind),
            Title:     tc.Title,
            Status:    string(tc.Status),
            Content:   convertToolCallContents(tc.Content),
            Locations: convertLocations(tc.Locations),
            RawInput:  tc.RawInput,
            RawOutput: tc.RawOutput,
        }
    case u.ToolCallUpdate != nil:
        tcu := u.ToolCallUpdate
        return ToolResultEvent{
            Meta:      tcu.Meta,
            ID:        string(tcu.ToolCallId),
            Status:    safeStatus(tcu.Status),
            Kind:      safeToolKind(tcu.Kind),
            Title:     safeStringPtr(tcu.Title),
            Content:   convertToolCallContents(tcu.Content),
            Locations: convertLocations(tcu.Locations),
            RawInput:  tcu.RawInput,
            RawOutput: tcu.RawOutput,
        }
    case u.Plan != nil:
        return PlanEvent{Entries: u.Plan.Entries}
    case u.AvailableCommandsUpdate != nil:
        ac := u.AvailableCommandsUpdate
        return AvailableCommandsEvent{
            Meta:     ac.Meta,
            Commands: convertCommands(ac.AvailableCommands),
        }
    case u.CurrentModeUpdate != nil:
        cm := u.CurrentModeUpdate
        return CurrentModeEvent{
            Meta:   cm.Meta,
            ModeID: string(cm.CurrentModeId),
        }
    case u.ConfigOptionUpdate != nil:
        co := u.ConfigOptionUpdate
        return ConfigOptionEvent{
            Meta:          co.Meta,
            ConfigOptions: convertConfigOptions(co.ConfigOptions),
        }
    case u.SessionInfoUpdate != nil:
        si := u.SessionInfoUpdate
        return SessionInfoEvent{
            Meta:      si.Meta,
            Title:     si.Title,
            UpdatedAt: si.UpdatedAt,
        }
    case u.UsageUpdate != nil:
        uu := u.UsageUpdate
        return UsageEvent{
            Meta: uu.Meta,
            Cost: convertCost(uu.Cost),
            Size: uu.Size,
            Used: uu.Used,
        }
    default:
        return ErrorEvent{Msg: "unknown session update variant"}
    }
}
```

### 步骤 7: 实现 union 类型的 MarshalJSON/UnmarshalJSON 和 convert 函数

#### 7a: Union MarshalJSON/UnmarshalJSON

所有 union 类型必须实现自定义 JSON 序列化以产出 flat ACP wire shape。以 `ContentBlock` 为例：

```go
func (c ContentBlock) MarshalJSON() ([]byte, error) {
    switch {
    case c.Text != nil:
        type wrapper struct {
            Type string `json:"type"`
            TextContent
        }
        return json.Marshal(wrapper{Type: "text", TextContent: *c.Text})
    case c.Image != nil:
        type wrapper struct {
            Type string `json:"type"`
            ImageContent
        }
        return json.Marshal(wrapper{Type: "image", ImageContent: *c.Image})
    // ... Audio, ResourceLink, Resource 同理
    default:
        return nil, fmt.Errorf("events: empty ContentBlock")
    }
}

func (c *ContentBlock) UnmarshalJSON(data []byte) error {
    var raw struct { Type string `json:"type"` }
    if err := json.Unmarshal(data, &raw); err != nil {
        return err
    }
    switch raw.Type {
    case "text":
        c.Text = &TextContent{}
        return json.Unmarshal(data, c.Text)
    case "image":
        c.Image = &ImageContent{}
        return json.Unmarshal(data, c.Image)
    // ... Audio, ResourceLink, Resource 同理
    default:
        return fmt.Errorf("events: unknown ContentBlock type %q", raw.Type)
    }
}
```

同理为以下 union 类型实现自定义 marshal/unmarshal：
- `ToolCallContent`：`type` 鉴别器，content/diff/terminal 三变体
- `ConfigOption`：`type` 鉴别器，目前仅 select 变体
- `AvailableCommandInput`：无 `type`，按 `hint` 字段存在性匹配
- `EmbeddedResource`：无 `type`，按 `text`/`blob` 字段存在性匹配
- `ConfigSelectOptions`：无 `type`，按数组元素结构匹配

#### 7b: convert 辅助函数

在 `translator.go` 中增加完整的 ACP → OAR 镜像类型转换函数：

- `convertContentBlock(acp.ContentBlock) *ContentBlock` — 处理所有 5 个变体，保留 Meta/Annotations
- `convertAnnotations(*acp.Annotations) *Annotations`
- `convertToolCallContents([]acp.ToolCallContent) []ToolCallContent` — 处理 content/diff/terminal 三个变体，terminal 变体保留 Meta
- `convertLocations([]acp.ToolCallLocation) []ToolCallLocation`
- `convertCommands([]acp.AvailableCommand) []AvailableCommand` — 保留 Input 对象结构
- `convertAvailableCommandInput(*acp.AvailableCommandInput) *AvailableCommandInput`
- `convertConfigOptions([]acp.SessionConfigOption) []ConfigOption` — 完整镜像 select 变体所有字段
- `convertConfigSelectOptions(acp.SessionConfigSelectOptions) ConfigSelectOptions`
- `convertCost(*acp.Cost) *Cost`
- `convertEmbeddedResource(acp.EmbeddedResourceResource) EmbeddedResource` — 处理 text/blob 两个变体
- `safeStringPtr(*string) string`
- `safeToolKind(*acp.ToolKind) string`

### 步骤 8: 更新 envelope 反序列化

`decodeEventPayload()` 需要增加所有新 event type 的 case：
- `EventTypeAvailableCommands` → `AvailableCommandsEvent`
- `EventTypeCurrentMode` → `CurrentModeEvent`
- `EventTypeConfigOption` → `ConfigOptionEvent`
- `EventTypeSessionInfo` → `SessionInfoEvent`
- `EventTypeUsage` → `UsageEvent`

### 步骤 9: 更新 docs/design

**`docs/design/runtime/runtime-spec.md`**：在"事件类型"表中新增：

| 事件类型 | 来源 | 说明 |
|---|---|---|
| `AvailableCommandsEvent` | ACP `available_commands_update` | 可用命令/工具列表更新 |
| `CurrentModeEvent` | ACP `current_mode_update` | 当前操作模式变更 |
| `ConfigOptionEvent` | ACP `config_option_update` | 配置选项变更 |
| `SessionInfoEvent` | ACP `session_info_update` | 会话元数据更新 |
| `UsageEvent` | ACP `usage_update` | Token/API 用量和费用统计 |

同时补充说明：所有事件类型完整保留 ACP 原始字段（包括 `_meta`），仅省略 ACP wire 格式的 `sessionUpdate` 鉴别器字段。

**`docs/design/runtime/shim-rpc-spec.md`**：在"Typed Event 类型"表中新增 5 行，并更新 `tool_call` 和 `tool_result` 的 payload 字段描述为完整字段列表。

### 步骤 10: 更新测试

**测试矩阵：**

| # | 测试场景 | 验证点 |
|---|---|---|
| 1 | ToolCall 完整翻译 | Meta、Content（含 diff/terminal/content 三变体）、Locations、RawInput、RawOutput、Status 均保留 |
| 2 | ToolCallUpdate 完整翻译 | Meta、nullable Status/Kind/Title、Content、Locations、RawInput、RawOutput 均保留 |
| 3 | ContentBlock — Text 变体 | Text + Annotations + Meta 均保留；便捷 `text` 字段仍填充 |
| 4 | ContentBlock — Image 变体 | Data、MimeType、URI、Annotations、Meta 均保留 |
| 5 | ContentBlock — Audio 变体 | Data、MimeType、Annotations、Meta 均保留 |
| 6 | ContentBlock — ResourceLink 变体 | URI、Name、Description、MimeType、Title、Size、Annotations、Meta 均保留 |
| 7 | ContentBlock — Resource 变体 | EmbeddedResource（text/blob 两变体）、Annotations、Meta 均保留 |
| 8 | AvailableCommandsUpdate 翻译 | Commands 列表含 Name/Description/Input 对象结构（Unstructured.Hint）、Meta |
| 9 | CurrentModeUpdate 翻译 | ModeID、Meta |
| 10 | ConfigOptionUpdate 翻译 | ConfigOptions 列表含 ID/Name/CurrentValue/Description/Category/Options（ungrouped + grouped）、Meta |
| 11 | SessionInfoUpdate 翻译 | Title、UpdatedAt、Meta |
| 12 | UsageUpdate 翻译 | Cost（Amount/Currency）、Size、Used、Meta |
| 13 | RawInput/RawOutput JSON round-trip | 任意 JSON 结构经翻译 + 序列化 + 反序列化后完全保留 |
| 14 | Envelope decode 所有新 event type | decodeEventPayload 能正确恢复全部 17 种 event type |
| 15 | 向后兼容 | 仅含原有字段的旧格式 JSON 仍能正确反序列化 |
| 16 | **ContentBlock JSON shape 对齐** | 5 个变体分别构造 ACP SDK 对象 → marshal → 与 OAR 镜像 marshal 结果对比 JSON key layout 一致（除 `sessionUpdate`） |
| 17 | **ToolCallContent JSON shape 对齐** | content/diff/terminal 三变体的 ACP marshal 与 OAR mirror marshal JSON key layout 一致 |
| 18 | **AvailableCommandInput JSON shape 对齐** | unstructured 变体的 ACP marshal 与 OAR mirror marshal 一致（flat `{"hint":"..."}` 无嵌套） |
| 19 | **ConfigOption JSON shape 对齐** | select 变体及 options（ungrouped/grouped）的 ACP marshal 与 OAR mirror marshal JSON key layout 一致 |
| 20 | **EmbeddedResource JSON shape 对齐** | text/blob 变体的 ACP marshal 与 OAR mirror marshal 一致（无 `type` 字段，按 text/blob 字段存在性区分） |

**最终构建验证：** `make build` 必须通过。

### 合理删减

以下字段**不保留**，属于合理删减：
- `SessionUpdate` 类型鉴别器字符串（`sessionUpdate` 字段）— 这是 ACP wire 格式的内部实现，我们的 `TypedEvent.Type` 已经承担了这个角色
- 各事件 chunk 上的 `sessionUpdate` 字符串字段 — 同上

### 涉及文件

| 文件 | 改动 |
|---|---|
| `pkg/events/types.go` | 丰富现有事件类型，新增事件类型和全部支撑类型 |
| `pkg/events/translator.go` | 更新 `translate()` 覆盖所有分支，新增全部 convert 辅助函数 |
| `pkg/events/envelope.go` | `decodeEventPayload()` 增加 5 个新类型 case |
| `api/types.go` | 新增 5 个 event type 常量 |
| `docs/design/runtime/runtime-spec.md` | 事件类型表新增 5 行，补充 payload 保留策略说明 |
| `docs/design/runtime/shim-rpc-spec.md` | Typed Event 类型表新增 5 行，更新 tool_call/tool_result payload 描述 |
| `pkg/events/*_test.go` | 按测试矩阵新增和更新测试 |

### 向后兼容性

- `TextEvent.Text`、`ThinkingEvent.Text`、`UserMessageEvent.Text` 保留原有字段，新增 `Content` 字段
- `ToolCallEvent.{ID, Kind, Title}` 保留，新增其他字段（`omitempty`）
- `ToolResultEvent.{ID, Status}` 保留，新增其他字段（`omitempty`）
- 已有的 JSON 消费者不会被新增的 `omitempty` 字段破坏

## 风险与取舍

1. **事件日志体积增大**：保留 `RawInput`/`RawOutput`/`Content`/`_meta` 会显著增加 JSONL 日志大小。取舍：数据完整性 > 存储成本，上层可以按需过滤。
2. **PlanEvent 直接依赖 `acp.PlanEntry`**：当前 `PlanEvent` 直接使用 ACP SDK 类型。为保持一致性，也应改为镜像类型，但这是独立的后续工作，本次不改动。

## 审查记录

### codex 第1轮

#### ✅ 认可项

- 目标方向正确：当前 `translate()` 确实把 ACP 的 rich event 压缩成了过窄的 OAR 事件，尤其是 tool call/content block/commands/mode 更新，方案提出“镜像 ACP 结构 + 保留 OAR 附加 metadata”的方向符合减少 shim 翻译的目标。
- 保持现有便捷字段的兼容策略合理：`TextEvent.Text`、`ToolCallEvent.ID/Kind/Title`、`ToolResultEvent.ID/Status` 继续保留，新增字段用 `omitempty`，能降低对现有消费者的影响。
- 不直接暴露 ACP SDK 类型的 clean-break 原则正确：`api` 包当前无外部依赖，事件 wire surface 应继续由 OAR 自己定义，避免 ACP SDK 版本变化直接泄漏给外部消费者。

#### ❌ 问题项

1. **ConfigOptionUpdate / SessionInfoUpdate / UsageUpdate 的处理与当前 SDK 事实不符。**  
   问题：方案第 5 步写明这些分支“不存在于 ACP SDK v0.6.3 的 SessionUpdate union”，因此暂不翻译。但当前仓库 `go.mod` 使用的是 `github.com/coder/acp-go-sdk v0.6.4-0.20260227160919-584abe6abe22`，该版本的 `SessionUpdate` 已包含 `ConfigOptionUpdate`、`SessionInfoUpdate`、`UsageUpdate` 三个分支。  
   为什么是问题：继续不处理会保留现有静默丢弃问题的一部分，且方案前面的“问题清单”和“步骤 4”已经把它们列为需要新增的事件类型，前后矛盾。  
   期望解决：修订第 5/6/7/8 步，明确实现并测试这三个分支；新增事件类型的 `eventType()`、`decodeEventPayload()` case，以及对应 converter。若暂不做，必须从目标和问题清单中移除并说明为什么本次允许继续丢弃。

2. **`_meta` / `Meta` 的删减理由不足，并且与“保留原始字段”的目标冲突。**  
   问题：问题清单明确指出 ToolCall/ToolCallUpdate 丢失了 `Meta`，但“合理删减”又决定不保留 `_meta`，理由是“当前无实际使用”。ACP SDK 对 `_meta` 的注释是扩展点，不能假设 key 的含义；这不等于它可以被无条件丢弃。  
   为什么是问题：本方案的目标是让上层消费者自行加工原始 ACP 数据，`_meta` 正是未来扩展和 provider-specific 信息的承载位置。现在丢弃会继续制造不可恢复的信息损失，且和表格中列出的丢失项不一致。  
   期望解决：默认在镜像类型中保留 `Meta map[string]any "json:\"_meta,omitempty\""`，至少覆盖 ToolCall、ToolCallUpdate、ToolCallContent variants、ContentBlock variants、AvailableCommands、CurrentMode、ConfigOption、SessionInfo、Usage 等 ACP 原始结构。若仍要删减，需要给出比“当前无实际使用”更强的 contract-level 理由，并从目标中移除“近完整保留”表述。

3. **ContentBlock 镜像不完整，仍会丢字段。**  
   问题：方案只在 `TextContent` 上放了 `Annotations`，但 ACP 的 Text/Image/Audio/ResourceLink/Resource 五个变体都有 `annotations` 和 `_meta`；`Annotations` 还是空结构占位；`ResourceContent.Resource any` 没有定义 `EmbeddedResourceResource` 的镜像结构。  
   为什么是问题：这会导致实现后仍不能“保留所有 5 个变体”的完整数据，尤其是非文本内容的 annotations/resource payload 仍不可验证、不可稳定解码。`any` 也削弱了 wire contract，后续消费者无法依赖结构。  
   期望解决：把 ContentBlock 五个变体按当前 ACP SDK 字段完整镜像，至少包括 `Meta`、`Annotations`、各 variant 的 `Type` 或统一 discriminator 策略，并为 embedded resource union 定义明确镜像类型；测试应覆盖 image/audio/resource_link/resource 和 annotations。

4. **AvailableCommand 的镜像结构不符合 ACP 当前结构。**  
   问题：方案定义 `AvailableCommand{ Name, Description, InputHint }`，但 ACP 当前结构是 `AvailableCommand{Meta, Description, Input *AvailableCommandInput, Name}`，其中 `AvailableCommandInput` 目前有 `Unstructured` variant，字段是 `input.hint`，不是顶层 `inputHint`。  
   为什么是问题：这又做了一次翻译和扁平化，会破坏“减少 shim 翻译”的目标，也会让 wire schema 偏离 ACP 原始语义。  
   期望解决：定义 `AvailableCommandInput` / `UnstructuredCommandInput` 镜像类型，保留 `input` 对象结构和 `Meta`，不要把它压成 `inputHint`。

5. **缺少 docs/design 同步更新，违反仓库开发约束。**  
   问题：`docs/design/runtime/runtime-spec.md` 的 typed event list 目前只列出原有事件类型，没有 `available_commands/current_mode/config_option/session_info/usage`，也没有 rich content/tool payload 的契约说明。方案“涉及文件”没有列入 `docs/design`。  
   为什么是问题：AGENTS.md 明确要求代码变更必须与 `docs/design` 对齐；如果只改实现和计划文档，设计文档会落后，后续实现缺少可审计的 API contract。  
   期望解决：把 `docs/design/runtime/runtime-spec.md`（必要时还有 shim RPC spec）纳入改动范围，补充新增事件类型及 payload 保留策略。

6. **测试计划还不够可验证。**  
   问题：当前测试项只笼统写“验证丰富后的事件字段”和“新增事件类型”，没有覆盖关键边界：非 text ContentBlock、tool content 的 content/diff/terminal 三个 variant、nullable status/kind/title、RawInput/RawOutput JSON round-trip、新增三类 SDK 已有更新分支，以及 envelope decode 对所有新 event type 的恢复。  
   为什么是问题：这些正是当前 translator 过度翻译造成的信息损失点；测试如果不逐项覆盖，容易实现成“看起来新增字段，实际仍丢数据”。  
   期望解决：在步骤 8 明确上述测试矩阵，并要求 `make build` 作为最终构建验证。

### claude-code 第2轮

逐条回应 codex 第 1 轮反馈：

1. **ConfigOptionUpdate / SessionInfoUpdate / UsageUpdate** — **接受**。已确认 go.mod 中 ACP SDK 为 v0.6.4，`SessionUpdate` 确实包含这三个分支。已在步骤 5 新增 `ConfigOptionEvent`、`SessionInfoEvent`、`UsageEvent` 三个完整事件类型，步骤 6 的 `translate()` 覆盖全部分支，步骤 7 新增对应 converter，步骤 8 注册 decode case，步骤 10 测试矩阵包含三者。已删除"不存在于 SDK"的错误表述。

2. **`_meta` / Meta 保留** — **接受**。已在所有镜像类型中添加 `Meta map[string]any "json:\"_meta,omitempty\""`，覆盖 ToolCallEvent、ToolResultEvent、ToolCallContent（DiffContent、TerminalRef 除外——ACP SDK 中 TerminalRef 无 Meta）、ContentBlock 五个变体、AvailableCommand、CurrentModeEvent、ConfigOptionEvent、SessionInfoEvent、UsageEvent、ToolCallLocation、EmbeddedResource。已从"合理删减"中移除 `_meta`。

3. **ContentBlock 镜像完整性** — **接受**。已：(a) 所有 5 个变体均添加 `Meta` 和 `Annotations`；(b) `Annotations` 定义了完整字段（Meta、Audience、LastModified、Priority）；(c) `ResourceContent.Resource` 改为 `EmbeddedResource` 明确镜像类型，区分 text/blob 两个变体。测试矩阵明确覆盖所有 5 个 ContentBlock 变体。

4. **AvailableCommand 结构** — **接受**。已改为保留 `Input *AvailableCommandInput` 对象结构，定义 `AvailableCommandInput` / `UnstructuredCommandInput` 镜像类型，不再扁平化为 `inputHint`。

5. **docs/design 同步更新** — **接受**。已新增步骤 9，涉及文件表新增 `docs/design/runtime/runtime-spec.md` 和 `docs/design/runtime/shim-rpc-spec.md`，明确更新内容。

6. **测试计划** — **接受**。已在步骤 10 给出完整测试矩阵（14 个场景），覆盖所有 codex 指出的边界：非 text ContentBlock、tool content 三变体、nullable 字段、RawInput/RawOutput round-trip、三个新增 SDK 分支、envelope decode 全部新 event type、向后兼容。最终验证使用 `make build`。

### codex 第2轮

#### ✅ 认可项

- 第 1 轮指出的 SDK 版本事实问题已修正：方案明确当前使用 `acp-go-sdk v0.6.4-0.20260227160919-584abe6abe22`，并将 `ConfigOptionUpdate`、`SessionInfoUpdate`、`UsageUpdate` 纳入事件类型、translator、decode 和测试范围。
- `_meta` 的保留策略已经回到正确方向：主要事件和支撑类型都计划携带 `Meta map[string]any`，不再以“当前无实际使用”为由丢弃扩展点。
- docs/design 同步和测试矩阵已明确纳入方案，符合仓库 `AGENTS.md` 对设计一致性和 `make build` 验证的要求。

#### ❌ 问题项

1. **多个 union 的 JSON 形状仍然不是 ACP wire shape，和“完整镜像、仅省略 sessionUpdate”的设计原则冲突。**  
   问题：方案把若干 ACP flat union 改成了二次嵌套结构。例如 ACP `ContentBlockText` 的 JSON 是 `{"type":"text","text":"..."}`，方案的 `ContentBlock` 会倾向于 `{"type":"text","text":{"text":"..."}}`；ACP `ToolCallContentDiff` 是 `{"type":"diff","path":"...","oldText":...,"newText":"..."}`，方案会变成 `{"type":"diff","diff":{...}}`；ACP `AvailableCommandInput` 当前 marshal 为 `{"hint":"..."}`，方案改成 `{"unstructured":{"hint":"..."}}`；ACP `SessionConfigOption` 的 select 也是 flat `{"type":"select", ...}`，方案改成 `{"select":{...}}`。  
   为什么是问题：这仍然是有损/变形翻译。上层消费者如果希望拿到接近 ACP 的原始数据，不能直接按 ACP contract 处理这些 payload；同时方案文本声称“完整镜像 ACP SDK 对应结构，仅省略 sessionUpdate”，实现形状却不是这样。  
   期望解决：统一决策并写入方案：要么镜像 ACP wire shape（推荐），为这些 union 定义 flat struct 或自定义 `MarshalJSON/UnmarshalJSON`，只省略 `sessionUpdate`；要么承认 OAR 采用自己的 canonical nested union，但必须把设计原则、docs/design 和测试目标改成“字段完整但 JSON shape 不等同 ACP”，并给出为什么这种额外翻译值得引入的理由。

2. **`TerminalRef` 漏掉了 ACP `ToolCallContentTerminal.Meta`。**  
   问题：修订说明写“TerminalRef 除外——ACP SDK 中 TerminalRef 无 Meta”，但当前 SDK 的 `ToolCallContentTerminal` 实际包含 `Meta map[string]any "json:\"_meta,omitempty\""`、`TerminalId`、`Type`。方案里的 `TerminalRef` 只有 `TerminalID`。  
   为什么是问题：这会继续丢失 `_meta`，且直接违反第 2 轮已经承诺的“所有镜像类型均添加 Meta”。  
   期望解决：`TerminalRef` 增加 `Meta map[string]any`；如果采用 flat wire-shape union，则 terminal variant 应直接是含 `_meta`、`terminalId`、`type` 的 flat payload。

3. **`ConfigOption` 镜像仍不完整且存在 `any` 占位。**  
   问题：当前 SDK 的 `SessionConfigOptionSelect` 至少包含 `Meta`、`Category`、`CurrentValue`、`Description`、`Id`、`Name`、`Options`、`Type`；`Options` 还是 ungrouped/grouped union，子项和 group 都有明确字段与 `_meta`。方案只保留了 `Meta` 和 `Options any`，注释里写“when defined / stable struct”，但这些字段在当前 SDK 已经定义。  
   为什么是问题：这会丢失 id/name/currentValue/category/description，并把 options contract 变成不可验证的 `any`，与本次减少翻译和可稳定解码的目标相悖。  
   期望解决：完整定义 `SessionConfigOptionSelect`、`SessionConfigOptionCategory`、`SessionConfigSelectOptions`、`SessionConfigSelectGroup`、`SessionConfigSelectOption` 等镜像，或用明确的 `json.RawMessage` 保留原始 JSON 并说明这是为避免频繁追随不稳定 config schema 的有意取舍；不应使用模糊的 `any` 加占位注释。

4. **EmbeddedResource 增加了 ACP 原本没有的 `type` 字段。**  
   问题：ACP `EmbeddedResourceResource` 通过 `text/blob + uri` 字段匹配变体，wire payload 没有 `type:"text"` 或 `type:"blob"`。方案新增 `Type string`。  
   为什么是问题：这虽然可能方便消费，但不是“仅省略 sessionUpdate”，而是改变原始 resource payload 的 shape。  
   期望解决：如果坚持 mirror ACP，去掉新增 `type`，按 `TextResourceContents` / `BlobResourceContents` 的 flat shape 或自定义 union marshal 保留；如果保留 `type`，需要在方案中明确它是 OAR 增强字段，并评估是否还需要同时保留原始 resource JSON 以满足“原始数据”目标。

5. **测试矩阵缺少“JSON shape 与 ACP marshal 对齐”的断言。**  
   问题：当前测试矩阵覆盖了字段存在，但没有要求将 ACP SDK 对象 marshal 后与 OAR 镜像 payload 的关键 JSON 形状对比。  
   为什么是问题：第 2 轮暴露的问题正是字段似乎齐了，但 wire shape 已经变了；只做 Go struct 字段断言无法防止继续发生过度翻译。  
   期望解决：测试矩阵增加 shape-level round-trip/对比：ContentBlock 五变体、ToolCallContent 三变体、AvailableCommandInput、ConfigOption select/options、EmbeddedResource text/blob 都应断言 JSON key layout 符合方案最终选择；若选择 ACP mirror，应与 ACP marshal 后除 `sessionUpdate` 外一致。

### claude-code 第3轮

逐条回应 codex 第 2 轮反馈：

1. **Union JSON shape 不是 ACP wire shape** — **接受，采纳推荐方案（镜像 ACP flat wire shape）**。经确认 ACP SDK 所有 union 类型均使用 flat JSON shape（变体字段直接提升到顶层 + `type` 鉴别器或字段模式匹配）。方案已全面重写：
   - 设计原则第 1 条改为"镜像 ACP wire shape"
   - 新增"ACP union 的 JSON wire shape 参考"一节，记录 6 种 union 的鉴别器策略和 shape 示例
   - 所有 union 类型（ContentBlock、ToolCallContent、AvailableCommandInput、ConfigOption、EmbeddedResource、ConfigSelectOptions）改为 Go 侧 variant 指针 + `json:"-"` + 自定义 `MarshalJSON`/`UnmarshalJSON` 产出 flat shape
   - 新增步骤 7a 详述 union marshal/unmarshal 实现模式

2. **TerminalRef 漏掉 Meta** — **接受**。已将 `TerminalRef` 改为 `ToolCallContentTerminal`，增加 `Meta map[string]any`。确认 ACP SDK 的 `ToolCallContentTerminal` 包含 `Meta`、`TerminalId`、`Type` 三个字段，第 2 轮修订说明误称无 Meta 是错误的。

3. **ConfigOption 镜像不完整** — **接受**。已完整定义：
   - `ConfigOptionSelect`：Meta、ID、Name、CurrentValue、Description、Category、Options
   - `ConfigSelectOptions`（union）：Ungrouped / Grouped，自定义 marshal 产出 bare array
   - `ConfigSelectOption`：Meta、Name、Value、Description
   - `ConfigSelectGroup`：Meta、Group、Name、Options
   - `Category` 使用 `*string`（ACP `SessionConfigOptionCategory.Other` 是原始字符串）
   - 不再使用 `any` 占位

4. **EmbeddedResource 增加了 ACP 不存在的 `type` 字段** — **接受**。已移除 `Type` 字段。改为 `EmbeddedResource` union 类型包含 `TextResource *TextResourceContents` 和 `BlobResource *BlobResourceContents` 两个变体，自定义 marshal/unmarshal 按 `text`/`blob` 字段存在性区分（与 ACP 行为一致）。

5. **测试矩阵缺少 JSON shape 断言** — **接受**。测试矩阵新增 #16-#20 共 5 个 shape-level 对比测试，覆盖 ContentBlock 五变体、ToolCallContent 三变体、AvailableCommandInput、ConfigOption（含 ungrouped/grouped options）、EmbeddedResource text/blob。每个测试构造 ACP SDK 对象 → marshal → 与 OAR 镜像 marshal 结果对比 JSON key layout 一致性。

### codex 第3轮

#### ✅ 认可项

- 核心分歧已收敛：方案明确采用 ACP flat wire shape，并要求 union 类型通过 `json:"-"` variant 指针 + 自定义 `MarshalJSON`/`UnmarshalJSON` 保持与 ACP SDK marshal 结果一致。这解决了上一轮最关键的 contract 风险。
- `ToolCallContentTerminal` 已补回 `_meta`，不再丢弃 terminal variant 的扩展字段。
- `ConfigOption` 已从 `any` 占位改为完整镜像 select/options/group/option 结构，满足稳定 decode 和字段保留要求。
- `EmbeddedResource` 已移除 ACP 不存在的 `type` 字段，改为按 `text`/`blob` 字段存在性匹配，符合 ACP wire shape。
- 测试矩阵新增 shape-level JSON 对比，能够防止“字段看似保留但 wire shape 被改写”的回归。

#### ❌ 问题项

1. **RISK: `PlanEvent` 仍然是本方案之外的契约不一致点。**  
   问题：方案风险中已承认 `PlanEvent` 仍直接依赖 `acp.PlanEntry`，但还需要注意 `SessionUpdatePlan` 自身也有 `Meta` 和 `sessionUpdate`；当前 `PlanEvent{Entries: u.Plan.Entries}` 会继续丢失 plan update 顶层 `_meta`。  
   为什么是问题：这与“所有 ACP 原始结构中含 `_meta` 的类型均保留”的原则存在一个已知例外。  
   最稳妥处理意见：允许本次执行，但在实现时至少在 `PlanEvent` 增加 `Meta map[string]any`，或在 docs/design/风险中明确 `PlanEvent` 是唯一延期例外并登记后续任务。若改动成本很低，建议本次一并补 `PlanEvent.Meta`，不必完整镜像 `PlanEntry`。

2. **RISK: 涉及文件表中 event type 常量文件写成了 `api/types.go`。**  
   问题：当前仓库事件类型常量实际在 `api/events.go`。  
   为什么是问题：执行者按表改错文件虽不一定导致编译失败，但会造成 API 常量位置不一致。  
   最稳妥处理意见：实现时将新增 `EventTypeAvailableCommands`、`EventTypeCurrentMode`、`EventTypeConfigOption`、`EventTypeSessionInfo`、`EventTypeUsage` 放入 `api/events.go`。

3. **RISK: union `UnmarshalJSON` 需要保持严格单变体语义。**  
   问题：方案说明了按 discriminator/字段存在性匹配，但未明确空 union、多变体同时命中、未知 discriminator 的错误行为。  
   为什么是问题：如果 unmarshal 宽松接受异常 payload，事件日志回放可能恢复出不确定结构。  
   最稳妥处理意见：实现时参考 ACP SDK 的 `Validate()` 思路，marshal/unmarshal 后确保恰好一个 variant 被设置；未知 `type`、空 variant、多 variant 同时设置都返回带类型名的错误。shape-level 测试可覆盖空/未知 type 至少各一个代表 union。

#### 审查结论

第 3 轮方案已足够严谨，可以执行。上述 RISK 不阻塞批准，但执行时应按处理意见消化，尤其是 `api/events.go` 文件位置和 union 严格单变体语义。

### claude-code RISK 回应

1. **PlanEvent.Meta** — 接受建议，本次一并补 `PlanEvent.Meta map[string]any`。改动成本极低（加一个字段 + translate 中赋值），且保持与"所有含 `_meta` 的 ACP 结构均保留 Meta"原则一致。PlanEntry 的完整镜像仍留作后续任务。

2. **event type 常量文件位置** — 接受。新增常量放入 `api/events.go`，不是 `api/types.go`。

3. **union 严格单变体语义** — 接受。所有 union 的 MarshalJSON 在零变体时返回错误；UnmarshalJSON 在未知 type / 无法匹配变体时返回带类型名的错误。测试矩阵增加空 union marshal 和未知 type unmarshal 各一个代表 case。

## 最终方案

以下是经过 3 轮审查通过的完整可执行方案。

### 设计原则

1. **镜像 ACP wire shape**：OAR 镜像类型的 JSON 序列化形状必须与 ACP SDK marshal 结果一致（仅省略 `sessionUpdate` 鉴别器字段）。union 类型使用 flat JSON shape + 自定义 `MarshalJSON`/`UnmarshalJSON`
2. **保留 `_meta` 扩展点**：所有 ACP 原始结构中含 `_meta` 的类型，镜像类型均保留 `Meta map[string]any`
3. **允许增加自定义字段**：turnId、streamSeq 等 OAR 附加字段继续保留
4. **保持 clean-break**：定义完整镜像类型，不直接暴露 ACP SDK 类型
5. **所有 SessionUpdate 分支都必须翻译**：不允许静默丢弃
6. **union 严格单变体语义**：MarshalJSON 零变体返回错误；UnmarshalJSON 未知 type / 无法匹配变体返回带类型名的错误

### 合理删减

仅删除以下字段：
- 各 ACP 事件上的 `sessionUpdate` 鉴别器字符串（OAR `TypedEvent.Type` 已承担此角色）

### 涉及文件

| 文件 | 改动 |
|---|---|
| `api/events.go` | 新增 5 个 event type 常量 |
| `pkg/events/types.go` | 丰富现有事件类型 + 新增 5 个事件类型 + 全部支撑类型 + union MarshalJSON/UnmarshalJSON |
| `pkg/events/translator.go` | 更新 `translate()` 覆盖所有分支 + 全部 convert 辅助函数 |
| `pkg/events/envelope.go` | `decodeEventPayload()` 增加 5 个新类型 case |
| `docs/design/runtime/runtime-spec.md` | 事件类型表新增 5 行 + payload 保留策略说明 |
| `docs/design/runtime/shim-rpc-spec.md` | Typed Event 类型表新增 5 行 + 更新 tool_call/tool_result payload 描述 |
| `pkg/events/*_test.go` | 按测试矩阵新增和更新测试 |

### 执行步骤

**Step 1: `api/events.go` — 新增 event type 常量**

```go
const (
    // ... existing constants ...
    EventTypeAvailableCommands = "available_commands"
    EventTypeCurrentMode       = "current_mode"
    EventTypeConfigOption      = "config_option"
    EventTypeSessionInfo       = "session_info"
    EventTypeUsage             = "usage"
)
```

**Step 2: `pkg/events/types.go` — 公共支撑类型**

定义以下镜像类型（Go 侧 variant 指针 + `json:"-"` + 自定义 MarshalJSON/UnmarshalJSON 实现 flat JSON shape）：

- `Annotations`（Meta, Audience, LastModified, Priority）
- `ContentBlock` union（Text/Image/Audio/ResourceLink/Resource，flat shape with `type` 鉴别器）
  - `TextContent`（Meta, Text, Annotations）
  - `ImageContent`（Meta, Data, MimeType, URI, Annotations）
  - `AudioContent`（Meta, Data, MimeType, Annotations）
  - `ResourceLinkContent`（Meta, URI, Name, Description, MimeType, Title, Size, Annotations）
  - `ResourceContent`（Meta, Resource, Annotations）
- `EmbeddedResource` union（TextResource/BlobResource，no type 鉴别器，按 text/blob 字段存在性区分）
  - `TextResourceContents`（Meta, URI, MimeType, Text）
  - `BlobResourceContents`（Meta, URI, MimeType, Blob）
- `ToolCallContent` union（Content/Diff/Terminal，flat shape with `type` 鉴别器）
  - `ToolCallContentContent`（Meta, Content）
  - `ToolCallContentDiff`（Meta, Path, OldText, NewText）
  - `ToolCallContentTerminal`（Meta, TerminalID）
- `ToolCallLocation`（Meta, Path, Line）
- `AvailableCommand`（Meta, Name, Description, Input）
- `AvailableCommandInput` union（Unstructured，no type 鉴别器，按 hint 字段存在性）
  - `UnstructuredCommandInput`（Meta, Hint）
- `ConfigOption` union（Select，flat shape with `type` 鉴别器）
  - `ConfigOptionSelect`（Meta, ID, Name, CurrentValue, Description, Category, Options）
- `ConfigSelectOptions` union（Ungrouped/Grouped，bare array，按元素结构区分）
  - `ConfigSelectOption`（Meta, Name, Value, Description）
  - `ConfigSelectGroup`（Meta, Group, Name, Options）
- `Cost`（Amount, Currency）

**Step 3: `pkg/events/types.go` — 丰富现有事件类型 + 新增事件类型**

丰富：
- `TextEvent` 增加 `Content *ContentBlock`
- `ThinkingEvent` 增加 `Content *ContentBlock`
- `UserMessageEvent` 增加 `Content *ContentBlock`
- `ToolCallEvent` 增加 `Meta`, `Status`, `Content`, `Locations`, `RawInput`, `RawOutput`
- `ToolResultEvent` 增加 `Meta`, `Kind`, `Title`, `Content`, `Locations`, `RawInput`, `RawOutput`
- `PlanEvent` 增加 `Meta map[string]any`

新增（含 `eventType()` 方法）：
- `AvailableCommandsEvent`（Meta, Commands）
- `CurrentModeEvent`（Meta, ModeID）
- `ConfigOptionEvent`（Meta, ConfigOptions）
- `SessionInfoEvent`（Meta, Title, UpdatedAt）
- `UsageEvent`（Meta, Cost, Size, Used）

**Step 4: `pkg/events/translator.go` — 更新 translate() + convert 函数**

`translate()` 覆盖所有 11 个 SessionUpdate 分支（AgentMessageChunk, AgentThoughtChunk, UserMessageChunk, ToolCall, ToolCallUpdate, Plan, AvailableCommandsUpdate, CurrentModeUpdate, ConfigOptionUpdate, SessionInfoUpdate, UsageUpdate），无 `return nil`。

新增 convert 函数：
- `convertContentBlock` — 5 个变体 + Meta + Annotations
- `convertAnnotations`
- `convertToolCallContents` — 3 个变体（terminal 含 Meta）
- `convertLocations`
- `convertCommands` + `convertAvailableCommandInput`
- `convertConfigOptions` + `convertConfigSelectOptions`
- `convertCost`
- `convertEmbeddedResource` — text/blob 两变体
- `safeStringPtr`, `safeToolKind`

**Step 5: `pkg/events/envelope.go` — 更新 decodeEventPayload()**

增加 5 个 case：
- `available_commands` → `AvailableCommandsEvent`
- `current_mode` → `CurrentModeEvent`
- `config_option` → `ConfigOptionEvent`
- `session_info` → `SessionInfoEvent`
- `usage` → `UsageEvent`

**Step 6: `docs/design/runtime/runtime-spec.md` — 更新事件类型表**

在"事件类型"表中新增 5 行：

| 事件类型 | 来源 | 说明 |
|---|---|---|
| `AvailableCommandsEvent` | ACP `available_commands_update` | 可用命令/工具列表更新 |
| `CurrentModeEvent` | ACP `current_mode_update` | 当前操作模式变更 |
| `ConfigOptionEvent` | ACP `config_option_update` | 配置选项变更 |
| `SessionInfoEvent` | ACP `session_info_update` | 会话元数据更新 |
| `UsageEvent` | ACP `usage_update` | Token/API 用量和费用统计 |

补充说明：所有事件类型完整保留 ACP 原始字段（包括 `_meta`），JSON wire shape 与 ACP SDK marshal 结果一致，仅省略 `sessionUpdate` 鉴别器字段。

**Step 7: `docs/design/runtime/shim-rpc-spec.md` — 更新 Typed Event 类型表**

新增 5 行事件类型 + 更新 `tool_call` 和 `tool_result` 的 payload 字段描述为完整字段列表。

**Step 8: `pkg/events/*_test.go` — 测试**

按以下矩阵实现测试：

| # | 测试场景 | 验证点 |
|---|---|---|
| 1 | ToolCall 完整翻译 | Meta、Content（含 diff/terminal/content 三变体）、Locations、RawInput、RawOutput、Status 均保留 |
| 2 | ToolCallUpdate 完整翻译 | Meta、nullable Status/Kind/Title、Content、Locations、RawInput、RawOutput 均保留 |
| 3 | ContentBlock — Text 变体 | Text + Annotations + Meta 均保留；便捷 `text` 字段仍填充 |
| 4 | ContentBlock — Image 变体 | Data、MimeType、URI、Annotations、Meta 均保留 |
| 5 | ContentBlock — Audio 变体 | Data、MimeType、Annotations、Meta 均保留 |
| 6 | ContentBlock — ResourceLink 变体 | URI、Name、Description、MimeType、Title、Size、Annotations、Meta 均保留 |
| 7 | ContentBlock — Resource 变体 | EmbeddedResource（text/blob 两变体）、Annotations、Meta 均保留 |
| 8 | AvailableCommandsUpdate 翻译 | Commands 列表含 Name/Description/Input 对象结构（Unstructured.Hint）、Meta |
| 9 | CurrentModeUpdate 翻译 | ModeID、Meta |
| 10 | ConfigOptionUpdate 翻译 | ConfigOptions 列表含 ID/Name/CurrentValue/Description/Category/Options（ungrouped + grouped）、Meta |
| 11 | SessionInfoUpdate 翻译 | Title、UpdatedAt、Meta |
| 12 | UsageUpdate 翻译 | Cost（Amount/Currency）、Size、Used、Meta |
| 13 | RawInput/RawOutput JSON round-trip | 任意 JSON 结构经翻译 + 序列化 + 反序列化后完全保留 |
| 14 | Envelope decode 所有新 event type | decodeEventPayload 能正确恢复全部 17 种 event type |
| 15 | 向后兼容 | 仅含原有字段的旧格式 JSON 仍能正确反序列化 |
| 16 | ContentBlock JSON shape 对齐 | 5 个变体分别构造 ACP SDK 对象 → marshal → 与 OAR 镜像 marshal 结果对比 JSON key layout 一致（除 `sessionUpdate`） |
| 17 | ToolCallContent JSON shape 对齐 | content/diff/terminal 三变体的 ACP marshal 与 OAR mirror marshal JSON key layout 一致 |
| 18 | AvailableCommandInput JSON shape 对齐 | unstructured 变体的 ACP marshal 与 OAR mirror marshal 一致（flat `{"hint":"..."}` 无嵌套） |
| 19 | ConfigOption JSON shape 对齐 | select 变体及 options（ungrouped/grouped）的 ACP marshal 与 OAR mirror marshal JSON key layout 一致 |
| 20 | EmbeddedResource JSON shape 对齐 | text/blob 变体的 ACP marshal 与 OAR mirror marshal 一致（无 `type` 字段，按 text/blob 字段存在性区分） |
| 21 | Union 空变体 marshal 错误 | ContentBlock 等 union 零变体时 MarshalJSON 返回错误 |
| 22 | Union 未知 type unmarshal 错误 | ContentBlock 等 union 收到未知 type 值时 UnmarshalJSON 返回带类型名的错误 |

**最终构建验证：** `make build` 必须通过。

### 向后兼容性

- `TextEvent.Text`、`ThinkingEvent.Text`、`UserMessageEvent.Text` 保留原有字段，新增 `Content` 字段
- `ToolCallEvent.{ID, Kind, Title}` 保留，新增其他字段（`omitempty`）
- `ToolResultEvent.{ID, Status}` 保留，新增其他字段（`omitempty`）
- `PlanEvent.Entries` 保留，新增 `Meta`（`omitempty`）
- 已有的 JSON 消费者不会被新增的 `omitempty` 字段破坏
