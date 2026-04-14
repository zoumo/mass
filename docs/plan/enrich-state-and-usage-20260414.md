# 扩展 state.json 与 usage 透传

## 背景

当前 state.json 只记录进程级状态（status, pid, bundle, exitCode），不包含 session 层面的元信息。
ACP 发来的 5 类 metadata 事件（session_info, config_option, current_mode, available_commands, usage）
虽然已经被 Translator 正确翻译并写入 event log，但：

1. **state.json 不反映这些信息** — 外部调用方无法通过读 state 获得 session 能力描述
2. **没有统一的 state_change 通知** — 这些变更不触发 runtime category 事件
3. **usage 没有通过 shim RPC 暴露** — 上层无法获取 token 消耗和费用信息
4. **file_write / file_read / command 是死代码** — 不来自 ACP，纯占位符，应清理

## 目标

1. 扩展 runtime-spec state.json，增加 session 元信息字段（不含 usage）
   - 包含：agentInfo, capabilities, availableCommands, configOptions, sessionInfo, currentMode
   - 不包含：usage（高频度量数据，只走事件流）
2. 当这些字段变更时，写入 state.json 并触发统一的 `state_change` 事件
3. usage 作为 session event 通知出去，不落盘到 state.json
4. 清理 file_write / file_read / command 死代码
5. 维护事件类型计数器，state 变更时一并写入

---

## 改动 1：扩展 state.json schema

### 新增字段

在 `State` 结构体中增加 `Session` 子结构，记录 ACP agent 在初始化和运行期间报告的能力与配置：

```go
type State struct {
    // ... 现有字段不变 ...
    OarVersion  string            `json:"oarVersion"`
    ID          string            `json:"id"`
    Status      Status            `json:"status"`
    PID         int               `json:"pid,omitempty"`
    Bundle      string            `json:"bundle"`
    Annotations map[string]string `json:"annotations,omitempty"`
    ExitCode    *int              `json:"exitCode,omitempty"`

    // UpdatedAt is the RFC 3339 timestamp of the last state.json write.
    // Set by Manager.writeState() and Manager.updateSessionMetadata()
    // before every spec.WriteState() call — covers all state write paths.
    UpdatedAt string `json:"updatedAt,omitempty"`

    // Session carries ACP-reported session metadata.
    // Populated progressively as the agent reports updates via ACP notifications.
    // Nil before the first session metadata arrives.
    Session *SessionState `json:"session,omitempty"`

    // EventCounts tracks the number of events by type.
    // Incremented in Translator.broadcast() after successful log append + seq increment.
    // Covers ALL event origins (ACP-translated + manual Notify*).
    // Flushed to state.json on every state write.
    EventCounts map[string]int `json:"eventCounts,omitempty"`
}
```

### SessionState

```go
// SessionState captures the latest session-level metadata reported by the agent.
// Fields are updated incrementally: each ACP notification overwrites the relevant
// field(s), leaving others unchanged.
type SessionState struct {
    // AgentInfo identifies the agent implementation (from InitializeResponse.AgentInfo).
    // Set once during ACP bootstrap; immutable afterward.
    AgentInfo *AgentInfo `json:"agentInfo,omitempty"`

    // Capabilities reports the agent's ACP capabilities (from InitializeResponse.AgentCapabilities).
    // Set once during ACP bootstrap; immutable afterward.
    Capabilities *AgentCapabilities `json:"capabilities,omitempty"`

    // AvailableCommands lists the agent's currently available commands (from AvailableCommandsUpdate).
    // Full replacement on each update; sorted by Name.
    AvailableCommands []AvailableCommand `json:"availableCommands,omitempty"`

    // ConfigOptions lists the agent's configurable options with current values (from ConfigOptionUpdate).
    // Full replacement on each update; sorted by ID.
    ConfigOptions []ConfigOption `json:"configOptions,omitempty"`

    // SessionInfo carries session-level metadata (title, agent-reported updatedAt).
    // Updated on each SessionInfoUpdate from ACP.
    SessionInfo *SessionInfo `json:"sessionInfo,omitempty"`

    // CurrentMode is the agent's current operational mode ID (from CurrentModeUpdate).
    // Updated on each CurrentModeUpdate from ACP.
    CurrentMode *string `json:"currentMode,omitempty"`
}
```

### AgentInfo — 镜像 ACP `Implementation`

```go
// AgentInfo identifies the agent implementation.
// Populated from ACP InitializeResponse.AgentInfo (type: acp.Implementation).
type AgentInfo struct {
    Name    string  `json:"name"`
    Version string  `json:"version"`
    Title   *string `json:"title,omitempty"`
}
```

### AgentCapabilities — 镜像 ACP JSON shape

**设计决策**：选择"原样镜像 ACP JSON shape"，不做规范化转换。
这减少协议歧义，消费者看到的字段命名与 ACP wire format 完全一致。

ACP SDK 类型参考（`github.com/coder/acp-go-sdk@v0.6.4` `types_gen.go`）：

| ACP SDK field | ACP JSON tag | state.json field |
|---|---|---|
| `AgentCapabilities.LoadSession` | `"loadSession"` | `capabilities.loadSession` |
| `AgentCapabilities.McpCapabilities` | `"mcpCapabilities"` | `capabilities.mcpCapabilities` |
| `AgentCapabilities.PromptCapabilities` | `"promptCapabilities"` | `capabilities.promptCapabilities` |
| `AgentCapabilities.SessionCapabilities` | `"sessionCapabilities"` | `capabilities.sessionCapabilities` |

```go
// AgentCapabilities mirrors ACP AgentCapabilities JSON shape exactly.
// Field names match ACP wire format to avoid protocol ambiguity.
type AgentCapabilities struct {
    // LoadSession indicates whether the agent supports 'session/load'.
    // Top-level field in ACP, NOT inside SessionCapabilities.
    LoadSession bool `json:"loadSession,omitempty"`

    // McpCapabilities reports supported MCP transport protocols.
    McpCapabilities *McpCapabilities `json:"mcpCapabilities,omitempty"`

    // PromptCapabilities reports supported prompt content types.
    PromptCapabilities *PromptCapabilities `json:"promptCapabilities,omitempty"`

    // SessionCapabilities reports session management capabilities.
    // Uses pointer-existence semantics: fork/list/resume fields are non-nil
    // when the capability is supported (matching ACP SDK pattern).
    SessionCapabilities *SessionCapabilities `json:"sessionCapabilities,omitempty"`
}

// McpCapabilities mirrors ACP McpCapabilities.
type McpCapabilities struct {
    Http bool `json:"http,omitempty"`
    Sse  bool `json:"sse,omitempty"`
}

// PromptCapabilities mirrors ACP PromptCapabilities.
type PromptCapabilities struct {
    Audio           bool `json:"audio,omitempty"`
    Image           bool `json:"image,omitempty"`
    EmbeddedContext bool `json:"embeddedContext,omitempty"`
}

// SessionCapabilities mirrors ACP SessionCapabilities.
// fork/list/resume use pointer-existence semantics:
// non-nil = capability supported, nil = not supported.
// NOTE: loadSession is NOT here — it is a top-level AgentCapabilities field.
type SessionCapabilities struct {
    Fork   *SessionForkCapabilities   `json:"fork,omitempty"`
    List   *SessionListCapabilities   `json:"list,omitempty"`
    Resume *SessionResumeCapabilities `json:"resume,omitempty"`
}

// SessionForkCapabilities / SessionListCapabilities / SessionResumeCapabilities
// are empty marker structs. Their presence (non-nil) indicates the capability is supported.
type SessionForkCapabilities struct{}
type SessionListCapabilities struct{}
type SessionResumeCapabilities struct{}
```

### SessionInfo

```go
// SessionInfo carries session-level metadata from ACP SessionInfoUpdate.
type SessionInfo struct {
    Title     *string `json:"title,omitempty"`
    UpdatedAt *string `json:"updatedAt,omitempty"`
}
```

### 转换规则（ACP SDK → state.json）

ACP `InitializeResponse` → `SessionState`：
```go
resp, _ := conn.Initialize(ctx, req)

// resp.AgentInfo is *acp.Implementation
state.Session.AgentInfo = &AgentInfo{
    Name:    resp.AgentInfo.Name,
    Version: resp.AgentInfo.Version,
    Title:   resp.AgentInfo.Title,
}

// resp.AgentCapabilities is acp.AgentCapabilities (value, not pointer)
state.Session.Capabilities = convertAgentCapabilities(resp.AgentCapabilities)
```

转换函数：
```go
func convertAgentCapabilities(ac acp.AgentCapabilities) *AgentCapabilities {
    caps := &AgentCapabilities{
        LoadSession: ac.LoadSession,
    }
    // McpCapabilities — always present in ACP (default: {http:false, sse:false})
    caps.McpCapabilities = &McpCapabilities{
        Http: ac.McpCapabilities.Http,
        Sse:  ac.McpCapabilities.Sse,
    }
    // PromptCapabilities — always present in ACP (default: {audio:false, ...})
    caps.PromptCapabilities = &PromptCapabilities{
        Audio:           ac.PromptCapabilities.Audio,
        Image:           ac.PromptCapabilities.Image,
        EmbeddedContext: ac.PromptCapabilities.EmbeddedContext,
    }
    // SessionCapabilities — pointer-existence semantics
    sc := &SessionCapabilities{}
    hasAny := false
    if ac.SessionCapabilities.Fork != nil {
        sc.Fork = &SessionForkCapabilities{}
        hasAny = true
    }
    if ac.SessionCapabilities.List != nil {
        sc.List = &SessionListCapabilities{}
        hasAny = true
    }
    if ac.SessionCapabilities.Resume != nil {
        sc.Resume = &SessionResumeCapabilities{}
        hasAny = true
    }
    if hasAny {
        caps.SessionCapabilities = sc
    }
    return caps
}
```

### 排序约定

- `AvailableCommands` 按 `Name` 字典序排列
- `ConfigOptions` 按 `ID` 字典序排列（ConfigOption 是 union，当前只有 Select variant，按 Select.ID 排序）

