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
    // Maintained by the shim — set automatically on every writeState() call.
    UpdatedAt string `json:"updatedAt,omitempty"`

    // Session carries ACP-reported session metadata.
    // Populated progressively as the agent reports updates via ACP notifications.
    // Nil before the first session metadata arrives.
    Session *SessionState `json:"session,omitempty"`

    // EventCounts tracks the number of events by type.
    // Updated in memory on every event; flushed to state.json on state changes.
    EventCounts map[string]int `json:"eventCounts,omitempty"`
}

// SessionState captures the latest session-level metadata reported by the agent.
// Fields are updated incrementally: each ACP notification overwrites the relevant
// field(s), leaving others unchanged.
type SessionState struct {
    // CurrentMode is the agent's current operational mode ID (from CurrentModeUpdate).
    // e.g. "plan", "code", "research"
    CurrentMode *string `json:"currentMode,omitempty"`

    // AvailableCommands lists the agent's currently available commands (from AvailableCommandsUpdate).
    // Full replacement on each update; sorted by Name.
    AvailableCommands []AvailableCommand `json:"availableCommands,omitempty"`

    // ConfigOptions lists the agent's configurable options with current values (from ConfigOptionUpdate).
    // Full replacement on each update; sorted by ID.
    ConfigOptions []ConfigOption `json:"configOptions,omitempty"`
}
```

**注意**：usage 不在 state.json 中。usage 是高频变更的度量数据，写入 state.json 会造成不必要的磁盘 IO。
usage 作为 session category 的 `shim/event` 通知实时推送给订阅方，也写入 event log 供回放。

### 排序约定

- `AvailableCommands` 按 `Name` 字典序排列
- `ConfigOptions` 按 `ID` 字典序排列

ACP 每次发完整列表，写入前先排序再替换。排序保证：
1. state.json 的 JSON 输出稳定（相同内容不因顺序产生 diff）
2. 消费方可以做 binary search 或稳定的 key 定位

### EventCounts — 事件类型计数器

`EventCounts` 是一个 `map[string]int`，key 是事件类型（如 `"text"`, `"tool_call"`, `"state_change"`），
value 是该类型事件的累计数量。

- **内存维护**：每个事件到达 Translator 时 +1，O(1) 开销
- **懒写入**：只在 state.json 因其他原因需要写入时（status 变更、session metadata 变更）顺带刷新计数
- **不单独触发写入**：text/thinking/tool_call 等高频事件只更新内存计数，不写磁盘

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
  "updatedAt": "2026-04-14T10:30:00Z",
  "session": {
    "currentMode": "code",
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
    ]
  },
  "eventCounts": {
    "text": 142,
    "thinking": 28,
    "tool_call": 15,
    "tool_result": 15,
    "turn_start": 3,
    "turn_end": 3,
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
- **全量替换 + 排序语义**：`availableCommands` 和 `configOptions` 每次收到事件都全量替换（与 ACP 语义一致），写入前按唯一 key 排序。
- **usage 不落盘**：高频度量数据只走内存 + 通知 + event log，不写 state.json。
- **eventCounts 懒写入**：内存中实时更新，搭便车写入，避免高频磁盘 IO。
- **runtime-spec/api 包独立定义类型**：不依赖 events 包，保持 runtime-spec 的独立性。JSON wire shape 保持一致。

---

## 改动 2：session metadata 变更触发 state_change

### 机制

当 Translator 收到 config_option / current_mode / available_commands 事件时：

1. **照常翻译**为对应的 session category 事件（保持现有行为不变）
2. **额外更新 state.json** 中的 `Session` 字段 + 刷新 `EventCounts`
3. **触发一个 `state_change` 事件**，reason 区分来源

session_info 和 usage 事件**不触发 state_change 也不更新 state.json**，只作为 session event 通知出去。

### state_change reason 扩展

现有 reason 值：`"prompt-started"`, `"prompt-completed"`, `"prompt-failed"`, `"process-exited"`, ...

新增：
- `"config-updated"` — 配置选项变更
- `"mode-changed"` — 操作模式切换
- `"commands-updated"` — 可用命令列表变更

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

`sessionChanged` 示例值：`["currentMode"]`, `["configOptions"]`, `["availableCommands"]`。
这让消费方可以过滤只关心的变更类型，而不需要 diff 整个 state。

### 实现位置

变更发生在 **runtime.Manager** 层（不是 Translator 层），因为 state.json 的写入权在 Manager：

```
ACP SessionNotification
    → Translator.translate() → session event (现有，不变)
    → Translator.translate() → eventCounts++ (内存)
    → Manager 监听 session metadata event → 更新 state.json Session 字段 + 刷新 EventCounts → 触发 state_change
