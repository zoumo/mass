# 合并 Envelope + Event 为统一的 ShimEvent

## Context

当前 event 系统有三层嵌套：`Event` → `TypedEvent{Type, Payload}` → `SessionUpdateParams{SequenceMeta, TurnID, Event}` → `Envelope{Method, Params}`。RPC 通知和 JSONL 日志用的是 Envelope，但消费者最终需要的是里面的 Event。

目标：定义统一的 `ShimEvent` 结构，它**既是** RPC 通知的 params，**也是** JSONL 日志的条目。消除 Envelope 这一层包装。

## ID 概念梳理

当前 `Translator.sessionID` 实际上是 shim/agent run 的标识（来自 `--id` 参数或 `cfg.Metadata.Name`），**不是** ACP 的 session ID。重构中：

- **RunID** — 原 `sessionID`，改名为 `runID`，标识这个 agent run 实例
- **SessionID** — ACP agent 在 `session/new` 握手时返回的 ID（`sessionResp.SessionId`），当前存在 `runtime.Manager.sessionID`（私有字段，无 getter），需要暴露出来传给 Translator

## 新类型设计

```go
type ShimEvent struct {
    RunID     string    `json:"runId"`
    SessionID string    `json:"sessionId,omitempty"` // ACP session ID, set after handshake
    Seq       int       `json:"seq"`
    Time      time.Time `json:"time"`       // RFC3339Nano
    Category  string    `json:"category"`   // "session" | "runtime"
    Type      string    `json:"type"`       // "text", "state_change", etc.

    // Turn-aware ordering (session events only, within active turn)
    TurnID    string    `json:"turnId,omitempty"`
    StreamSeq int       `json:"streamSeq,omitempty"`
    Phase     string    `json:"phase,omitempty"`      // "thinking" | "acting" | "tool_call"

    Content   any       `json:"content"`    // 具体 Event 结构体
}
```

### 事件分类

**session** — 所有 ACP SessionUpdate 翻译产出的事件：
- text, thinking, tool_call, tool_result, file_write, file_read, command, plan, user_message, turn_start, turn_end, error
- session_info, config_option, available_commands, current_mode, usage

**runtime** — shim/runtime 自身产生的生命周期事件：
- state_change（新增）

**Turn 字段规则（统一）**：turn 字段反映事件的时间上下文，不反映事件内容语义。规则只有一条：
- 若当前存在 active turn（`currentTurnId != ""`），**所有** session category 事件都携带 TurnID/StreamSeq/Phase
- 若无 active turn，所有 session category 事件都不携带 turn 字段
- runtime category 事件（state_change）永远不携带 turn 字段

这意味着 session_info、usage 等 metadata 事件如果在 turn 内到达，会参与 turn ordering——这是正确的，因为它们是 turn 执行期间发生的真实事件，chat/replay 应该能看到它们在 turn 中的时间位置。turn 外到达的同类事件则不携带 turn 字段。

### Turn-aware ordering 保留在 ShimEvent 层

TurnID、StreamSeq、Phase 保留在 ShimEvent 顶层字段（而非下沉到每个 Event Content 结构体中）。理由：
- turn ordering 是事件在流中的属性，不是事件内容的属性
- 避免给 12 个 session event 结构体各加 3 个字段
- 与当前设计文档中 `session/update.params` 携带 turn 字段的语义一致
- **不需要指针化 Event 或 setTurnID mutation**：Event 保持值类型，turn metadata 在构建 ShimEvent 时设置

排序规则（与现有设计文档 Turn-Aware Event Ordering 章节一致）：
- `turn_start` 时分配新 TurnID，StreamSeq 重置为 0
- turn 内每个 session event 的 StreamSeq 递增
- `turn_end` 事件携带 TurnID，之后清除
- runtime category 事件（state_change）不携带 TurnID/StreamSeq/Phase

Phase 映射：thinking event → `"thinking"`，tool_call/tool_result → `"tool_call"`，其余 session event → `"acting"`

### StateChangeEvent（新增，runtime category）

```go
type StateChangeEvent struct {
    PreviousStatus string `json:"previousStatus"`
    Status         string `json:"status"`
    PID            int    `json:"pid,omitempty"`
    Reason         string `json:"reason,omitempty"`
}
```

Event Content 结构体不变（TextEvent、ThinkingEvent 等保持现有字段，不加 TurnID）。

## Before/After 对比

```
Before:  conn.Notify(ctx, "session/update", SessionUpdateParams{seq, ts, turnId, TypedEvent{type, payload}})
After:   conn.Notify(ctx, "shim/event", ShimEvent{runId, sessionId, seq, time, category:"session", type, turnId, streamSeq, phase, content:{text:"hi"}})

Before:  conn.Notify(ctx, "runtime/state_change", RuntimeStateChangeParams{seq, ts, prevStatus, status, ...})
After:   conn.Notify(ctx, "shim/event", ShimEvent{runId, sessionId, seq, time, category:"runtime", type:"state_change", content:{...}})

JSONL Before: {"method":"session/update","params":{"sessionId":"...","seq":0,"event":{"type":"text","payload":{"text":"hi"}}}}
JSONL After:  {"runId":"codex","sessionId":"acp-xxx","seq":0,"time":"...","category":"session","type":"text","turnId":"turn-001","streamSeq":3,"phase":"acting","content":{"text":"hi"}}
```

## 实施步骤

### Step 0: 同步设计文档（先于代码变更）

方案引入 `shim/event` 统一通知方法，替代现有 `session/update` + `runtime/state_change` 两个通知方法。这是 notification surface 的变更，request/response surface（`session/*` + `runtime/*`）不变。

需要同步更新的设计文档：

**docs/design/runtime/shim-rpc-spec.md**:
- 设计原则第 1 条 "clean-break surface" 更新为：request/response surface 为 `session/*` + `runtime/*`，notification surface 为 `shim/event`
- "Notifications" 章节：将 `session/update` 和 `runtime/state_change` 两个 notification 合并为 `shim/event`，给出新的 JSON 示例
- "Turn-Aware Event Ordering" 章节：更新示例，`turnId`/`streamSeq`/`phase` 现在是 `shim/event` 顶层字段
- "Socket 发现与恢复语义" 章节：更新规范要求中的 notification 名称引用
- `runtime/history` 响应示例：entries 改为 ShimEvent 格式（不再是 Envelope 格式）
- 方法速查表更新 notification 行