ACP 每次发完整列表，写入前先排序再替换。排序保证：
1. state.json 的 JSON 输出稳定（相同内容不因顺序产生 diff）
2. 消费方可以做 binary search 或稳定的 key 定位

### EventCounts — 事件类型计数器

`EventCounts` 是一个 `map[string]int`，key 是事件类型（如 `"text"`, `"tool_call"`, `"state_change"`），
value 是该类型事件的累计数量。

- **统一计数点**：在 `Translator.broadcast()` 中，成功 log append + `nextSeq++` 之后、fanout 之前，执行 `t.eventCounts[ev.Type]++`
- **覆盖所有来源**：
  - ACP 翻译事件（text, thinking, tool_call, tool_result, config_option, available_commands, session_info, current_mode, usage, plan, error）
  - 手工广播事件（turn_start, user_message, turn_end）
  - 运行时事件（state_change）
- **失败不计数**：log append 失败时 broadcast() 提前返回（不 increment nextSeq，不 fanout），自然也不计数
- **懒写入**：只在 state.json 因其他原因需要写入时顺带刷新计数

用途：
- 诊断/监控：快速查看 session 产出了多少 tool_call、text chunk 等
- agentd 恢复后可从 state.json 读取计数作为参考（不是精确值，因为懒写入可能丢失最后几个计数）

### state.json 示例

```json
{
  "oarVersion": "0.1.0",
  "id": "session-abc123",
  "status": "idle",
  "pid": 12345,
  "bundle": "/var/lib/agentd/bundles/session-abc123",
  "updatedAt": "2026-04-14T10:30:00.123456789Z",
  "session": {
    "agentInfo": {
      "name": "claude-code",
      "version": "1.0.0"
    },
    "capabilities": {
      "loadSession": true,
      "mcpCapabilities": {"http": true, "sse": true},
      "promptCapabilities": {"audio": false, "image": true, "embeddedContext": true},
      "sessionCapabilities": {"fork": {}, "list": {}, "resume": {}}
    },
    "availableCommands": [
      {"name": "create_plan", "description": "Create an execution plan"},
      {"name": "research_codebase", "description": "Search and analyze code"}
    ],
    "configOptions": [
      {
        "type": "select",
        "id": "model",
        "name": "Model",
        "currentValue": "opus",
        "options": [
          {"name": "Opus", "value": "opus"},
          {"name": "Sonnet", "value": "sonnet"}
        ]
      }
    ],
    "sessionInfo": {
      "title": "Debug authentication flow"
    },
    "currentMode": "code"
  },
  "eventCounts": {
    "text": 142,
    "thinking": 28,
    "tool_call": 15,
    "tool_result": 15,
    "turn_start": 3,
    "turn_end": 3,
    "user_message": 3,
    "state_change": 8,
    "usage": 6,
    "session_info": 2,
    "config_option": 1,
    "available_commands": 1,
    "current_mode": 1
  }
}
```

### 设计决策

- **`Session` 是可选子结构**：agent 启动后 ACP 通知陆续到达，字段渐进填充。未收到的字段保持 nil/空。
- **AgentInfo + Capabilities 不可变**：在 ACP `Initialize` 握手时一次性设定，之后不再更新。这是 agent 的静态能力描述。
- **全量替换 + 排序语义**：`availableCommands` 和 `configOptions` 每次收到事件都全量替换（与 ACP 语义一致），写入前按唯一 key 排序。
- **SessionInfo + CurrentMode 纳入 state**：它们是会话元信息，变更频率低，外部调用方需要从 state 获取 session title/mode。
- **usage 不落盘**：高频度量数据只走内存 + 通知 + event log，不写 state.json。
- **eventCounts 在 broadcast() 统一计数**：唯一计数点在成功写入事件流之后，覆盖所有事件来源，失败不计数。
- **Capabilities 镜像 ACP JSON shape**：字段命名与 ACP wire format 一致，loadSession 保留在顶层，sessionCapabilities 使用指针存在性语义。消除协议歧义。
- **runtime-spec/api 包独立定义类型**：不依赖 events 包，保持 runtime-spec 的独立性。

---

## 改动 2：metadata 更新链路 — Translator hook + Manager.updateSessionMetadata

### 问题分析

当前数据流：`Manager.Events() → chan acp.SessionNotification → Translator.run()` 是单消费者模型。
Manager 看不到翻译后的 `apishim.Event`，也不能与 Translator 竞争消费同一个 channel。

### 数据流设计

引入 **Translator session metadata hook**：Translator 在翻译+广播 session metadata 事件后，
回调通知上层（Service 组合层）更新 state.json。

```
ACP SessionNotification
    → Translator.run() 从 channel 读取
    → translate(n) → 类型化 Event
    → broadcastSessionEvent(ev) → 持锁 log + fanout + eventCounts++（session 事件先进入事件流）
    → 锁释放后，run() 检查是否为 metadata 类型
    → 是：调用 t.sessionMetadataHook(ev)
        → Service 层转发 → Manager.updateSessionMetadata(changed, apply)
            → 读 state → apply 更新 Session 字段 → 设 UpdatedAt → 写 state.json（含最新 EventCounts）
            → emitSessionStateChange(changed, reason)
                → stateChangeHook → trans.NotifyStateChange(...)
                    → broadcast() → state_change 事件进入事件流
```

### 关键设计点

**1. hook 调用时机：broadcastSessionEvent 返回之后（锁已释放）**

```go
// In Translator.run():
func (t *Translator) run() {
    for {
        select {
        case <-t.done:
            return
        case n, ok := <-t.in:
            if !ok {
                return
            }
            ev := translate(n)
            if ev == nil {
                continue
            }
            t.broadcastSessionEvent(ev)     // ① session event 先进入事件流（持锁）
            t.maybeNotifyMetadata(ev)        // ② 锁已释放后回调 hook
        }
    }
}

func (t *Translator) maybeNotifyMetadata(ev apishim.Event) {
    // sessionMetadataHook is set once before Start(), never changed — no lock needed.
    if t.sessionMetadataHook == nil {
        return
    }
    switch ev.(type) {
    case apishim.AvailableCommandsEvent,
         apishim.ConfigOptionEvent,
         apishim.SessionInfoEvent,
         apishim.CurrentModeEvent:
        t.sessionMetadataHook(ev)
    }
}
```

**2. 锁顺序无死锁风险 — 统一策略**

所有 Manager state 读写（lifecycle writeState + metadata updateSessionMetadata）都在 `Manager.mu` 下进行。
调用 stateChangeHook 前：复制 hook 引用和 StateChange 数据，释放 `Manager.mu`，再调 hook。

具体序列（metadata 事件路径）：
1. `Translator.run()` → `broadcastSessionEvent(ev)` 持有 `Translator.mu` → 完成后释放
2. `maybeNotifyMetadata(ev)` — 不持锁，直接调用 hook
3. hook → `Manager.updateSessionMetadata()` 获取 `Manager.mu` → 读-改-写 state → 复制 hook + change → 释放 `Manager.mu`
4. 调用 `hook(change)` → `trans.NotifyStateChange()` → `broadcast()` 获取 `Translator.mu`

锁获取顺序：`Translator.mu → 释放 → Manager.mu → 释放 → Translator.mu`。
**每把锁在获取下一把前已释放，无嵌套无死锁。**

lifecycle writeState 路径同理：`Manager.mu → 释放 → hook → Translator.mu`。

**3. state 更新失败不影响事件 fanout**

session 事件已经在 ① 成功进入事件流（log + fanout）。② 中的 state.json 写入失败：
- 记录 structured error log
- 不回滚已广播的 session 事件
- 不触发 state_change（因为 state 没变成功）
- 不 panic — 下次 metadata 更新会重试写入

### Manager.updateSessionMetadata

Manager 新增方法，与现有 `writeState()` 分离，专门处理 metadata-only 状态更新：

```go
// updateSessionMetadata reads current state, applies session field updates,
// writes state.json, and emits a state_change even when lifecycle status is unchanged.
// changed lists which SessionState fields were modified (e.g., ["configOptions"]).
//
// Locking: same strategy as writeState — acquires Manager.mu for read-apply-write,
// releases BEFORE calling stateChangeHook.
func (m *Manager) updateSessionMetadata(changed []string, apply func(*apiruntime.State)) error {
    m.mu.Lock()

    st, err := spec.ReadState(m.stateDir)
    if err != nil {
        m.mu.Unlock()
        return err
    }

    // Ensure Session sub-struct exists.
    if st.Session == nil {
        st.Session = &apiruntime.SessionState{}
    }

    // Apply caller-provided mutations to the session fields.
    apply(&st)

    // Derived fields — same as writeState.
    st.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
    st.EventCounts = m.getEventCounts()

    if err := spec.WriteState(m.stateDir, st); err != nil {
        m.mu.Unlock()
        return err
    }

    // Prepare hook data under lock, then release before calling.
    hook := m.stateChangeHook
    reason := deriveReason(changed)
    change := StateChange{
        SessionID:      st.ID,
        PreviousStatus: st.Status,
        Status:         st.Status,       // same status — metadata-only change
        PID:            st.PID,
        Reason:         reason,
        SessionChanged: changed,         // NEW field
    }
    m.mu.Unlock()

    // Call hook OUTSIDE Manager.mu — same pattern as writeState.
    if hook != nil {
        hook(change)
    }
    return nil
}
```

`deriveReason` 从 changed 字段列表推导 reason 字符串：

```go
func deriveReason(changed []string) string {
    if len(changed) == 1 {
        switch changed[0] {
        case "configOptions":
            return "config-updated"
        case "availableCommands":
            return "commands-updated"
        case "sessionInfo":
            return "session-info-updated"
        case "currentMode":
            return "mode-updated"
        }
    }
    return "session-metadata-updated"
}
```

### StateChange 结构体扩展

```go
type StateChange struct {
    SessionID      string
    PreviousStatus apiruntime.Status
    Status         apiruntime.Status
    PID            int
    Reason         string
    SessionChanged []string // NEW: which session fields changed; empty for lifecycle transitions
}
```

