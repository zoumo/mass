---
last_updated: 2026-04-26
---

# Watch Framework

## 概述

`pkg/watch` 是 MASS 的通用事件推送框架，提供 `WatchServer[T]`（服务端 fan-out）、
`Interface[T]`（单次流抽象）和 `RetryWatcher[T]`（自动重连客户端）。
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

| | K8s | MASS |
|---|---|---|
| 信封结构 | `{type, object}` | `{seq, payload}` |
| 游标位置 | object 内部（`metadata.resourceVersion`） | 信封上（`Event.Seq`） |
| 游标语义 | 对象版本（每个 object 各自的 RV） | 流位置（全局单调递增） |

K8s 游标在 object 内部是因为 ResourceVersion 是对象自身属性；
`Seq` 是流的位置，放在信封上更准确。

---

## 包结构

```
pkg/watch/
  event.go      — Event[T] 标准信封
  interface.go  — Interface[T]：单次流抽象（Stop + ResultChan）
  server.go     — WatchServer[T]：连接管理，有序 fan-out
  retry.go      — RetryWatcher[T]：自动重连，本地队列，游标追踪
```

EventLog、seq 分配、replay 由 agentrun 层负责，不在框架内。

---

## Interface[T]

单次 watch 流的标准接口，对齐 `k8s.io/apimachinery/pkg/watch.Interface`：

```go
type Interface[T any] interface {
    Stop()               // 终止流并关闭 ResultChan；幂等，协程安全
    ResultChan() <-chan T // 事件 channel；Stop 或底层流结束时关闭
}
```

使用方式：

```go
w, err := client.WatchEvent(ctx, fromSeq)
if err != nil { ... }
defer w.Stop()
for ev := range w.ResultChan() { ... }
```

---

## Server 端

### 设计原则

- **每个 watcher 一个串行 send goroutine**：`Accept` 时启动，持续从 mailbox 读并写 conn
- **`Publish` 串行化**：`publishMu` 保证多个并发 `Publish` 调用不会交叉，所有 watcher 看到相同的全局顺序
- **mailbox 无 buffer**：`Publish` 阻塞等上一次 Send 完成再推下一个，保证 per-watcher 有序
- **失败即断**：任何 Send error 立即关闭连接，触发 client 自动重连

### 接口

```go
type ServerConn[T any] interface {
    Send(ev Event[T]) error  // 阻塞写；返回 error 时框架关闭此连接
    Close() error
}

type WatchServer[T any] struct{}

func NewWatchServer[T any]() *WatchServer[T]

// Publish 向所有已连接的 watcher 推送事件。
// publishMu 串行化并发调用，保证全局事件顺序一致。
// 每个 watcher 通过独立 send goroutine 消费 mailbox，互不阻塞。
func (s *WatchServer[T]) Publish(ev Event[T])

// Accept 注册新连接，启动对应 send goroutine，断开时自动清理。
func (s *WatchServer[T]) Accept(conn ServerConn[T])
```

### 内部实现

每个连接对应一个固定 goroutine，拥有自己的 mailbox（无 buffer channel）和 done channel：

```
Accept(conn):
  id := nextID++
  mailbox := make(chan Event[T])   // 无 buffer
  done := make(chan struct{})
  启动 send goroutine：
    for ev := range mailbox:
      if err := conn.Send(ev):
        conn.Close()
        close(done)
        return    // goroutine 退出，watcher 从 map 中移除

Publish(ev):
  publishMu.Lock()           // 串行化并发 Publish
  snapshot := 当前所有 watcher
  publishMu.Unlock() 前完成所有 send：
    for each watcher:
      select:
        case mailbox <- ev:  // 阻塞，等该 watcher 完成上一次 Send
        case <-done:         // watcher 已断开，跳过
```

`publishMu` 保证 ev1 进入所有 mailbox 后 ev2 才开始，消除并发 Publish 的交叉风险。

---

## Client 端（RetryWatcher）

### 整体流向