**docs/design/runtime/agent-shim.md**:
- 所有 `session/update` + `runtime/state_change` 引用更新为 `shim/event`
- "ACP 是实现细节" 章节的边界描述更新
- shim RPC 稳定性声明段落更新

**docs/design/README.md**:
- shim-rpc-spec.md 描述行更新 notification 名称
- 架构图中的 shim RPC 注释（如有 notification 引用）

**docs/design/contract-convergence.md**:
- "Shim Target Contract" 章节中 `session/update` + `runtime/state_change` 更新为 `shim/event`
- notification surface 描述更新

**docs/design/roadmap.md**:
- "Shim RPC Surface" 中 Live notifications 行更新

### Step 1: 新增常量和类型（纯增量，不破坏编译）

**api/events.go** — 加 `EventTypeStateChange = "state_change"`；加分类常量：
```go
const (
    CategorySession = "session"
    CategoryRuntime = "runtime"
)
```

**api/methods.go** — 加 `MethodShimEvent = "shim/event"`

**pkg/events/types.go**:
- Event Content 结构体不做任何修改（不加 TurnID，保持值类型）
- 新增 `StateChangeEvent` 结构体 + `eventType()` 实现

**pkg/events/envelope.go** — 在 `decodeEventPayload` 里加 `state_change` 分支

**新建 pkg/events/shim_event.go**:
- `ShimEvent` 结构体（含 `RunID`、`SessionID`、`TurnID`、`StreamSeq`、`Phase` 顶层字段）
- `NewShimEvent(...)` 工厂函数
- `CategoryForEvent(eventType string) string` — 根据 event type 返回 category（只有 `state_change` 返回 runtime，其余全部返回 session）
- `PhaseForEvent(eventType string) string` — thinking → `"thinking"`，tool_call/tool_result → `"tool_call"`，其余 → `"acting"`
- 自定义 `MarshalJSON` / `UnmarshalJSON`（反序列化复用现有 `decodeEventPayload`）

**pkg/runtime/runtime.go** — 暴露 ACP session ID：
- 保持 `sessionID acp.SessionId` 字段不变
- **修正 `Create()` 中的写入**：当前代码 `m.sessionID = sessionResp.SessionId`（line 153）不在 `m.mu` 保护下。改为：
```go
m.mu.Lock()
m.sessionID = sessionResp.SessionId
m.mu.Unlock()
```
- 新增 mutex 保护的 getter：
```go
// SessionID returns the ACP session ID obtained during session/new handshake.
// Returns empty string if the session has not been created yet.
func (m *Manager) SessionID() string {
    m.mu.Lock()
    defer m.mu.Unlock()
    return string(m.sessionID)
}
```
- 写入和读取使用同一把 `m.mu`，happens-before 关系明确。`Create()` 中 `m.cmd` 和 `m.conn` 的写入同样不在锁下，但这属于现有代码的已有状态，不在本次重构范围内。

### Step 2: 迁移 Translator

**pkg/events/translator.go**:
- `sessionID string` 字段 → `runID string` + `sessionID string`
- `NewTranslator(runID string, in, log)` — runID 在构造时传入
- 新增 `SetSessionID(id string)` 方法 — ACP 握手完成后由 shim 调用注入
- `subs map[int]chan Envelope` → `map[int]chan ShimEvent`
- `Subscribe()` 返回 `<-chan ShimEvent`
- `SubscribeFromSeq()` 返回 `[]ShimEvent, <-chan ShimEvent`
- 新增 turn-aware 状态：`currentTurnId string`（已有）+ `streamSeq int`（新增）
- 删除 `broadcastEnvelope` 和 `broadcastSessionEvent`
- 新增 `broadcast(build func(seq int, at time.Time) ShimEvent)` 统一入口：
  1. **Under mutex**: 调用 build 获得 ShimEvent，赋 `ev.Seq = t.nextSeq`
  2. **Under mutex**: 若 log 非 nil，调用 `log.Append(ev)`（durable append 先于 live fan-out）
     - 若 Append 失败（**fail-closed 策略**）：`slog.Error("events: log append failed, event dropped", "seq", ev.Seq, "error", err)`，**不递增 nextSeq，不 fan-out，直接返回**。下一个事件会复用同一个 seq 号——该事件被彻底丢弃（live 和 history 都不可见），seq 空间保持连续无空洞。
     - 若 Append 成功：继续下一步
  3. **Under mutex**: `t.nextSeq++`
  4. **Under mutex**: fan-out 到所有订阅者（非阻塞 send）
  5. 释放 mutex
  
  **fail-closed 的代价与权衡**：append 失败时丢弃事件（live + history 均不可见）。这是有意取舍——保护 seq 空间连续性和 history/live 同一序列空间的恢复不变量。如果失败是瞬时的（如 disk hiccup），后续事件能正常写入和 fan-out，系统自动恢复；如果失败是持久的（如 disk full），所有事件都被丢弃，系统处于 degraded mode，需通过监控告警发现。

- `NotifyTurnStart` → build 回调中：设置 `currentTurnId = uuid.New()`，`streamSeq = 0`，构建 ShimEvent{Category:session, Type:turn_start, TurnID:newId, StreamSeq:0, Phase:"acting", Content:TurnStartEvent{}}
- `NotifyUserPrompt` → build 回调中：streamSeq++，构建 ShimEvent{TurnID:currentTurnId, StreamSeq:streamSeq, Phase:"acting", Content:UserMessageEvent{Text:...}}
- `NotifyTurnEnd` → build 回调中：streamSeq++，构建 ShimEvent{TurnID:currentTurnId, StreamSeq:streamSeq, Content:TurnEndEvent{StopReason:...}}，然后清除 currentTurnId
- `NotifyStateChange` → build 回调中：构建 ShimEvent{Category:runtime, Type:state_change, Content:StateChangeEvent{...}}（无 turn 字段）
- `run()` 中 `translate()` 返回 Event（值类型，不修改），broadcast 时：
  - `category = CategoryForEvent(ev.eventType())`
  - 若 `currentTurnId != ""` 且 `category == "session"`：streamSeq++，设置 TurnID/StreamSeq/Phase（所有 session event 统一处理，包括 metadata event）
  - 若无 active turn 或 `category == "runtime"`：不设置 turn 字段
  - 构建 ShimEvent{Content: ev}