### EventCounts 传递给 Manager

Manager 需要读取 Translator 的内存 eventCounts。通过回调注入（避免循环依赖）：

```go
// Manager 增加字段
type Manager struct {
    // ... existing fields ...
    eventCountsFn func() map[string]int // injected by Service layer
}

func (m *Manager) SetEventCountsFn(fn func() map[string]int) {
    m.eventCountsFn = fn
}

func (m *Manager) getEventCounts() map[string]int {
    if m.eventCountsFn == nil {
        return nil
    }
    return m.eventCountsFn()
}
```

### Translator 增加 hook 接口

```go
// Translator 增加字段
type Translator struct {
    // ... existing fields ...
    sessionMetadataHook func(apishim.Event) // set once before Start(), never changed
}

func (t *Translator) SetSessionMetadataHook(hook func(apishim.Event)) {
    t.sessionMetadataHook = hook
}
```

### Service 层接线

```go
// In shim command setup (after creating Manager and Translator):
trans.SetSessionMetadataHook(func(ev apishim.Event) {
    changed, apply := buildSessionUpdate(ev)
    if changed == nil {
        return
    }
    if err := mgr.UpdateSessionMetadata(changed, apply); err != nil {
        logger.Error("session metadata update failed", "error", err)
    }
})

mgr.SetEventCountsFn(trans.EventCounts)
```

`buildSessionUpdate` 根据事件类型构建更新函数：

```go
func buildSessionUpdate(ev apishim.Event) (changed []string, apply func(*apiruntime.State)) {
    switch e := ev.(type) {
    case apishim.AvailableCommandsEvent:
        return []string{"availableCommands"}, func(st *apiruntime.State) {
            st.Session.AvailableCommands = sortCommandsByName(convertToStateCommands(e.Commands))
        }
    case apishim.ConfigOptionEvent:
        return []string{"configOptions"}, func(st *apiruntime.State) {
            st.Session.ConfigOptions = sortConfigOptionsByID(convertToStateConfigOptions(e.ConfigOptions))
        }
    case apishim.SessionInfoEvent:
        return []string{"sessionInfo"}, func(st *apiruntime.State) {
            st.Session.SessionInfo = &apiruntime.SessionInfo{
                Title:     e.Title,
                UpdatedAt: e.UpdatedAt,
            }
        }
    case apishim.CurrentModeEvent:
        return []string{"currentMode"}, func(st *apiruntime.State) {
            st.Session.CurrentMode = &e.ModeID
        }
    default:
        return nil, nil
    }
}
```

### state_change reason 扩展

现有 reason 值：`"prompt-started"`, `"prompt-completed"`, `"prompt-failed"`, `"process-exited"`, `"bootstrap-started"`, `"bootstrap-complete"`, `"bootstrap-failed"`, `"runtime-stop"`

新增：
- `"config-updated"` — 配置选项变更
- `"commands-updated"` — 可用命令列表变更
- `"session-info-updated"` — session 标题/时间变更
- `"mode-updated"` — 当前模式变更

### StateChangeEvent 扩展

```go
type StateChangeEvent struct {
    PreviousStatus string   `json:"previousStatus"`
    Status         string   `json:"status"`
    PID            int      `json:"pid,omitempty"`
    Reason         string   `json:"reason,omitempty"`
    // SessionChanged lists which session fields were updated in this state change.
    // Empty for pure lifecycle status changes.
    SessionChanged []string `json:"sessionChanged,omitempty"`
}
```

`sessionChanged` 示例值：`["configOptions"]`, `["availableCommands"]`, `["sessionInfo"]`, `["currentMode"]`。
这让消费方可以过滤只关心的变更类型，而不需要 diff 整个 state。

`NotifyStateChange` 需要扩展签名以传递 `sessionChanged`：

```go
func (t *Translator) NotifyStateChange(previousStatus, status string, pid int, reason string, sessionChanged []string) {
    t.broadcast(func(seq int, at time.Time) apishim.ShimEvent {
        return apishim.ShimEvent{
            // ... existing fields ...
            Content: apishim.StateChangeEvent{
                PreviousStatus: previousStatus,
                Status:         status,
                PID:            pid,
                Reason:         reason,
                SessionChanged: sessionChanged,
            },
        }
    })
}
```

现有调用点传 `nil` 保持兼容。Manager 的 stateChangeHook 签名不变（StateChange struct 已有 SessionChanged 字段），
Service 层在 hook 回调中传递。

---

## 改动 3：writeState 统一为读-改-写 + UpdatedAt + EventCounts 刷新

### 问题

当前 `Manager.writeState(state State, reason)` 接收调用方传入的完整 `State` 字面量。
`Create()`、`Kill()`、process-exited 等路径用 `apiruntime.State{...}` 重建 state，
会覆盖已持久化的 `Session` 和 `EventCounts`——直接破坏目标 1/5。

此外，writeState 只设置了 `UpdatedAt`，没有刷新 `EventCounts`，导致 status 变更等最重要的 state 写路径不携带最新计数。

### 方案：统一读-改-写模式

将 `writeState` 改为闭包式接口。调用方只声明需要变更的字段，其余字段自动保留：

```go
// writeState reads current state.json, applies caller mutations, sets derived
// fields (UpdatedAt, EventCounts), writes atomically, and emits state_change
// if lifecycle status changed.
//
// Locking: acquires Manager.mu for the read-apply-write cycle; releases BEFORE
// calling stateChangeHook to avoid Manager.mu → Translator.mu nesting.
func (m *Manager) writeState(apply func(*apiruntime.State), reason string) error {
    m.mu.Lock()

    previous, readErr := spec.ReadState(m.stateDir)
    st := previous // value copy — preserves all existing fields
    if readErr != nil {
        // First write (state.json doesn't exist yet): start from zero-value.
        st = apiruntime.State{}
    }

    // Caller mutations — only touch the fields they care about.
    apply(&st)

    // Derived fields — set on EVERY state write, never by callers.
    st.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
    st.EventCounts = m.getEventCounts()

    if err := spec.WriteState(m.stateDir, st); err != nil {
        m.mu.Unlock()
        return err
    }

    // Prepare hook data under lock, then release before calling.
    hook := m.stateChangeHook
    var change *StateChange
    if readErr == nil && previous.Status != st.Status {
        change = &StateChange{
            SessionID:      st.ID,
            PreviousStatus: previous.Status,
            Status:         st.Status,
            PID:            st.PID,
            Reason:         reason,
        }
    }
    m.mu.Unlock()

    // Call hook OUTSIDE Manager.mu to avoid lock nesting.
    if change != nil && hook != nil {
        hook(*change)
    }
    return nil
}
```

### 调用方迁移

所有 lifecycle 写路径从字面量改为闭包，只 mutate 自己关心的字段：

```go
// Create() — bootstrap-started (state.json 不存在，闭包设定全部基础字段)
m.writeState(func(st *apiruntime.State) {
    st.OarVersion  = m.cfg.OarVersion
    st.ID          = m.cfg.Metadata.Name
    st.Status      = apiruntime.StatusCreating
    st.Bundle      = m.bundleDir
    st.Annotations = m.cfg.Metadata.Annotations
}, "bootstrap-started")

// Create() — bootstrap-complete (st 已有基础字段，只改 Status/PID/Session)
m.writeState(func(st *apiruntime.State) {
    st.Status  = apiruntime.StatusIdle
    st.PID     = cmd.Process.Pid
    st.Session = session // agentInfo + capabilities
}, "bootstrap-complete")

// Create() — bootstrap-failed
m.writeState(func(st *apiruntime.State) {
    st.Status = apiruntime.StatusStopped
}, "bootstrap-failed")

// Create() — process-exited goroutine (Session/EventCounts 自动保留)
m.writeState(func(st *apiruntime.State) {
    st.Status = apiruntime.StatusStopped
}, "process-exited")

// Prompt() — prompt-started
m.writeState(func(st *apiruntime.State) {
    st.Status = apiruntime.StatusRunning
}, "prompt-started")

// Prompt() — prompt-completed / prompt-failed
m.writeState(func(st *apiruntime.State) {
    st.Status = apiruntime.StatusIdle
}, reason) // reason = "prompt-completed" or "prompt-failed"

// Kill() — runtime-stop (Session/EventCounts 自动保留)
m.writeState(func(st *apiruntime.State) {
    st.Status = apiruntime.StatusStopped
}, "runtime-stop")
```

### 保证

1. **Session 不被覆盖**：`Kill()`、process-exited 只设 `Status`，其余字段保留读到的值
2. **EventCounts 每次刷新**：所有 state 写路径都带最新计数（lifecycle + metadata）
3. **UpdatedAt 每次刷新**：统一在 `writeState()` 和 `updateSessionMetadata()` 中设置
4. **hook 在 Manager.mu 外调用**：避免 `Manager.mu → Translator.mu` 嵌套死锁

### 不在 spec.WriteState() 层设置

`spec.WriteState()` 是纯 IO helper（序列化 + 原子写文件），不主动修改入参语义。
由调用方（Manager）负责填充 `UpdatedAt` 和 `EventCounts`，spec 包保持无副作用，测试更简单。

---

## 改动 4：usage 通知透传

usage 不写 state.json，但需要确保上层能实时获取：

1. **session event 通知** — 已有。usage 事件作为 `shim/event` (category=session, type=usage) 推送给所有 subscriber。现有实现已经做到了。
2. **event log 持久化** — 已有。usage 事件写入 events.jsonl，断线恢复后可通过 `runtime/history` 回放。
3. **不触发 state_change** — usage 高频更新，避免 state_change 风暴。metadata hook 中不包含 UsageEvent。

上层获取 usage 的方式：
- **实时**：监听 `shim/event` 中 type=usage 的 session event
- **历史**：`runtime/history` 回放 event log 中的 usage 事件
- **当前值**：从最近的 usage event 中获取（event log 尾部）

