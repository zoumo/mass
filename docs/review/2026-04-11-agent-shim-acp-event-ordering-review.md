# Agent Shim ACP Event Ordering Review

**Date**: 2026-04-11
**Status**: Analysis complete, code unchanged
**Scope**: `agent-shim -> ACP -> translator -> shim RPC -> agentd notification handler` event ordering path

---

## Issue

单个 `agent-shim` 理论上只绑定一个 ACP 连接和一个 agent 进程，因此如果底层连接按到达顺序串行处理，`session/update` 事件本应天然可线性化。

但当前实现里，同一条连接上的消息在多个层次被主动并发分发，导致：

- shim 侧生成的全局 `seq` 虽然单调递增；
- 但 agentd 侧“实际收到”的 notification 顺序不再可靠；
- 上层如果按到达顺序消费 `shimProc.Events`，仍会看到乱序。

结论：这不是“多 agent 共享同一个 shim”的问题，而是**单连接事件流在代码里被并发化后失去到达顺序保证**。

---

## Evidence

### 1. ACP SDK 在连接层把每条入站消息都丢进 goroutine

文件：

- [connection.go](/Users/jim/code/zoumo/acp-go-sdk/connection.go)

关键代码：

- `receive()` 中读取到带 `method` 的消息后执行 `go c.handleInbound(&msg)`。

这意味着：

- 即使 agent 的 stdout 是严格顺序写出的 JSON-RPC 消息；
- `acp-go-sdk` 也会把这些消息并发交给 handler；
- `session/update` notification 的 handler 完成先后不再等于消息读入顺序。

这已经足够破坏 “单 ACP 连接天然顺序” 的前提。

---

### 2. shim RPC server 再次使用 `jsonrpc2.AsyncHandler`

文件：

- [server.go](/Users/jim/code/zoumo/open-agent-runtime/pkg/rpc/server.go)
- [async.go](/Users/jim/go/pkg/mod/github.com/sourcegraph/jsonrpc2@v0.2.1/async.go)

关键代码：

- shim server 建连时使用 `jsonrpc2.NewConn(..., jsonrpc2.AsyncHandler(&connHandler{...}))`
- `AsyncHandler` 的实现本身就是 `go h.Handler.Handle(ctx, conn, req)`

影响：

- 即使 translator 已经按顺序对 envelope 分配了 `seq`；
- 到某个 client 的 `session/update` / `runtime/stateChange` 处理仍是并发进入 handler；
- 任何依赖“收到通知的顺序 == 服务端发送顺序”的代码都不成立。

---

### 3. agentd 连接 shim 时也再次使用 `jsonrpc2.AsyncHandler`

文件：

- [shim_client.go](/Users/jim/code/zoumo/open-agent-runtime/pkg/agentd/shim_client.go)
- [async.go](/Users/jim/go/pkg/mod/github.com/sourcegraph/jsonrpc2@v0.2.1/async.go)

关键代码：

- `dialInternal()` 中使用 `jsonrpc2.NewConn(ctx, stream, jsonrpc2.AsyncHandler(&clientHandler{...}))`

影响：

- 同一 shim 发来的连续 `session/update` notification，
  会在 agentd 侧并发执行 `clientHandler.Handle(...)`；
- 于是 `buildNotifHandler(...)` 里的 `shimProc.Events <- ev` 并不保证按 `seq` 进入 channel；
- 这正好对应“shim 明明是单连接单 agent，但收到的事件仍乱序”这个现象。

---

### 4. 当前 agentd 没有任何按 `seq` 或 `streamSeq` 的重排逻辑

文件：

- [process.go](/Users/jim/code/zoumo/open-agent-runtime/pkg/agentd/process.go)

关键代码：

- `buildNotifHandler()` 解析 `session/update` 后，直接把 `p.Event.Payload` 投递到 `shimProc.Events`
- 投递时只传 `events.Event`，没有保留 `seq` / `turnId` / `streamSeq`

