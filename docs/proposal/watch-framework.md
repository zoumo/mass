# Watch Framework — 实现提案

## 状态

**Proposal** — 尚未实现。设计见 [docs/design/mass/watch-framework.md](../design/mass/watch-framework.md)。

---

## 问题背景

### 事件丢失（根因）

当前事件交付路径存在致命 drop：

```
server watchWithReplay goroutine
  peer.Notify(ev) ──► notifCh (buf=1024)
  （全速推送历史事件）      │
                          ▼
                     newWatcher goroutine
                          │ watchID 过滤 + 反序列化
                          ▼
                     eventCh (buf=256)
                          │ 满了 → default: drop（永久丢失）
                          ▼
                     waitNotif → tea.Msg → Update()
                     （每次只处理一个，极慢）
```

`newWatcher` 的 `default` drop 对 replay 场景致命：server 全速推送数千个历史事件，
256 buffer 迅速打满，大量事件被静默丢弃。

现象：
- TUI 每次连接同一 agent-run 显示的 seq 不一样（随机 drop 位置不同）
- chat 内容乱码、顺序错乱（事件丢失导致渲染状态机状态不完整）

### 无游标追踪，无自动重连

- `maxSeq` 只记录最大收到 seq，连接断开时无法用正确的 `fromSeq` 重连
- TUI、agentd 遇到 `connClosedMsg` 直接退出，不重连

### 逻辑分散，无法复用

Watch 逻辑分散在 `agentrun/server`、`agentrun/client`、`tui/chat` 三处，agent-run 专用。
未来 AI agent 消费事件需重新实现。

---

## 终态设计

见 [docs/design/mass/watch-framework.md](../design/mass/watch-framework.md)。

核心：K8s informer 模式，watch loop 与消费方通过本地队列解耦：

```
server ──watch stream──► local queue（WatchClient 拥有）──► consumer
                              ↑
                         cursor = 最后入队的 Event.Seq
                         入队成功即更新，断线重连用 cursor+1
```

---

## 实现计划

### 阶段 1：热修复（去掉 drop，允许背压）

**目标**：修复事件丢失，不做架构重构。

**T1.1 — 去掉 eventCh drop，改为阻塞**

文件：`pkg/agentrun/client/watcher.go`

```go
// 现在
select {
case eventCh <- ev:
default:
    slog.Debug("watcher: result channel full, event dropped", ...)
}

// 改为阻塞
eventCh <- ev
```

效果：consumer 慢时，`eventCh` 满 → `newWatcher` goroutine 阻塞停止读 `notifCh`
→ `notifCh` 满 → `jsonrpc notifWorker` 阻塞 → `peer.Notify` 写 socket 阻塞。

对 replay 路径：`watchWithReplay` goroutine 直接调 `peer.Notify`，
peer.Notify 阻塞即背压限速，不再无限速推送。
注意：replay 路径不经过 Translator subscriber channel，
Translator 的 channel-full eviction **不**覆盖 replay 阶段，
T1.1 的效果是"replay 时 server 端自然限速"，而非 Translator eviction。

**T1.2 — TUI 自动重连（需与 T1.1 同步上线）**

文件：`pkg/tui/chat/chat.go`

T1.1 去掉 drop 后，背压最终会触发 server 端关闭连接（socket write timeout 或
`watchWithReplay` goroutine 感知 peer 断开）。单独改 `connClosedMsg` 为不退出而不加重连，
会让 TUI 存活但永久无事件，需同时加最简重连：

```go
case connClosedMsg:
    // 用最后收到的 maxSeq 作为 fromSeq 重连
    m.chat.AppendMessages(component.NewSystemItem(..., "reconnecting...", styleDim))
    cmds = append(cmds, reconnectCmd(m.sock, m.maxSeq))
```

`reconnectCmd` 与现有 `connectCmd` 相同，但传入 `fromSeq = maxSeq`（而非 0）。

---

### 阶段 2：引入 WatchClient

**目标**：封装重连逻辑和游标追踪，消费方不再手动管理。

