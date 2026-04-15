# 统一 massctl shim RPC client

## 背景

`cmd/massctl/subcommands/shim/command.go` 实现了一个完整的私有 JSON-RPC client（约 150 行），包含：

| 私有类型/方法 | 行数 | 功能 |
|-------------|------|------|
| `rpcRequest` | 24-29 | JSON-RPC 2.0 请求 |
| `rpcResponse` | 31-38 | JSON-RPC 2.0 响应（含 Method/Params 用于通知） |
| `rpcError` | 40-43 | JSON-RPC 2.0 错误 |
| `client` struct | 47-58 | 连接 + ndjson.Reader + pending map + notifs channel |
| `dial()` | 60-74 | 连接 Unix socket |
| `readLoop()` | 76-112 | 读循环，分发 response/notification |
| `call()` | 114-142 | 阻塞 RPC 调用 |
| `send()` | 145-152 | fire-and-forget RPC（不等响应） |
| `close()` | 154 | 关闭连接 |

同时，项目已有两层复用的 client：

1. **`pkg/jsonrpc.Client`** — 基于 `sourcegraph/jsonrpc2` 的通用 JSON-RPC 2.0 client
   - `Call(ctx, method, params, result)` — 阻塞 RPC，结果自动解码到 `result`
   - `Notify(ctx, method, params)` — 发送 JSON-RPC notification（无 id 字段）
   - `WithNotificationHandler(h)` — 注册 callback，通过内部 worker goroutine 串行回调
   - `Close()` / `DisconnectNotify()`

2. **`pkg/shim/api.ShimClient`** — shim 协议的类型化包装
   - `Prompt(ctx, params)` / `Cancel(ctx)` / `Subscribe(ctx, params)` / `Status(ctx)` / `History(ctx, params)` / `Stop(ctx)`
   - `RawClient()` — 暴露底层 `*jsonrpc.Client`

此外 `pkg/ari/client.Client` 也有一套重复的 `rpcRequest`/`rpcResponse`/`rpcError`，同样的问题。

### 为什么私有 client 存在

私有 client 的存在不是无意义的——它解决了 `pkg/jsonrpc.Client` 当前不具备的三个能力：

1. **Channel-based 通知投递**：BubbleTea TUI 需要从 channel 读取通知（`<-c.notifs`），用于 `tea.Cmd` 调度。`pkg/jsonrpc.Client` 使用 callback（`NotificationHandler`），没有 channel 投递选项。

2. **Fire-and-forget RPC**：`session/prompt` 阻塞到 turn 结束。chat TUI 用 `c.send()` 发送 prompt 后立即返回，不注册 pending handler。`pkg/jsonrpc.Client` 的 `Call()` 阻塞等待响应，`Notify()` 是 JSON-RPC notification（无 id 字段），不是 fire-and-forget RPC call。

3. **NDJSON 容错**：私有 client 用 `ndjson.Reader`（行边界切分），遇到非 JSON 行 skip + log，不中断连接。`sourcegraph/jsonrpc2` 内部用 `json.Decoder`，遇到非 JSON 数据后流位置损坏，无法恢复。

## 目标

1. `cmd/massctl/subcommands/shim/` 的 RPC 通信改用 `pkg/shim/api.ShimClient`
2. 删除私有 `rpcRequest`、`rpcResponse`、`rpcError` 类型和 `client` struct 及其所有方法
3. 同时统一 `pkg/ari/client.Client`，改用 `pkg/jsonrpc.Client`
4. 解决 channel 投递、fire-and-forget、NDJSON 容错三个 gap

---

## 改动 1：`pkg/jsonrpc.Client` 增加 `WithNotificationChannel` 选项

### 问题

BubbleTea 的 `tea.Cmd` 模型需要从 channel 读取通知来驱动 Update 循环。当前 `pkg/jsonrpc.Client` 只支持 callback 模式。

### 方案

新增 `ClientOption`，注册一个 `chan<- NotificationMsg` 作为通知投递目标。与 callback 互斥——同时设置两者在构造时 panic。

