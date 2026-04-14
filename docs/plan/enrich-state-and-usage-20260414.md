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

1. 扩展 runtime-spec state.json，增加 session 元信息字段
2. 当这些字段变更时，写入 state.json 并触发统一的 `state_change` 事件
3. usage 通过 shim RPC 暴露给上层
4. 清理 file_write / file_read / command 死代码

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

    // Session carries ACP-reported session metadata.
    // Populated progressively as the agent reports updates via ACP notifications.
    // Nil before the first session metadata arrives.
    Session *SessionState `json:"session,omitempty"`
}

// SessionState captures the latest session-level metadata reported by the agent.
// Fields are updated incrementally: each ACP notification overwrites the relevant
// field(s), leaving others unchanged.
type SessionState struct {
    // Title is the human-readable session title (from SessionInfoUpdate).
    Title *string `json:"title,omitempty"`

    // UpdatedAt is the last activity timestamp in RFC 3339 (from SessionInfoUpdate).
    UpdatedAt *string `json:"updatedAt,omitempty"`

    // CurrentMode is the agent's current operational mode ID (from CurrentModeUpdate).
    // e.g. "plan", "code", "research"
    CurrentMode *string `json:"currentMode,omitempty"`

    // AvailableCommands lists the agent's currently available commands (from AvailableCommandsUpdate).
    // Full replacement on each update.
    AvailableCommands []AvailableCommand `json:"availableCommands,omitempty"`

    // ConfigOptions lists the agent's configurable options with current values (from ConfigOptionUpdate).
    // Full replacement on each update.
    ConfigOptions []ConfigOption `json:"configOptions,omitempty"`

    // Usage carries the latest token/API usage statistics (from UsageUpdate).
    Usage *UsageState `json:"usage,omitempty"`
}

// UsageState carries token usage and optional cost for the session.
type UsageState struct {
    Size int    `json:"size"`            // Total context window size (tokens)
    Used int    `json:"used"`            // Tokens currently used
    Cost *Cost  `json:"cost,omitempty"`  // Cumulative session cost
}

// Cost mirrors acp.Cost.
type Cost struct {
    Amount   float64 `json:"amount"`
    Currency string  `json:"currency"`
}

