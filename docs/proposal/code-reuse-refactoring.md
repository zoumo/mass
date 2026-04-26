# 代码复用与抽象重构提案

## 状态

**Design Proposal** — 尚未实现。已纳入 Codex review 反馈（见 `docs/review/code-reuse-refactoring-review.md`）。

## 动机

全量代码扫描发现 `pkg/` 下存在大量结构性重复，集中在以下几个维度：

- **fan-out / subscribe** 模式在 `jsonrpc.Client`、`watch.WatchServer`、`agentrun/server.Translator` 三处各自维护 map+mutex+递增ID+evict 逻辑
- **RPC 注册 / 调用** 的 unmarshal 样板在每个 method handler 和 client method 中重复
- **Store CRUD** 的 read-modify-write 骨架在 agentd/store 的多个方法中逐字重复
- **ARI handler** 的 "reserve agent → dispatch" 流程在 4 个 handler 中各抄一遍
- **TUI 组件** 的渲染缓存、动画初始化、消息创建等模式多处拷贝

预估共可消除 **~800-900 行**重复代码，同时降低新增功能时的拷贝风险。

---

## Wave 1：立竿见影（独立、低风险）

### 1.1 `jsonrpc.UnaryMethod` / `NullaryMethod` — 消除 Register 样板

**现状：** `agentrun/server/register.go` 中每个带参数的 method handler 重复相同的 unmarshal → ErrInvalidParams 模式：

```go
"prompt": func(ctx context.Context, unmarshal func(any) error) (any, error) {
    var req runapi.SessionPromptParams
    if err := unmarshal(&req); err != nil {
        return nil, jsonrpc.ErrInvalidParams(err.Error())
    }
    return svc.Prompt(ctx, &req)
},
```

`prompt`、`load`、`set_model` 完全相同结构；`cancel`、`status`、`stop` 是无参数变体。

**方案：** 在 `pkg/jsonrpc` 中添加四个泛型 helper，覆盖 query（有返回值）和 command（仅返回 error）两种模式：

```go
// Query-style: 有请求参数，有返回值
func UnaryMethod[Req, Res any](fn func(ctx context.Context, req *Req) (*Res, error)) Method {
    return func(ctx context.Context, unmarshal func(any) error) (any, error) {
        var req Req
        if err := unmarshal(&req); err != nil {
            return nil, ErrInvalidParams(err.Error())
        }
        return fn(ctx, &req)
    }
}

// Query-style: 无请求参数，有返回值
func NullaryMethod[Res any](fn func(ctx context.Context) (*Res, error)) Method {
    return func(ctx context.Context, _ func(any) error) (any, error) {
        return fn(ctx)
    }
}

// Command-style: 有请求参数，仅返回 error（如 Load, SetModel）
func UnaryCommand[Req any](fn func(ctx context.Context, req *Req) error) Method {
    return func(ctx context.Context, unmarshal func(any) error) (any, error) {
        var req Req
        if err := unmarshal(&req); err != nil {
            return nil, ErrInvalidParams(err.Error())
        }
        return nil, fn(ctx, &req)
    }
}

// Command-style: 无请求参数，仅返回 error（如 Cancel, Stop）
func NullaryCommand(fn func(ctx context.Context) error) Method {
    return func(ctx context.Context, _ func(any) error) (any, error) {
        return nil, fn(ctx)
    }
}
```

注册代码变为：

```go
"prompt": jsonrpc.UnaryMethod(svc.Prompt),    // (*Res, error)
"status": jsonrpc.NullaryMethod(svc.Status),   // (*Res, error)
"cancel": jsonrpc.NullaryCommand(svc.Cancel),  // error
"load":   jsonrpc.UnaryCommand(svc.Load),      // error
```

> `watch_event` 因需同时提取 transport 层 `watchId` 和业务字段，保持手写。

**预估消除：** ~30 行 + 每新增 RPC 方法省 5 行。

**涉及文件：** `pkg/jsonrpc/method.go`（新增）、`pkg/agentrun/server/register.go`、`pkg/ari/server/service.go`

---

### 1.2 `reserveAndConnect` — 消除 ARI reserve-dispatch 重复

**现状：** `pkg/ari/server/server.go` 中 `Prompt`、`Send`、`TaskCreate`、`TaskRetry` 四个 handler 各重复 30-40 行相同流程：