```go
// NotificationMsg is a channel-friendly notification envelope.
type NotificationMsg struct {
    Method string
    Params json.RawMessage
}

// WithNotificationChannel registers a channel for inbound notifications.
// The channel is written to by the notification worker goroutine.
// Mutually exclusive with WithNotificationHandler — setting both panics.
func WithNotificationChannel(ch chan<- NotificationMsg) ClientOption {
    return func(c *Client) {
        c.notifCh = ch  // reuse existing field, just expose to caller
    }
}
```

实现：在 `notifWorker()` 中，如果 `notifCh` 来自外部注入，则直接写入该 channel 而非调用 callback。

```go
func (c *Client) notifWorker() {
    defer close(c.workerDone)
    for msg := range c.internalNotifCh {
        if c.externalNotifCh != nil {
            c.externalNotifCh <- NotificationMsg{
                Method: msg.method,
                Params: msg.params,
            }
        } else if c.notifHandler != nil {
            c.notifHandler(msg.ctx, msg.method, msg.params)
        }
    }
}
```

**修改字段**：

```go
type Client struct {
    conn            *jsonrpc2.Conn
    notifHandler    NotificationHandler
    externalNotifCh chan<- NotificationMsg  // NEW: caller-provided channel
    internalNotifCh chan notificationMsg    // renamed from notifCh
    workerDone      chan struct{}
}
```

channel 满时的行为：与当前 callback 模式一致——阻塞（backpressure）。由调用方保证 channel 容量足够大或及时消费。

### 不做的事

- 不做 drop-on-full 语义——当前私有 client 在 channel 满时 log warning 并继续 `c.notifs <- msg`（也是阻塞），所以行为等价。

---

## 改动 2：`pkg/shim/api.ShimClient` 增加 `SendPrompt` 方法

### 问题

`session/prompt` RPC 阻塞到 turn 结束。chat TUI 需要发送 prompt 后立即返回，通过通知流监听进度和 turn_end。

当前 `ShimClient.Prompt()` 使用 `c.Call()`，阻塞到结果返回。

### 方案

新增 `SendPrompt` 方法，使用底层 `jsonrpc2.Conn.Call` 的异步模式。

```go
// SendPrompt sends a session/prompt request without waiting for the response.
// The caller should monitor the notification stream for turn progress and
// turn_end events. Use this for interactive/TUI scenarios where blocking
// until turn completion is not desired.
func (c *ShimClient) SendPrompt(ctx context.Context, req *SessionPromptParams) error {
    // Use Notify to send a request-like message without waiting for response.
    // NOTE: jsonrpc2 Notify sends without ID field, which is JSON-RPC notification
    // semantics. The shim server must accept prompts sent as notifications.
    return c.c.Notify(ctx, MethodSessionPrompt, req)
}
```

**问题**：JSON-RPC notification 没有 `id` 字段，服务端目前可能要求 `id` 才能处理 prompt。

**备选方案**（如果服务端不接受 notification）：在 `pkg/jsonrpc.Client` 新增 `CallAsync` 方法：

```go
// CallAsync sends a JSON-RPC request with an ID but does not wait for the response.
// The response (if any) is silently discarded when it arrives.
// Use this for long-running RPC methods where the caller monitors progress
// through notifications instead of waiting for the response.
func (c *Client) CallAsync(ctx context.Context, method string, params any) error {
    // sourcegraph/jsonrpc2 doesn't expose fire-and-forget with ID.
    // Implementation: start c.conn.Call in a goroutine, discard result.
    go func() {
        _ = c.conn.Call(ctx, method, params, nil)
    }()
    return nil
}
```

然后 `ShimClient.SendPrompt` 使用 `c.c.CallAsync()`。

**推荐**：先检查 shim server 是否接受 JSON-RPC notification 格式的 prompt。如果接受，用 `Notify` 更简洁；如果不接受，用 `CallAsync`。

---

## 改动 3：NDJSON 容错评估

### 分析

`sourcegraph/jsonrpc2` 使用 `jsonrpc2.PlainObjectStream`，底层是 `json.Decoder` + `json.Encoder`。`json.Decoder` 在遇到非 JSON 输入后流位置不确定，无法可靠恢复。