// AvailableCommand 和 ConfigOption 的类型定义复用 pkg/events/types.go 中现有定义。
// runtime-spec/api 包需要独立定义（避免 runtime-spec 依赖 events 包），
// 但 JSON wire shape 保持一致。
```

### state.json 示例

```json
{
  "oarVersion": "0.1.0",
  "id": "session-abc123",
  "status": "idle",
  "pid": 12345,
  "bundle": "/var/lib/agentd/bundles/session-abc123",
  "session": {
    "title": "Refactor auth module",
    "updatedAt": "2026-04-14T10:30:00Z",
    "currentMode": "code",
    "availableCommands": [
      {"name": "create_plan", "description": "Create an execution plan"}
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
    "usage": {
      "size": 200000,
      "used": 45000,
      "cost": {"amount": 0.12, "currency": "USD"}
    }
  }
}
```

### 设计决策

- **`Session` 是可选子结构**：agent 启动后 ACP 通知陆续到达，字段渐进填充。未收到的字段保持 nil/空。
- **全量替换语义**：`availableCommands` 和 `configOptions` 每次收到事件都全量替换（与 ACP 语义一致，ACP 每次发完整列表）。
- **runtime-spec/api 包独立定义类型**：不依赖 events 包，保持 runtime-spec 的独立性。JSON wire shape 保持一致。

---

## 改动 2：session metadata 变更触发 state_change

### 机制

当 Translator 收到 session_info / config_option / current_mode / available_commands / usage 事件时：

1. **照常翻译**为对应的 session category 事件（保持现有行为不变）
2. **额外更新 state.json** 中的 `Session` 字段
3. **触发一个 `state_change` 事件**，reason 区分来源

### state_change reason 扩展

现有 reason 值：`"prompt-started"`, `"prompt-completed"`, `"prompt-failed"`, `"process-exited"`, ...

新增：
- `"session-info-updated"` — session title/updatedAt 变更
- `"config-updated"` — 配置选项变更
- `"mode-changed"` — 操作模式切换
- `"commands-updated"` — 可用命令列表变更
- `"usage-updated"` — usage 统计变更

### StateChangeEvent 扩展

```go
type StateChangeEvent struct {
    PreviousStatus string `json:"previousStatus"`
    Status         string `json:"status"`
    PID            int    `json:"pid,omitempty"`
    Reason         string `json:"reason,omitempty"`
    // SessionChanged lists which session fields were updated in this state change.
    // Empty for pure lifecycle status changes.
    SessionChanged []string `json:"sessionChanged,omitempty"`
}
```

`sessionChanged` 示例值：`["title"]`, `["currentMode"]`, `["usage"]`, `["configOptions", "availableCommands"]`。
这让消费方可以过滤只关心的变更类型，而不需要 diff 整个 state。

### 实现位置

变更发生在 **runtime.Manager** 层（不是 Translator 层），因为 state.json 的写入权在 Manager：

```
ACP SessionNotification
    → Translator.translate() → session event (现有，不变)
    → Manager 监听 session event → 更新 state.json Session 字段 → 触发 state_change
```

Manager 需要新增一个 event consumer 来处理这些 session metadata 事件。
Translator 不需要改动。

---

## 改动 3：usage 通过 shim RPC 暴露

### 方案：扩展 runtime/status response

在 `runtime/status` 的 response 中，`state` 字段已经包含完整的 state.json 内容。
因为改动 1 把 usage 加入了 `State.Session.Usage`，所以 `runtime/status` 自动暴露 usage，**不需要新增 RPC 方法**。

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "result": {
    "state": {
      "oarVersion": "0.1.0",
      "id": "session-abc123",
      "status": "idle",
      "pid": 12345,
      "bundle": "...",
      "session": {
        "usage": {
          "size": 200000,
          "used": 45000,
          "cost": {"amount": 0.12, "currency": "USD"}
        }
      }
    },
    "recovery": {
      "lastSeq": 41
    }
  }
}
```

### 实时 usage 推送

usage 变更会通过改动 2 的 `state_change` 事件（reason: `"usage-updated"`）实时推送到所有 subscriber。
上层通过 `shim/event` notification 就能收到：

```json
{
  "method": "shim/event",
  "params": {
    "seq": 50,
    "category": "runtime",
    "type": "state_change",
    "content": {
      "previousStatus": "running",
      "status": "running",
      "reason": "usage-updated",
      "sessionChanged": ["usage"]
    }
  }
}
```

消费方收到 `state_change` + `sessionChanged` 包含 `"usage"` 后，可以：
- 直接从 event log 里找最近的 `usage` session event 获取详情
- 或者调 `runtime/status` 获取最新 state snapshot

### usage 在 session 维度聚合

ACP 的 `UsageUpdate` 本身就是 session 级别的累计值（`size` = context window 总大小，`used` = 已用 tokens，`cost.amount` = 累计费用），直接存入 state 即可，不需要额外聚合。

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
| `docs/design/runtime/shim-rpc-spec.md` | Typed Event 表格中的 `file_write`, `file_read`, `command` 行 |

### 不删除

- shim-rpc-spec.md 的 `shim/event` Category 划分列表中提到这些类型 — 需要同步移除
- 如果未来 ACP 增加 side-channel 事件（如 fs/terminal 权限通知），再按实际 ACP 定义重新加入

---

## 实现顺序

1. **清理死代码**（改动 4）— 先清理干净，减少噪音
2. **扩展 runtime-spec state.go**（改动 1）— 定义 SessionState 类型
3. **Manager 监听 session metadata 事件并写入 state + 触发 state_change**（改动 2）
4. **更新设计文档**（shim-rpc-spec.md, agent-shim.md）— 反映 state.json 新字段和 state_change reason 扩展
5. **验证 runtime/status 自动暴露 usage**（改动 3）— 应该是自动的，只需验证

## 涉及文件

| 文件 | 改动 |
|------|------|
| `pkg/runtime-spec/api/state.go` | 新增 SessionState, UsageState, Cost, AvailableCommand, ConfigOption 等类型 |
| `pkg/runtime/runtime.go` | Manager 新增 session metadata event consumer，更新 state.json |
| `api/events.go` | 删除 file_write/file_read/command 常量 |
| `pkg/events/types.go` | 删除 FileWriteEvent/FileReadEvent/CommandEvent |
| `pkg/events/shim_event.go` | 删除 decodeEventPayload 中对应 case |
| `docs/design/runtime/shim-rpc-spec.md` | 更新 state.json schema 描述，删除 file_write/file_read/command，扩展 state_change reason |
| `docs/design/runtime/agent-shim.md` | 提及 session metadata 写入 state 的职责 |