**结论**：usage 透传不需要额外代码改动，现有的 Translator + event broadcast + event log 已经覆盖。

---

## 改动 5：`runtime/status` 返回实时状态

### 现状

`runtime/status` 通过 `mgr.GetState()` → `spec.ReadState()` 从 state.json 读取并返回。
扩展 State 结构后，`Session`、`EventCounts` 等新字段会自动序列化到 state.json 并通过该接口返回——**前提是已经落盘**。

### 问题

`EventCounts` 采用懒写入策略（只搭便车写入），高频事件（text/thinking/tool_call）只更新内存计数。
如果在两次 state 写入之间调用 `runtime/status`，返回的 `eventCounts` 是滞后的。

### 方案

`Service.Status()` 在返回前，将内存中的最新 `EventCounts` 合并到从 state.json 读取的 State 上：

```go
func (s *Service) Status(_ context.Context) (*apishim.RuntimeStatusResult, error) {
    st, err := s.mgr.GetState()
    if err != nil {
        return nil, jsonrpc.ErrInternal(err.Error())
    }
    // Overlay in-memory event counts for real-time accuracy.
    st.EventCounts = s.trans.EventCounts()
    return &apishim.RuntimeStatusResult{
        State:    st,
        Recovery: apishim.RuntimeStatusRecovery{LastSeq: s.trans.LastSeq()},
    }, nil
}
```

Translator 暴露 `EventCounts() map[string]int` 方法，返回内存计数的快照（copy）：

```go
func (t *Translator) EventCounts() map[string]int {
    t.mu.Lock()
    defer t.mu.Unlock()
    if len(t.eventCounts) == 0 {
        return nil
    }
    cp := make(map[string]int, len(t.eventCounts))
    for k, v := range t.eventCounts {
        cp[k] = v
    }
    return cp
}
```

### 不需要额外改动的字段

- `Session`（AgentInfo, Capabilities, AvailableCommands, ConfigOptions, SessionInfo, CurrentMode）— 变更时立即写入 state.json，`ReadState()` 始终最新
- `UpdatedAt` — 每次 writeState/updateSessionMetadata 时更新，读取即最新

---

## 改动 6：清理 file_write / file_read / command 死代码

### 调查结论

- **这三个事件类型不来自 ACP** — ACP SessionUpdate union 中没有对应的 variant
- **从未被生成过** — Translator 中没有任何代码路径产出这三类事件
- **纯占位符** — 初始提交时加入，注释声称 "from the ACP client" 是不准确的

### 清理范围

| 文件 | 删除内容 |
|------|---------|
| `pkg/shim/api/event_constants.go` | `EventTypeFileWrite`, `EventTypeFileRead`, `EventTypeCommand` 常量 |
| `pkg/shim/api/event_types.go` | `FileWriteEvent`, `FileReadEvent`, `CommandEvent` 结构体及其 `eventType()` 方法 |
| `pkg/shim/api/shim_event.go` | `decodeEventPayload()` 中对应的 case 分支 |
| `docs/design/runtime/shim-rpc-spec.md` | Typed Event 表格中的 `file_write`, `file_read`, `command` 行；`shim/event` Category 列表中的引用 |

### rg 验证

删除后执行以下命令，结果应为空（或仅匹配本方案文档和审查记录中的引用）：

```bash
rg "EventTypeFileWrite|EventTypeFileRead|EventTypeCommand|FileWriteEvent|FileReadEvent|CommandEvent|file_write|file_read" \
  --glob '!docs/plan/*' \
  --glob '!.gsd/*'
```

如果 `pkg/shim/server/translator_test.go`、`translate_rich_test.go`、`wire_shape_test.go` 中有引用，需同步清理。
如果 `pkg/tui/chat/*` 中有引用，也需同步清理。

### 不删除

- 如果未来 ACP 增加 side-channel 事件（如 fs/terminal 权限通知），再按实际 ACP 定义重新加入

---

## 改动 7：ACP bootstrap 时捕获 AgentInfo + Capabilities

当前 `pkg/shim/runtime/acp/runtime.go` 中 `conn.Initialize()` 的返回值被丢弃（`_`）。
需要捕获 `InitializeResponse`，从中提取 `AgentInfo` 和 `AgentCapabilities`，写入 state.json 的 `Session` 字段。

```go
// In Manager.Create(), replace:
//   _, handshakeErr = conn.Initialize(ctx, req)
// with:
initResp, handshakeErr := conn.Initialize(ctx, acp.InitializeRequest{
    ProtocolVersion:    acp.ProtocolVersionNumber,
    ClientCapabilities: acp.ClientCapabilities{},
})
if handshakeErr != nil {
    return fmt.Errorf("runtime: acp initialize: %w", handshakeErr)
}

// Store capabilities for bootstrap-complete state write.
m.mu.Lock()
m.initResp = initResp
m.mu.Unlock()
```

Manager 增加字段：
```go
type Manager struct {
    // ... existing fields ...
    initResp acp.InitializeResponse // captured during Create(), used once at bootstrap-complete
}
```

在 `bootstrap-complete` 写 state 时填充 Session（使用新 writeState 闭包模式）：

```go
session := buildBootstrapSession(m.initResp)

m.writeState(func(st *apiruntime.State) {
    st.Status  = apiruntime.StatusIdle
    st.PID     = cmd.Process.Pid
    st.Session = session // agentInfo + capabilities
}, "bootstrap-complete")
```

`buildBootstrapSession` 提取 InitializeResponse 中的静态能力描述：

```go
func buildBootstrapSession(resp acp.InitializeResponse) *apiruntime.SessionState {
    session := &apiruntime.SessionState{}
    if resp.AgentInfo != nil {
        session.AgentInfo = &apiruntime.AgentInfo{
            Name:    resp.AgentInfo.Name,
            Version: resp.AgentInfo.Version,
            Title:   resp.AgentInfo.Title,
        }
    }
    session.Capabilities = convertAgentCapabilities(resp.AgentCapabilities)
    return session
}
```

这些字段在 ACP bootstrap 时一次性设定，之后不变。

### Bootstrap 合成事件

**问题**：`mgr.Create()` 在 Translator 创建前完成（`command.go` 启动顺序）。bootstrap-complete 时 stateChangeHook 尚未注册，
因此 agentInfo/capabilities 写入 state.json 但订阅方无法通过事件得知。

**方案**：在 `command.go` 中，Translator 启动并注册所有 hook 之后，主动发一个合成 state_change：

```go
// command.go — after trans.Start() and all hooks registered:
// Emit synthetic state_change for bootstrap metadata written before Translator existed.
st, _ := mgr.GetState()
trans.NotifyStateChange(
    st.Status.String(), st.Status.String(), st.PID,
    "bootstrap-metadata", []string{"agentInfo", "capabilities"},
)
```

这个合成事件的特征：
- `previousStatus == status == "idle"`（lifecycle 无变化）
- `reason: "bootstrap-metadata"`
- `sessionChanged: ["agentInfo", "capabilities"]`

订阅方可据此获知 agent 的静态能力描述已可用。

---

## 改动 8：派生字段触发规则

### 规则

`updatedAt` 和 `eventCounts` 是**派生字段（derived fields）**，遵循以下规则：

1. **每次 state 写入自动更新**：由 `writeState()` 和 `updateSessionMetadata()` 在 `spec.WriteState()` 前统一设置
2. **不独立触发 `state_change`**：它们只随其他 state 写入搭载更新，不作为独立变更源
3. **不出现在 `sessionChanged`**：`sessionChanged` 只包含显式的 session metadata 字段名（如 `"configOptions"`, `"availableCommands"`）
4. **不产生递归**：state_change 事件导致 eventCounts 增长，但下一次 eventCounts 写入是在 state_change 之后的某次 state write 中搭载的，不会产生新的 state_change

### 逐项定义表

| 字段 | 独立触发 state_change | 出现在 sessionChanged | 说明 |
|------|:---:|:---:|------|
| `status` | ✅ | ❌ | lifecycle 变更，由 writeState 自动 emit |
| `agentInfo` | ❌（bootstrap 合成事件） | ✅ | 只在 bootstrap-metadata 合成事件中 |
| `capabilities` | ❌（bootstrap 合成事件） | ✅ | 只在 bootstrap-metadata 合成事件中 |
| `availableCommands` | ✅（通过 updateSessionMetadata） | ✅ | metadata hook 路径 |
| `configOptions` | ✅（通过 updateSessionMetadata） | ✅ | metadata hook 路径 |
| `sessionInfo` | ✅（通过 updateSessionMetadata） | ✅ | metadata hook 路径 |
| `currentMode` | ✅（通过 updateSessionMetadata） | ✅ | metadata hook 路径 |
| `updatedAt` | ❌ | ❌ | 派生字段，搭载写入 |
| `eventCounts` | ❌ | ❌ | 派生字段，搭载写入 |

### 测试断言

- 一个 metadata 事件（如 config_option）产生恰好 **1 条** state_change，不会因 eventCounts/updatedAt 变化产生额外事件
- state_change 的 `sessionChanged` 只包含 `["configOptions"]`，不包含 `"eventCounts"` 或 `"updatedAt"`
- 连续两次相同 metadata 更新各产生 1 条 state_change（因为 updatedAt 变化不阻止 emit，但也不独立触发）

---

## 改动 9：runtime-spec/api 完整支持类型清单

runtime-spec/api 包需新增以下类型，用于 `SessionState` 中的 `AvailableCommands` 和 `ConfigOptions` 字段。
**JSON wire shape 与 `pkg/shim/api` 对应事件 payload 完全一致**。
通过**复制纯类型定义**实现，**不** import `pkg/shim/api`，保持 runtime-spec 的独立性。

### 新增类型列表