**Event 保持值类型**：translate() 继续返回值类型 Event，不需要指针化。Turn metadata 在 ShimEvent 层设置，不需要修改 Event Content。decodeEventPayload 继续返回值类型。所有构造路径和测试断言保持一致。

**cmd/agentd/subcommands/shim/command.go** — shim 启动流程改为：
```go
trans := events.NewTranslator(id, mgr.Events(), evLog)  // id → runID
// ... after mgr.Create() succeeds ...
trans.SetSessionID(mgr.SessionID())  // inject ACP session ID
```

### Step 3: 迁移 EventLog

**pkg/events/log.go**:
- `Append(Envelope)` → `Append(ShimEvent)`
- `env.Seq()` → `ev.Seq`
- `ReadEventLog` 返回 `[]ShimEvent`
- 注意：EventLog 的 `Append` 现在在 Translator mutex 内调用，两把锁嵌套顺序固定为 Translator.mu → EventLog.mu，无死锁风险

### Step 4: 迁移 shimapi 类型

**pkg/shimapi/types.go**:
- `SessionSubscribeResult.Entries []events.Envelope` → `[]events.ShimEvent`
- `RuntimeHistoryResult.Entries []events.Envelope` → `[]events.ShimEvent`

### Step 5: 迁移 RPC Server

**pkg/rpc/server.go**:
- `handleSubscribe`: `conn.Notify(ctx, env.Method, env.Params)` → `conn.Notify(ctx, api.MethodShimEvent, ev)`
- `env.Seq()` → `ev.Seq`
- `handleHistory`: `[]events.Envelope{}` → `[]events.ShimEvent{}`

### Step 6: 迁移 agentd 客户端

**pkg/agentd/shim_client.go**:
- `clientHandler.Handle`: 过滤 `api.MethodShimEvent`（替代两个旧方法）
- 删除 `ParseSessionUpdate` / `ParseRuntimeStateChange`
- 新增 `ParseShimEvent(json.RawMessage) (events.ShimEvent, error)`

**pkg/agentd/process.go**:
- `EventHandler func(ctx, events.SessionUpdateParams)` → `func(ctx, events.ShimEvent)`
- `ShimProcess.Events chan events.SessionUpdateParams` → `chan events.ShimEvent`
- `buildNotifHandler`: 统一解析 `ShimEvent`，按 `ev.Category` 或 `ev.Type` 路由：
  - `state_change` → 提取 StateChangeEvent，执行 DB 更新
  - 其他 → 推到 `shimProc.Events`

### Step 7: 迁移 CLI 客户端

**cmd/agentdctl/subcommands/shim/command.go**:
- 删除本地 `sessionUpdateParams` / `runtimeStateChangeParams` 镜像类型
- 新增本地 `shimEvent` 类型（Content 为 `json.RawMessage`）
- `printNotification`: 匹配 `api.MethodShimEvent`，按 Type 分发

**cmd/agentdctl/subcommands/shim/chat.go**:
- `waitNotif`: 解析 shimEvent，按 Type 生成 turnEndMsg / stateChangeMsg / notifMsg
- `handleNotif`: 按 Type switch，不再先判断 Method

### Step 8: 删除旧类型

**pkg/events/envelope.go** — 删除（将 `decodeEventPayload` 移到 `shim_event.go`）:
- `Envelope`, `sequenceParams`, `SequenceMeta`
- `TypedEvent` + `newTypedEvent()`
- `SessionUpdateParams`, `RuntimeStateChangeParams`
- `NewSessionUpdateEnvelope()`, `NewRuntimeStateChangeEnvelope()`
- 废弃常量别名

**api/methods.go** — 删除 `MethodSessionUpdate` 和 `MethodRuntimeStateChange`

### Step 9: 更新测试

- **pkg/events/translator_test.go**: `chan Envelope` → `chan ShimEvent`，断言改为直接字段访问；新增测试：
  - 验证 turn 内 StreamSeq 递增、Phase 正确映射、turn_end 后 turn 字段清除
  - 验证 metadata event（usage、session_info 等）在 active turn 内携带 TurnID/StreamSeq/Phase
  - 验证 metadata event 在 turn 外不携带 turn 字段
  - 验证 EventLog Append 失败时 fail-closed：事件不 fan-out、seq 不递增、下一事件复用 seq
  - 并发广播 seq 连续性测试：并发调用 NotifyStateChange + ACP event 广播，验证 JSONL 中 seq 连续无空洞
- **pkg/events/wire_shape_test.go**: Envelope 相关断言更新为 ShimEvent JSON roundtrip
- **pkg/events/translate_rich_test.go**: 同上
- **pkg/rpc/server_test.go**: notifHandler 收集 `[]ShimEvent`，匹配 `MethodShimEvent`
- **pkg/agentd/shim_boundary_test.go**: `chan SessionUpdateParams` → `chan ShimEvent`
- **pkg/agentd/process_test.go**: Events chan 类型更新
- **pkg/ari/server_test.go**: Events chan 类型更新
- **pkg/runtime/runtime_test.go**（若已有）: 新增 `Create()` 后调用 `SessionID()` 验证 race-safe

## 关键文件清单

