# Watch 客户端统一设计方案（v8）

## 背景

当前 watch 相关代码散布在三层，概念互相重叠：

| 层 | 职责 |
|----|------|
| `jsonrpc.Subscribe()` | 按 method 过滤，fan-out 到 channel，non-blocking send（会 drop） |
| `Watcher` + `relay goroutine` | 按 watchID 过滤 + 无界 relay 缓冲 + 反序列化 |
| `WatchClient` / `ClientConn` / `DialFunc` | 断线重连 + cursor 追踪 |

这三层本质做的都是同一件事：把 server 推送的通知流交付到消费方。

## 核心洞察

1. **JSON-RPC 天然支持双向多路复用**：control RPC 和 watch notification 共享一条连接，watchID 是 client 生成并注入 RPC params 的 demux key。不需要分离连接。
2. **Watch 是 JSON-RPC 的一等模式**：类似 gRPC 的 server streaming，watch 应该内建在 jsonrpc 包中，而不是在上层拼装。
3. **标准信封**：所有 watch 通知共用 `{watchId, seq, payload}` 结构。jsonrpc 包按 watchId 路由，不关心 payload 类型。业务层只做 payload 反序列化。

## 根因分析：事件丢失

当前 drop 发生在 `jsonrpc.routeToSubscribers`（`client.go:257-276`）的 non-blocking send。

本方案不修 Subscribe（保留但 watch 不用它），而是在 jsonrpc 包内建 watch 路由：非阻塞 dispatch + K8s 风格驱逐，不堵 read loop，不丢事件（驱逐后 RetryWatcher 自动重连恢复）。

---

## 新设计

### 分层职责

```
pkg/jsonrpc     — WatchEvent 信封 + WatchStream + watchID 路由 + 慢 consumer 驱逐
pkg/watch       — Interface[T] 接口 + RetryWatcher[T] 重连
pkg/agentrun/client — 调 jsonrpc.Watch + payload 反序列化
```

| 层 | 关心什么 | 不关心什么 |
|----|---------|-----------|
| jsonrpc | watchId 路由、stream 生命周期、驱逐 | payload 类型 |
| watch | 重连策略、cursor 追踪 | 传输协议 |
| agentrun/client | 具体 RPC + AgentRunEvent 类型 | 路由和重连 |

### 改动前 vs 改动后

```
改动前（4 层）：
    jsonrpc.Subscribe → relay goroutine → Watcher filter → WatchClient → consumer

改动后（直通）：
    jsonrpc.Client notifWorker → WatchStream.ch → consumer
    （业务层只做一层 typed wrapper）
```

---

### Step 1: pkg/jsonrpc — 内建 watch 能力

#### WatchEvent 标准信封

```go
// WatchEvent is the standard envelope for all watch notifications.
// jsonrpc routes by WatchID; business code unmarshals Payload.
type WatchEvent struct {
    WatchID string          `json:"watchId"`
    Seq     int             `json:"seq"`
    Payload json.RawMessage `json:"payload"`
}
```

#### WatchStream

```go
// WatchStream delivers events for a single watch stream identified by watchID.
// Stop must be idempotent and concurrent-safe.
type WatchStream struct {
    watchID     string
    ch          chan WatchEvent
    done        chan struct{}
    client      *Client
    stopOnce    sync.Once
    closeChOnce sync.Once
}

func (ws *WatchStream) WatchID() string              { return ws.watchID }
func (ws *WatchStream) ResultChan() <-chan WatchEvent { return ws.ch }
func (ws *WatchStream) Done() <-chan struct{}         { return ws.done }

func (ws *WatchStream) Stop() {
    ws.stopOnce.Do(func() {
        close(ws.done)
        ws.client.removeWatch(ws.watchID, ws)
    })
}

func (ws *WatchStream) closeCh() {
    ws.closeChOnce.Do(func() { close(ws.ch) })
}
```

`Stop()` 只关闭 `done` 并从路由表移除 stream，不直接关闭 `ch`；`ch` 由 notifWorker 在 eviction 或连接关闭时通过 `closeCh()` 幂等关闭。业务层 wrapper 必须同时监听 `ResultChan()` 和 `Done()`，并且在向 typed channel 转发时也要能被 `Done()` 打断。

#### Client 增加 watch 管理