1. recovery 检查
2. `store.GetAgentRun` 检查存在
3. 校验 idle 状态
4. `TransitionState(idle → running)` CAS
5. CAS 失败后获取当前状态构造错误（子模式也重复 4 次）
6. `processes.Connect`

**方案：** 提取到 `*Service` 方法，职责边界到 CAS 成功为止（**不包含 Connect**）：

```go
// reserveIdleAgent performs recovery check, validates idle state, and
// CAS idle→running. Returns the reserved agent record.
// On CAS failure, returns an error containing the agent's current state.
//
// Does NOT call Connect or handle connect failures — callers retain full
// control over Connect + recordPromptDeliveryFailure behavior, because:
// - connect failure 的当前行为是 recordPromptDeliveryFailure(..., true)，
//   当 runtime 状态不可查时标记 agent 为 error（非 idle）
// - 不同 caller 的 failure target（To/Name）、日志消息、RPC error 格式各不相同
// - 自动 rollback to idle 会让坏掉的 agent 看起来可用，导致反复 dispatch
func (a *Service) reserveIdleAgent(ctx context.Context, ws, name string) (*pkgariapi.AgentRun, error) {
    // 1. recovery check
    // 2. get agent run
    // 3. validate idle
    // 4. CAS idle→running (with current-state error on failure)
}
```

同时提取 `rollbackAgentToIdle(ws, name, op string, cause error) error`，消除 `TaskCreate` 和 `TaskRetry` 中完全一样的 rollback 闭包。

各 caller 的使用模式：

```go
// Prompt handler
agent, err := a.reserveIdleAgent(ctx, ws, name)
if err != nil { return nil, err }
client, err := a.processes.Connect(ctx, ws, name)
if err != nil {
    a.recordPromptDeliveryFailure(ctx, ws, name, err, true)
    return nil, jsonrpc.ErrInternal(err.Error())
}
// ... dispatch prompt
```

**预估消除：** ~80 行 reserve + ~30 行 rollback。

**涉及文件：** `pkg/ari/server/server.go`

---

### 1.3 `store.updateAgentRun` — 消除 read-modify-write 样板

**现状：** `pkg/agentd/store/agentrun.go` 中 `UpdateAgentRunStatus`、`UpdateAgentRunState`、`UpdateAgentRunSessionInfo`、`TransitionAgentRunState` 四个方法共享完全相同的骨架：

1. 校验 workspace/name
2. `db.Update` 开事务
3. `workspaceBucket(tx, workspace)` 获取 bucket
4. `wb.Get(key)` → `json.Unmarshal`
5. 修改若干字段
6. `UpdatedAt = time.Now()`
7. `json.Marshal` → `wb.Put`

**方案：** 提取通用方法：

```go
func (s *Store) updateAgentRun(workspace, name string, mutate func(*pkgariapi.AgentRun) error) error {
    if workspace == "" {
        return fmt.Errorf("store: workspace is required")
    }
    if name == "" {
        return fmt.Errorf("store: agent name is required")
    }
    return s.db.Update(func(tx *bolt.Tx) error {
        wb := workspaceBucket(tx, workspace)
        // get → unmarshal → mutate → set UpdatedAt → marshal → put
    })
}
```

四个 Update 方法各简化为 3-5 行的 mutate 回调。

**预估消除：** ~120 行。

**涉及文件：** `pkg/agentd/store/agentrun.go`

---

## Wave 2：统一基础设施

### 2.1 Fan-out 抽象 — **Design-First**，不急于统一

> **Review feedback (P1):** 三处实现的 delivery 语义根本不同，不能用一个 `Fanout[T]` 一刀切。

**现状：** 三处独立实现了 subscribe/publish/evict，但 delivery policy 有本质差异：

| 位置 | 发送方式 | 保序机制 | 慢消费者策略 |
|------|---------|---------|------------|
| `watch.WatchServer` | **blocking send** | `publishMu` 全局排序 | done channel 检测，不主动 evict |
| `agentrun/server.Translator` | **nonblocking send** | log-before-fanout 在同一 mutex 下 | default 分支 close+delete（K8s-style eviction） |
| `jsonrpc.Client` | **nonblocking send** | 无全局保序 | drop 消息但不 evict subscriber，保证 fallback routing 正确 |