| 文件 | 操作 |
|------|------|
| `docs/design/runtime/shim-rpc-spec.md` | 更新 notification surface 为 `shim/event`，更新 Turn-Aware Ordering 示例和 history 示例 |
| `docs/design/runtime/agent-shim.md` | 更新 notification 引用 |
| `docs/design/README.md` | 更新 shim-rpc-spec 描述 |
| `docs/design/contract-convergence.md` | 更新 Shim Target Contract |
| `docs/design/roadmap.md` | 更新 Live notifications 行 |
| `api/events.go` | 加 `EventTypeStateChange`；加分类常量 |
| `api/methods.go` | 加 `MethodShimEvent`，删旧通知方法 |
| `pkg/runtime/runtime.go` | 新增 mutex 保护的 `SessionID()` getter |
| `pkg/events/shim_event.go` | **新建** — ShimEvent（RunID + SessionID + TurnID + StreamSeq + Phase）+ CategoryForEvent + PhaseForEvent + JSON codec |
| `pkg/events/types.go` | Event Content 不变；新增 StateChangeEvent |
| `pkg/events/envelope.go` | 大量删除，`decodeEventPayload` 移到 shim_event.go |
| `pkg/events/translator.go` | sessionID→runID+sessionID，重写广播机制（log-then-fanout under mutex），turn-aware StreamSeq/Phase |
| `pkg/events/log.go` | Envelope → ShimEvent |
| `pkg/shimapi/types.go` | []Envelope → []ShimEvent |
| `pkg/rpc/server.go` | 统一通知方法 |
| `pkg/agentd/shim_client.go` | 统一解析 |
| `pkg/agentd/process.go` | channel 类型 + 路由逻辑 |
| `cmd/agentd/subcommands/shim/command.go` | CLI：sessionID→runID + 注入 ACP sessionID |
| `cmd/agentdctl/subcommands/shim/chat.go` | TUI 事件处理 |

## 验证

1. `make build` 编译通过
2. `go test ./pkg/events/... ./pkg/rpc/... ./pkg/agentd/... ./cmd/...` 全部通过
3. `bin/e2e/setup.sh` 启动后 4 个 pane 正常，shim chat 正常显示 agent 输出
4. 在 ctl pane 执行 `ctl agentrun prompt --workspace $WS --name codex --text 'hello' --wait`，验证事件流

## 审查记录

### codex 第1轮

#### ✅ 认可项

1. **合并 Envelope 与 typed event 的目标方向正确**：当前 live notification 与 JSONL 都以 Envelope 包一层 `method/params`，消费者还需要继续解析 `TypedEvent{type,payload}`，统一为可回放的 `ShimEvent` 能降低解析路径和日志形状的复杂度。
2. **RunID / SessionID 拆分是必要修正**：现有 `Translator.sessionID` 实际承载的是 shim run 标识，而 ACP `session/new` 返回的 session id 另存于 runtime.Manager；显式拆分能避免 wire 字段语义继续混淆。
3. **迁移范围覆盖了主要代码路径**：方案已覆盖 `Translator`、`EventLog`、RPC server、shimapi、agentd client、agentdctl 和主要测试文件，整体拆分顺序基本可执行。

#### ❌ 问题项

1. **设计规范更新缺失，且新 method 与当前 clean-break surface 冲突**
   - 问题是什么：方案把 live notification 统一为 `shim/event`，但 `docs/design/runtime/shim-rpc-spec.md` 当前明确规定订阅后只发送 `session/update` 和 `runtime/state_change`，并在设计原则中声明 clean-break surface 是 `session/*` + `runtime/*`。方案的实施步骤没有更新任何 `docs/design` 文件，也没有解释为什么新增 `shim/*` surface 是被允许的。
   - 为什么是问题：仓库要求 code changes 必须 aligned with `docs/design`。如果直接实现，代码会和设计 authority 冲突；如果只改代码不改规范，后续恢复、订阅、历史回放、客户端兼容边界都会缺少权威定义。
   - 期望如何解决：新增一个明确步骤同步 `docs/design/runtime/shim-rpc-spec.md`、`docs/design/runtime/agent-shim.md`、`docs/design/README.md`、`docs/design/roadmap.md` 中的 notification 和 history 示例；同时说明 `shim/event` 是新的规范 surface，或改用仍符合 `session/*` / `runtime/*` 原则的命名。若坚持 `shim/event`，必须在设计原则中更新 clean-break surface 的定义和理由。

2. **Turn-aware ordering 语义被遗漏**
   - 问题是什么：当前设计文档要求 turn-bound notification 携带 `turnId`、`streamSeq`、`phase` 三个字段，用于 turn 内确定性排序；方案只把 `TurnID` 下沉到 Content，没有定义 `streamSeq` 和 `phase` 在 `ShimEvent` 中保留、迁移、删除还是废弃。
   - 为什么是问题：`seq` 只能保证全局顺序，设计文档明确说明 chat/replay 需要 `(turnId, streamSeq)` 做 turn 内排序。若新结构只保留 `turnId`，会丢失已设计的排序能力；若决定删除，也必须同步修改设计和测试目标，不能隐式丢弃。
   - 期望如何解决：在 `ShimEvent` 或 session event content 中明确保留 `StreamSeq int` 与 `Phase string`，并说明 `turn_start` 重置、turn 内递增、`runtime/state_change` 不携带这些字段的规则；或者在方案中正式废弃该能力，并同步更新 `docs/design` 的 Turn-Aware Event Ordering 章节与相关测试。

3. **事件 category 划分把 ACP session metadata 错归为 runtime**
   - 问题是什么：方案把 `session_info`、`config_option`、`available_commands`、`current_mode`、`usage` 放入 runtime category，但这些事件来自 ACP `SessionUpdate` 分支，当前设计也把它们列为 `session/update.params.event.type` 的 typed event 集合。
   - 为什么是问题：runtime category 应只表达 runtime/process truth，例如 `state_change`。把 ACP session metadata 放到 runtime 会污染运行时生命周期语义，也会使消费者按 category 路由时难以区分“进程状态变化”和“agent session 元数据变化”。此外方案又说 session event 的 Content 中包含 TurnID，但这些 metadata 事件未列入 TurnID 注入清单，语义前后不一致。
   - 期望如何解决：将所有 ACP `SessionUpdate` 翻译出的事件归为 `category:"session"`；只有 shim/runtime 自身产生的 lifecycle 事件（目前是 `state_change`）归为 `category:"runtime"`。同时明确 session category 不等于必然 turn-bound：只有处于 active turn 内的事件才携带 turn 字段。

4. **EventLog 写入与 live fan-out 的顺序/失败语义未定义**
   - 问题是什么：方案描述 `broadcast(ev)` 同时 fan-out 到订阅者和 EventLog，但没有说明 durable append 与 live notify 的先后关系，也没有处理 `EventLog.Append` 失败时的行为。
   - 为什么是问题：当前恢复语义依赖 live notification 与 JSONL history 共享同一 `seq` 空间。若先向订阅者发送再异步/忽略日志失败，客户端可能收到一个永远无法从 `runtime/history` 补齐的 seq；若并发广播时日志 append 顺序与 seq 顺序不一致，`EventLog.Append` 会因为 nextSeq 不匹配而失败并造成历史空洞。
   - 期望如何解决：在 Translator 广播步骤中规定在同一互斥区内按 seq 顺序完成 log append，再 fan-out live notification；若 append 失败，应至少记录结构化错误，并明确是否继续发送 live 事件。建议新增测试覆盖并发 `NotifyStateChange` 与 ACP event 广播时 JSONL seq 连续、history/live 无空洞。