| 类型 | 说明 | 是否需要 custom MarshalJSON |
|------|------|:---:|
| `AvailableCommand` | 可用命令 | ❌ |
| `AvailableCommandInput` | 命令输入参数（union: Unstructured） | ✅ field-presence 判别 |
| `UnstructuredCommandInput` | 非结构化输入（hint） | ❌ |
| `ConfigOption` | 配置选项（union: Select） | ✅ "type" 判别器 |
| `ConfigOptionSelect` | Select 类型配置选项 | ❌ |
| `ConfigSelectOptions` | 选项列表（union: Ungrouped/Grouped） | ✅ array element shape 判别 |
| `ConfigSelectOption` | 单个选项 | ❌ |
| `ConfigSelectGroup` | 选项分组 | ❌ |

### 类型定义

```go
// ── AvailableCommand support types ──────────────────────────────────────

// AvailableCommand mirrors acp.AvailableCommand.
type AvailableCommand struct {
    Meta        map[string]any         `json:"_meta,omitempty"`
    Name        string                 `json:"name"`
    Description string                 `json:"description"`
    Input       *AvailableCommandInput `json:"input,omitempty"`
}

// AvailableCommandInput mirrors acp.AvailableCommandInput — union with no type
// discriminator, matched by field presence (hint field => Unstructured variant).
type AvailableCommandInput struct {
    Unstructured *UnstructuredCommandInput `json:"-"`
}

// Custom MarshalJSON/UnmarshalJSON — identical logic to pkg/shim/api version.
// Discriminated by field presence: "hint" => Unstructured.

// UnstructuredCommandInput mirrors acp.UnstructuredCommandInput.
type UnstructuredCommandInput struct {
    Meta map[string]any `json:"_meta,omitempty"`
    Hint string         `json:"hint"`
}

// ── ConfigOption support types ──────────────────────────────────────────

// ConfigOption mirrors acp.SessionConfigOption — union with "type" discriminator.
// Currently single variant: select.
type ConfigOption struct {
    Select *ConfigOptionSelect `json:"-"`
}

// Custom MarshalJSON: wraps with {"type":"select", ...ConfigOptionSelect fields}.
// Custom UnmarshalJSON: reads "type" field, dispatches to Select variant.
// Identical logic to pkg/shim/api version.

// ConfigOptionSelect mirrors acp.SessionConfigOptionSelect.
type ConfigOptionSelect struct {
    Meta         map[string]any      `json:"_meta,omitempty"`
    ID           string              `json:"id"`
    Name         string              `json:"name"`
    CurrentValue string              `json:"currentValue"`
    Description  *string             `json:"description,omitempty"`
    Category     *string             `json:"category,omitempty"`
    Options      ConfigSelectOptions `json:"options"`
}

// ConfigSelectOptions mirrors acp.SessionConfigSelectOptions — union of ungrouped/grouped.
// JSON wire shape is a bare array; discriminated by element structure.
type ConfigSelectOptions struct {
    Ungrouped []ConfigSelectOption `json:"-"`
    Grouped   []ConfigSelectGroup  `json:"-"`
}

// Custom MarshalJSON: marshals Ungrouped or Grouped as JSON array.
// Custom UnmarshalJSON: inspects first array element to discriminate:
//   "group" + "options" fields => Grouped; "value" field => Ungrouped.
// Identical logic to pkg/shim/api version.

// ConfigSelectOption mirrors acp.SessionConfigSelectOption.
type ConfigSelectOption struct {
    Meta        map[string]any `json:"_meta,omitempty"`
    Name        string         `json:"name"`
    Value       string         `json:"value"`
    Description *string        `json:"description,omitempty"`
}

// ConfigSelectGroup mirrors acp.SessionConfigSelectGroup.
type ConfigSelectGroup struct {
    Meta    map[string]any       `json:"_meta,omitempty"`
    Group   string               `json:"group"`
    Name    string               `json:"name"`
    Options []ConfigSelectOption `json:"options"`
}
```

### 实现说明

1. **Custom marshal 逻辑直接复制** `pkg/shim/api/event_types.go` 中对应类型的 MarshalJSON/UnmarshalJSON
2. **JSON wire shape 完全一致**：state.json 中 `configOptions` 的 JSON 与 `shim/event` payload 中的一模一样
3. **无 import 依赖**：runtime-spec/api 不 import pkg/shim/api，两套类型独立维护
4. **未来共享**：如果类型演化频繁，可抽取到无依赖的共享 internal/types 包，但当前复制更简单

---

## 实现顺序

1. **清理死代码**（改动 6）— 删除 file_write/file_read/command 占位符 + rg 验证 + 更新受影响的测试
2. **扩展 runtime-spec state.go**（改动 1 + 改动 9）— 定义 SessionState, AgentInfo, AgentCapabilities, AvailableCommand, ConfigOption 等全部类型 + UpdatedAt, EventCounts 字段 + custom MarshalJSON
3. **writeState 重构为读-改-写**（改动 3）— 新签名 `writeState(apply func(*State), reason)`，迁移所有调用方
4. **ACP bootstrap 捕获 capabilities**（改动 7）— Initialize 返回值写入 state.Session + bootstrap 合成事件
5. **Translator 增加 eventCounts 统一计数 + EventCounts() 方法 + sessionMetadataHook**（改动 2 前置）
6. **Manager.updateSessionMetadata + Service 层接线**（改动 2）— metadata hook → state.json 写入 → state_change
7. **`runtime/status` 返回实时 eventCounts**（改动 5）— Status() 合并内存计数
8. **验证派生字段触发规则**（改动 8）— 断言 updatedAt/eventCounts 不独立触发 state_change
9. **验证 usage 透传**（改动 4）— 现有实现已覆盖，只需验证
10. **更新设计文档**（shim-rpc-spec.md, agent-shim.md）— 反映所有变更

## 涉及文件

| 文件 | 改动 |
|------|------|
| `pkg/runtime-spec/api/state.go` | 新增 SessionState, AgentInfo, AgentCapabilities, McpCapabilities, PromptCapabilities, SessionCapabilities, SessionInfo, AvailableCommand, AvailableCommandInput, UnstructuredCommandInput, ConfigOption, ConfigOptionSelect, ConfigSelectOptions, ConfigSelectOption, ConfigSelectGroup 等类型（含 custom MarshalJSON/UnmarshalJSON）；新增 UpdatedAt, Session, EventCounts 字段 |
| `pkg/shim/runtime/acp/runtime.go` | 捕获 Initialize 返回值；新增 `initResp` 字段；`writeState()` 重构为读-改-写闭包接口 `writeState(apply func(*State), reason)`；所有调用点迁移为闭包模式；新增 `updateSessionMetadata()`、`buildBootstrapSession()`、`SetEventCountsFn()`、`eventCountsFn` 字段；`StateChange` struct 增加 SessionChanged |
| `pkg/shim/server/translator.go` | 新增 `eventCounts map[string]int` 字段；`broadcast()` 中 nextSeq++ 后 eventCounts++；新增 `EventCounts()` 返回快照；新增 `sessionMetadataHook` 字段 + `SetSessionMetadataHook()`；`run()` 中调用 `maybeNotifyMetadata()`；`NotifyStateChange` 签名增加 `sessionChanged []string` |
| `cmd/agentd/subcommands/shim/command.go` | bootstrap 合成事件（trans.NotifyStateChange "bootstrap-metadata"）；接线 `SetSessionMetadataHook`、`SetEventCountsFn`、`buildSessionUpdate` |
| `pkg/shim/server/service.go` | `Status()` 合并内存 eventCounts |
| `pkg/shim/api/event_types.go` | StateChangeEvent 增加 SessionChanged 字段；删除 FileWriteEvent/FileReadEvent/CommandEvent |
| `pkg/shim/api/event_constants.go` | 删除 EventTypeFileWrite/EventTypeFileRead/EventTypeCommand 常量 |
| `pkg/shim/api/shim_event.go` | 删除 decodeEventPayload 中对应 case |
| `pkg/shim/server/translator_test.go` | 更新：覆盖 eventCounts（ACP + manual + state_change）；覆盖 metadata hook 回调；清理死代码引用 |
| `pkg/shim/server/translate_rich_test.go` | 清理死代码引用（如有） |
| `pkg/shim/server/wire_shape_test.go` | 清理死代码引用（如有） |
| `pkg/runtime-spec/state_test.go` (新增或扩展) | state round-trip 覆盖 updatedAt/session/eventCounts |
| `pkg/shim/runtime/acp/runtime_test.go` | updateSessionMetadata + bootstrap capabilities + UpdatedAt 全路径 |
| `docs/design/runtime/shim-rpc-spec.md` | 更新 state.json schema、删除 file_write/file_read/command、扩展 state_change reason + sessionChanged |
| `docs/design/runtime/agent-shim.md` | 提及 session metadata 写入 state 的职责 + capabilities 捕获 + metadata hook 链路 |

### rg 清理验证

每步完成后执行验证确认无残留引用：

```bash
# 死代码清理后
rg "EventTypeFileWrite|EventTypeFileRead|EventTypeCommand|FileWriteEvent|FileReadEvent|CommandEvent" \
  --glob '!docs/plan/*' --glob '!.gsd/*'

# 确认 file_write/file_read 只出现在设计文档历史/plan 中
rg "file_write|file_read" --glob '!docs/plan/*' --glob '!.gsd/*' --glob '!docs/design/*'
```

## 测试与验收标准

### 必须新增/修改的测试

