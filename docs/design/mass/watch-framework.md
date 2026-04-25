---
last_updated: 2026-04-25
---

# Watch Framework

## 概述

`pkg/watch` 是 MASS 的通用事件推送框架，提供配套的 `WatchServer[T]` 和 `WatchClient[T]`。
框架定义标准事件信封 `watch.Event[T]`，与 K8s watch 机制对齐：信封携带流级别游标，
payload 是业务类型 `T`，框架不感知 `T` 的内容。

---

## 标准事件信封

```go
// pkg/watch/event.go

type Event[T any] struct {
    Seq     int `json:"seq"`      // 流级别游标，由发布方赋值
    Payload T   `json:"payload"`  // 业务内容，框架不感知
}
```

与 K8s 对比：

| | K8s | 我们 |
|---|---|---|
| 信封结构 | `{type, object}` | `{seq, payload}` |
| 游标位置 | object 内部（`metadata.resourceVersion`） | 信封上（`Event.Seq`） |
| 游标语义 | 对象版本（每个 object 各自的 RV） | 流位置（全局单调递增） |

K8s 游标在 object 内部是因为 ResourceVersion 是对象自身属性；
我们的 `Seq` 是流的位置，放在信封上更准确。

---

## 包结构

```
pkg/watch/
  event.go    — Event[T] 标准信封
  server.go   — WatchServer[T]：连接管理，有序 fan-out
  client.go   — WatchClient[T]：本地队列，游标追踪，自动重连
  conn.go     — ServerConn[T] / ClientConn[T] 传输抽象
```

EventLog、seq 分配、replay 由 agentrun 层负责，不在框架内。

---

## Server 端

### 设计原则

- **每个 watcher 一个串行 send goroutine**：`Accept` 时启动，持续从 mailbox 读并写 conn
- **`Publish` 向每个 mailbox 阻塞写**：等上一次 Send 完成再推下一个，保证 per-watcher 有序
- **mailbox 无 buffer**：client 入队极快，socket 写几乎不阻塞；Send 失败立即关闭连接
- **失败即断**：任何写错误立即关闭对应连接，触发 client 自动重连

> **后续改进方向**：watcher 数量变多时可引入小 buffer（10）+ 超时驱逐
> （参考 K8s apiserver `dispatchTimeoutBudget`），当前场景不需要。

### 接口

```go
type ServerConn[T any] interface {
    Send(ev Event[T]) error  // 阻塞写；返回 error 时框架关闭此连接
    Close() error
}

type WatchServer[T any] struct{}

func NewWatchServer[T any]() *WatchServer[T]

// Publish 向所有已连接的 watcher 推送事件
// 每个 watcher 有独立串行 send goroutine，互不阻塞
func (s *WatchServer[T]) Publish(ev Event[T])

// Accept 注册新连接，启动对应 send goroutine，断开时自动清理
func (s *WatchServer[T]) Accept(conn ServerConn[T])
```

### 内部实现

每个连接对应一个固定 goroutine，拥有自己的 mailbox（无 buffer channel）：

```
Accept(conn):
  mailbox := make(chan Event[T])   // 无 buffer
  启动 send goroutine：
    for ev := range mailbox:
      if err := conn.Send(ev):
        conn.Close()
        return               // goroutine 退出，连接从 server 清除

Publish(ev):
  对每个 watcher 的 mailbox（并发）：
    mailbox <- ev            // 阻塞，等该 watcher 完成上一次 Send
```

`Publish` 向各 watcher 并发写 mailbox，各 watcher 的 send goroutine 串行处理，
保证同一 watcher 的事件严格按 Seq 顺序交付。

---

## Client 端

### 整体流向

watch loop（快）与 consumer（慢）通过本地队列解耦：

```
server
  │ 阻塞推送 Event[T]
  ▼
WatchClient.runOnce（watch loop，顺序接收）
  │ 入队，入队成功即更新 cursor = Event.Seq
  ▼
local queue（WatchClient 拥有，内存）   ← 游标边界
  │ 顺序出队，consumer 自己的节奏
  ▼
consumer（TUI / agentd / AI agent）
```

### 游标初始值

`WatchClient` 初始 `cursor = -1`，首次 `dial(ctx, cursor+1)` 传入 `0`，
确保全量 replay 从 `seq=0` 开始，不遗漏第一个事件。

需要从中间位置续接时（如 agentd recovery），由调用方在构造时传入起始值：

```go
// 全量 replay（TUI 场景）
wc := watch.NewWatchClient(dialFn, -1, queueSize)

// 只订阅 live（agentd recovery 场景）
wc := watch.NewWatchClient(dialFn, lastSeq, queueSize)
```