```go
type Client struct {
    // ... 现有字段不变

    watchMu sync.RWMutex
    watches map[string]*WatchStream
}

// AddWatch 注册一个 watch stream，通知自动按 watchID 路由到 WatchStream.ch。
func (c *Client) AddWatch(watchID string, bufSize int) *WatchStream {
    ws := &WatchStream{
        watchID: watchID,
        ch:      make(chan WatchEvent, bufSize),
        done:    make(chan struct{}),
        client:  c,
    }
    c.watchMu.Lock()
    c.watches[watchID] = ws
    c.watchMu.Unlock()
    return ws
}

func (c *Client) removeWatch(watchID string, ws *WatchStream) {
    c.watchMu.Lock()
    if c.watches[watchID] == ws {
        delete(c.watches, watchID)
    }
    c.watchMu.Unlock()
}
```

#### notifWorker 增加 watch 路由

在现有 notifWorker 中，notification 到达时**先尝试 watch 路由**，match 不到再走现有的 Subscribe / global handler fallback：

```go
// 在 notifWorker 的 case msg := <-c.notifCh 分支中：
nm := NotificationMsg{Method: msg.method, Params: msg.params}

// Step 1: try watch routing (by watchId in payload)
if c.routeWatchEvent(msg.params) {
    continue
}

// Step 2: existing routing (Subscribe fan-out → global handler fallback)
matched := c.routeToSubscribers(nm)
if !matched { /* global handler/channel */ }
```

```go
func (c *Client) routeWatchEvent(params json.RawMessage) bool {
    // 快速检查：只解析 watchId 字段
    var probe struct {
        WatchID string `json:"watchId"`
    }
    if json.Unmarshal(params, &probe) != nil || probe.WatchID == "" {
        return false
    }

    c.watchMu.RLock()
    ws := c.watches[probe.WatchID]
    c.watchMu.RUnlock()
    if ws == nil {
        return false
    }

    // 完整解析
    var ev WatchEvent
    if err := json.Unmarshal(params, &ev); err != nil {
        return true // matched but malformed; don't fall through
    }

    select {
    case ws.ch <- ev:     // 正常投递
    case <-ws.done:       // watcher 已停止
    default:              // consumer 慢 → 驱逐（K8s 风格）
        c.evictWatch(ws)
    }
    return true
}

func (c *Client) evictWatch(ws *WatchStream) {
    c.watchMu.Lock()
    if c.watches[ws.watchID] == ws {
        delete(c.watches, ws.watchID)
    }
    c.watchMu.Unlock()
    ws.closeCh()
}
```

#### notifWorker shutdown 时关闭所有 WatchStream

```go
// 在 notifWorker 的 cleanup/done 阶段，增加：
c.watchMu.Lock()
for _, ws := range c.watches {
    ws.closeCh()
}
c.watches = make(map[string]*WatchStream)
c.watchMu.Unlock()
```

#### 时序保证：为什么 replay 不会早于本地路由注册

`jsonrpc.Client.Watch()` 在发送 RPC 前完成 watchID 生成和本地路由注册：

时序链：
1. Client 生成 UUID watchID
2. Client 调用 `AddWatch(watchID, bufSize)` 注册本地 `WatchStream`
3. Client 把 `watchId` 注入 `runtime/watch_event` params
4. Client 发送 RPC 请求
5. Server 使用请求里的 watchID 注册 subscriber，并在 replay/live notification 上打同一个 watchID
6. `notifWorker` 按 watchID 路由到已经存在的 `WatchStream`

关键：listener 在 RPC 发出前就已经就绪，因此即使 server 在返回 response 前就发送 replay notification，也不会落入“未知 watchID”窗口。

---

### Step 2: pkg/watch — 精简

只保留三个文件：

#### `interface.go`

```go
// Interface 对应 k8s watch.Interface，generic。
// Stop 必须幂等、并发安全。
type Interface[T any] interface {
    Stop()
    ResultChan() <-chan T
}
```

#### `retry.go` — RetryWatcher

```go
type WatchFunc[T any] func(ctx context.Context, fromSeq int) (Interface[T], error)

type RetryWatcher[T any] struct {
    wf       WatchFunc[T]
    getSeq   func(T) int
    cursor   atomic.Int64    // 最后成功写入 result 的事件 seq；-1 = 尚无事件
    result   chan T
    activeMu sync.Mutex
    active   Interface[T]
    cancel   context.CancelFunc
    once     sync.Once
}

func NewRetryWatcher[T any](
    ctx        context.Context,
    wf         WatchFunc[T],
    initCursor int,
    getSeq     func(T) int,
    queueSize  int,
) *RetryWatcher[T]
// 构造时立即启动 reconnectLoop goroutine
```

cursor 更新边界：入队成功后才推进。