私有 client 使用 `ndjson.Reader`（行边界切分），非 JSON 行返回 `ErrInvalidJSON`，调用方可 skip + continue。

### 实际风险评估

shim server 的输出是标准 JSON-RPC 2.0 消息，每条消息一行。非 JSON 行的来源：
- agent 进程的 stderr 泄露到 stdout（极罕见——shim 已做 fd 分离）
- 编码 bug 产出半条 JSON（极罕见——`json.Encoder.Encode` 是原子输出）

**结论**：NDJSON 容错在实际运行中几乎不会触发。`sourcegraph/jsonrpc2` 的 `json.Decoder` 对正常 JSON-RPC 流完全可靠。保留 NDJSON 容错只是防御性设计。

### 方案

**不改 `sourcegraph/jsonrpc2` 的流处理**。迁移到 `pkg/jsonrpc.Client` 后，NDJSON 容错能力丢失，但实际风险极低。如果未来确需容错，可以在 `pkg/jsonrpc` 中引入 NDJSON 兼容的 `ObjectStream` 实现，替换 `PlainObjectStream`——但不在本次改动范围内。

---

## 改动 4：迁移 `cmd/massctl/subcommands/shim/` 使用 `ShimClient`

### 4.1 迁移 `command.go` 的简单 RPC 命令

`state`、`history`、`stop` 命令是简单的 request-response 模式，直接用 `ShimClient`。

**Before**:
```go
cmd.AddCommand(&cobra.Command{
    Use: "state",
    RunE: func(cmd *cobra.Command, _ []string) error {
        c, err := dial(socket)
        if err != nil { return err }
        defer c.close()
        result, err := c.call(shimapi.MethodRuntimeStatus, nil)
        // ...
    },
})
```

**After**:
```go
cmd.AddCommand(&cobra.Command{
    Use: "state",
    RunE: func(cmd *cobra.Command, _ []string) error {
        sc, err := dialShim(cmd.Context(), socket)
        if err != nil { return err }
        defer sc.Close()
        result, err := sc.Status(cmd.Context())
        // ...
        enc := json.NewEncoder(os.Stdout)
        enc.SetIndent("", "  ")
        return enc.Encode(result)
    },
})
```

辅助函数：
```go
func dialShim(ctx context.Context, socketPath string, opts ...jsonrpc.ClientOption) (*shimapi.ShimClient, error) {
    c, err := jsonrpc.Dial(ctx, "unix", socketPath, opts...)
    if err != nil {
        return nil, fmt.Errorf("connect %s: %w", socketPath, err)
    }
    return shimapi.NewShimClient(c), nil
}
```

### 4.2 迁移 `command.go` 的 prompt 命令

`runPrompt` 需要 subscribe + send prompt + 等待通知。

**After**:
```go
func runPrompt(sock, text string) error {
    notifs := make(chan jsonrpc.NotificationMsg, 1024)
    ctx := context.Background()

    sc, err := dialShim(ctx, sock, jsonrpc.WithNotificationChannel(notifs))
    if err != nil { return err }
    defer sc.Close()

    if _, err := sc.Subscribe(ctx, nil); err != nil {
        return fmt.Errorf("session/subscribe: %w", err)
    }

    ctx, cancel := context.WithCancel(ctx)
    defer cancel()
    turnEnd := startNotificationPrinterV2(ctx, notifs)
    drainTurnEnd(turnEnd)

    if err := sc.SendPrompt(ctx, &shimapi.SessionPromptParams{Prompt: text}); err != nil {
        return fmt.Errorf("session/prompt: %w", err)
    }
    <-turnEnd
    return nil
}
```

`startNotificationPrinterV2` 从 `chan jsonrpc.NotificationMsg` 消费，替代原来从 `c.notifs` 消费：

```go
func startNotificationPrinterV2(ctx context.Context, notifs <-chan jsonrpc.NotificationMsg) <-chan struct{} {
    turnEnd := make(chan struct{}, 16)
    go func() {
        defer func() {
            if r := recover(); r != nil {
                fmt.Fprintf(os.Stderr, "\n[notification printer] PANIC: %v\n%s\n", r, debug.Stack())
            }
        }()
        for {
            select {
            case <-ctx.Done():
                return
            case msg, ok := <-notifs:
                if !ok { return }
                ev := parseNotification(msg)
                if ev == nil { continue }
                printNotification(*ev)
                if ev.Type == shimapi.EventTypeTurnEnd {
                    turnEnd <- struct{}{}
                }
            }
        }
    }()
    return turnEnd
}
```