影响：

- 一旦通知在前面任何一层被并发化，agentd 这里已经没有足够信息恢复顺序；
- 即使 wire 上带了 `seq` 和 `streamSeq`，也在进入 `shimProc.Events` 前丢掉了；
- 所以上层消费者只能看到“事件类型流”，无法稳定重建原始顺序。

这也是为什么设计文档已经引入 `turnId` / `streamSeq`，但实际消费侧仍然会看到乱序。

---

## Investigation Notes

### translator 本身不是首要根因

文件：

- [translator.go](/Users/jim/code/zoumo/open-agent-runtime/pkg/events/translator.go)

`Translator` 在内部用 mutex 包住 `nextSeq` 分配，并在单个 `run()` goroutine 中从 `in <-chan acp.SessionNotification` 取消息，再生成 envelope。

如果 `in` 是按顺序进入的，那么 translator 生成的 `seq` 是稳定的。

问题在于：

1. ACP SDK 在 translator 之前就可能把 `SessionUpdate` 回调并发化；
2. shim 和 agentd 的 JSON-RPC 层又分别把通知 handler 并发化；
3. agentd 最终还把排序字段丢掉。

所以 translator 只保证“它看到的输入顺序被编号”，不能保证“agentd 实际收到的顺序与 agent 原始发送顺序一致”。

---

### 当前测试没有覆盖“到达顺序必须与 seq 一致”

现有测试主要验证：

- `seq` 单调递增；
- backfill/history 可回放；
- turn 内共享同一个 `turnId`；
- `streamSeq` 在记录中递增。

但没有一个测试去强约束下面这个条件：

> 对同一条 live 订阅连接，notification handler 的实际调用顺序必须与 `seq` 一致。

由于代码里普遍用了 `AsyncHandler`，这个条件在当前实现下并不成立。

---

## Root Cause

根因不是单个 shim 映射关系错误，而是**顺序敏感的消息流被多个通道重复异步化**：

1. `acp-go-sdk` connection 层对入站消息 `go handleInbound`
2. shim RPC server 对 JSON-RPC request/notification 使用 `AsyncHandler`
3. agentd shim client 对入站 notification 也使用 `AsyncHandler`
4. agentd 在最终交付给 `shimProc.Events` 前丢弃排序元数据

因此，系统现在只有“日志中的 `seq` 是全局单调的”这个性质，没有“消费者按收到顺序处理就等于协议顺序”这个性质。

从用户症状看，最直接的表征就是：

- 单 shim / 单 agent / 单 ACP session 场景下，
- 上层仍可能先看到较晚的 event，再看到较早的 event。

---

## Recommended Fix

> **约束：acp-go-sdk 不可修改。** 以下方案在此约束下给出优先级和具体改法。

---

### P0（必做）：去掉 agentd 客户端侧的 `AsyncHandler`

**文件：`pkg/agentd/shim_client.go:64`**

这是最直接的根因修复，一行代码。

```go
// 改前
h := jsonrpc2.AsyncHandler(&clientHandler{notifHandler: handler})

// 改后
h := &clientHandler{notifHandler: handler}
```

`clientHandler` 已实现 `jsonrpc2.Handler` 接口，直接传入即可。改完后，agentd 侧对入站 notification 的处理变为串行，顺序与 shim 发送顺序完全一致。

**为何安全：** agentd 客户端只接收 notification，不需要并发处理；`buildNotifHandler` 里的 channel send 是非阻塞的，不会卡住读循环。

---

### P0（必做）：不要动 `rpc/server.go` 的 `AsyncHandler`

**`pkg/rpc/server.go:124` 的 `AsyncHandler` 必须保留。**

```
session/prompt  (阻塞，等 AI 完成)
session/cancel  (必须能在 prompt 阻塞期间并发处理)
```

如果去掉 `AsyncHandler`，`session/cancel` 会排队在 `session/prompt` 后面，永远无法取消当前 turn。