```go
func (rw *RetryWatcher[T]) runOnce(ctx context.Context) {
    cursor := int(rw.cursor.Load())
    fromSeq := cursor + 1
    if cursor < 0 { fromSeq = 0 }

    active, err := rw.wf(ctx, fromSeq)
    if err != nil { return }

    rw.activeMu.Lock()
    rw.active = active
    rw.activeMu.Unlock()

    defer func() {
        active.Stop()
        rw.activeMu.Lock()
        if rw.active == active { rw.active = nil }
        rw.activeMu.Unlock()
    }()

    for ev := range active.ResultChan() {
        select {
        case rw.result <- ev:
            rw.cursor.Store(int64(rw.getSeq(ev)))
        case <-ctx.Done():
            return
        }
    }
}

func (rw *RetryWatcher[T]) Stop() {
    rw.once.Do(func() {
        rw.cancel()
        rw.activeMu.Lock()
        active := rw.active
        rw.activeMu.Unlock()
        if active != nil { active.Stop() }
    })
}
```

#### `event.go` — 保留

`Event[T]` 保留，`WatchServer` 依赖它。

---

### Step 3: pkg/agentrun/client — 极薄业务层

#### client.go 更新

`Dial` 不变（不再传 `WithNotificationHandler`，jsonrpc 内部处理 watch 路由）。

`WatchEvent` 返回 `watch.Interface[AgentRunEvent]`：

```go
func (c *Client) WatchEvent(
    ctx context.Context,
    req *runapi.SessionWatchEventParams,
) (watch.Interface[runapi.AgentRunEvent], error) {
    if req == nil {
        req = &runapi.SessionWatchEventParams{}
    }
    var result runapi.SessionWatchEventResult
    ws, err := c.c.Watch(ctx, runapi.MethodRuntimeWatchEvent, req, &result, 256)
    if err != nil {
        return nil, err
    }
    return newTypedWatcher(ws), nil
}
```

#### watch.go（新增）

```go
// typedWatcher wraps jsonrpc.WatchStream, deserializes payload into AgentRunEvent.
type typedWatcher struct {
    ws     *jsonrpc.WatchStream
    ch     chan runapi.AgentRunEvent
    once   sync.Once
}

func newTypedWatcher(ws *jsonrpc.WatchStream) *typedWatcher {
    tw := &typedWatcher{
        ws: ws,
        ch: make(chan runapi.AgentRunEvent, cap(ws.ResultChan())),
    }
    go tw.pump()
    return tw
}

// pump reads from WatchStream, deserializes payload, delivers typed events.
func (tw *typedWatcher) pump() {
    defer close(tw.ch)
    for {
        select {
        case wev, ok := <-tw.ws.ResultChan():
            if !ok { return }
            var ev runapi.AgentRunEvent
            if err := json.Unmarshal(wev.Payload, &ev); err != nil {
                continue
            }
            select {
            case tw.ch <- ev:
            case <-tw.ws.Done():
                return
            }
        case <-tw.ws.Done():
            return
        }
    }
}

func (tw *typedWatcher) ResultChan() <-chan runapi.AgentRunEvent { return tw.ch }
func (tw *typedWatcher) Stop()                                    { tw.once.Do(func() { tw.ws.Stop() }) }

// ownedWatcher wraps Interface + io.Closer, Stop 时一并关闭底层 Client
type ownedWatcher struct {
    watch.Interface[runapi.AgentRunEvent]
    owner io.Closer
    once  sync.Once
}

func (o *ownedWatcher) Stop() {
    o.once.Do(func() {
        o.Interface.Stop()
        _ = o.owner.Close()
    })
}

// NewWatchFunc returns a WatchFunc for use with RetryWatcher.
// Each call Dials a new Client, calls WatchEvent, and returns an ownedWatcher.
func NewWatchFunc(socketPath string) watch.WatchFunc[runapi.AgentRunEvent] {
    return func(ctx context.Context, fromSeq int) (watch.Interface[runapi.AgentRunEvent], error) {
        c, err := Dial(ctx, socketPath)
        if err != nil { return nil, err }
        req := &runapi.SessionWatchEventParams{}
        if fromSeq >= 0 { req.FromSeq = &fromSeq }
        w, err := c.WatchEvent(ctx, req)
        if err != nil { _ = c.Close(); return nil, err }
        return &ownedWatcher{Interface: w, owner: c}, nil
    }
}
```

---

### Step 4: 调用方迁移

#### agentd/process.go

```go
// RunProcess：删除 WC + WCStop，统一为 Watcher
Watcher watch.Interface[runapi.AgentRunEvent]

// Start()：control RPC client 保留，watch 使用 RetryWatcher 自动重连
client, err := runclient.Dial(ctx, socketPath)
runProc.Client = client
runProc.Watcher = watch.NewRetryWatcher(
    ctx, runclient.NewWatchFunc(socketPath),
    -1,
    func(ev runapi.AgentRunEvent) int { return ev.Seq },
    1024,
)

// startEventConsumer：单循环
for ev := range runProc.Watcher.ResultChan() {
    m.routeEvent(workspace, name, runProc, ev, logger)
}
```