### 4.3 迁移 `chat.go`

chat TUI 的核心变化：`*client` → `*shimapi.ShimClient` + `<-chan jsonrpc.NotificationMsg`。

```go
type chatModel struct {
    // ...
    sock   string
    client *shimapi.ShimClient
    notifs <-chan jsonrpc.NotificationMsg  // 从 rpcResponse 改为 NotificationMsg
    // ...
}
```

`connectCmd` 迁移：
```go
func connectCmd(sock string) tea.Cmd {
    return safeCmd(func() tea.Msg {
        notifCh := make(chan jsonrpc.NotificationMsg, 1024)
        sc, err := dialShim(context.Background(), sock, jsonrpc.WithNotificationChannel(notifCh))
        if err != nil {
            return connErrMsg{fmt.Errorf("connect: %w", err)}
        }
        if _, err := sc.Subscribe(context.Background(), nil); err != nil {
            sc.Close()
            return connErrMsg{fmt.Errorf("session/subscribe: %w", err)}
        }
        return connReadyMsg{c: sc, notifs: notifCh}
    })
}
```

`waitNotif` 从 `chan rpcResponse` 改为 `chan jsonrpc.NotificationMsg`：
```go
func waitNotif(ch <-chan jsonrpc.NotificationMsg) tea.Cmd {
    return safeCmd(func() tea.Msg {
        msg, ok := <-ch
        if !ok { return connClosedMsg{} }
        if msg.Method != shimapi.MethodShimEvent {
            return nil
        }
        // 和现有逻辑相同，只是 msg.Params 已经是 json.RawMessage
        var peek struct{ Type string `json:"type"` }
        _ = json.Unmarshal(msg.Params, &peek)
        if peek.Type == shimapi.EventTypeTurnEnd {
            return turnEndMsg{}
        }
        var ev shimapi.ShimEvent
        if err := json.Unmarshal(msg.Params, &ev); err != nil {
            return nil
        }
        if sc, ok := ev.Content.(shimapi.StateChangeEvent); ok {
            return stateChangeMsg{previous: sc.PreviousStatus, status: sc.Status, reason: sc.Reason}
        }
        return notifMsg{ev: ev}
    })
}
```

`sendPromptCmd` / `cancelPromptCmd` / `fetchStatusCmd` 迁移：
```go
func sendPromptCmd(c *shimapi.ShimClient, text string) tea.Cmd {
    return safeCmd(func() tea.Msg {
        if err := c.SendPrompt(context.Background(), &shimapi.SessionPromptParams{Prompt: text}); err != nil {
            return promptErrMsg{err}
        }
        return nil
    })
}

func cancelPromptCmd(c *shimapi.ShimClient) tea.Cmd {
    return safeCmd(func() tea.Msg {
        _ = c.Cancel(context.Background())
        return nil
    })
}

func fetchStatusCmd(c *shimapi.ShimClient) tea.Cmd {
    return safeCmd(func() tea.Msg {
        result, err := c.Status(context.Background())
        if err != nil { return nil }
        return stateChangeMsg{status: string(result.State.Status)}
    })
}
```

### 4.4 消息类型更新

```go
type connReadyMsg struct {
    c      *shimapi.ShimClient
    notifs <-chan jsonrpc.NotificationMsg
}
```

`connClosedMsg` 的触发方式变化：原来依赖 `close(c.notifs)` 在 readLoop 退出时触发。迁移后，`jsonrpc2.Conn` 关闭时会关闭 `pkg/jsonrpc.Client` 内部的 notification channel，但不会关闭外部注入的 channel。

**解决方案**：监听 `DisconnectNotify()`：