5. **runtime.Manager 的 SessionID 暴露方案引入不必要的 atomic.Value 风险**
   - 问题是什么：方案把 `sessionID acp.SessionId` 改为 `atomic.Value` 并在 `Prompt`/`Cancel` 中使用 `Load().(string)`，但当前 `Prompt`/`Cancel` 已在 mutex 下读取 `conn` 和 `sessionID`；`atomic.Value` 在未 Store 前调用会导致 nil 处理复杂，类型断言也更脆弱。
   - 为什么是问题：这不是该重构必须承担的并发模型变化，而且会扩大风险面。`SessionID()` 只需要在 `Create()` 成功后读取已建立的 ACP session id；沿用现有 mutex 更简单，也能避免 `Load().(string)` 的 panic 边界。
   - 期望如何解决：保持 `sessionID acp.SessionId` 字段，新增在 `m.mu` 下读取的 `SessionID() string` getter；在 `Create()` 中写入 `sessionID` 时也使用同一把锁，或者至少明确当前 `Create()` 与外部调用的 happens-before。除非有明确数据竞争证据，不要引入 `atomic.Value`。

6. **指针化 Event 的迁移策略不完整**
   - 问题是什么：方案提出 `translate()` 改为返回指针以支持 `setTurnID`，但现有 `Event` 类型、`decodeEventPayload`、测试和若干 `Notify*` 构造路径大量使用值类型；`eventType()` 目前是值接收器，值和指针都会实现接口，容易形成混用。
   - 为什么是问题：如果某些路径仍构造值类型，`setTurnID` 类型 switch 只覆盖指针会漏注入；如果同时支持值和指针，JSON roundtrip、断言测试和 content 类型比较需要统一策略，否则会出现同一 event type 在不同来源下动态类型不同的问题。
   - 期望如何解决：方案中明确 Event 动态类型的最终约定：要么所有 Translator 产物和 decode 结果统一为指针，并更新测试断言；要么不依赖指针 mutation，改为在构造时返回带 turn metadata 的新值。`setTurnID` 必须覆盖所有 session category 事件，并有测试验证每种 turn-bound event 的 turn 字段注入。

### claude-code 第1轮（回应 codex 第1轮）

**反馈1 — 设计规范更新缺失**：接受。新增 Step 0，在所有代码变更之前同步 5 个设计文档。保留 `shim/event` 命名，理由：request/response surface 仍然是 `session/*` + `runtime/*`（prompt, cancel, subscribe, status, history, stop），`shim/event` 只是 notification surface；clean-break 原则的核心是不使用 legacy PascalCase，`shim/event` 符合此原则。设计文档中会明确解释这个 surface 划分。

**反馈2 — Turn-aware ordering 遗漏**：接受。将 `TurnID`、`StreamSeq`、`Phase` 保留在 ShimEvent 顶层字段（与现有设计文档中 `session/update.params` 携带 turn 字段的语义一致）。排序规则不变：`turn_start` 重置 StreamSeq=0，turn 内递增，runtime 事件不携带。Phase 映射：thinking→`"thinking"`，tool_call/tool_result→`"tool_call"`，其余→`"acting"`。新增测试覆盖。

**反馈3 — Category 划分错误**：接受。修正为：所有 ACP SessionUpdate 翻译产出的事件（包括 session_info、config_option、available_commands、current_mode、usage）归为 `session` category；只有 `state_change` 归为 `runtime` category。同时明确：session category 不等于 turn-bound，metadata 事件即使归为 session，在 turn 外也不携带 TurnID/StreamSeq/Phase。

**反馈4 — EventLog 写入与 fan-out 顺序**：接受。修订 `broadcast` 逻辑为：在同一个 mutex 区内，先分配 seq、再 log.Append（durable）、再 fan-out（live）。两把锁嵌套顺序固定为 Translator.mu → EventLog.mu。若 Append 失败，slog.Error 记录结构化错误，但继续发送 live 事件（保障实时可观测性）。新增并发广播 seq 连续性测试。

**反馈5 — atomic.Value → mutex getter**：接受。保持 `sessionID acp.SessionId` 字段不变，新增在 `m.mu` 下读取的 `SessionID() string` getter。Create() 是唯一写入者，在 mutex 保护下写入，getter 用同一把锁，happens-before 明确。不引入 atomic.Value。

**反馈6 — 指针化 Event 策略**：接受，但通过反馈2的决策自然消解。TurnID/StreamSeq/Phase 保留在 ShimEvent 层而非下沉到 Event Content，因此：不需要 setTurnID、不需要指针化 Event、不需要 pointer mutation。translate() 继续返回值类型 Event，decodeEventPayload 继续返回值类型。所有路径类型一致，无混用风险。

### codex 第2轮

#### ✅ 认可项

1. **上一轮关于设计一致性的核心问题已基本解决**：新增 Step 0 覆盖 `docs/design/runtime/shim-rpc-spec.md`、`agent-shim.md`、`README.md`、`contract-convergence.md`、`roadmap.md`，并明确 request/response surface 与 notification surface 的划分，满足“代码变更必须对齐 docs/design”的执行前提。
2. **Turn-aware ordering 的保留方式合理**：将 `TurnID`、`StreamSeq`、`Phase` 放在 `ShimEvent` 顶层，既延续了原 `session/update.params` 的语义位置，也避免污染每个 typed content 结构体。
3. **category 划分方向已修正**：所有 ACP `SessionUpdate` 派生事件归为 `session`，只有 runtime/process truth 归为 `runtime`，边界比上一版清晰。
4. **Event 保持值类型是正确取舍**：turn metadata 不再下沉到 Content 后，不需要指针化 `Event`，避免了 translate/decode/Notify 路径动态类型混用的问题。

#### ❌ 问题项