#### agentd/recovery.go

```go
watcher := watch.NewRetryWatcher(
    ctx, runclient.NewWatchFunc(socketPath),
    status.Recovery.LastSeq,
    func(ev runapi.AgentRunEvent) int { return ev.Seq },
    64,
)
runProc.Watcher = watcher

// watchRecoveredProcess 停止
runProc.Watcher.Stop()
```

#### tui/chat.go

```go
watcher := watch.NewRetryWatcher(
    ctx, runclient.NewWatchFunc(opts.SocketPath),
    -1, func(ev runapi.AgentRunEvent) int { return ev.Seq }, 4096,
)

// waitNotif
ev, ok := <-watcher.ResultChan()  // 直接是 AgentRunEvent

// cleanup
watcher.Stop()
```

#### cmd/massctl/commands/agentrun/prompt.go

```go
watcher := watch.NewRetryWatcher(
    ctx, runclient.NewWatchFunc(socketPath),
    -1, func(ev runapi.AgentRunEvent) int { return ev.Seq }, 1024,
)
defer watcher.Stop()
for ev := range watcher.ResultChan() { ... }
```

---

## 变化总结

| 维度 | 改动前 | 改动后 |
|------|--------|--------|
| 分层 | Subscribe → relay → Watcher → WatchClient | jsonrpc.WatchStream → typedWatcher |
| 层数 | 4 | 1（+ 薄 typed wrapper） |
| Drop 位置 | routeToSubscribers non-blocking | 消除：不走 Subscribe |
| 慢 consumer | drop（静默丢失） | 驱逐 + RetryWatcher 重连（零丢失） |
| 连接模型 | 共享（有堵 read loop 风险） | 共享（非阻塞路由，不堵） |
| jsonrpc 层 | Subscribe（通用但 watch 不该用） | 内建 WatchStream（watch 一等公民） |
| Watch 信封 | 无标准，AgentRunEvent 自带 watchId | `WatchEvent{watchId, seq, payload}` 标准化 |
| relay goroutine | 有（无界内存） | 删除 |
| Decoder/StreamWatcher | 有 | 删除 |
| pkg/watch 文件数 | 6 | 3（interface + retry + event） |
| RunProcess 字段 | Watcher + WC + WCStop | 一个 Watcher |

---

## Known Issues

- **WatchServer**：保留不动，不在本方案范围内。慢 watcher 阻塞问题另行处理。
- **`notifCh` 满**：`enqueueNotification` 阻塞 read loop 时，所有 notification 被暂停，但 RPC response 在此之前已被 read loop dispatch（不经过 notifCh）。极端情况下 notification 排队延迟，但不会丢（notifCh 是阻塞写）。
- **Subscribe**：保留不删除，其他场景可用。Watch 路径不再使用它。

---

## 实施顺序

1. `pkg/jsonrpc`：增加 WatchEvent/WatchStream/AddWatch/routeWatchEvent/evictWatch
2. `pkg/watch`：新建 interface.go + retry.go；删除 conn.go、client.go、client_test.go
3. `pkg/agentrun/client`：新建 watch.go（typedWatcher/ownedWatcher/NewWatchFunc）；更新 client.go WatchEvent；删除 watcher.go、watchconn.go 及其测试
4. 调用方迁移：agentd、tui、prompt
5. 编译验证：make build && make lint

## 文件改动清单

### 修改
- `pkg/jsonrpc/client.go`（增加 watch 能力）

### 新建
- `pkg/watch/interface.go`
- `pkg/watch/retry.go`
- `pkg/agentrun/client/watch.go`
- `pkg/agentrun/client/watch_test.go`

### 更新
- `pkg/agentrun/client/client.go`
- `pkg/agentd/process.go`
- `pkg/agentd/recovery.go`
- `pkg/tui/chat/chat.go`
- `cmd/massctl/commands/agentrun/prompt.go`

### 保留不动
- `pkg/watch/event.go`
- `pkg/watch/server.go` + `server_test.go`

### 删除
- `pkg/watch/conn.go`
- `pkg/watch/client.go`
- `pkg/watch/client_test.go`
- `pkg/agentrun/client/watcher.go`
- `pkg/agentrun/client/watchconn.go`
- `pkg/agentrun/client/watcher_test.go`
- `pkg/agentrun/client/watchconn_test.go`