RetryWatcher 封装 `WatchFunc`，提供自动重连、本地队列和游标追踪。
watch loop（快）与 consumer（慢）通过 buffered channel 解耦：

```
server
  │ 推送 T（业务类型，非 Event[T]）
  ▼
WatchFunc → Interface[T]（单次流）
  │ 每个事件入队到 result channel
  ▼
RetryWatcher.result（buffered channel，本地队列）  ← 游标边界
  │ consumer 按自己节奏消费
  ▼
consumer（TUI / agentd / AI agent）
```

### WatchFunc

```go
// WatchFunc 建立新的 watch 流，fromSeq 是续接位置。
// fromSeq=0 表示全量 replay。
type WatchFunc[T any] func(ctx context.Context, fromSeq int) (Interface[T], error)
```

调用方注入具体实现。agentrun 场景由 `runclient.NewWatchFunc(socketPath)` 提供，
每次调用 Dial 新连接、发起 `runtime/watch_event` RPC，返回 `Interface[AgentRunEvent]`。

### 游标初始值

`RetryWatcher` 初始 `cursor` 由构造参数 `initCursor` 指定。
`runOnce` 计算 `fromSeq = cursor + 1`（cursor < 0 时 fromSeq = 0）。

```go
// 全量 replay（TUI 场景）：cursor=-1 → fromSeq=0
watcher := watch.NewRetryWatcher(ctx, wf, -1, getSeq, 4096)

// 只订阅 live（agentd recovery 场景）：cursor=lastSeq → fromSeq=lastSeq+1
watcher := watch.NewRetryWatcher(ctx, wf, lastSeq, getSeq, 64)
```

### 接口

```go
type RetryWatcher[T any] struct{}

// initCursor: -1 表示从 seq=0 开始全量 replay；N 表示从 N+1 续接。
// getSeq: 从事件中提取序列号，用于游标追踪。
// queueSize: result channel 容量。
func NewRetryWatcher[T any](
    ctx context.Context,
    wf WatchFunc[T],
    initCursor int,
    getSeq func(T) int,
    queueSize int,
) *RetryWatcher[T]

func (rw *RetryWatcher[T]) ResultChan() <-chan T  // consumer range 此 channel
func (rw *RetryWatcher[T]) Cursor() int           // 最后入队的 Seq，用于诊断
func (rw *RetryWatcher[T]) Stop()                 // 终止重连循环，关闭 result channel
```

`RetryWatcher` 自身实现 `Interface[T]`。

### Reconnect Loop（内部）

```go
func (rw *RetryWatcher[T]) reconnectLoop(ctx context.Context) {
    backoff := 500ms
    for {
        delivered := rw.runOnce(ctx)
        if delivered {
            backoff = 500ms    // 成功交付过事件 → 重置退避
            continue
        }
        sleep(backoff)
        backoff = min(backoff * 2, 10s)
    }
}

func (rw *RetryWatcher[T]) runOnce(ctx context.Context) bool {
    fromSeq := cursor + 1   // cursor < 0 时用 0
    active, err := wf(ctx, fromSeq)
    if err != nil { return false }
    defer active.Stop()

    for ev := range active.ResultChan() {
        result <- ev
        cursor = getSeq(ev)  // 入队成功即推进游标
        delivered = true
    }
    return delivered
}
```

退避策略：初始 500ms，倍增，上限 10s。成功交付事件后立即重置。

### 消费方写法

```go
// TUI：全量 replay
watcher := watch.NewRetryWatcher(ctx, wf, -1, getSeq, 4096)
defer watcher.Stop()
for ev := range watcher.ResultChan() {
    process(ev)
}

// agentd recovery：只订阅 live
watcher := watch.NewRetryWatcher(ctx, wf, lastSeq, getSeq, 64)
defer watcher.Stop()
for ev := range watcher.ResultChan() {
    process(ev)
}
```

---

## 背压链