1. **EventLog Append 失败后继续发送 live event 会破坏恢复不变量，且会使后续日志追加持续失败**
   - 问题是什么：方案规定 `broadcast` 在 mutex 内先 `log.Append(ev)`，Append 失败时 `slog.Error` 并继续 fan-out live event。当前 `EventLog.Append` 只有成功写入后才递增 `EventLog.nextSeq`；如果某个 seq append 失败而 Translator 继续递增 `Translator.nextSeq` 并发送 live，下一个事件再 Append 时会遇到 `expected seq N, got N+1`，导致日志路径永久不匹配。
   - 为什么是问题：这会同时造成两个后果：客户端可能收到无法通过 `runtime/history` 补齐的 seq，违反设计中的 history/live 同一序列空间恢复语义；并且一次 append 失败会毒化后续所有 append，使 JSONL 历史持续产生空洞。
   - 期望如何解决：方案必须定义失败策略并保持 seq 不变量。建议选择 fail-closed：若 durable append 失败，则不 fan-out 该事件，不递增 Translator.nextSeq，记录错误并让后续 `runtime/status` / health 能暴露日志失败状态；或者显式进入 degraded mode，关闭 history/recovery 承诺并在设计文档说明。若仍要 live 优先，必须同步推进 EventLog.nextSeq 并定义空洞语义，但这会削弱当前恢复契约，不建议。

2. **`runtime.Manager.SessionID()` 的锁语义仍不严谨，方案正文声称 Create 写入已在 mutex 保护下是不符合现状的**
   - 问题是什么：方案写道 “`Create()` 中 `m.sessionID = sessionResp.SessionId` 写入已在 mutex 保护下”，但当前代码在 `Create()` 中直接赋值 `m.sessionID = sessionResp.SessionId`，没有持有 `m.mu`。同时 `m.cmd`、`m.conn` 也在 `Create()` 中直接写入，而 `Prompt`/`Cancel`/`done` 在锁下读取。
   - 为什么是问题：如果新增 getter 使用 `m.mu` 读取，但写入不使用同一把锁，仍然不能构成方案声称的 happens-before；在 Go race detector 视角下这是潜在数据竞争。虽然调用顺序通常是 `Create()` 成功后再 `SessionID()`，但方案需要把这个约束写准，不能把不存在的锁保护当成事实。
   - 期望如何解决：在 Step 1 明确把 `Create()` 中对 `m.cmd`、`m.conn`、`m.sessionID` 的写入放到 `m.mu` 保护下，或至少将 `m.sessionID` 写入改为 `m.mu.Lock(); m.sessionID = sessionResp.SessionId; m.mu.Unlock()` 并确保 `Prompt` 不会在 sessionID 写入前调用。同步增加一个 race-sensitive 的单元测试或在现有 runtime 测试中覆盖 `Create()` 后调用 `SessionID()`。

3. **metadata session event 是否携带 turn 字段的规则前后不一致**
   - 问题是什么：事件分类章节写 `session_info`、`config_option`、`available_commands`、`current_mode`、`usage` 等 metadata 事件 “不携带 turn 字段”，但 Step 2 的 `run()` 逻辑写成只要 `currentTurnId != ""`，所有 translated session event 都设置 `TurnID/StreamSeq/Phase`。若 metadata event 在 active turn 内到达，两处规则会产生不同结果。
   - 为什么是问题：消费者会按 `turnId/streamSeq` 决定回放排序和归属；metadata 事件如果有时参与 turn、有时不参与 turn，需要明确标准，否则 chat/replay 和历史回放会在边界事件上出现不一致。
   - 期望如何解决：定义一个 `IsTurnBoundEvent(eventType string) bool` 或等价规则。若决定“active turn 内所有 ACP SessionUpdate 都参与 turn ordering”，就删除 metadata “不携带 turn 字段”的绝对表述，改为“turn 外不携带，turn 内携带”。若决定 metadata 永远不参与 turn ordering，则 Step 2 的注入逻辑必须只对 turn-bound event types 设置 turn 字段，并新增测试覆盖 metadata 事件在 active turn 内不携带 turn 字段。

### claude-code 第2轮（回应 codex 第2轮）

**反馈1 — EventLog Append 失败策略**：接受。改为 fail-closed：append 失败时不递增 nextSeq、不 fan-out、事件被彻底丢弃。下一个事件复用同一 seq 号，seq 空间保持连续。代价是 append 失败时丢失事件（live 和 history 均不可见），但保护了 history/live 同一序列空间的恢复不变量。持久性失败（如 disk full）会使系统进入 degraded mode，需通过监控发现。

**反馈2 — SessionID 锁语义**：接受。确认当前代码 `Create()` line 153 的 `m.sessionID = sessionResp.SessionId` 不在 `m.mu` 保护下，方案上一版的说法有误。修正为：在 Step 1 中显式将该赋值包裹在 `m.mu.Lock()/Unlock()` 中。`m.cmd` 和 `m.conn` 的同类问题属于现有代码状态，不在本次重构范围。

**反馈3 — metadata event turn 字段**：接受。删除"metadata 不携带 turn 字段"的绝对表述，统一为一条规则：turn 字段反映时间上下文——active turn 内所有 session event 都携带 TurnID/StreamSeq/Phase，turn 外不携带，runtime 永不携带。这消除了前后不一致：事件分类章节和 Step 2 run() 逻辑现在遵循同一规则。新增测试覆盖 metadata 事件在 turn 内/外的 turn 字段有无。

### codex 第3轮

#### ✅ 认可项

1. **EventLog 失败策略已收敛到可维护的不变量**：fail-closed 选择牺牲故障期间事件可见性，换取 `seq` 连续和 history/live 同一序列空间，这比 live 优先但制造不可回放 seq 更符合当前恢复契约。
2. **SessionID getter 的数据竞争问题已修正到本次变更范围内**：方案明确对 `m.sessionID` 写入加 `m.mu`，getter 同锁读取；`m.cmd` / `m.conn` 的既有并发问题不再错误纳入本方案承诺。
3. **metadata turn 字段规则已统一**：active turn 内所有 session event 都携带 `TurnID/StreamSeq/Phase`，turn 外不携带，runtime 永不携带；这与 Step 2 注入逻辑一致，可通过测试验证。
4. **测试补充足够覆盖本次风险面**：新增 metadata turn 内/外、fail-closed、并发 seq 连续性、phase/streamSeq 等测试，能覆盖方案中最容易回归的协议语义。