| 测试类别 | 文件 | 覆盖内容 |
|---------|------|---------|
| **State round-trip** | `pkg/runtime-spec/state_test.go` | WriteState → ReadState 覆盖完整 State（含 updatedAt, session 全部子字段, eventCounts）；验证 JSON 输出稳定性（排序后的 commands/configOptions） |
| **Translator eventCounts** | `pkg/shim/server/translator_test.go` | ① ACP 翻译事件（text, tool_call 等）计数正确；② 手工事件（turn_start, user_message, turn_end）计数正确；③ NotifyStateChange 计数正确；④ log append 失败时不计数（注入 failing log）；⑤ EventCounts() 返回 copy 而非引用 |
| **Metadata hook + state_change** | `pkg/shim/server/translator_test.go` 或 integration | ① broadcastSessionEvent 后 hook 被调用；② config_option/available_commands/session_info/current_mode 触发 hook；③ text/thinking/usage 不触发 hook；④ hook 中收到正确的 Event 类型 |
| **Manager.updateSessionMetadata** | `pkg/shim/runtime/acp/runtime_test.go` | ① 同 status 下更新 session 字段，state.json 正确写入；② state_change 被触发且 reason/sessionChanged 正确；③ Session 字段合并保留已有字段（更新 configOptions 不清除 availableCommands）；④ UpdatedAt 被设置 |
| **Manager.writeState UpdatedAt** | `pkg/shim/runtime/acp/runtime_test.go` | 所有 writeState 路径（bootstrap-started, bootstrap-complete, prompt-started 等）的结果 state 都有 UpdatedAt |
| **ACP bootstrap capabilities** | `pkg/shim/runtime/acp/runtime_test.go` | bootstrap-complete 后 state.json 包含 AgentInfo + Capabilities，字段值与 InitializeResponse 一致 |
| **Bootstrap 合成事件** | `pkg/shim/server/translator_test.go` 或 integration | bootstrap-metadata 合成事件正确发出，sessionChanged 为 ["agentInfo", "capabilities"]，previousStatus == status == "idle" |
| **writeState 读-改-写** | `pkg/shim/runtime/acp/runtime_test.go` | ① Kill() 后 state.json 保留 Session（不被覆盖）；② process-exited 后 EventCounts 仍在；③ Prompt() 写入带最新 EventCounts |
| **派生字段不独立触发** | `pkg/shim/server/translator_test.go` 或 integration | 一个 config_option 事件产生恰好 1 条 state_change；sessionChanged 不含 "eventCounts" 或 "updatedAt" |
| **runtime/status overlay** | `pkg/shim/server/service_test.go` | Status() 返回的 eventCounts 是 Translator 内存值（而非 state.json 中的滞后值） |
| **Usage 透传** | `pkg/shim/server/translator_test.go` | usage 事件正确翻译、广播、记入 log；subscriber 收到 usage event；usage 不触发 metadata hook |
| **Dead-code 删除** | `pkg/shim/api/*_test.go`, `pkg/shim/server/*_test.go` | decodeEventPayload 不再包含 file_write/file_read/command case；golden test / wire shape test 同步更新 |

### 验收标准

1. `make build` 通过
2. `go test ./pkg/runtime-spec/... ./pkg/shim/...` 全部通过
3. `rg "EventTypeFileWrite|EventTypeFileRead|EventTypeCommand|FileWriteEvent|FileReadEvent|CommandEvent" --glob '!docs/plan/*' --glob '!.gsd/*'` 无输出
4. `rg "file_write|file_read" --glob '!docs/plan/*' --glob '!.gsd/*' --glob '!docs/design/*'` 无输出（或仅 event log 历史相关注释）
5. state.json round-trip 测试验证所有新字段的序列化/反序列化正确性（含 ConfigOption/AvailableCommand 的 custom marshal）
6. metadata-only state_change 测试验证同 status 下也能触发事件
7. Kill()/process-exited 后 state.json 仍包含 Session 和 EventCounts（读-改-写不覆盖）
8. bootstrap-metadata 合成事件正确发出，sessionChanged 为 `["agentInfo", "capabilities"]`
9. 一个 metadata 事件产生恰好 1 条 state_change（派生字段不产生额外事件）

---

## 审查记录

### codex 第1轮

#### ✅ 认可项

1. **usage 不落盘、只走事件流的方向正确**
   - usage 属于高频度量数据，已有 Translator → `shim/event` → event log 的实时与回放链路，避免写入 state.json 是合理取舍。

2. **清理 `file_write` / `file_read` / `command` 占位事件的方向正确**
   - 当前 ACP `SessionUpdate` union 没有这些 variant，Translator 也没有产出路径，继续保留会误导协议消费者。

3. **`runtime/status` 叠加内存 `eventCounts` 的思路正确**
   - 如果 `eventCounts` 采用懒写入，仅从 state.json 读取会天然滞后；Status 返回前 overlay 内存快照可以同时满足低 IO 和实时查询。

4. **排序后全量替换 `availableCommands` / `configOptions` 的语义合理**
   - ACP 通知本身是完整列表，写入 state 前排序能减少无意义 diff，也便于消费方稳定定位。

#### ❌ 问题项

1. **metadata 更新链路描述与当前代码结构不符**
   - Manager 看不到翻译后的事件，不能与 Translator 竞争消费同一个 channel。
2. **metadata-only state 写入不会触发现有 `state_change`**
   - 当前 writeState 只在 status 变化时 emit。
3. **`UpdatedAt` 设计缺少实际落点**
   - 需要明确由哪一层负责设置。
4. **ACP capabilities schema 与当前 SDK 类型不一致**
   - loadSession 是顶层字段，命名和结构需与 SDK 对齐。
5. **`SessionState` 漏掉了 session_info/current_mode**
   - 目标与机制冲突。
6. **事件计数的覆盖范围不完整**
   - turn_start/user_message/turn_end/state_change 是手工广播，不走 translate()。
7. **文件清单与当前代码路径不匹配**
   - 应使用 pkg/shim/ 下的真实路径。
8. **缺少测试与验收标准**
   - 跨 state/event/metadata 多模块改动需要完备的测试矩阵。

**结论：未批准。**

#### 用户补充要求

1. `SessionState` 必须包含 `session_info` 与 `current_mode` 对应状态。
2. state 中非 `status` 部分的变更也必须通过 event 发出去。

### claude-code 第2轮

逐条回应并修订：

**问题 1 — metadata 更新链路（接受）**
- 接受：原方案中"Manager 监听 session metadata event"不可行。
- 修订：引入 Translator `sessionMetadataHook` 回调模式。在 `run()` 中，`broadcastSessionEvent(ev)` 返回（锁释放）后，调用 `maybeNotifyMetadata(ev)` → hook → `Manager.updateSessionMetadata()`。详见改动 2 修订版。
- 锁顺序：`Translator.mu → 释放 → Manager.mu → 释放 → Translator.mu`，无嵌套无死锁。
- 失败处理：state.json 写失败只 log error，不回滚已广播的 session 事件。

**问题 2 — metadata-only state_change（接受）**
- 接受：现有 `writeState()` 的条件逻辑不适合 metadata-only 更新。
- 修订：新增 `Manager.updateSessionMetadata(changed, apply)` 方法，独立于 `writeState()`。在同一临界区内读 state → apply → 设 UpdatedAt → 刷 EventCounts → 写 state → 显式 emit `StateChange{Status: same, Reason: ..., SessionChanged: changed}`。详见改动 2。

**问题 3 — UpdatedAt 落点（接受）**
- 接受：需要明确统一设置位置。
- 修订：在 `Manager.writeState()` 中 `spec.WriteState()` 调用前统一 set `state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)`。在新增的 `updateSessionMetadata()` 中同样设置。这覆盖所有 state 写路径。不在 `spec.WriteState()` 层改动。详见改动 3。

**问题 4 — ACP capabilities schema（接受）**
- 接受：原方案的字段命名与 ACP wire format 不一致。
- 修订：选择"原样镜像 ACP JSON shape"。字段命名改为 `loadSession`（顶层）、`mcpCapabilities`、`promptCapabilities`、`sessionCapabilities`。`loadSession` 不放入 SessionCapabilities。SessionCapabilities 使用 `Fork *SessionForkCapabilities` 等指针存在性语义（与 SDK 完全一致）。详见改动 1 AgentCapabilities 节。增加完整转换规则表和 `convertAgentCapabilities()` 实现。

**问题 5 — SessionState 漏掉 session_info/current_mode（接受）**
- 接受：目标 1 说"session 元信息（不含 usage）"，session_info 和 current_mode 属于 session 元信息，应纳入。
- 修订：SessionState 增加 `SessionInfo *SessionInfo` 和 `CurrentMode *string`。两者变更时触发 state_change，reason 分别为 `"session-info-updated"` 和 `"mode-updated"`。metadata hook 中增加 SessionInfoEvent 和 CurrentModeEvent 的处理。详见改动 1 SessionState 定义和改动 2 buildSessionUpdate。
- 同时响应用户补充要求：SessionState 必须包含 session_info 与 current_mode。

**问题 6 — eventCounts 覆盖范围（接受）**
- 接受：原方案在 `translate()` 处计数会漏掉手工广播事件。
- 修订：计数点改为 `Translator.broadcast()` 中 `t.nextSeq++` 之后，按 `ev.Type` 计数。这是所有事件进入事件流的唯一入口，自然覆盖 ACP 翻译事件 + 手工广播事件（turn_start/user_message/turn_end/state_change）。log append 失败时提前返回，不 increment nextSeq 也不计数。详见改动 1 EventCounts 节。