**问题：** 一个统一的 `Fanout[T]` 如果不显式建模这些差异，会静默改变 watch 排序、recovery 行为或通知投递语义。

**修订方案：** 此项从"直接实现"降级为"设计任务"。实施前需先完成以下设计：

1. **分类 delivery policy：** 确定是拆成 2-3 个不同抽象（如 `OrderedFanout` vs `BestEffortFanout`），还是通过 policy 参数统一
2. **明确语义边界：** 每种 policy 需显式定义：
   - 发送阻塞 vs 非阻塞
   - 是否需要全局保序
   - 慢消费者：evict（close channel）vs drop（跳过消息）vs block
   - 是否支持 publish 前的 hook（如 log-before-fanout）
3. **验证迁移安全性：** 对每个使用方写出迁移前后的行为对比，确认无语义退化

**可能的拆分方向：**

```go
// 方案 A：策略参数
type DropPolicy int
const (
    DropMessage DropPolicy = iota  // jsonrpc.Client: 丢消息不 evict
    EvictSlow                       // Translator: close channel + 移除
    BlockSend                       // WatchServer: 阻塞直到消费
)

type Fanout[T any] struct { ... }
func NewFanout[T any](bufSize int, policy DropPolicy) *Fanout[T]

// 方案 B：拆成独立类型
type BlockingFanout[T any] struct { ... }   // WatchServer
type EvictingFanout[T any] struct { ... }   // Translator
// jsonrpc.Client 的 method-level subscribe 语义太特殊，可能不适合统一
```

**预估消除：** ~100 行跨 3 个包（设计确认后）。

**涉及文件：** 待设计确认。`pkg/fanout/fanout.go`（新增）为初始候选位置。

---

### 2.2 `decodeAs[T]` — 简化事件解码

**现状：** `pkg/agentrun/api/event.go` 的 `decodeEventPayload` 包含两个 switch：第一个根据类型选择 `dst` 指针，第二个解引用返回值类型。每新增事件类型需改 4 处。

**方案：**

```go
func decodeAs[T Event](data []byte) (Event, error) {
    var v T
    if err := json.Unmarshal(data, &v); err != nil {
        return nil, err
    }
    return v, nil
}
```

switch 简化为：

```go
case EventTypeToolCall:   return decodeAs[ToolCallEvent](payload)
case EventTypeToolResult: return decodeAs[ToolResultEvent](payload)
```

**预估消除：** ~20 行，新增事件类型从 4 行降为 1 行。

**涉及文件：** `pkg/agentrun/api/event.go`

---

### 2.3 `writeState` 自动填充不变字段

**现状：** `pkg/agentrun/runtime/acp/runtime.go` 中 `writeState` 被调用 7 次，其中 5 次重复设置 `MassVersion`/`ID`/`Bundle`/`Annotations` 四个不变字段。

**方案：** 让 `writeState` 内部自动填充：

```go
func (m *Manager) writeState(apply func(*apiruntime.State), reason string) error {
    // ... read existing ...
    state.MassVersion = m.cfg.MassVersion
    state.ID = m.cfg.Metadata.Name
    state.Bundle = m.bundleDir
    state.Annotations = m.cfg.Metadata.Annotations
    apply(&state)
    // ... write ...
}
```

**预估消除：** ~20 行，降低遗漏字段风险。

**涉及文件：** `pkg/agentrun/runtime/acp/runtime.go`

---

## Wave 3：TUI 层清理

### 3.1 `startNewAssistantMessage()` — 统一 assistant message 创建

**现状：** `pkg/tui/chat/chat.go` 中 3 处重复相同的 "创建 StreamingMessage → 设 currentMsg/currentMsgID → AppendMessages → StartAnimation" 序列。

**方案：** 提取 `(m *chatModel) startNewAssistantMessage() tea.Cmd`。

**预估消除：** ~30 行。

---

### 3.2 `confirmCompletion()` — 统一 Tab/Enter completion 确认

**现状：** `chat.go` 中 Tab 键和 Enter 键处理的 completion 确认逻辑完全一样（~15 行）。

**方案：** 提取 `(m *chatModel) confirmCompletion()`。

---

### 3.3 `appendSystemMsg` / `appendErrorMsg` — 简化系统消息追加

**现状：** `chat.go` 和 `command_handlers.go` 中 10+ 处重复 `m.chat.AppendMessages(component.NewSystemItem(m.nextID("sys"), text, styleXxx))`。