#### ❌ 问题项

1. **RISK：EventLog append 失败可能留下部分写入的 damaged tail**
   - 问题是什么：fail-closed 规定 append 失败后不递增 `nextSeq`，下一个事件复用同一 seq。但若 `json.Encoder.Encode` 在底层文件中留下了部分行，后续成功 append 可能把新 JSON 写在 damaged tail 之后，导致 `ReadEventLog` 看到“损坏行后跟有效行”，按现有逻辑会判定 mid-file corruption。
   - 为什么是风险：这不是方案方向错误，但实现时必须处理，否则一次部分写入仍可能毒化 history。
   - 最稳妥处理意见：实现 `EventLog.Append` 时在写入前记录 offset；Encode/flush 失败时 truncate 回该 offset，或在失败后标记 EventLog 不可用并拒绝后续 append/fan-out，避免 damaged tail 后继续写有效事件。对应测试应模拟 writer failure/partial write 后下一事件不会造成 mid-file corruption。

#### 批准结论

第3轮剩余问题已标注为 RISK，方案主体可以执行。执行时必须按上述风险处理 EventLog 部分写入，否则 fail-closed 的恢复不变量不成立。

## 最终方案

以下是经过 3 轮审查通过的完整执行方案。

### 目标

合并 Envelope + Event 为统一的 ShimEvent 结构，消除三层嵌套。将 seq/timestamp 等系统元数据提升到最外层。统一 RPC 通知方法为 `shim/event`。

### ShimEvent 结构体

```go
type ShimEvent struct {
    RunID     string    `json:"runId"`
    SessionID string    `json:"sessionId,omitempty"`
    Seq       int       `json:"seq"`
    Time      time.Time `json:"time"`
    Category  string    `json:"category"`   // "session" | "runtime"
    Type      string    `json:"type"`

    // Turn-aware ordering (session events only, within active turn)
    TurnID    string    `json:"turnId,omitempty"`
    StreamSeq int       `json:"streamSeq,omitempty"`
    Phase     string    `json:"phase,omitempty"`

    Content   any       `json:"content"`
}
```

### 核心规则

**ID 拆分**：RunID（原 sessionID，shim 实例标识）+ SessionID（ACP session/new 返回的 ID）

**Category 划分**：
- `session`：所有 ACP SessionUpdate 翻译产出的事件（text, thinking, tool_call, tool_result, file_write, file_read, command, plan, user_message, turn_start, turn_end, error, session_info, config_option, available_commands, current_mode, usage）
- `runtime`：仅 state_change

**Turn 字段规则（统一）**：
- active turn 内（`currentTurnId != ""`）所有 session event 都携带 TurnID/StreamSeq/Phase（包括 metadata event）
- turn 外所有 session event 不携带 turn 字段
- runtime event 永不携带 turn 字段
- Phase 映射：thinking→`"thinking"`，tool_call/tool_result→`"tool_call"`，其余→`"acting"`

**Event Content 不变**：Event 保持值类型，TurnID/StreamSeq/Phase 在 ShimEvent 层设置，不下沉到 Content。

### 执行步骤

#### Step 0: 同步设计文档

在所有代码变更之前，更新以下 5 个设计文档：

1. **docs/design/runtime/shim-rpc-spec.md**:
   - 设计原则第 1 条 "clean-break surface" 更新为：request/response surface 为 `session/*` + `runtime/*`，notification surface 为 `shim/event`
   - "Notifications" 章节：将 `session/update` 和 `runtime/state_change` 合并为 `shim/event`，给出新 JSON 示例
   - "Turn-Aware Event Ordering" 章节：更新示例，turn 字段现在是 `shim/event` 顶层字段
   - "Socket 发现与恢复语义" 章节：更新 notification 名称引用
   - `runtime/history` 响应示例：entries 改为 ShimEvent 格式
   - 方法速查表更新 notification 行

2. **docs/design/runtime/agent-shim.md**:
   - 所有 `session/update` + `runtime/state_change` 引用更新为 `shim/event`
   - "ACP 是实现细节" 章节边界描述更新
   - shim RPC 稳定性声明段落更新

3. **docs/design/README.md**:
   - shim-rpc-spec.md 描述行更新 notification 名称

4. **docs/design/contract-convergence.md**:
   - "Shim Target Contract" 章节更新为 `shim/event`
   - notification surface 描述更新

5. **docs/design/roadmap.md**:
   - "Shim RPC Surface" 中 Live notifications 行更新

#### Step 1: 新增常量和类型（纯增量，不破坏编译）

**api/events.go**：
- 加 `EventTypeStateChange = "state_change"`
- 加分类常量 `CategorySession = "session"`、`CategoryRuntime = "runtime"`

**api/methods.go**：
- 加 `MethodShimEvent = "shim/event"`

**pkg/events/types.go**：
- Event Content 结构体不做任何修改
- 新增 `StateChangeEvent` 结构体 + `eventType()` 实现

**pkg/events/envelope.go**：
- 在 `decodeEventPayload` 里加 `state_change` 分支

**新建 pkg/events/shim_event.go**：
- `ShimEvent` 结构体
- `NewShimEvent(...)` 工厂函数
- `CategoryForEvent(eventType string) string`（只有 `state_change` 返回 runtime，其余全部返回 session）
- `PhaseForEvent(eventType string) string`（thinking→`"thinking"`，tool_call/tool_result→`"tool_call"`，其余→`"acting"`）
- 自定义 `MarshalJSON` / `UnmarshalJSON`（反序列化复用 `decodeEventPayload`）

**pkg/runtime/runtime.go**：
- 保持 `sessionID acp.SessionId` 字段不变
- 修正 `Create()` 中 `m.sessionID = sessionResp.SessionId` 写入，包裹在 `m.mu.Lock()/Unlock()` 中
- 新增 mutex 保护的 getter：
```go
func (m *Manager) SessionID() string {
    m.mu.Lock()
    defer m.mu.Unlock()
    return string(m.sessionID)
}
```

#### Step 2: 迁移 Translator