```go
func watchDisconnect(sc *shimapi.ShimClient, notifCh chan jsonrpc.NotificationMsg) tea.Cmd {
    return safeCmd(func() tea.Msg {
        <-sc.DisconnectNotify()
        close(notifCh) // close externally so waitNotif returns connClosedMsg
        return nil      // the close will trigger connClosedMsg via waitNotif
    })
}
```

在 `connReadyMsg` 处理中启动：
```go
case connReadyMsg:
    m.client = msg.c
    m.notifs = msg.notifs
    cmds = append(cmds, waitNotif(m.notifs), fetchStatusCmd(m.client), watchDisconnect(msg.c, msg.notifsCh))
```

### 4.5 打印函数更新

`printNotification` 签名从 `func printNotification(msg rpcResponse)` 改为接受 `shimapi.ShimEvent`：

```go
func printNotification(ev shimapi.ShimEvent) {
    if sc, ok := ev.Content.(shimapi.StateChangeEvent); ok {
        fmt.Fprintf(os.Stderr, "\033[2m[stateChange seq=%d] %s → %s pid=%d reason=%q\033[0m\n",
            ev.Seq, sc.PreviousStatus, sc.Status, sc.PID, sc.Reason)
        return
    }
    printShimEvent(ev)
}
```

`parseNotification` 辅助函数：
```go
func parseNotification(msg jsonrpc.NotificationMsg) *shimapi.ShimEvent {
    if msg.Method != shimapi.MethodShimEvent {
        return nil
    }
    var ev shimapi.ShimEvent
    if err := json.Unmarshal(msg.Params, &ev); err != nil {
        return nil
    }
    return &ev
}
```

---

## 改动 5：迁移 `pkg/ari/client.Client`

### 问题

`pkg/ari/client.Client` 也有重复的 `rpcRequest`/`rpcResponse`/`rpcError` 类型，手动实现 `Call()`。它是单次 request-response 模式，不需要通知。

### 方案

用 `pkg/jsonrpc.Client` 替换。

```go
package client

import (
    "context"
    "fmt"

    "github.com/zoumo/mass/pkg/jsonrpc"
)

// Client is a typed JSON-RPC client for ARI socket communication.
type Client struct {
    c *jsonrpc.Client
}

// NewClient connects to the ARI server over a Unix domain socket.
func NewClient(socketPath string) (*Client, error) {
    c, err := jsonrpc.Dial(context.Background(), "unix", socketPath)
    if err != nil {
        return nil, fmt.Errorf("connect %s: %w", socketPath, err)
    }
    return &Client{c: c}, nil
}

// Call sends a JSON-RPC request and unmarshals the response.
func (c *Client) Call(method string, params, result any) error {
    return c.c.Call(context.Background(), method, params, result)
}

// Close closes the connection.
func (c *Client) Close() error {
    return c.c.Close()
}
```

删除 `rpcRequest`、`rpcResponse`、`rpcError` 类型。

### 兼容性

`Client.Call(method, params, result)` 签名不变（除了 context 由内部提供）。如果调用方需要 context 支持，可以改为 `CallContext(ctx, method, params, result)`，但当前 `massctl` 的 ARI 调用都是短生命周期，context.Background() 足够。

---

## 删除清单

| 文件 | 删除内容 |
|------|---------|
| `cmd/massctl/subcommands/shim/command.go` | `rpcRequest`, `rpcResponse`, `rpcError` 类型；`client` struct 及其 `dial()`, `readLoop()`, `call()`, `send()`, `close()` 方法 |
| `pkg/ari/client/simple.go` | `rpcRequest`, `rpcResponse`, `rpcError` 类型 |

### 验证

```bash
# 确认无残留的私有 RPC 类型
rg "type rpcRequest struct|type rpcResponse struct|type rpcError struct" \
  --glob '!docs/plan/*' --glob '!.gsd/*'

# 确认 pkg/ndjson 仅被需要它的包引用（不再被 massctl 引用）
rg "ndjson" --glob '!docs/*' --glob '!.gsd/*' --glob '!pkg/ndjson/*'
```

---

## 实现顺序