### 接口

```go
type ClientConn[T any] interface {
    Recv() (Event[T], error)  // 阻塞读；error 时框架关闭连接并重连
    Close() error
}

// DialFunc 由调用方注入；cursor 是本次重连的续接位置
type DialFunc[T any] func(ctx context.Context, cursor int) (ClientConn[T], error)

type WatchClient[T any] struct{}

// initialCursor: -1 表示从 seq=0 开始；传入具体值表示从 cursor+1 续接
func NewWatchClient[T any](dial DialFunc[T], initialCursor int, queueSize int) *WatchClient[T]

func (c *WatchClient[T]) Start(ctx context.Context)
func (c *WatchClient[T]) Events() <-chan Event[T]  // consumer range 此 channel
func (c *WatchClient[T]) Cursor() int             // 最后入队的 Seq，用于诊断
```

### Watch Loop（内部）

```go
func (c *WatchClient[T]) runOnce(ctx context.Context) error {
    conn, err := c.dial(ctx, c.cursor+1)   // cursor=-1 时传 0，正确请求全量 replay
    if err != nil { return err }
    defer conn.Close()

    for {
        ev, err := conn.Recv()
        if err != nil { return err }

        select {
        case c.queue <- ev:
            c.cursor = ev.Seq   // 入队成功即推进游标，直接读 Event.Seq
        case <-ctx.Done():
            return ctx.Err()
        }
    }
}

func (c *WatchClient[T]) Start(ctx context.Context) {
    go func() {
        for {
            _ = c.runOnce(ctx)
            select {
            case <-ctx.Done(): return
            case <-backoff.Next(): // 指数退避，初始 500ms，上限 10s
            }
        }
    }()
}
```

### 消费方写法（统一）

```go
// TUI：全量 replay
wc := watch.NewWatchClient(dialFn, -1, 4096)
wc.Start(ctx)
for ev := range wc.Events() {
    process(ev.Payload)
}

// agentd recovery：只订阅 live
wc := watch.NewWatchClient(dialFn, lastSeq, 64)
wc.Start(ctx)
for ev := range wc.Events() {
    process(ev.Payload)
}
```

---

## 背压链

```
consumer 慢
  → queue 满 → queue <- ev 阻塞 → watch loop 停止 Recv
  → server mailbox <- ev 阻塞（send goroutine 未完成上一次 Send）
    或 conn.Send 阻塞（socket 写满）
  → server 检测 Send 失败 → 关闭连接
  → client Recv 返回 error → 重连，cursor+1 续接
  → queue 中已有事件不丢，consumer 继续消费
```

整条背压链闭合，无 drop，无静默丢失。

---

## 游标语义

| 游标 | 含义 | 初始值 | 更新时机 |
|------|------|--------|---------|
| `WatchClient.cursor` | 最后成功入队的 `Event.Seq` | `-1`（或调用方指定） | `queue <- ev` 成功后 |
| `Cursor()` 返回值 | 同上，供诊断用（如 TUI header 显示） | `-1` | 同上 |
| consumer 处理进度 | consumer 自己维护（可选） | 业务定义 | 业务层，不影响重连 |

---

## agentrun 层适配

当前 `AgentRunEvent` 的变化：

| 字段 | 现在 | 新设计 |
|------|------|--------|
| `Seq` | `AgentRunEvent.Seq` | 移到 `watch.Event.Seq` |
| `WatchID` | `AgentRunEvent.WatchID`（传输层 demux） | 移到传输层，不在事件结构中 |
| 其余字段 | `AgentRunEvent` 内 | 保持，作为 `T` = payload |

新 wire 结构：

```json
{
  "seq": 42,
  "payload": {
    "runId":     "verifier",
    "sessionId": "1e087ec3-...",
    "time":      "2026-04-24T16:33:41Z",
    "type":      "text",
    "turnId":    "fa549d5b-...",
    "payload":   { ... }
  }
}
```

agentrun 层发布事件：

```go
server.Publish(watch.Event[AgentRunEvent]{
    Seq:     t.nextSeq,
    Payload: agentRunEvent,   // 不含 Seq、WatchID
})
```

---

## 不在此范围内

- EventLog / 持久化（agentrun 层职责）
- seq 分配（agentrun Translator 职责）
- replay 逻辑（agentrun Service.watchWithReplay 职责）
- `runtime/watch_event` RPC 协议细节
- 传输层实现（JSON-RPC、HTTP streaming 等）