```

Manager 需要新增一个 event consumer 来处理这些 session metadata 事件。
Translator 不需要改动（除了 eventCounts 计数逻辑）。

### EventCounts 更新流

```
每个事件到达:
    Translator.translate()
        → eventCounts[type]++          (内存，无锁竞争，Translator 单线程)

state.json 写入时机（现有 + 新增）:
    status 变更 (creating→idle, idle→running, etc.)     → writeState() 包含 eventCounts
    session metadata 变更 (config/mode/commands)          → writeState() 包含 eventCounts
    进程退出                                              → writeState() 包含 eventCounts
```

---

## 改动 3：usage 通知透传

usage 不写 state.json，但需要确保上层能实时获取：

1. **session event 通知** — 已有。usage 事件作为 `shim/event` (category=session, type=usage) 推送给所有 subscriber。现有实现已经做到了。
2. **event log 持久化** — 已有。usage 事件写入 events.jsonl，断线恢复后可通过 `runtime/history` 回放。
3. **不触发 state_change** — usage 高频更新，避免 state_change 风暴。

上层获取 usage 的方式：
- **实时**：监听 `shim/event` 中 type=usage 的 session event
- **历史**：`runtime/history` 回放 event log 中的 usage 事件
- **当前值**：从最近的 usage event 中获取（event log 尾部）

**结论**：usage 透传不需要额外代码改动，现有的 Translator + event broadcast + event log 已经覆盖。

---

## 改动 4：清理 file_write / file_read / command 死代码

### 调查结论

- **这三个事件类型不来自 ACP** — ACP SessionUpdate union 中没有对应的 variant
- **从未被生成过** — Translator 中没有任何代码路径产出这三类事件
- **纯占位符** — 初始提交时加入，注释声称 "from the ACP client" 是不准确的

### 清理范围

| 文件 | 删除内容 |
|------|---------|
| `api/events.go` | `EventTypeFileWrite`, `EventTypeFileRead`, `EventTypeCommand` 常量 |
| `pkg/events/types.go` | `FileWriteEvent`, `FileReadEvent`, `CommandEvent` 结构体 |
| `pkg/events/shim_event.go` | `decodeEventPayload()` 中对应的 case 分支 |
| `docs/design/runtime/shim-rpc-spec.md` | Typed Event 表格中的 `file_write`, `file_read`, `command` 行；`shim/event` Category 列表中的引用 |

### 不删除

- 如果未来 ACP 增加 side-channel 事件（如 fs/terminal 权限通知），再按实际 ACP 定义重新加入

---

## 实现顺序

1. **清理死代码**（改动 4）— 先清理干净，减少噪音
2. **扩展 runtime-spec state.go**（改动 1）— 定义 SessionState 类型 + EventCounts 字段
3. **Translator 增加 eventCounts 内存计数**（改动 2 前置）
4. **Manager 监听 session metadata 事件并写入 state + 触发 state_change**（改动 2）
5. **更新设计文档**（shim-rpc-spec.md, agent-shim.md）— 反映 state.json 新字段和 state_change reason 扩展
6. **验证 usage 透传**（改动 3）— 现有实现已覆盖，只需验证

## 涉及文件

| 文件 | 改动 |
|------|------|
| `pkg/runtime-spec/api/state.go` | 新增 SessionState, AvailableCommand, ConfigOption 等类型；新增 EventCounts 字段 |
| `pkg/events/translator.go` | 新增 eventCounts 内存计数（每个事件 +1） |
| `pkg/runtime/runtime.go` | Manager 新增 session metadata event consumer，更新 state.json，刷新 EventCounts |
| `pkg/events/types.go` | StateChangeEvent 增加 SessionChanged 字段；删除 FileWriteEvent/FileReadEvent/CommandEvent |
| `api/events.go` | 删除 file_write/file_read/command 常量 |
| `pkg/events/shim_event.go` | 删除 decodeEventPayload 中对应 case |
| `docs/design/runtime/shim-rpc-spec.md` | 更新 state.json schema 描述，删除 file_write/file_read/command，扩展 state_change reason |
| `docs/design/runtime/agent-shim.md` | 提及 session metadata 写入 state 的职责 |