**T2.1 — 建 pkg/watch/client.go**

实现 `WatchClient[T]`，包含：
- `ClientConn[T]` / `DialFunc[T]` 接口
- 本地有界队列（`queueSize` 参数）
- watch loop + 指数退避重连（初始 500ms，上限 10s）
- `cursor` 初始值 `-1`，入队成功后更新为 `Event.Seq`
- `Start(ctx)` / `Events() <-chan Event[T]` / `Cursor() int`

**T2.2 — runclient.Client 实现 ClientConn 接口**

文件：`pkg/agentrun/client/client.go`

```go
// 返回 watch.ClientConn[AgentRunEvent]
func (c *Client) WatchEvent(ctx context.Context, fromSeq int) (watch.ClientConn[AgentRunEvent], error)
```

**T2.3 — TUI 迁移到 WatchClient**

文件：`pkg/tui/chat/chat.go`

- 删除 `maxSeq`、`liveSeq`、手动 `connectCmd` / `reconnectCmd` 逻辑
- `waitNotif` 改为从 `wc.Events()` 读取
- header seq 从 `wc.Cursor()` 读取
- `connClosedMsg` 不退出，`WatchClient` 内部自动重连

TUI 场景使用大队列（`queueSize=4096`）以容纳完整 replay，避免在 replay 阶段频繁触发重连。

**T2.4 — agentd recovery 迁移**

文件：`pkg/agentd/recovery.go`

`recoverAgent` 使用 `WatchClient`，初始 `cursor = lastSeq`（只订阅 live，`dial` 传入 `lastSeq+1`），
后续断线由 `WatchClient` 自动重连。

---

### 阶段 3：提取通用 WatchServer（可选，待评估）

**目标**：把 agentrun server 中通用的 fan-out + 连接管理逻辑提取到 `pkg/watch`。

**范围**：

仅提取以下部分到 `pkg/watch/server.go`：
- 连接注册（`Accept`）
- 有序 fan-out（per-watcher send goroutine + mailbox）

**不迁移**：
- EventLog / 持久化（agentrun 层职责）
- seq 分配（Translator 职责）
- replay 逻辑（`watchWithReplay` 职责）

`agentrun/server` 保留 `Translator`、`Service`、EventLog，
仅把 fan-out 部分 delegate 到 `pkg/watch.WatchServer`。

> **建议**：待阶段 2 稳定、`WatchClient` 行为经过生产验证后，再评估是否需要阶段 3。
> 当前 agentrun server 实现可用，提取收益有限，不应急于迁移。

---

## 验收测试

实现前应明确以下测试用例：

| 测试 | 验收条件 |
|------|---------|
| 全量 replay | `fromSeq=0` 时 `seq=0` 的事件被收到，不跳过 |
| 无事件丢失 | 慢 TUI consumer 下 4117 个 replay 事件全部到达，seq 连续 |
| 游标续接 | 断线重连后从 `lastCursor+1` 续接，无重复，无跳过 |
| per-watcher 有序 | 背压下同一 watcher 收到的事件 Seq 严格递增 |
| agentd recovery | 初始 `cursor=lastSeq`，只收到断线后的 live 事件 |

---

## 影响范围

| 阶段 | 文件 | 改动量 |
|------|------|--------|
| 1 | `pkg/agentrun/client/watcher.go` | ~5 行 |
| 1 | `pkg/tui/chat/chat.go` | ~20 行 |
| 2 | `pkg/watch/client.go`（新建） | ~150 行 |
| 2 | `pkg/agentrun/client/client.go` | ~20 行 |
| 2 | `pkg/tui/chat/chat.go` | ~50 行删除，~30 行新增 |
| 2 | `pkg/agentd/recovery.go` | ~30 行 |
| 3 | `pkg/watch/server.go`（新建） | ~100 行 |
| 3 | `pkg/agentrun/server/`（部分重构） | ~50 行 |

阶段 1 可独立上线，解决当前紧急问题；阶段 2/3 分 PR 进行。