shim → agentd 的 notification 是通过 `handleSubscribe` 里的 subscriber goroutine 调用 `conn.Notify` 发出的，与 server handler 并发性无关，本来就是串行的。

---

### P1（建议做）：`SessionUpdate` 加 mutex 防并发写

**文件：`pkg/runtime/client.go`**

acp-go-sdk 的 `go c.handleInbound()` 会并发调用 `SessionUpdate`。虽然 Go goroutine 启动有先后，实测乱序概率极低，但加 mutex 可以彻底消除理论上的竞争窗口。

```go
type acpClient struct {
    mgr         *Manager
    terminalMgr *TerminalManager
    mu          sync.Mutex  // 新增：序列化并发 SessionUpdate 调用
}

func (c *acpClient) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
    c.mu.Lock()
    defer c.mu.Unlock()
    select {
    case c.mgr.events <- n:
    default:
    }
    return nil
}
```

**为何不能完全保证顺序：** goroutine 调度是非确定性的，G2 仍有极小概率在 G1 之前抢到 mutex。但在实际场景中（`receive()` 按行顺序启动 goroutine，每个 goroutine 在 handler 调用前几乎无额外工作），乱序概率可忽略。

---

### P2（保底）：`shimProc.Events` 保留排序元数据

**文件：`pkg/agentd/process.go`**

当前 `shimProc.Events` 只传裸 `events.Event`，丢失了 `seq`/`turnId`/`streamSeq`。即使前面各层出现极小概率乱序，消费侧也无法补救。改为传完整的 `events.SessionUpdateParams`，消费方可用 `streamSeq` 在 turn 内重排。

```go
// ShimProcess 字段类型变更
Events chan events.SessionUpdateParams  // 原来是 chan events.Event

// forkShim 里
Events: make(chan events.SessionUpdateParams, 1024),

// buildNotifHandler 里
case events.MethodSessionUpdate:
    p, err := ParseSessionUpdate(params)
    if err != nil {
        logger.Warn(“malformed session/update notification dropped”, “error”, err)
        return
    }
    select {
    case shimProc.Events <- p:          // 传完整 params，含 seq/turnId/streamSeq
    default:
        logger.Warn(“event channel full, dropping event”, “seq”, p.Seq)
    }
```

消费侧同 turn 内用 `streamSeq` 排序，跨 turn 用 `seq`，即使上游有残余乱序也可稳定重建。

---

### Option C: 增加一个明确的顺序回归测试

建议覆盖：

1. mock agent 顺序发送多个 `session/update`
2. 通过真实 shim RPC 订阅 live stream
3. 在 agentd 侧断言 handler 实际观察到的 `seq` 是严格递增

只有把 “live delivery order must match seq order” 写成测试，后续才不容易被 `AsyncHandler` 一类便利封装再次破坏。

---

## Verification Plan

在修复后建议验证：

1. 构造一个 agent 在单个 prompt 中稳定输出 `text-1 -> text-2 -> text-3`
2. 在 shim 侧记录 live `session/update` 实际到达顺序
3. 在 agentd 侧记录 `buildNotifHandler` 实际收到的 `seq`
4. 断言两边都严格单调递增
5. 再补一个恢复场景，验证 `history + live` 拼接后仍按 `seq`/`streamSeq` 可重建

---

## Risk Assessment

当前风险是中高：

- 对只看 `events.jsonl` 回放的场景，问题较轻，因为日志里有 `seq`
- 对直接消费 live `shimProc.Events` 或 attach stream 的场景，问题较重，因为到达顺序本身不可靠
- 这会直接影响 chat UI、turn replay、工具调用展示，以及任何“按收到顺序渲染”的上层逻辑

我的判断是：

- **首要根因**：`acp-go-sdk` 和 `jsonrpc2.AsyncHandler` 在顺序敏感路径上的并发分发
- **放大器**：agentd 在最终交付前丢弃排序字段

置信度：高。