1. **`pkg/jsonrpc.Client` 增加 `WithNotificationChannel`**（改动 1）— 新增 `NotificationMsg` 类型 + channel option + notifWorker 分支
2. **`pkg/shim/api.ShimClient` 增加 `SendPrompt`**（改动 2）— 确定 Notify vs CallAsync 方案
3. **迁移 `cmd/massctl/subcommands/shim/`**（改动 4）— command.go + chat.go 全面迁移到 ShimClient
4. **迁移 `pkg/ari/client.Client`**（改动 5）— 简单替换
5. **删除残留代码 + 验证**（删除清单）

## 涉及文件

| 文件 | 改动 |
|------|------|
| `pkg/jsonrpc/client.go` | 新增 `NotificationMsg` 类型、`WithNotificationChannel` option、`externalNotifCh` 字段、`notifWorker` 分支逻辑 |
| `pkg/shim/api/client.go` | 新增 `SendPrompt` 方法 |
| `cmd/massctl/subcommands/shim/command.go` | 删除私有 client 全部代码；新增 `dialShim` 辅助函数；`state`/`history`/`stop`/`prompt` 命令迁移到 `ShimClient`；`printNotification`/`isTurnEndNotification`/`printShimEvent` 签名更新 |
| `cmd/massctl/subcommands/shim/chat.go` | `chatModel` 字段类型更新；`connectCmd`/`waitNotif`/`sendPromptCmd`/`cancelPromptCmd`/`fetchStatusCmd` 重写；新增 `watchDisconnect`；消息类型 `connReadyMsg` 更新 |
| `pkg/ari/client/simple.go` | 用 `pkg/jsonrpc.Client` 重写，删除私有 RPC 类型 |

## 测试与验收标准

| 测试 | 覆盖内容 |
|------|---------|
| `pkg/jsonrpc/client_test.go` | `WithNotificationChannel` 投递正确性；与 `WithNotificationHandler` 互斥 panic；channel backpressure 行为 |
| `pkg/shim/api/client_test.go` | `SendPrompt` 发送正确的 method 和 params |
| `cmd/massctl/subcommands/shim/` | 现有测试（如有）通过 |
| `pkg/ari/client/` | 现有测试通过 |
| 编译 | `make build` 通过 |
| rg 验证 | 无残留私有 RPC 类型 |

### 验收标准

1. `make build` 通过
2. `go test ./pkg/jsonrpc/... ./pkg/shim/api/... ./pkg/ari/client/... ./cmd/massctl/...` 全部通过
3. `rg "type rpcRequest struct|type rpcResponse struct|type rpcError struct" --glob '!docs/plan/*' --glob '!.gsd/*'` 无输出
4. massctl shim 的 `state`、`history`、`stop`、`prompt`、`chat` 子命令功能正常
5. `pkg/ndjson` 不再被 `cmd/massctl` 直接引用

## 风险与决策点

### RISK-1：`session/prompt` 的 fire-and-forget 实现方式

shim server 是否接受 JSON-RPC notification（无 id）格式的 prompt 请求？

- **如果接受**：用 `jsonrpc.Client.Notify()` 最简洁
- **如果不接受**：需要在 `pkg/jsonrpc.Client` 新增 `CallAsync()` 方法（goroutine 中调 `Call` 并丢弃结果）

实现前需检查 `pkg/shim/server/service.go` 中 prompt handler 的注册方式。

### RISK-2：连接断开通知

原私有 client 在 `readLoop` 退出时 `close(c.notifs)`，`waitNotif` 从 closed channel 读到 zero value 返回 `connClosedMsg`。

迁移后需要用 `DisconnectNotify()` + 手动 close 外部 channel 来实现等价行为。需确保：
- close 只执行一次（防止 double close panic）
- `watchDisconnect` goroutine 不泄露

### RISK-3：`sourcegraph/jsonrpc2` 的 response ID 与 call ID 匹配

原私有 client 用 `int` 作为 ID，`sourcegraph/jsonrpc2` 内部用 `jsonrpc2.ID`（可以是 string 或 int）。shim server 发送的响应 ID 类型需要与 `jsonrpc2` 预期一致。当前 server 端也使用 `sourcegraph/jsonrpc2`，所以 ID 格式天然兼容，无需额外处理。