**方案：** 在 `chatModel` 上添加便捷方法。

---

### 3.4 `renderCachedBlock()` — 统一 simple_items 渲染骨架

**现状：** `simple_items.go` 中 `ThinkingItem`、`PlanItem`、`SystemItem` 的 `Render` 方法骨架完全一致（cache check → render → setCachedRender），区别仅在 `BlockConfig` 参数。

**方案：** 提取 `renderCachedBlock(c *cachedMessageItem, width int, buildConfig func(int) BlockConfig) string`。

**预估消除：** ~40 行。

---

### 3.5 `animatableItem` — 统一动画初始化

**现状：** `AssistantMessageItem` 和 `baseToolMessageItem` 的 `anim.New()` 初始化参数完全一致，`StartAnimation()` 和 `Animate()` 方法也完全相同。

**方案：** 提取 `animatableItem` 嵌入结构体。

---

### 3.6 杂项

| 项 | 内容 |
|----|------|
| 删除手写 `itoa()` | `header.go` 20+ 行手写 int→string，用 `strconv.Itoa` 替代 |
| `renderStatusIndicator()` | `renderStatusLine` 和 `renderHeaderStatusWithSeq` 的 status switch 重复 |
| `AvailableCommand` type alias | `command_handlers.go` 两个 case 循环体完全一样 |

---

## Wave 4：长线（需要设计）

### 4.1 `TypedBucket[T]` — 泛型 Store CRUD

**现状：** `agentd/store/` 中 Workspace、Agent、AgentRun 三种资源的 Create/Get/List/Delete 遵循完全相同的模式。

**方案：** 构建泛型 bucket 抽象：

```go
type TypedBucket[T any] struct {
    bucketPath func(tx *bolt.Tx) *bolt.Bucket
    keyFunc    func(*T) string
    initFunc   func(*T) // set Kind, timestamps, etc.
}

func (b *TypedBucket[T]) Get(tx *bolt.Tx, key string) (*T, error)
func (b *TypedBucket[T]) Put(tx *bolt.Tx, key string, v *T) error
func (b *TypedBucket[T]) List(tx *bolt.Tx, filter func(*T) bool) ([]*T, error)
func (b *TypedBucket[T]) Delete(tx *bolt.Tx, key string) error
```

**预估消除：** ~150-200 行，但需要仔细设计 API 以覆盖各种 CRUD 变体。建议在下次 store 需求时一起实施。

---

## 实施建议

| Wave | 范围 | 依赖 | 建议时间 |
|------|------|------|---------|
| 1 | UnaryMethod/Command + reserveAndConnect + updateAgentRun | 无，各自独立 | 可并行开发 |
| 2 | Fan-out 设计 + decodeAs + writeState | Fan-out 需先完成设计文档 | Wave 1 之后 |
| 3 | TUI 层清理 | 无 | 穿插在功能迭代中 |
| 4 | TypedBucket[T] | 需要设计 | 下次 store 需求时 |

每个 Wave 内的改动互不依赖，可以拆成独立 PR 逐步合入。

---

## Review 记录

### Codex Review Round 2 (2026-04-26)

详见 `docs/review/code-reuse-refactoring-review.md`。

**Round 1 (3 项) — 全部已解决：**

| 编号 | 严重度 | 问题 | 处置 |
|------|--------|------|------|
| 1 | P1 | Fanout[T] 抹平了不兼容的 delivery 语义 | Wave 2.1 降级为 design-first |
| 2 | P2 | UnaryMethod/NullaryMethod 不覆盖 error-only RPC | 新增 `UnaryCommand` / `NullaryCommand` |
| 3 | P2 | reserveAndConnect 未定义失败语义 | 明确职责边界（见 Round 2 进一步修正） |

**Round 2 (2 项) — 全部已解决：**

| 编号 | 严重度 | 问题 | 处置 |
|------|--------|------|------|
| 4 | P2 | connect 失败自动 rollback to idle 会掩盖 runtime 故障 | 重命名为 `reserveIdleAgent`，职责收窄到 CAS 成功为止，Connect + 失败处理留给 caller |
| 5 | P2 | `ErrInvalidInput` 未定义，与现有错误风格不一致 | 改为保持现有具体错误信息（`"store: workspace is required"` 等） |