**问题 7 — 文件清单路径（接受）**
- 接受：旧路径已过期。
- 修订：所有文件路径更新为当前真实路径（pkg/shim/api/*, pkg/shim/server/*, pkg/shim/runtime/acp/*）。增加 rg 清理验证命令。增加 translator_test.go、translate_rich_test.go、wire_shape_test.go 到受影响文件列表。详见涉及文件表。

**问题 8 — 测试与验收标准（接受）**
- 接受：需要完备的测试矩阵。
- 修订：新增完整"测试与验收标准"章节，覆盖 9 类测试：state round-trip、Translator eventCounts（含失败不计数）、metadata hook + state_change、Manager.updateSessionMetadata（含字段合并保留）、UpdatedAt 全路径、bootstrap capabilities、runtime/status overlay、usage 透传、dead-code 删除后的 decode/golden 更新。详见测试与验收标准节。

**用户补充要求 — state 非 status 变更必须通过 event 发出（接受）**
- 修订：所有 4 类 metadata 变更（config_option, available_commands, session_info, current_mode）都触发 state_change 事件。事件中 `previousStatus == status`（lifecycle 不变），通过 `sessionChanged` 字段标明变更范围。消费者可据此判断是 lifecycle 变更还是 metadata 变更。

## 最终方案

**状态：已批准（codex 第3轮 final-approved）**

最终方案即本文档改动 1–9 的全部内容，实现顺序按"实现顺序"章节执行。

### 实现约束（RISK）

以下 4 个风险项由 codex 审查时提出，作为实现约束必须遵守：

**RISK-1：writeState 首次写入判断**
- `writeState()` 中 `spec.ReadState()` 返回错误时，**只允许** `os.ErrNotExist`（或等价的 file-not-found）走 zero-value 路径
- JSON 损坏、权限错误等其他错误**必须直接返回**，不写文件、不发 state_change
- 实现时使用 `errors.Is(readErr, os.ErrNotExist)` 判断

**RISK-2：bootstrap 合成事件的到达保证**
- 合成事件在 RPC server listen 前发送，**保证进入 event log/history**
- **不保证** fresh live subscriber 实时收到（因为 subscriber 注册在 listen 之后）
- 测试和文档不宣称所有 live subscriber 必达
- agentd 如需实时处理 bootstrap metadata，应通过 subscribe 的 backfill 机制（fromSeq）回放获取

**RISK-3：eventCounts 的一条滞后**
- state_change 事件自身的计数（`eventCounts["state_change"]++`）发生在 `broadcast()` 中
- 但触发该 state_change 的 `writeState()` 调用已经在 broadcast 之前写入了 `eventCounts` 快照
- 因此磁盘上的 state.json 中 `eventCounts.state_change` 会比实际**少 1**（缺少刚发出的那条）
- 测试不要求磁盘 state 立即包含刚发出的 state_change 计数
- `runtime/status` overlay 可返回 Translator 内存中的最新值（已覆盖）

**RISK-4：UpdateSessionMetadata 导出边界**
- `Manager.UpdateSessionMetadata()` 必须导出（大写），因为 command.go 中 Service 层接线需要外部调用
- `buildSessionUpdate()`、`buildBootstrapSession()` 等 helper 放在调用侧（`cmd/agentd/subcommands/shim/` 或 `pkg/shim/server/`），避免反向依赖
- `convertAgentCapabilities()` 放在 `pkg/shim/runtime/acp/` 内部（不导出），因为只有 Manager 使用

### codex 第2轮

#### ✅ 认可项

1. **metadata 更新链路已从不可实现的 Manager 监听模式收敛为 Translator hook 模式**
   - 修订版承认当前 `Manager.Events()` 是单消费者 channel，改为 `Translator` 在 session event 已进入事件流后回调 metadata hook，整体方向可实现，也避免了 Manager 和 Translator 竞争消费 ACP notification。

2. **`session_info` / `current_mode` 已按用户要求纳入 `SessionState`**
   - 新增 `SessionInfo` 与 `CurrentMode`，并为两者定义 `sessionChanged` 与 reason，解决了第 1 轮目标冲突。

3. **metadata-only 变更会显式发 `state_change`**
   - 新增 `updateSessionMetadata()` 独立于 lifecycle `writeState()`，并允许 `previousStatus == status` 的 state_change，符合用户要求“state 中非 status 的部分变更也通过 event 发出去”。

4. **eventCounts 计数点移动到统一 broadcast 成功路径是正确修正**
   - 这覆盖 ACP 翻译事件、手工 turn 事件和 runtime `state_change`，且 log append 失败不计数的语义清晰。

5. **capabilities 改为镜像 ACP JSON shape，方向正确**
   - `loadSession` 顶层保留、`mcpCapabilities` / `promptCapabilities` / `sessionCapabilities` 命名与 SDK 对齐，基本消除了第 1 轮 schema 歧义。

#### ❌ 问题项

1. **lifecycle 写 state 仍可能覆盖已持久化的 Session/EventCounts**
   - 问题是什么：方案只在 `Manager.updateSessionMetadata()` 中读当前 state 后合并字段；但 `Manager.writeState(state, reason)` 仍接收调用点传入的完整 `State`。当前代码里 `Create()`、`Kill()`、process-exited 等路径大量用 `apiruntime.State{...}` 字面量重建 state，若照方案只加 `UpdatedAt`，这些写入会把已经存在的 `Session` 和 `EventCounts` 清空。`Prompt()` 的两次写入虽然先读 state 再改 status，但 `Kill()` 和 process-exited 明确会丢 metadata。
   - 为什么是问题：这直接破坏目标 1/5。外部刚通过 state 看到的 session metadata，会在 stop/process exit 后消失；eventCounts 也不会“state 变更时一并写入”。
   - 期望如何解决：方案必须规定 lifecycle `writeState()` 也是保留式写入：写前读取 previous state，将未显式覆盖的 `Session`、`EventCounts`（从 `eventCountsFn` 最新快照）、必要时 `ExitCode`/Annotations 等字段合并进去，再写 state。更好的接口是不要让调用点传完整 `State` 字面量，而是提供 `updateLifecycleStatus(status, pid, exitCode, reason)` 这类读-改-写方法，保证所有 state 写路径不会丢 session 子树。

2. **`writeState()` 没有刷新 EventCounts，和方案目标冲突**
   - 问题是什么：修订文档多处说 EventCounts “flushed to state.json on every state write”，但第 3 节给出的 `Manager.writeState()` 代码只设置 `UpdatedAt`，没有 `state.EventCounts = m.getEventCounts()`。只有 `updateSessionMetadata()` 里刷新了 EventCounts。
   - 为什么是问题：status 变更、prompt-started/completed、process-exited 等最重要的 state 写路径不会带最新 eventCounts；`runtime/status` 虽然 overlay 内存值，但 state.json 本身仍不满足设计目标。
   - 期望如何解决：把 `EventCounts` 刷新明确放入所有 state 写路径，尤其是 lifecycle `writeState()`。测试也要验证 status-only 写入后 state.json 中 eventCounts 被更新。

3. **bootstrap 写入的 `agentInfo/capabilities` 没有按“非 status state 变更”明确发出变更范围**
   - 问题是什么：用户补充要求非 `status` 部分变更也要通过 event 发出。修订版只明确 4 类 runtime metadata（config/options/session_info/current_mode）触发 `sessionChanged`，但 bootstrap 阶段写入的 `agentInfo` / `capabilities` 也是 `SessionState` 变更。当前 shim command 又是在 `mgr.Create()` 完成后才创建 Translator 和注册 stateChangeHook，因此 bootstrap-complete 期间即使 status 变了，也没有外部 `state_change` 事件，更不会携带 `sessionChanged:["agentInfo","capabilities"]`。
   - 为什么是问题：这留下了一个明显例外：最重要的静态能力描述进入 state.json，但订阅方无法通过 event 知道这些非 status 字段已出现。它也没有响应第 1 轮补充要求里“bootstrap capabilities 是否触发 event 需要逐项定义”。
   - 期望如何解决：必须明确 bootstrap capabilities 的事件语义。可选方案：
     - 保持现有 bootstrap boundary，但在 Translator 启动并注册 hook 后主动发一个 runtime `state_change`，`previousStatus == status == idle`，`reason:"session-metadata-updated"`，`sessionChanged:["agentInfo","capabilities"]`；
     - 或调整启动顺序，让 bootstrap-complete 的 state_change 能被捕获，并携带 `sessionChanged`。
     - 若要例外处理，必须在方案中明确写出“bootstrap agentInfo/capabilities 不发 event”的理由，但这会与用户要求冲突，不建议。

4. **`UpdatedAt` / `EventCounts` 自身是否触发 state_change 仍未逐项定义**
   - 问题是什么：用户补充要求“config options、available commands、session info、current mode、bootstrap capabilities、eventCounts/updatedAt 是否触发 event 需要逐项定义”。修订版定义了前四类，遗漏了 `agentInfo/capabilities`（见上）以及 `eventCounts/updatedAt`。
   - 为什么是问题：如果 eventCounts 或 UpdatedAt 也触发 state_change，会产生自增/递归/噪声风险；如果不触发，需要明确它们是派生字段，只随其他 state 写入搭载。
   - 期望如何解决：在方案中增加明确规则：`updatedAt` 和 `eventCounts` 不作为独立 state_change 触发源，也不出现在 `sessionChanged`；它们只作为任意 state 写入的附带派生字段更新。并补充测试断言 metadata event 产生一条 metadata state_change，而不会因 eventCounts/updatedAt 再产生额外 state_change。

5. **Manager/Translator 锁顺序描述与伪代码不一致，且 writeState 缺少统一锁策略**
   - 问题是什么：文档说锁顺序是 `Translator.mu → 释放 → Manager.mu → 释放 → Translator.mu`，但 `updateSessionMetadata()` 伪代码 `defer m.mu.Unlock()` 后调用 `m.emitSessionStateChange()`，实际是持有 `Manager.mu` 时进入 stateChangeHook，再进入 `trans.NotifyStateChange()` 获取 `Translator.mu`。另外 lifecycle `writeState()` 伪代码没有加 `Manager.mu`，metadata 写和 lifecycle 写可能并发操作同一个 state.json。
   - 为什么是问题：锁顺序文档不准确会误导实现；更重要的是 metadata 写与 prompt/stop/process-exited 写并发时可能出现 lost update。即使没有死锁，也会有状态覆盖风险。
   - 期望如何解决：明确统一锁策略：所有 Manager state 读写（lifecycle + metadata）都必须在 `Manager.mu` 下进行；调用 stateChangeHook 前复制 hook/change 后释放 `Manager.mu`，再调用 hook，避免持有 Manager 锁进入 Translator。伪代码要与这个策略一致。

6. **runtime-spec/api 独立类型的支持结构仍未完整列出**
   - 问题是什么：`SessionState` 使用 `AvailableCommand`、`ConfigOption` 等类型，但 schema 章节只完整定义了 capabilities/sessionInfo，没有列出 runtime-spec/api 中要新增的 AvailableCommand、ConfigOption、ConfigSelectOptions 等镜像类型，也没有说明是否复制 `pkg/shim/api` 的 union marshal 逻辑。
   - 为什么是问题：AGENTS 要求 api 包只有纯类型，且方案强调 runtime-spec/api 不依赖 events 包。若这些类型定义不完整，执行时容易直接 import `pkg/shim/api` 或漏掉自定义 MarshalJSON，导致 state.json shape 与 `shim/event` shape 不一致。
   - 期望如何解决：在方案中补充 runtime-spec/api 需要新增的完整 support types 清单，并明确 JSON shape 与 `pkg/shim/api` 对应事件 payload 一致；如果要复用逻辑，应通过复制纯类型或抽取到无依赖的共享 api 包，不能让 `runtime-spec/api` import `pkg/shim/api`。

**结论：未批准。** 第 2 轮已经解决主要方向问题，但 lifecycle state 写覆盖、bootstrap capabilities event 语义、派生字段触发规则和锁/并发策略仍需修订。下一轮是第 3 轮；若仍有分歧，需要按 RISK 强制收敛。

### claude-code 第3轮

逐条回应并修订：

**问题 1 — lifecycle writeState 覆盖 Session/EventCounts（接受）**
- 接受：原方案 `writeState(state State, reason)` 接收完整字面量，`Kill()`/process-exited 等路径会清空已持久化的 Session 和 EventCounts。
- 修订：将 `writeState` 重构为读-改-写闭包接口 `writeState(apply func(*apiruntime.State), reason string)`。实现：读当前 state.json → 值拷贝 → 调用 apply 闭包只 mutate 需要变更的字段 → 设派生字段 → 原子写。所有调用方迁移为闭包模式（bootstrap-started/complete/failed, process-exited, prompt-started/completed/failed, runtime-stop）。例如 Kill() 变为 `m.writeState(func(st) { st.Status = StatusStopped }, "runtime-stop")`，Session 和 EventCounts 自动保留。详见改动 3 修订版。

**问题 2 — writeState 没有刷新 EventCounts（接受）**
- 接受：原方案 writeState 只设 UpdatedAt，没有 `st.EventCounts = m.getEventCounts()`。
- 修订：在新 `writeState()` 中统一刷新：`st.UpdatedAt = ...` 和 `st.EventCounts = m.getEventCounts()` 放在同一位置，apply 闭包之后、`spec.WriteState()` 之前。所有 state 写路径（lifecycle + metadata）都带最新 EventCounts。详见改动 3 修订版。

**问题 3 — bootstrap capabilities 没有发事件（接受）**
- 接受：agentInfo/capabilities 在 `mgr.Create()` 时写入 state.json，但此时 Translator 和 stateChangeHook 都还不存在。
- 修订：在 `command.go` 中 `trans.Start()` 和所有 hook 注册完成后，主动发一个合成 state_change：`trans.NotifyStateChange("idle", "idle", pid, "bootstrap-metadata", ["agentInfo", "capabilities"])`。这是 `previousStatus == status` 的 metadata-only 事件，订阅方据此获知 agent 静态能力描述已可用。详见改动 7 修订版"Bootstrap 合成事件"节。

**问题 4 — updatedAt/eventCounts 是否触发 state_change 未定义（接受）**
- 接受：需要逐项明确规则。
- 修订：新增改动 8"派生字段触发规则"。明确规则：updatedAt 和 eventCounts 是派生字段，每次 state 写入自动更新，但不独立触发 state_change，不出现在 sessionChanged，不产生递归。新增逐项定义表覆盖所有 state 字段（status, agentInfo, capabilities, availableCommands, configOptions, sessionInfo, currentMode, updatedAt, eventCounts）。新增测试断言：一个 metadata 事件产生恰好 1 条 state_change。详见改动 8。

**问题 5 — Manager/Translator 锁顺序与伪代码不一致（接受）**
- 接受：原方案 updateSessionMetadata 用 `defer m.mu.Unlock()` 后调用 emitSessionStateChange，实际持有 Manager.mu 进入 hook → Translator.mu，与文档描述不一致。
- 修订：统一锁策略——所有 Manager state 读写在 `Manager.mu` 下进行；调用 stateChangeHook 前复制 hook 引用和 StateChange 数据，释放 `Manager.mu`，再调 hook。writeState 和 updateSessionMetadata 伪代码都已更新为：`m.mu.Lock() → 读-改-写 → 复制 hook + change → m.mu.Unlock() → hook(change)`。不再使用 `defer m.mu.Unlock()`。详见改动 2 和改动 3 修订版。lifecycle writeState 也加了 `m.mu.Lock()`，metadata 写和 lifecycle 写不会并发操作同一 state.json。

**问题 6 — runtime-spec/api 支持类型不完整（接受）**
- 接受：SessionState 引用的 AvailableCommand, ConfigOption 等类型未在方案中列出。
- 修订：新增改动 9"runtime-spec/api 完整支持类型清单"。列出全部 8 个新增类型（AvailableCommand, AvailableCommandInput, UnstructuredCommandInput, ConfigOption, ConfigOptionSelect, ConfigSelectOptions, ConfigSelectOption, ConfigSelectGroup），标注哪些需要 custom MarshalJSON，给出完整 Go 类型定义。JSON wire shape 与 `pkg/shim/api` 对应事件 payload 完全一致，通过复制纯类型实现，runtime-spec/api 不 import pkg/shim/api。详见改动 9。

### codex 第3轮

#### ✅ 认可项

1. **lifecycle state 写入覆盖问题已实质解决**
   - `writeState(apply func(*State), reason)` 读-改-写接口比传完整 `State` 字面量安全，能保留 `Session`、`EventCounts`、Annotations 等已有字段，适合当前多路径写 state 的代码形态。

2. **EventCounts 刷新策略已闭环**
   - 新方案把 `EventCounts = m.getEventCounts()` 放入所有 state 写路径，`runtime/status` 再叠加内存快照，磁盘态和查询态的语义都清楚。

3. **非 status state 变更的事件语义已覆盖用户要求**
   - config/options/session_info/current_mode 走 metadata hook，agentInfo/capabilities 走 bootstrap 合成事件，updatedAt/eventCounts 明确为派生字段不独立触发，整体边界清晰。

4. **锁策略已收敛到可执行形态**
   - Manager state 读写统一在 `Manager.mu` 下完成，调用 stateChangeHook 前释放 Manager 锁，避免 Manager 与 Translator 锁嵌套，这是实现时应坚持的关键约束。

5. **runtime-spec/api 类型独立性已明确**
   - 支持类型通过复制纯类型和 custom marshal 实现，不让 `runtime-spec/api` 依赖 `pkg/shim/api`，符合 api 包只放纯类型的架构约束。

#### ❌ 问题项 / RISK

1. **RISK：`writeState` 不能把任意 `ReadState` 错误都当成首次写入**
   - 问题是什么：伪代码里 `readErr != nil` 时直接用 zero-value state。这个逻辑只适用于 state.json 不存在；如果是 JSON 损坏、权限错误、短读等错误，继续写会掩盖数据损坏并丢失已有状态。
   - 为什么是问题：state.json 是 shim 的状态真相之一，遇到非不存在错误时静默覆盖会让恢复和诊断变得不可信。
   - 最稳妥处理意见：实现时只对 `errors.Is(err, os.ErrNotExist)` 或明确的 missing state file 走 first-write zero-value；其他 `ReadState` 错误必须返回，不写文件、不发 state_change，并记录 error。

2. **RISK：bootstrap 合成事件如果在 RPC server listen 前发送，只能进 event log，不能被 live subscriber 实时收到**
   - 问题是什么：方案写在 `command.go` 中 `trans.Start()` 和 hook 注册后主动 `NotifyStateChange("bootstrap-metadata")`。当前 command 启动顺序是 `mgr.Create()` → open event log → create/start Translator → register service → listen socket。若合成事件在 listen 前发出，fresh `session/subscribe` 不会实时收到，只能通过 history/status 获取。
   - 为什么是问题：用户要求“通过 event 发出去”，event log 语义满足“进入事件流”，但不满足“任意稍后 live subscriber 都能收到”的直觉。如果 agentd fresh start 只 Subscribe 不 backfill，该事件不会走 live handler。
   - 最稳妥处理意见：接受合成事件写入 event log 作为最低语义，但实现和测试必须明确验证它进入 history；如果希望 agentd 也实时处理该事件，应把 agentd fresh subscribe 改为 `fromSeq=0` 或在 server 可订阅后再发。当前任务不强制改 agentd DB 行为，但不能把该事件描述成所有 live subscriber 必达。

3. **RISK：`eventCounts` 会滞后一条 `state_change` 写入磁盘是设计内结果**
   - 问题是什么：metadata 更新时先写 state.json，再调用 hook 发 `state_change`；`state_change` 本身的计数发生在写 state 之后，所以该次 state.json 中的 `eventCounts.state_change` 不包含刚刚发出的这条事件。
   - 为什么是问题：如果测试期望 metadata state 写后磁盘计数立即包含对应 state_change，会出现 off-by-one。方案已经说 eventCounts 是懒写入参考值，但这里需要执行时按这个语义写测试。
   - 最稳妥处理意见：测试只要求 `runtime/status` overlay 返回最新内存计数；state.json 中 eventCounts 允许落后一条刚由该写入触发的 state_change，下一次 state 写入再搭载刷新。

4. **RISK：`UpdateSessionMetadata` / `updateSessionMetadata` 的导出边界需要和调用点一致**
   - 问题是什么：文档部分伪代码使用 `updateSessionMetadata`，Service/command 接线使用 `mgr.UpdateSessionMetadata`。如果该方法由 `cmd/agentd/subcommands/shim` 或 `pkg/shim/server` 调用，必须导出。
   - 为什么是问题：这是小问题但会直接导致编译失败，且容易在实现时来回移动 helper。
   - 最稳妥处理意见：把 Manager 上供外部接线调用的方法命名为 `UpdateSessionMetadata`，内部 helper 再使用小写；`buildSessionUpdate` 可放在 shim command 或 server 包，但要避免引入反向依赖。

**结论：final-approved。** 方案可以执行；以上 RISK 作为实现约束处理，不再阻塞进入实现。