**pkg/events/translator.go**：
- `sessionID string` → `runID string` + `sessionID string`
- `NewTranslator(runID string, in, log)`
- 新增 `SetSessionID(id string)` 方法
- `subs map[int]chan Envelope` → `map[int]chan ShimEvent`
- `Subscribe()` 返回 `<-chan ShimEvent`
- `SubscribeFromSeq()` 返回 `[]ShimEvent, <-chan ShimEvent`
- 新增 turn-aware 状态：`streamSeq int`
- 删除 `broadcastEnvelope` 和 `broadcastSessionEvent`
- 新增 `broadcast(build func(seq int, at time.Time) ShimEvent)` 统一入口：
  1. Under mutex: 调用 build 获得 ShimEvent，赋 `ev.Seq = t.nextSeq`
  2. Under mutex: 若 log 非 nil，调用 `log.Append(ev)`
     - **fail-closed**：Append 失败时 slog.Error，不递增 nextSeq，不 fan-out，直接返回
     - Append 成功：继续
  3. Under mutex: `t.nextSeq++`
  4. Under mutex: fan-out 到所有订阅者（非阻塞 send）
  5. 释放 mutex

- `NotifyTurnStart`：设置 `currentTurnId = uuid.New()`，`streamSeq = 0`，构建 ShimEvent
- `NotifyUserPrompt`：streamSeq++，构建 ShimEvent
- `NotifyTurnEnd`：streamSeq++，构建 ShimEvent，清除 currentTurnId
- `NotifyStateChange`：构建 ShimEvent{Category:runtime}，无 turn 字段
- `run()` 中 translate() 返回 Event 后：category = CategoryForEvent；若 currentTurnId != "" 且 category == "session"：streamSeq++，设置 turn 字段；构建 ShimEvent

**cmd/agentd/subcommands/shim/command.go**：
```go
trans := events.NewTranslator(id, mgr.Events(), evLog)
// after mgr.Create() succeeds:
trans.SetSessionID(mgr.SessionID())
```

**⚠️ RISK 处理 — EventLog 部分写入**：
`EventLog.Append` 实现必须在 `json.Encoder.Encode` 前记录文件 offset，若 Encode/flush 失败则 truncate 回该 offset，防止 damaged tail 后继续写有效事件导致 `ReadEventLog` 判定 mid-file corruption。对应测试应模拟 writer failure/partial write 场景。

#### Step 3: 迁移 EventLog

**pkg/events/log.go**：
- `Append(Envelope)` → `Append(ShimEvent)`
- `env.Seq()` → `ev.Seq`
- `ReadEventLog` 返回 `[]ShimEvent`
- Append 内部：写入前记录 offset，失败时 truncate 回 offset
- 锁嵌套顺序固定为 Translator.mu → EventLog.mu

#### Step 4: 迁移 shimapi 类型

**pkg/shimapi/types.go**：
- `SessionSubscribeResult.Entries []events.Envelope` → `[]events.ShimEvent`
- `RuntimeHistoryResult.Entries []events.Envelope` → `[]events.ShimEvent`

#### Step 5: 迁移 RPC Server

**pkg/rpc/server.go**：
- `handleSubscribe`: `conn.Notify(ctx, env.Method, env.Params)` → `conn.Notify(ctx, api.MethodShimEvent, ev)`
- `env.Seq()` → `ev.Seq`
- `handleHistory`: `[]events.Envelope{}` → `[]events.ShimEvent{}`

#### Step 6: 迁移 agentd 客户端

**pkg/agentd/shim_client.go**：
- `clientHandler.Handle`: 过滤 `api.MethodShimEvent`
- 删除 `ParseSessionUpdate` / `ParseRuntimeStateChange`
- 新增 `ParseShimEvent(json.RawMessage) (events.ShimEvent, error)`

**pkg/agentd/process.go**：
- `EventHandler func(ctx, events.SessionUpdateParams)` → `func(ctx, events.ShimEvent)`
- `ShimProcess.Events chan events.SessionUpdateParams` → `chan events.ShimEvent`
- `buildNotifHandler`: 统一解析 ShimEvent，按 category/type 路由

#### Step 7: 迁移 CLI 客户端

**cmd/agentdctl/subcommands/shim/command.go**：
- 删除本地镜像类型，新增 `shimEvent` 类型（Content 为 json.RawMessage）
- `printNotification`: 匹配 `api.MethodShimEvent`

**cmd/agentdctl/subcommands/shim/chat.go**：
- `waitNotif`/`handleNotif`: 按 Type switch

#### Step 8: 删除旧类型

**pkg/events/envelope.go** — 删除（`decodeEventPayload` 已移到 shim_event.go）：
- `Envelope`, `sequenceParams`, `SequenceMeta`
- `TypedEvent` + `newTypedEvent()`
- `SessionUpdateParams`, `RuntimeStateChangeParams`
- `NewSessionUpdateEnvelope()`, `NewRuntimeStateChangeEnvelope()`
- 废弃常量别名

**api/methods.go** — 删除 `MethodSessionUpdate` 和 `MethodRuntimeStateChange`

#### Step 9: 更新测试

- **pkg/events/translator_test.go**: chan → ShimEvent；新增：turn 内 StreamSeq 递增、Phase 映射、metadata event turn 内/外行为、fail-closed、并发 seq 连续性
- **pkg/events/wire_shape_test.go**: ShimEvent JSON roundtrip
- **pkg/events/translate_rich_test.go**: 同上
- **pkg/rpc/server_test.go**: 匹配 MethodShimEvent
- **pkg/agentd/shim_boundary_test.go**: chan → ShimEvent
- **pkg/agentd/process_test.go**: Events chan 类型更新
- **pkg/ari/server_test.go**: Events chan 类型更新
- **pkg/runtime/runtime_test.go**: Create() 后 SessionID() race-safe
- **pkg/events/log_test.go**: 模拟 partial write 后 Append 不造成 mid-file corruption

### 验证

1. `make build` 编译通过
2. `go test ./pkg/events/... ./pkg/rpc/... ./pkg/agentd/... ./cmd/...` 全部通过
3. `bin/e2e/setup.sh` 启动后 4 个 pane 正常，shim chat 正常显示 agent 输出
4. 在 ctl pane 执行 `ctl agentrun prompt --workspace $WS --name codex --text 'hello' --wait`，验证事件流
