# 代码复用与抽象重构提案

## 状态

**Design Proposal** — 尚未实现。

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

**方案：** 在 `pkg/jsonrpc` 中添加两个泛型 helper：

```go
func UnaryMethod[Req, Res any](fn func(ctx context.Context, req *Req) (*Res, error)) Method {
    return func(ctx context.Context, unmarshal func(any) error) (any, error) {
        var req Req
        if err := unmarshal(&req); err != nil {
            return nil, ErrInvalidParams(err.Error())
        }
        return fn(ctx, &req)
    }
}

func NullaryMethod[Res any](fn func(ctx context.Context) (*Res, error)) Method {
    return func(ctx context.Context, _ func(any) error) (any, error) {
        return fn(ctx)
    }
}
```

注册代码变为：

```go
"prompt": jsonrpc.UnaryMethod(svc.Prompt),
"status": jsonrpc.NullaryMethod(svc.Status),
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

**方案：** 提取到 `*Service` 方法：

```go
func (a *Service) reserveAndConnect(ctx context.Context, ws, name string) (*runclient.Client, *pkgariapi.AgentRun, error) {
    // recovery check
    // get agent run
    // validate idle
    // CAS idle→running (with current-state error on failure)
    // connect
}
```

同时提取 `rollbackAgentToIdle(ws, name, op string, cause error) error`，消除 `TaskCreate` 和 `TaskRetry` 中完全一样的 rollback 闭包。

**预估消除：** ~120 行 reserve-dispatch + ~30 行 rollback。

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
    if workspace == "" || name == "" {
        return ErrInvalidInput
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

### 2.1 `Fanout[T]` — 统一 subscribe/publish/evict

**现状：** 三处独立实现了相同的 fan-out 模式：

| 位置 | subscribe | publish | evict 策略 |
|------|-----------|---------|-----------|
| `watch.WatchServer` | `Accept()` → watchers map + nextID | `Publish()` → 遍历发送 | done channel 检测 |
| `agentrun/server.Translator` | `Subscribe()` → subs map + nextID | `broadcast()` → 遍历发送 | default 分支 close+delete |
| `jsonrpc.Client` | `Subscribe()` → subs map + nextSub | `routeToSubscribers()` → 遍历发送 | — |

三处都维护 map + mutex + 递增 ID + cleanup 逻辑。

**方案：** 在 `pkg/fanout`（或 `pkg/pubsub`）中提供泛型类型：

```go
type Fanout[T any] struct {
    mu     sync.RWMutex
    subs   map[int]chan T
    nextID int
    bufSize int
}

func (f *Fanout[T]) Subscribe() (<-chan T, int)
func (f *Fanout[T]) Unsubscribe(id int)
func (f *Fanout[T]) Publish(ev T, onEvict func(id int))
func (f *Fanout[T]) Close()
```

`Translator` 组合 `Fanout`，publish 前调 log-before-fanout 钩子。`WatchServer` 组合 `Fanout`，evict 回调触发 watcher stop。`Client` 的 method-level subscribe 也组合 `Fanout`。

**预估消除：** ~100 行跨 3 个包。

**涉及文件：** `pkg/fanout/fanout.go`（新增）、`pkg/watch/server.go`、`pkg/agentrun/server/translator.go`、`pkg/jsonrpc/client.go`

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
| 1 | UnaryMethod + reserveAndConnect + updateAgentRun | 无，各自独立 | 可并行开发 |
| 2 | Fanout[T] + decodeAs + writeState | Fanout 需先设计 API | Wave 1 之后 |
| 3 | TUI 层清理 | 无 | 穿插在功能迭代中 |
| 4 | TypedBucket[T] | 需要设计 | 下次 store 需求时 |

每个 Wave 内的改动互不依赖，可以拆成独立 PR 逐步合入。