```
consumer 慢
  → result channel 满 → result <- ev 阻塞 → runOnce 停止从 active 读
  → active.ResultChan() 阻塞（上游写不进）
  → 上游检测超时或 buffer 满 → 关闭流（如 Translator 驱逐慢订阅者）
  → active.ResultChan() 关闭 → runOnce 返回
  → reconnectLoop 重连，fromSeq = cursor+1 续接
  → result 中已有事件不丢，consumer 继续消费
```

整条背压链闭合，无 drop，无静默丢失。

---

## 游标语义

| 游标 | 含义 | 初始值 | 更新时机 |
|------|------|--------|---------|
| `RetryWatcher.cursor` | 最后成功入队的事件的 Seq | `initCursor`（-1 或调用方指定） | `result <- ev` 成功后 |
| `Cursor()` 返回值 | 同上，供诊断用（如 TUI header 显示） | 同上 | 同上 |
| consumer 处理进度 | consumer 自己维护（可选） | 业务定义 | 业务层，不影响重连 |

---

## agentrun 层适配

### 事件结构

agentrun 层使用 `AgentRunEvent` 作为业务事件类型。`Seq` 由 `Translator` 在 `broadcast()` 中分配，
是全局单调递增的流游标。`WatchID` 是传输层 demux 标识，由 `forwardLiveEvents` 在推送时设置，
不在事件结构中持久化。

Wire 结构（`runtime/event_update` 通知）：

```json
{
  "watchId":   "abc-123",
  "seq":       42,
  "runId":     "verifier",
  "sessionId": "1e087ec3-...",
  "time":      "2026-04-24T16:33:41Z",
  "type":      "text",
  "turnId":    "fa549d5b-...",
  "payload":   { ... }
}
```

### Translator fan-out

`Translator` 是 agentrun 层的事件源，负责：

1. 将 ACP 协议通知翻译为 `AgentRunEvent`
2. 分配 `Seq`（全局单调递增）
3. Log-before-fanout：在同一个 mutex 下先写 EventLog 再 fan-out 到订阅者
4. 慢订阅者驱逐：subscriber channel buffer 满时关闭 channel 并移除（K8s 风格）

```go
broadcast(build):
    mu.Lock()
    ev := build(nextSeq, now)
    if log != nil:
        log.Append(ev)    // 失败则 drop 事件，不递增 nextSeq
    nextSeq++
    for each subscriber:
        select:
            case ch <- ev:  // 非阻塞发送
            default:        // buffer 满 → 驱逐
                close(ch)
                delete(subs, id)
    mu.Unlock()
```

### Watch with Replay

`Service.WatchEvent` 实现 `runtime/watch_event` RPC，支持两种模式：

- **Live-only**（`fromSeq` 未设置）：仅订阅实时事件
- **Replay + Live**（`fromSeq` 已设置）：两阶段无锁 replay
  1. Phase 1（持 Translator mutex，O(1)）：注册 subscriber，获取 `nextSeq`
  2. Phase 2（后台 goroutine，无 mutex）：从 EventLog 读取 `[fromSeq, nextSeq)` 历史事件推送，然后切换到 live 事件（`seq < nextSeq` 去重）

去重保证：log-before-fanout 确保 subscribe 时文件包含所有 `seq < nextSeq` 的事件，
live 事件中 `seq < nextSeq` 的被跳过，无重复无遗漏。

### EventLog

`EventLog` 是 per-session 的 JSONL 文件，提供持久化 replay 能力：

- **Append**：写前记录文件偏移，写失败则 truncate 回去（partial-write safety）
- **Seq 校验**：`Append` 校验 `ev.Seq == nextSeq`，拒绝乱序写入
- **OpenEventLog**：打开时扫描文件，truncate 损坏的尾部，从最后一个有效事件恢复 `nextSeq`
- **ReadEventLog**：读取时容忍尾部损坏（crash 场景），中间损坏仍报错

---

## 不在此范围内

- `runtime/watch_event` RPC 协议细节
- 传输层实现（JSON-RPC `WatchStream`、watchID 注入等）
- `AgentRunEvent` 事件类型定义
