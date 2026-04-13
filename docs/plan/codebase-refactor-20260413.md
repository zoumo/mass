# 代码库重构计划：Service Interface + 统一 RPC + 目录整理

> Date: 2026-04-13
> Status: **FINAL** (approved with RISK constraints, codex round-3)

## Context

当前代码库存在三个核心问题，加上本次讨论中发现的四个具体设计缺陷：

### 原始问题

1. **代码复用率低** — JSON-RPC client/server 逻辑在多处重复实现
2. **代码抽象能力差** — Shim RPC 和 ARI 都基于 jsonrpc2，但没有 Service Interface
3. **目录结构混乱** — API 类型散落、包职责不清、命名模糊

### 讨论中发现的设计缺陷

4. **Server 绑死 socket** — `NewServer(socketPath)` 硬编码 Unix socket，未来无法扩展到 WebSocket/TCP 等
5. **Client 没有对称抽象** — Server 端有 Service Interface，Client 端却没有。正确分层应该是 `pkg/ari/{server,client}` + `pkg/shim/{server,client}`
6. **`api/meta` 与 `api/ari` 类型分散** — `api/meta` 定义领域对象 (Agent/AgentRun/Workspace)，`api/ari` 定义 wire DTO (AgentInfo/AgentRunInfo/WorkspaceInfo)，转换函数散落在 `pkg/ari/server.go`。应将领域类型集中到 `api/ari/domain.go`，转换函数集中到 `api/ari/adapter.go`
7. **`api/spec` 命名模糊** — 是 runtime spec? workspace spec? 实际内容是 Runtime Config (config.json) + Runtime State (state.json)，应该叫 `api/runtime`

### 参考框架

| 层次 | ttrpc (containerd) | ACP SDK |
|------|-------|---------|
| Service Interface | `type TaskService interface { Create(ctx, *Req) (*Resp, error) }` | `type Agent interface { Prompt(ctx, Req) (Resp, error) }` |
| Server Registration | `RegisterTaskService(server, impl)` | `NewAgentSideConnection(agent, io)` |
| Typed Client | `type taskClient struct { client *ttrpc.Client }` + generated callers | generated typed callers |
| Transport | `server.Serve(ctx, listener)` — 不绑定具体传输 | `NewConnection(handler, writer, reader)` — IO 抽象 |
| Interceptor | `UnaryServerInterceptor` chain | Extension methods |

---

## 现状分析

### 重复的 Server 实现

两套独立实现，重复 socket listen/accept、jsonrpc2 conn 管理、switch/case 分发、unmarshalParams()、replyOK/replyErr：

| 位置 | 行数 | 职责 |
|------|------|------|
| `pkg/rpc/server.go` | ~300 | Shim RPC (session/*, runtime/*) |
| `pkg/ari/server.go` | ~1260 | ARI (workspace/*, agentrun/*, agent/*) |

### 三套不一致的 Client 实现

| 位置 | 方式 | 问题 |
|------|------|------|
| `pkg/agentd/shim_client.go` | jsonrpc2 库 | 无 Service Interface |
| `pkg/ari/client.go` | 手写 JSON-RPC | 自建 rpcRequest/rpcResponse，不用 jsonrpc2 |
| `cmd/.../workspacemcp/command.go` | ad-hoc callARI() | 重复定义 types + 临时 client |

### 两套平行的 ARI 类型 + 转换层

```
api/meta/types.go:
  Agent { Metadata{Name,Labels,CreatedAt,UpdatedAt}, Spec{Command,Args,Env,...} }
  AgentRun { Metadata{Name,Workspace,...}, Spec{Agent,RestartPolicy,...}, Status{State,Error,...} }
  Workspace { Metadata{...}, Spec{Source,Hooks}, Status{Phase,Path} }

api/ari/types.go:
  AgentInfo { Name, Command, Args, Env, ..., CreatedAt, UpdatedAt }        ← meta.Agent 的扁平版
  AgentRunInfo { Workspace, Name, Agent, State, ErrorMessage, Labels, ... } ← meta.AgentRun 的扁平版
  WorkspaceInfo { Name, Phase, Path }                                       ← meta.Workspace 的扁平版

pkg/ari/server.go:
  func agentRunToInfo(ag *meta.AgentRun) apiari.AgentRunInfo { ... }  ← 手写转换
  func agentToInfo(ag *meta.Agent) apiari.AgentInfo { ... }           ← 手写转换
```

**问题**：领域类型和 wire DTO 分散在不同包，转换函数散落在 server handler 中，应集中管理。

### `api/spec` 命名模糊

```
api/spec/
├── types.go  → Config, AcpAgent, AcpProcess, McpServer, ... (runtime bundle config)
└── state.go  → State, LastTurn (runtime state.json)
```

这是 **OAR Runtime Specification** 的类型，叫 `spec` 让人以为是某种通用的 spec 定义。

---

## 重构方案

### Phase 1: Transport-agnostic JSON-RPC 框架 (`pkg/jsonrpc/`)

**设计原则**：Server 不关心传输层，只接收 `net.Listener`；Client 只接收 `net.Conn` 或 `io.ReadWriteCloser`。

#### Server — 参考 ttrpc.Server

```go
package jsonrpc

// ─── Service Description (参考 ttrpc ServiceDesc) ───

// Method is a handler for a single RPC method.
// unmarshal 回调让 handler 延迟反序列化，框架不需要知道具体类型。
type Method func(ctx context.Context, unmarshal func(any) error) (any, error)

// ServiceDesc describes a set of RPC methods.
type ServiceDesc struct {
    Methods map[string]Method
}

// ─── Server (transport-agnostic) ───

type Server struct {
    services map[string]*ServiceDesc
    logger   *slog.Logger
    ...
}

func NewServer(logger *slog.Logger, opts ...ServerOption) *Server

// RegisterService registers methods (参考 ttrpc.RegisterService).
func (s *Server) RegisterService(name string, desc *ServiceDesc)

// Serve accepts connections from ANY net.Listener.
// 不绑定 Unix socket — 调用者决定传输方式:
//   net.Listen("unix", path)     → Unix socket
//   net.Listen("tcp", ":8080")   → TCP
//   websocket upgrade listener   → WebSocket
func (s *Server) Serve(ln net.Listener) error

func (s *Server) Shutdown(ctx context.Context) error

// ─── Interceptor (参考 ttrpc UnaryServerInterceptor) ───

type UnaryServerInfo struct {
    FullMethod string
}

type Interceptor func(
    ctx context.Context,
    unmarshal func(any) error,
    info *UnaryServerInfo,
    method Method,
) (any, error)

func WithInterceptor(i Interceptor) ServerOption
```

#### Client — 基于 sourcegraph/jsonrpc2 封装

**设计决策**：不手写协议核心，封装现有 `sourcegraph/jsonrpc2`（已是项目依赖）。jsonrpc2 已提供：读循环、request id 生成、pending response map、write serialization、notification 分发。我们在其上增加类型安全和 API 统一。

```go
// NewClient wraps an existing connection (transport-agnostic).
// 内部使用 sourcegraph/jsonrpc2.NewConn() 管理协议。
func NewClient(conn io.ReadWriteCloser, opts ...ClientOption) *Client

// Dial is a convenience for common case: dial + NewClient.
func Dial(ctx context.Context, network, address string, opts ...DialOption) (*Client, error)

func (c *Client) Call(ctx context.Context, method string, params, result any) error
func (c *Client) Notify(ctx context.Context, method string, params any) error
func (c *Client) Close() error
func (c *Client) DisconnectNotify() <-chan struct{}

// NotificationHandler handles inbound server-side notifications.
type NotificationHandler func(ctx context.Context, method string, params json.RawMessage)

func WithNotificationHandler(h NotificationHandler) ClientOption
```

**并发模型约束**：

由 jsonrpc2 v0.2.1 保证：
- 单读循环统一解码所有 inbound messages（response + notification）
- Response 按 id 投递到 pending map，支持并发 Call
- 所有 writes 通过 jsonrpc2.Conn 串行化
- Close 关闭连接并唤醒所有 pending calls（`close()` 遍历 pending map）

Context cancel 行为（jsonrpc2 真实语义）：
- `Call(ctx)` 在 context cancel 时立即返回 `ctx.Err()`
- 底层 jsonrpc2 pending entry **不会**被 cancel 移除（Waiter.Wait 只 select ctx.Done，不操作 pending map）
- pending entry 在后续 response 到达或 connection Close 时清理
- 这不影响正确性：cancel 后调用方已返回，迟到的 response 被静默消费

Notification 分发策略（封装层实现）：
- jsonrpc2 read loop 对 inbound notification 会同步调用 `Handler.Handle`，慢 handler 会阻塞 response 读取
- 封装层使用 **per-client 无界 FIFO channel + worker goroutine** 解决此问题：
  - jsonrpc2 Handler 收到 notification 时只投递到 `chan notificationMsg`，立即返回（不阻塞 read loop）
  - 独立 worker goroutine 串行消费 channel，调用用户注册的 `NotificationHandler`
  - 保证 notification 按接收顺序交付（FIFO），同时不阻塞 response 分发
- 队列设计：无界 buffered channel（初始 buffer 256）；无背压/丢弃策略——notification 产生速率由 server 控制，client 无法施压
- Close 行为：关闭 channel，worker drain 剩余消息后退出

**关键**：`Server.Serve(ln)` 和 `NewClient(conn)` 让传输层完全由调用者控制。

#### 错误类型

```go
// pkg/jsonrpc/errors.go

// RPCError is a JSON-RPC 2.0 error with code, message, and optional data.
// Method handlers return *RPCError to control the JSON-RPC error response;
// plain Go errors are mapped to InternalError (-32603).
type RPCError struct {
    Code    int64  `json:"code"`
    Message string `json:"message"`
    Data    any    `json:"data,omitempty"`
}

func (e *RPCError) Error() string

// Predefined error constructors for standard JSON-RPC codes:
func ErrMethodNotFound(method string) *RPCError            // -32601
func ErrInvalidParams(msg string) *RPCError                // -32602
func ErrInternal(msg string) *RPCError                     // -32603

// Domain error codes (application-defined):
// Handlers return &RPCError{Code: -32001, Message: "..."} for domain-specific errors.
// 例如 ARI 的 CodeRecoveryBlocked = -32001。

// 框架映射策略：
// 1. handler 返回 *RPCError → 直接作为 JSON-RPC error response
// 2. handler 返回其他 error → 包装为 InternalError (-32603)
// 3. unmarshal 回调失败 → InvalidParams (-32602)
// 4. method 不存在 → MethodNotFound (-32601)
```

#### Peer 抽象（解决 subscribe streaming）

```go
// pkg/jsonrpc/peer.go

// Peer represents the remote side of a JSON-RPC connection.
// Injected into handler context by the framework for each request.
// Enables server-initiated notifications (e.g., shim/event streaming).
type Peer struct { ... }

// PeerFromContext extracts the Peer from a handler's context.
func PeerFromContext(ctx context.Context) *Peer

// Notify sends a notification to the remote peer.
// Writes are serialized with response writes on the same connection.
func (p *Peer) Notify(ctx context.Context, method string, params any) error

// DisconnectNotify returns a channel closed when the peer disconnects.
func (p *Peer) DisconnectNotify() <-chan struct{}
```

#### 涉及文件

| 操作 | 文件 |
|------|------|
| 新建 | `pkg/jsonrpc/server.go` |
| 新建 | `pkg/jsonrpc/client.go` — 封装 sourcegraph/jsonrpc2 |
| 新建 | `pkg/jsonrpc/errors.go` — RPCError + 标准错误码 + 映射策略 |
| 新建 | `pkg/jsonrpc/peer.go` — Peer 抽象 (Notify, DisconnectNotify) |
| 新建 | `pkg/jsonrpc/notification.go` — FIFO notification worker (channel + goroutine) |
| 新建 | `pkg/jsonrpc/server_test.go` |
| 新建 | `pkg/jsonrpc/client_test.go` |

---

### Phase 2: 统一 API 类型（拆分为安全子阶段）

> **设计决策**：保留 ARI 扁平 DTO（AgentInfo/AgentRunInfo/WorkspaceInfo）作为 wire format，
> 不改变 JSON wire shape，与 `docs/design/agentd/ari-spec.md` 保持一致。
> 转换函数从 server handler 中集中到 `api/ari/` 包内，命名为 view adapter。

#### 2a. 纯 rename/move（不改变任何 wire format）

**验收标准**：所有 JSON output 与重构前完全一致。纯 import 路径变更。

- `api/spec/` → `api/runtime/`

```
api/runtime/
├── config.go   # Config, AcpAgent, AcpProcess, McpServer (原 api/spec/types.go)
└── state.go    # State, LastTurn (原 api/spec/state.go)
```

Package doc: `Package runtime defines the OAR Runtime Specification types: config.json (bundle configuration) and state.json (runtime state).`

- `pkg/shimapi/` → `api/shim/`

```
api/shim/
└── types.go    # SessionPromptParams, RuntimeStatusResult, etc. (原 pkg/shimapi/types.go)
```

#### 2b. 领域类型迁移（保持 wire format 不变）

**验收标准**：wire JSON 不变。ARI RPC result 仍返回 Info 扁平类型。

将 `api/meta/` 的领域类型移入 `api/ari/`，**但保留** Info 类型作为 wire DTO：

Before:
```
api/meta/types.go  → Agent{Metadata,Spec}  AgentRun{Metadata,Spec,Status}  Workspace{Metadata,Spec,Status}
api/ari/types.go   → AgentInfo{flat}        AgentRunInfo{flat}              WorkspaceInfo{flat}
                      + 每个 RPC 方法的 Params/Result
```

After:
```
api/ari/
├── types.go        → AgentInfo, AgentRunInfo, WorkspaceInfo (wire DTO，不变)
│                      + 每个 RPC 方法的 Params/Result (不变)
├── domain.go       → Agent{Metadata,Spec}, AgentRun{Metadata,Spec,Status}, Workspace{Metadata,Spec,Status}
│                      ObjectMeta, AgentRunFilter, WorkspaceFilter
│                      (从 api/meta 移入，store 和 agentd 内部使用)
└── adapter.go      → AgentToInfo(*Agent) AgentInfo
                       AgentRunToInfo(*AgentRun) AgentRunInfo
                       WorkspaceToInfo(*Workspace) WorkspaceInfo
                       (转换函数从 pkg/ari/server.go 集中至此)
```

**关键**：
- ARI RPC result 类型 (AgentRunListResult, AgentGetResult 等) **不变**，仍然引用 Info 类型
- 转换函数从 `pkg/ari/server.go` 移到 `api/ari/adapter.go`，成为 exported 函数，使用重构后的术语（domain → wire DTO，无 `meta` 概念）
- `WorkspaceToInfo` 补全——当前 `workspace/list` 和 `workspace/status` 都返回 `WorkspaceInfo`，`registry` fast path 直接构造 `WorkspaceInfo` 的场景也应统一走 adapter
- 如果 registry 缓存中只有部分字段（name, phase, path），`WorkspaceToInfo` 接受完整 `*Workspace`；registry 层需要先构造完整 domain 对象再调用 adapter，避免转换散落回 handler
- `api/meta/` 包删除，所有 import 改为 `api/ari`

#### 涉及文件

| 操作 | 文件 | 子阶段 |
|------|------|--------|
| 重命名 | `api/spec/` → `api/runtime/` | 2a |
| 移动 | `pkg/shimapi/types.go` → `api/shim/types.go` | 2a |
| 删除 | `pkg/shimapi/` 目录 | 2a |
| 更新 | ~17 files import `api/spec` → `api/runtime` | 2a |
| 更新 | ~6 files import `pkg/shimapi` → `api/shim` | 2a |
| 移动 | `api/meta/types.go` 类型 → `api/ari/domain.go` | 2b |
| 新建 | `api/ari/adapter.go` — AgentToInfo, AgentRunToInfo, WorkspaceToInfo | 2b |
| 删除 | `api/meta/` 目录 | 2b |
| 更新 | ~20 files import `api/meta` → `api/ari` | 2b |
| 更新 | `pkg/ari/server.go` 调用 `ari.AgentToInfo()` 替代本地函数 | 2b |

---

### Phase 3: 定义 Service Interface + 注册函数

#### ARI Service Interfaces — `api/ari/service.go`

```go
package ari

// WorkspaceService defines workspace management RPC methods.
type WorkspaceService interface {
    Create(ctx context.Context, req *WorkspaceCreateParams) (*WorkspaceCreateResult, error)
    Status(ctx context.Context, req *WorkspaceStatusParams) (*WorkspaceStatusResult, error)
    List(ctx context.Context) (*WorkspaceListResult, error)
    Delete(ctx context.Context, req *WorkspaceDeleteParams) error
    Send(ctx context.Context, req *WorkspaceSendParams) (*WorkspaceSendResult, error)
}

// AgentRunService defines agent run lifecycle RPC methods.
type AgentRunService interface {
    Create(ctx context.Context, req *AgentRunCreateParams) (*AgentRunCreateResult, error)
    Prompt(ctx context.Context, req *AgentRunPromptParams) (*AgentRunPromptResult, error)
    Cancel(ctx context.Context, req *AgentRunCancelParams) error
    Stop(ctx context.Context, req *AgentRunStopParams) error
    Delete(ctx context.Context, req *AgentRunDeleteParams) error
    Restart(ctx context.Context, req *AgentRunRestartParams) (*AgentRunRestartResult, error)
    List(ctx context.Context, req *AgentRunListParams) (*AgentRunListResult, error)
    Status(ctx context.Context, req *AgentRunStatusParams) (*AgentRunStatusResult, error)
    Attach(ctx context.Context, req *AgentRunAttachParams) (*AgentRunAttachResult, error)
}

// AgentService defines agent definition CRUD methods.
// All return types use wire DTO (Info types), not domain types.
type AgentService interface {
    Set(ctx context.Context, req *AgentSetParams) (*AgentInfo, error)
    Get(ctx context.Context, req *AgentGetParams) (*AgentGetResult, error)
    List(ctx context.Context) (*AgentListResult, error)
    Delete(ctx context.Context, req *AgentDeleteParams) error
}

// Register functions (参考 ttrpc RegisterXxxService pattern)
func RegisterWorkspaceService(s *jsonrpc.Server, svc WorkspaceService)
func RegisterAgentRunService(s *jsonrpc.Server, svc AgentRunService)
func RegisterAgentService(s *jsonrpc.Server, svc AgentService)
```

#### ARI Typed Client — `api/ari/client.go`

```go
package ari

// WorkspaceClient is a typed client for WorkspaceService.
// 参考 ttrpc generated client pattern.
type WorkspaceClient struct {
    c *jsonrpc.Client
}

func NewWorkspaceClient(c *jsonrpc.Client) *WorkspaceClient

func (c *WorkspaceClient) Create(ctx context.Context, req *WorkspaceCreateParams) (*WorkspaceCreateResult, error) {
    var resp WorkspaceCreateResult
    if err := c.c.Call(ctx, api.MethodWorkspaceCreate, req, &resp); err != nil {
        return nil, err
    }
    return &resp, nil
}
// ... 其他方法

// AgentRunClient, AgentClient 同理
type AgentRunClient struct { c *jsonrpc.Client }
type AgentClient struct { c *jsonrpc.Client }
```

#### Shim Service Interface — `api/shim/service.go`

```go
package shim

type ShimService interface {
    Prompt(ctx context.Context, req *SessionPromptParams) (*SessionPromptResult, error)
    Cancel(ctx context.Context) error
    Load(ctx context.Context, req *SessionLoadParams) error
    // Subscribe 使用 Peer 抽象处理 streaming notification。
    // handler 通过 jsonrpc.PeerFromContext(ctx) 获取 Peer，
    // 用 Peer.Notify() 发送 shim/event，用 Peer.DisconnectNotify() 监听断开。
    // 返回的 SessionSubscribeResult 作为初始 reply (含 nextSeq + backfill entries)，
    // 之后 handler 保持 goroutine 通过 Peer 持续推送事件直到 disconnect。
    Subscribe(ctx context.Context, req *SessionSubscribeParams) (*SessionSubscribeResult, error)
    Status(ctx context.Context) (*RuntimeStatusResult, error)
    History(ctx context.Context, req *RuntimeHistoryParams) (*RuntimeHistoryResult, error)
    Stop(ctx context.Context) error
}

func RegisterShimService(s *jsonrpc.Server, svc ShimService)
```

**Subscribe 实现约束**（迁移自现有 `pkg/rpc/server.go` 逻辑）：
1. **原子 backfill**：`SubscribeFromSeq(fromSeq)` 读历史 + 注册 live subscription 在同一锁内完成
2. **Legacy afterSeq**：Subscribe 注册后过滤 seq <= afterSeq 的事件
3. **事件推送**：通过 `peer.Notify(ctx, "shim/event", shimEvent)` 发送，写入与 response 串行化
4. **Disconnect unsubscribe**：`<-peer.DisconnectNotify()` 触发取消订阅 + goroutine 退出
5. **慢连接**：Peer.Notify 返回 error 时 unsubscribe 并退出（不阻塞其他连接）

#### Shim Typed Client — `api/shim/client.go`

```go
package shim

type ShimClient struct {
    c *jsonrpc.Client
}

func NewShimClient(c *jsonrpc.Client) *ShimClient

func (c *ShimClient) Prompt(ctx context.Context, req *SessionPromptParams) (*SessionPromptResult, error) { ... }
func (c *ShimClient) Cancel(ctx context.Context) error { ... }
func (c *ShimClient) Subscribe(ctx context.Context, req *SessionSubscribeParams) (*SessionSubscribeResult, error) { ... }
// ... 其他方法
```

#### 涉及文件

| 操作 | 文件 |
|------|------|
| 新建 | `api/ari/service.go` — ARI service interfaces + Register |
| 新建 | `api/ari/client.go` — typed ARI clients |
| 新建 | `api/shim/service.go` — ShimService interface + Register |
| 新建 | `api/shim/client.go` — typed ShimClient |

---

### Phase 4: 实现 — `pkg/shim/{server,client}` + `pkg/ari/{server,client}`

对称分层：

```
pkg/shim/
├── server/
│   └── service.go    # implements api/shim.ShimService
└── client/
    └── client.go     # 构造 api/shim.ShimClient 的 helper (Dial, notification wiring)

pkg/ari/
├── server/
│   ├── workspace.go  # implements api/ari.WorkspaceService
│   ├── agentrun.go   # implements api/ari.AgentRunService
│   └── agent.go      # implements api/ari.AgentService
├── client/
│   └── client.go     # 构造 api/ari clients 的 helper
└── registry.go       # Workspace 元数据缓存 (从原 pkg/ari/registry.go 保留)
```

#### Shim Server 实现

```go
// pkg/shim/server/service.go
package server

// Service implements shim.ShimService.
type Service struct {
    mgr     *runtime.Manager
    trans   *events.Translator
    logPath string
    logger  *slog.Logger
}

func (s *Service) Prompt(ctx context.Context, req *shim.SessionPromptParams) (*shim.SessionPromptResult, error) {
    // 纯业务逻辑，无 RPC boilerplate
    s.trans.NotifyTurnStart()
    s.trans.NotifyUserPrompt(req.Prompt)
    resp, err := s.mgr.Prompt(ctx, []acp.ContentBlock{acp.TextBlock(req.Prompt)})
    ...
    return &shim.SessionPromptResult{StopReason: string(resp.StopReason)}, nil
}
```

cmd 入口的组装变为：

```go
// cmd/agentd/subcommands/shim/command.go
svc := &shimserver.Service{mgr: mgr, trans: trans, ...}
srv := jsonrpc.NewServer(logger)
shim.RegisterShimService(srv, svc)

ln, _ := net.Listen("unix", socketPath)
srv.Serve(ln)
```

#### ARI Server 实现

```go
// pkg/ari/server/workspace.go
package server

type WorkspaceServiceImpl struct { ... }

func (s *WorkspaceServiceImpl) Create(ctx context.Context, req *ari.WorkspaceCreateParams) (*ari.WorkspaceCreateResult, error) {
    // 业务逻辑使用 domain 类型 (ari.Workspace)，
    // handler 边界通过 ari.WorkspaceToInfo() 输出 wire DTO
    ...
}
```

cmd 入口：
```go
// cmd/agentd/subcommands/server/command.go
srv := jsonrpc.NewServer(logger, jsonrpc.WithInterceptor(loggingInterceptor))
ari.RegisterWorkspaceService(srv, workspaceSvc)
ari.RegisterAgentRunService(srv, agentRunSvc)
ari.RegisterAgentService(srv, agentSvc)

ln, _ := net.Listen("unix", socketPath)
srv.Serve(ln)
```

#### Shim Client

```go
// pkg/shim/client/client.go
package client

// Dial connects to a shim and returns a typed ShimClient.
func Dial(ctx context.Context, socketPath string, opts ...jsonrpc.DialOption) (*shim.ShimClient, error) {
    c, err := jsonrpc.Dial(ctx, "unix", socketPath, opts...)
    if err != nil {
        return nil, err
    }
    return shim.NewShimClient(c), nil
}
```

#### ARI Client

```go
// pkg/ari/client/client.go
package client

// Dial connects to ARI server and returns typed clients.
func Dial(ctx context.Context, socketPath string) (*Clients, error) {
    c, err := jsonrpc.Dial(ctx, "unix", socketPath)
    ...
    return &Clients{
        Workspace: ari.NewWorkspaceClient(c),
        AgentRun:  ari.NewAgentRunClient(c),
        Agent:     ari.NewAgentClient(c),
        raw:       c,
    }, nil
}
```

#### 涉及文件

| 操作 | 文件 |
|------|------|
| 新建 | `pkg/shim/server/service.go` — ShimService 实现 |
| 新建 | `pkg/shim/client/client.go` — Dial helper |
| 新建 | `pkg/ari/server/workspace.go` — WorkspaceService 实现 |
| 新建 | `pkg/ari/server/agentrun.go` — AgentRunService 实现 |
| 新建 | `pkg/ari/server/agent.go` — AgentService 实现 |
| 新建 | `pkg/ari/client/client.go` — Dial helper |
| 删除 | `pkg/rpc/` (被 pkg/shim/server 替代) |
| 删除 | `pkg/ari/server.go` (被 pkg/ari/server/ 替代) |
| 删除 | `pkg/ari/client.go` (被 pkg/ari/client/ 替代) |
| 删除 | `pkg/agentd/shim_client.go` (被 pkg/shim/client 替代) |
| 更新 | `cmd/agentd/subcommands/shim/command.go` |
| 更新 | `cmd/agentd/subcommands/server/command.go` |
| 更新 | `pkg/agentd/process.go` — 使用 shim/client |
| 更新 | `cmd/agentdctl/subcommands/` — 使用 ari/client |

---

### Phase 5: 清理

- 删除 `cmd/agentd/subcommands/workspacemcp/command.go` 中的重复类型 + ad-hoc client
- 删除不再需要的包 (`pkg/rpc/`, `pkg/shimapi/`, `api/meta/`)
- 更新所有 import

---

## 最终目录结构

```
api/                                  # 协议定义层
├── ari/                              # ARI 协议
│   ├── types.go                      # Wire DTO (AgentInfo, AgentRunInfo, WorkspaceInfo) + RPC Params/Results
│   ├── domain.go                     # Domain/store 类型 (Agent, AgentRun, Workspace, ObjectMeta, Filter)
│   ├── adapter.go                    # DTO adapter (AgentToInfo, AgentRunToInfo, WorkspaceToInfo)
│   ├── service.go                    # Service Interfaces + RegisterXxxService
│   └── client.go                     # Typed Client wrappers
├── shim/                             # Shim RPC 协议
│   ├── types.go                      # Shim RPC Params/Results
│   ├── service.go                    # ShimService Interface + RegisterShimService
│   └── client.go                     # Typed ShimClient wrapper
├── runtime/                          # OAR Runtime Spec (原 api/spec)
│   ├── config.go                     # Config, AcpAgent, AcpProcess, etc.
│   └── state.go                      # State, LastTurn
├── methods.go                        # RPC method 常量
├── events.go                         # Event type 常量
└── types.go                          # Status, EnvVar

pkg/
├── jsonrpc/                          # JSON-RPC 框架 (transport-agnostic)
│   ├── server.go                     # Server + ServiceDesc + Interceptor
│   ├── client.go                     # Client (封装 jsonrpc2, FIFO notification worker)
│   ├── errors.go                     # RPCError + 标准错误码
│   └── peer.go                       # Peer 抽象 (Notify, DisconnectNotify)
├── ari/                              # ARI 实现层
│   ├── server/                       # Service 实现
│   │   ├── workspace.go              # implements ari.WorkspaceService
│   │   ├── agentrun.go               # implements ari.AgentRunService
│   │   └── agent.go                  # implements ari.AgentService
│   ├── client/                       # Client helper (Dial)
│   │   └── client.go
│   └── registry.go                   # Workspace 元数据缓存
├── shim/                             # Shim 实现层
│   ├── server/                       # Service 实现
│   │   └── service.go                # implements shim.ShimService
│   └── client/                       # Client helper (Dial)
│       └── client.go
├── agentd/                           # 守护进程管理 (精简)
│   ├── agent.go                      # AgentRunManager
│   ├── process.go                    # ProcessManager
│   ├── recovery.go
│   └── ...                           # shim_client.go 删除
├── events/                           # 事件系统 (不变)
├── runtime/                          # ACP 进程管理 (不变)
├── store/                            # 持久化 (不变)
└── workspace/                        # 工作空间管理 (不变)
```

---

## 对比总结

| 维度 | Before | After |
|------|--------|-------|
| Transport | `NewServer(socketPath)` 绑死 socket | `Server.Serve(net.Listener)` — 传输无关 |
| Server dispatch | 两处 switch/case (300+1260 行) | 框架 dispatch + RegisterXxxService |
| Client | 3 套不同实现，无抽象 | typed client: `ari.{Workspace,AgentRun,Agent}Client` + `shim.ShimClient` |
| 对称性 | Server 有结构, Client 是 ad-hoc | `pkg/{ari,shim}/{server,client}` 完全对称 |
| ARI 类型 | 领域/DTO 分散 + 转换散落 | domain.go + wire DTO + adapter.go 集中管理 |
| api/spec | 命名模糊 | `api/runtime/` — 明确是 Runtime Spec |
| api/shimapi | 在 pkg/ 下，与 api/ari 不对称 | `api/shim/` — 与 api/ari 对称 |
| Handler 签名 | `func(ctx, *Conn, *Request)` | `func(ctx, *Req) (*Resp, error)` |
| 参数解析 | 每个 handler 手写 unmarshalParams | 框架 unmarshal callback |
| 回复 | 手动 Reply/ReplyWithError | 框架根据 return value 处理 |
| Interceptor | 无 | 支持 logging/metrics 中间件 |

## 执行顺序

```
Phase 1: pkg/jsonrpc/ 框架 (Server + Client + RPCError + Peer)
   ↓
Phase 2a: 纯 rename/move (api/spec → api/runtime, pkg/shimapi → api/shim)
   ↓
Phase 2b: 领域类型迁移 (api/meta → api/ari, 保留 Info DTO, 集中 adapter)
   ↓
Phase 3: Service Interface + Register + typed Clients (api/ari/*.go, api/shim/*.go)
   ↓
Phase 4: 实现迁移 (pkg/{ari,shim}/{server,client})
   ↓
Phase 5: 清理 (删除旧包, 更新 imports)
```

每个 Phase/子阶段独立可提交，每步之后 `make build` + `go test ./...` 应通过。

## 测试计划

### Phase 1: `pkg/jsonrpc/` 协议测试

| 测试 | 描述 |
|------|------|
| `TestServer_Dispatch` | 注册 ServiceDesc，验证 method 路由正确 |
| `TestServer_MethodNotFound` | 未注册 method 返回 -32601 |
| `TestServer_InvalidParams` | unmarshal 失败返回 -32602 |
| `TestServer_RPCError` | handler 返回 *RPCError 保留 code/message/data |
| `TestServer_PlainError` | handler 返回 plain error 映射为 -32603 |
| `TestServer_Interceptor` | 拦截器链正确执行 |
| `TestServer_PeerNotify` | handler 通过 PeerFromContext 发送 notification |
| `TestServer_PeerDisconnect` | client 断开后 DisconnectNotify channel 关闭 |
| `TestClient_Call` | 基本 Call round-trip |
| `TestClient_ConcurrentCall` | 并发 Call 正确 demux response |
| `TestClient_CallWithNotification` | Call 期间收到 notification 不阻塞 response（FIFO worker 解耦） |
| `TestClient_NotificationHandler` | inbound notification 正确分发到 handler |
| `TestClient_NotificationOrder` | notification 按接收顺序串行交付（FIFO 保序） |
| `TestClient_SlowNotificationHandler` | 慢 notification handler 不阻塞 Call response |
| `TestClient_ContextCancel` | context cancel 后 Call 立即返回 ctx.Err()，后续 response 不破坏连接 |
| `TestClient_Close` | Close 唤醒所有 pending calls，notification worker 退出 |
| `TestClient_ResponseOutOfOrder` | response 乱序仍能正确匹配 |

### Phase 2a: rename/move 验证

| 测试 | 描述 |
|------|------|
| 编译通过 | `make build` — 纯 import 路径变更 |
| 全量测试 | `go test ./...` — 无行为变更 |

### Phase 2b: 领域类型迁移验证

| 测试 | 描述 |
|------|------|
| `TestAgentToInfo` | adapter 函数正确转换 Agent → AgentInfo |
| `TestAgentRunToInfo` | adapter 函数正确转换 AgentRun → AgentRunInfo |
| `TestWorkspaceToInfo` | adapter 函数正确转换 Workspace → WorkspaceInfo |
| ARI JSON shape | 确保 ARI result 的 JSON marshal 与迁移前一致（golden test 或手动验证） |
| 全量测试 | `go test ./...` — wire format 不变 |

### Phase 3: Service Interface round-trip 测试

| 测试 | 描述 |
|------|------|
| `TestRegisterWorkspaceService` | Register + typed client 完整 round-trip |
| `TestRegisterAgentRunService` | AgentRun CRUD round-trip |
| `TestRegisterAgentService` | Agent CRUD round-trip |
| `TestRegisterShimService` | Shim prompt/status round-trip |
| `TestShimSubscribe_Backfill` | Subscribe 原子 backfill 行为 |
| `TestShimSubscribe_Disconnect` | 断开连接时 unsubscribe |
| Error code 测试 | Register 对 InvalidParams/domain error 正确映射 |

### Phase 4: 行为迁移测试

| 测试 | 描述 |
|------|------|
| 迁移 `pkg/rpc/server_test.go` | subscribe backfill、notification 保序、legacy afterSeq |
| 迁移 `pkg/ari/server_test.go` | ARI handler 行为、error code、method rejection |
| 迁移 `pkg/agentd/shim_client_test.go` | shim client notification filtering |
| 迁移 `pkg/agentd/shim_boundary_test.go` | shim 边界条件测试 |
| ARI error code golden | `-32001`, `-32602`, `-32603` 等返回值验证 |

### Phase 5: 清理验证

| 测试 | 描述 |
|------|------|
| 无残留 import | 确认无文件 import 已删除的包 |
| 全量测试 | `make build` + `go test ./...` |

---

## 审查记录

### codex 第1轮

#### ✅ 认可项

- 方向上认可抽出 `pkg/jsonrpc/`：当前 `pkg/rpc/server.go`、`pkg/ari/server.go`、`pkg/agentd/shim_client.go` 确实重复了 listen/accept、dispatch、params unmarshal、reply/error、client call 等机制性代码，抽出 transport-agnostic JSON-RPC 层可以降低重复和后续协议实现成本。
- 认可 server/client 对称分层：`pkg/{ari,shim}/{server,client}` 比当前 `pkg/ari/server.go`、`pkg/ari/client.go`、`pkg/agentd/shim_client.go` 的职责边界更清晰，也更符合 Service Interface + typed client 的目标。
- 认可 `api/spec` 重命名为 `api/runtime`、`pkg/shimapi` 移到 `api/shim`：这两个调整与 `docs/design/runtime/*` 的概念边界更一致，能减少包名歧义。
- 认可每个 Phase 后要求 `make build` + `go test ./...` 通过：这对大规模移动包和拆分实现是最低必要验证。

#### ❌ 问题项

1. **Phase 2 会直接改变 ARI wire contract，且与 `docs/design/agentd/ari-spec.md` 不一致。**
   - 问题是什么：方案把 `AgentInfo`/`AgentRunInfo`/`WorkspaceInfo` 替换为 `Agent`/`AgentRun`/`Workspace` 的 `metadata/spec/status` 嵌套结构，并让 RPC result 直接返回这些领域对象。
   - 为什么是问题：当前设计文档明确规定 ARI 返回扁平形状，例如 `agent/set` 返回 `AgentInfo`，`workspace/status.members` 和 `agentrun/list` 使用 `AgentRunInfo`，`agentrun/status` 示例也是 `{agentRun: {workspace,name,agent,state,...}}`。AGENTS.md 要求代码变更必须对齐 `docs/design`；“现在无需兼容”不能绕过设计契约不一致。更重要的是，`api/meta` 的 package 注释已经说它是 store 与 ARI 共享的 wire-format 类型，方案中“`api/meta` 是 agentd 内部对象”的判断与当前代码事实不完全一致。
   - 期望如何解决：必须二选一并写清楚。方案 A：保留 ARI 外部扁平 DTO，内部 store/domain 类型可迁到 `api/ari` 或新包，但 Register/Service 仍按 `ari-spec.md` 输出 Info 形状，转换函数可以集中命名为 marshal/view adapter 而不是散落在 server。方案 B：明确这是 ARI vNext wire breaking change，同时先修改 `docs/design/agentd/ari-spec.md` 中所有 result schema、示例、`AgentRunInfo Schema`、`AgentInfo`、`WorkspaceInfo` 描述，再执行代码迁移。当前方案不能在不更新设计文档的情况下通过。

2. **`pkg/jsonrpc.Client` 的并发模型和响应分发设计不足，不能替代现有 shim client。**
   - 问题是什么：方案只定义 `Call/Notify/DisconnectNotify/WithNotificationHandler`，没有说明 request id 生成、并发 Call 的 response demux、读循环、写锁、context timeout/cancel、Close 与 pending calls 的错误传播、notification 与 response 交错时的处理。
   - 为什么是问题：现有 `pkg/ari/client.go` 是单连接阻塞读写，不能并发；现有 shim client 依赖 `jsonrpc2.Conn` 支持同一连接上请求响应和 inbound `shim/event` notification。新 client 如果只是简单 encoder/decoder，会在并发调用或 notification 插入响应流时错读，造成死锁、response ID mismatch 或事件丢失。
   - 期望如何解决：Phase 1 需要补充明确实现约束：一个读循环统一解码所有 inbound messages；按 id 将 response 投递到 pending map；notification 进入 handler 队列；所有 writes 串行化；context 取消要移除 pending 并返回；Close 要关闭 pending；测试覆盖并发 Call、response out-of-order、Call 期间收到 notification、handler 慢时是否保序/背压、Close 唤醒 pending。也可以明确继续封装 `sourcegraph/jsonrpc2`，避免手写协议核心。

3. **错误语义没有设计到可迁移程度。**
   - 问题是什么：`Method func(...) (any, error)` 只返回 Go error，`errors.go` 未定义如何携带 JSON-RPC code、message、data，也未说明 unmarshal/validation/domain error 到 `-32602`、`-32601`、`-32603`、`api/ari.CodeRecoveryBlocked(-32001)` 的映射。
   - 为什么是问题：现有 ARI 和 shim handler 明确区分 MethodNotFound、InvalidParams、InternalError、RecoveryBlocked。若框架把所有业务错误都映射为 internal error，会破坏 `docs/design/agentd/ari-spec.md` 的 Error Codes，也会改变 agentdctl/workspacemcp 的控制流判断。
   - 期望如何解决：在 Phase 1 增加 `type RPCError struct { Code int64; Message string; Data any }` 或等价机制，并规定 `InvalidParams`、`MethodNotFound`、domain blocked error 的映射策略。Phase 3 的 Register 函数必须对参数解析错误、必填字段校验、业务错误进行一致转换，并添加错误码测试。

4. **`session/subscribe` 的 streaming/notification 语义没有被 Service Interface 正确表达。**
   - 问题是什么：方案把 `Subscribe(ctx, req) (*SessionSubscribeResult, error)` 当作普通 unary 方法，并用 `ConnFromContext` 暗示 handler 可以拿底层连接发送通知。
   - 为什么是问题：现有 shim subscribe 需要在同一连接上 reply 后持续发送 `shim/event`，并在 disconnect 时 unsubscribe；`fromSeq` 路径要求“读历史 + 注册 live subscription”原子化；通知写入必须和普通 replies 共用同一个串行写路径。仅把 raw conn 放到 context 会把框架抽象泄漏给业务层，且没有定义连接断开、取消订阅、通知发送失败、慢连接背压的行为。
   - 期望如何解决：为 streaming 场景设计显式抽象，例如 `Notifier`/`Peer` 放入 context，至少包含 `Notify(ctx, method, params) error` 和 `DisconnectNotify() <-chan struct{}`，并保证写入串行化。`api/shim.RegisterShimService` 或 `pkg/shim/server.Service.Subscribe` 的计划必须写出如何保持 `SubscribeFromSeq` 的原子 backfill、`afterSeq` 过滤、disconnect unsubscribe，并为这些行为迁移现有测试。

5. **Phase 顺序声称每步独立可提交，但 Phase 2 很可能无法独立保持行为和测试。**
   - 问题是什么：Phase 2 先移动/合并 API 类型并删除转换层，Phase 3/4 才引入 service/client 和实现迁移。
   - 为什么是问题：如果 Phase 2 同时改变 `api/ari` result 类型，必须修改 `pkg/ari/server.go`、`cmd/agentdctl`、`workspacemcp`、store tests、ARI spec tests 等大量调用点；若又删除 `agentRunToInfo()`/`agentToInfo()`，现有 server handler 的返回 shape 会立刻改变。这个 Phase 不是单纯 import rename，风险比方案描述高。
   - 期望如何解决：拆小 Phase 2：先做纯 rename/move 且保持 wire JSON 不变；再在独立 Phase 做 ARI type consolidation，并明确是否更新设计文档和所有 result schema。每个子 Phase 需要列出“保持 JSON golden output 不变”或“设计文档同步变更”的验收标准。

6. **测试计划过粗，缺少协议级回归验收。**
   - 问题是什么：方案只写每步 `make build` + `go test ./...`，没有列出要迁移/新增的协议行为测试。
   - 为什么是问题：这次重构最容易破坏的是 JSON-RPC 边界行为，而不是编译。现有测试覆盖 subscribe backfill、notification 保序、legacy method rejection、invalid params、shim client notification filtering 等关键语义；方案没有承诺这些测试迁移到新包，容易出现“测试删了但行为丢了”。
   - 期望如何解决：在每个 Phase 增加具体测试清单。最低要求：`pkg/jsonrpc` 协议测试；`api/ari`/`api/shim` Register/typed client round-trip 测试；shim subscribe/history/recovery 行为测试迁移；ARI error code 和 result JSON shape golden 测试；workspacemcp 使用 typed ARI client 的集成或 handler-level 测试。

### claude-code 第2轮

逐条回应 codex 第1轮的6个问题项：

#### 问题1：Phase 2 ARI wire contract — ✅ 接受

**回应**：选择方案 A — 保留 ARI 扁平 DTO。

codex 说得对：`docs/design/agentd/ari-spec.md` 明确规定 wire format 使用 AgentInfo/AgentRunInfo/WorkspaceInfo 扁平结构，AGENTS.md 要求代码对齐设计文档。我之前把 `api/meta` 判断为"内部对象"确实与 package 注释不完全一致。

**修改**：
- 保留 Info 扁平类型作为 ARI wire DTO，不改变任何 JSON shape
- 将 `api/meta/` 的领域类型移入 `api/ari/domain.go`（store 和 agentd 内部使用）
- 转换函数从 `pkg/ari/server.go` 集中到 `api/ari/adapter.go`，命名为 `AgentToInfo()`, `AgentRunToInfo()`
- 不需要修改 `docs/design/agentd/ari-spec.md`

#### 问题2：Client 并发模型 — ✅ 接受

**回应**：封装 `sourcegraph/jsonrpc2` 而非手写协议核心。

codex 的担忧完全合理——手写 read loop + pending map + write serialization 容易出错，而 jsonrpc2 已经是项目依赖且提供了这些能力。

**修改**：
- Phase 1 Client 明确基于 sourcegraph/jsonrpc2 封装
- 补充了并发模型约束清单（read loop、pending map、write serialization、context cancel、Close semantics）
- 测试计划新增：并发 Call、response out-of-order、Call 期间 notification、Close 唤醒 pending

#### 问题3：错误语义 — ✅ 接受

**回应**：完全同意，缺少 RPCError 会破坏 ARI error code 语义。

**修改**：
- Phase 1 新增 `RPCError` 类型 + 标准错误码构造函数
- 明确框架映射策略：`*RPCError` 直接使用、plain error → -32603、unmarshal 失败 → -32602
- 测试计划覆盖各种 error code 场景

#### 问题4：Subscribe streaming 语义 — ✅ 接受

**回应**：同意 `ConnFromContext` 会泄漏框架抽象。

**修改**：
- 引入 `Peer` 抽象替代 raw conn，提供 `Notify()` 和 `DisconnectNotify()`
- ShimService.Subscribe 的文档注释说明了 Peer 使用方式
- 补充了5条具体的实现约束（原子 backfill、legacy afterSeq、事件推送、disconnect unsubscribe、慢连接处理）
- 测试计划覆盖 subscribe backfill 和 disconnect 行为

#### 问题5：Phase 2 拆分 — ✅ 接受

**回应**：codex 对风险的判断准确，Phase 2 如果同时做 rename + type change + 删转换层，确实不是"独立可提交"。

**修改**：
- Phase 2 拆为 2a（纯 rename/move）和 2b（领域类型迁移）
- 2a 验收标准：纯 import 路径变更，JSON output 完全一致
- 2b 验收标准：wire format 不变，保留 Info 类型，转换函数集中但功能不变
- 执行顺序更新为 6 步

#### 问题6：测试计划 — ✅ 接受

**回应**：codex 说得对，"编译通过"不等于"行为正确"，协议边界行为是最容易丢失的。

**修改**：
- 新增完整「测试计划」章节，按 Phase 列出具体测试清单
- Phase 1：15 个协议级测试
- Phase 2a/2b：golden test + 编译验证
- Phase 3：7 个 round-trip + error code 测试
- Phase 4：明确列出要迁移的现有测试文件
- Phase 5：残留 import 检查 + 全量测试

### codex 第2轮

#### ✅ 认可项

- 认可保留 `AgentInfo` / `AgentRunInfo` / `WorkspaceInfo` 作为 ARI wire DTO。这个选择与 `docs/design/agentd/ari-spec.md` 对齐，避免了在重构中隐式改变外部 JSON shape。
- 认可 Phase 2 拆成 2a rename/move 和 2b domain 类型迁移。拆分后的验收标准更清晰，可以做到每个子阶段独立构建和测试。
- 认可用 `Peer` 抽象替代 `ConnFromContext`。这比把底层 jsonrpc2 connection 泄漏给业务层更稳妥，也能显式表达 shim subscribe 的 notification 需求。
- 认可新增 `RPCError` 和协议级测试清单。错误码和 subscribe/recovery 行为是这次重构的关键回归面，必须被测试固定。

#### ❌ 问题项

1. **`sourcegraph/jsonrpc2` 的 context cancel 语义被方案写错了。**
   - 问题是什么：方案写“Context cancel 移除 pending 并返回 error”，并把它列为由 jsonrpc2 保证的并发模型约束。
   - 为什么是问题：当前依赖版本 `github.com/sourcegraph/jsonrpc2 v0.2.1` 的 `Waiter.Wait(ctx, result)` 在 `ctx.Done()` 时只返回 `ctx.Err()`，不会从 `Conn.pending` 中删除对应 call；pending entry 会等到 response 到达或 connection close 时才被移除。`pending` 是未导出字段，薄封装无法实现“取消即移除 pending”。如果按当前方案实现测试 `TestClient_ContextCancel`，要么测试会失败，要么需要手写/改造协议核心，与“封装 jsonrpc2 不手写核心”的决策冲突。
   - 期望如何解决：把 Phase 1 的 client 语义改成真实可实现的版本：`Call(ctx)` 在 context cancel 时返回错误，但底层 jsonrpc2 pending 会在后续 response 或 Close 时清理；不要承诺“移除 pending”。如果确实要求取消立即清理 pending，需要明确不再只是薄封装 jsonrpc2，并给出实现策略和额外测试。测试计划也要相应改名/改断言，例如验证 cancel 后调用方返回、后续 response 不破坏连接，而不是验证 pending 被移除。

2. **client notification handler 是否阻塞 response 分发仍未定义清楚。**
   - 问题是什么：方案写“Notification 进入 handler 回调（与 response 分发互不阻塞）”，但 `jsonrpc2.NewConn` 的 read loop 对 inbound request/notification 会同步调用 `Handler.Handle`；只有显式使用 `jsonrpc2.AsyncHandler` 或自建队列时，慢 handler 才不会阻塞后续 response 读取。
   - 为什么是问题：shim client 现有测试强调 notification handler 串行化以保持事件顺序；而 ARI/shim typed client 又需要 Call 期间收到 notification 不导致 response 卡死。这里存在真实取舍：同步 handler 保序但会阻塞 response；AsyncHandler 不阻塞但可能重排 notification；单独队列可以保序但需要定义队列容量、背压和丢弃策略。当前方案同时声称“互不阻塞”和“迁移 notification 保序测试”，缺少可执行设计。
   - 期望如何解决：明确 `pkg/jsonrpc.Client` 的 notification 分发策略。建议采用单独 FIFO worker：read loop/`jsonrpc2.Handler` 只把 notification 投递到 per-client 队列后立即返回，worker 串行调用用户 handler，从而保持 notification 顺序并减少对 response 分发的阻塞；同时定义队列无界/有界、满队列行为和 Close 时如何退出。若选择同步 handler，也要删除“与 response 分发互不阻塞”的承诺并接受慢 handler 会影响同连接 Call。

3. **`AgentService.Set` 返回类型仍会改变 `agent/set` wire shape。**
   - 问题是什么：Phase 3 的 `AgentService` 定义为 `Set(ctx, req *AgentSetParams) (*Agent, error)`，但 v4 又声明保留 Info DTO 作为 wire format。
   - 为什么是问题：当前 `pkg/ari/server.go` 的 `handleAgentSet` 返回的是扁平 `AgentInfo`，`docs/design/agentd/ari-spec.md` 也写 `agent/set` Result 是 `AgentInfo`。如果 Register 函数直接把 `*Agent` 作为 result，JSON 会变成 `metadata/spec` 嵌套对象，违反 v4 的核心修正。
   - 期望如何解决：把接口改为 `Set(ctx context.Context, req *AgentSetParams) (*AgentInfo, error)`，或新增 `AgentSetResult` 但必须同步设计文档；在当前选择方案 A 的前提下，推荐直接返回 `*AgentInfo`。同时检查示例中所有 “直接使用 `ari.Workspace`、无转换” 的描述，避免实现者误以为 RPC result 可以返回 domain 类型。

4. **文档仍残留 v3 的相反设计，会误导执行。**
   - 问题是什么：多处文字仍写“应该只有一套类型”、“ARI 类型 After 是一套 Agent/AgentRun/Workspace 直接用”、“最终目录结构 `api/ari/types.go` 放领域对象 + RPC Params/Results”、“`pkg/jsonrpc/context.go` — ConnFromContext”、“对比总结：一套 (Agent, AgentRun, Workspace 直接用)”、“ARI Server 实现直接使用 `ari.Workspace` 无转换”。
   - 为什么是问题：这些描述与 v4 的“保留 Info DTO + `domain.go` + `adapter.go` + Peer”设计相矛盾。执行者如果按这些段落实现，会再次引入第1轮已否决的 wire contract 变更，或实现已被废弃的 `ConnFromContext`。
   - 期望如何解决：全篇清理旧表述：最终目录改为 `types.go` = wire DTO/Params/Results，`domain.go` = domain/store 类型，`adapter.go` = DTO adapter；`pkg/jsonrpc` 最终结构改为 `peer.go` 而不是 `context.go`；对比总结改为“domain 类型集中到 `api/ari/domain.go`，wire DTO 保持扁平，adapter 集中”；实现示例不要写“无转换”，而应写“业务层使用 domain 类型，Register/handler 边界通过 adapter 输出 Info DTO”。

5. **adapter 命名和职责需要更精确，避免 domain 与 wire 再次混用。**
   - 问题是什么：方案写 `AgentToInfo(meta→flat)`、`AgentRunToInfo(meta→flat)`，但 `api/meta` 会被删除；同时没有提到 `WorkspaceToInfo`，而 `workspace/list` 也有 `WorkspaceInfo`。
   - 为什么是问题：重构后的包内已经没有 `meta` 概念，继续使用 `meta→flat` 会造成命名残留；漏掉 workspace adapter 会让部分转换继续散落在 server。
   - 期望如何解决：把 adapter 职责写完整并使用重构后的术语，例如 `AgentToInfo(*Agent) AgentInfo`、`AgentRunToInfo(*AgentRun) AgentRunInfo`、`WorkspaceToInfo(*Workspace) WorkspaceInfo`。如果 `workspace/status` 的 registry fast path 不是完整 domain `Workspace`，也要明确该路径如何构造 `WorkspaceInfo`，不要把转换重新散落回 handler。

### claude-code 第3轮

逐条回应 codex 第2轮的5个问题项：

#### 问题1：context cancel 语义 — ✅ 接受

**回应**：codex 完全正确。我验证了 jsonrpc2 v0.2.1 源码：`Waiter.Wait(ctx, result)` 在 `ctx.Done()` 时直接 `return ctx.Err()`，不操作 `Conn.pending` map（pending 是未导出字段）。

**修改**：
- 将并发模型约束拆为"jsonrpc2 保证"和"真实 cancel 行为"两部分
- 明确：Call(ctx) cancel 后立即返回 ctx.Err()，pending entry 在后续 response 或 Close 时清理
- 不再承诺"cancel 移除 pending"
- 测试 `TestClient_ContextCancel` 改为验证"cancel 后调用方返回 + 后续 response 不破坏连接"

#### 问题2：notification handler 阻塞问题 — ✅ 接受

**回应**：codex 对取舍分析准确。jsonrpc2 同步调用 Handler.Handle，AsyncHandler 用 goroutine 但会破坏顺序。

**修改**：
- 采用 codex 建议的 per-client FIFO worker 策略
- jsonrpc2 Handler 收到 notification 时只投递到 channel，立即返回（不阻塞 read loop）
- 独立 worker goroutine 串行消费 channel，保证 notification 顺序
- 队列设计：无界 buffered channel（初始 256），无背压——notification 产生速率由 server 控制
- Close 行为：关闭 channel，worker drain 后退出
- 新增测试：`TestClient_NotificationOrder`（保序）、`TestClient_SlowNotificationHandler`（不阻塞 Call）

#### 问题3：AgentService.Set 返回类型 — ✅ 接受

**回应**：确实是 v3 残留。既然选了方案 A 保留 Info DTO，接口必须一致。

**修改**：
- `AgentService.Set` 返回 `*AgentInfo` 而非 `*Agent`
- 添加注释说明所有 Service Interface 返回类型使用 wire DTO

#### 问题4：文档 v3 残留 — ✅ 接受

**回应**：全篇清理完成，逐项修改：

| 旧表述 | 修改 |
|--------|------|
| "应该只有一套类型" | → "领域类型集中到 domain.go，转换集中到 adapter.go" |
| 最终目录 types.go = "领域对象" | → types.go = wire DTO/Params/Results, domain.go = domain 类型, adapter.go = 转换 |
| `context.go` / `ConnFromContext` | → `peer.go` / `Peer` 抽象 |
| 对比总结 "一套直接用" | → "domain.go + wire DTO + adapter.go 集中管理" |
| ARI server 示例 "无转换" | → "业务层使用 domain 类型，handler 边界通过 adapter 输出 Info DTO" |

#### 问题5：adapter 命名和完整性 — ✅ 接受

**回应**：确实漏掉了 WorkspaceToInfo，且 `meta→flat` 是旧术语。

**修改**：
- adapter.go 补全三个函数：`AgentToInfo(*Agent) AgentInfo`、`AgentRunToInfo(*AgentRun) AgentRunInfo`、`WorkspaceToInfo(*Workspace) WorkspaceInfo`
- 去除所有 `meta→flat` 表述，使用 `domain → wire DTO`
- 明确 registry fast path 也应构造完整 domain Workspace 再走 adapter，不散落转换到 handler
- Phase 2b 测试计划新增 `TestWorkspaceToInfo`

### codex 第3轮

#### ✅ 认可项

- 认可 v5 对 `sourcegraph/jsonrpc2 v0.2.1` context cancel 语义的修正：`Call(ctx)` 在 cancel 后返回 `ctx.Err()`，pending 由后续 response 或 Close 清理。这个描述与当前依赖源码一致。
- 认可 per-client FIFO notification worker 的方向：它比同步 handler 更适合当前 shim client 的保序要求，也避免慢 notification handler 长时间阻塞 response 分发。
- 认可 `Peer`、`RPCError`、Phase 拆分、协议级测试清单这些机制性修正。除 ARI contract 方向外，v5 已经具备可执行基础。

#### ⚠️ RISK

1. **RISK：v5 仍以“保留 ARI 扁平 Info DTO / wire JSON 不变”为目标，但管理员已明确 ARI wire contract 改动是用户要求。**
   - 风险是什么：如果按 v5 执行，会得到一个技术上更整洁但仍保留旧 wire shape 的实现，偏离用户要求；同时 `docs/design/agentd/ari-spec.md` 仍会描述旧的 `AgentInfo` / `AgentRunInfo` / `WorkspaceInfo` 扁平契约。
   - 最稳妥处理意见：执行前将 Phase 2b 从“领域类型迁移且保持 wire format 不变”改为“ARI clean-break contract convergence”。同步修改 `docs/design/agentd/ari-spec.md`，把所有 `AgentInfo` / `AgentRunInfo` / `WorkspaceInfo` result schema 和示例替换为新的 `Agent` / `AgentRun` / `Workspace` domain shape（`metadata/spec/status`）。明确不兼容旧 JSON shape。代码层删除或降级 Info DTO，不再以 adapter 维持旧 wire DTO；typed client、service interface、golden tests 都以更新后的设计文档为准。

2. **RISK：若直接返回 domain 对象，必须重新定义 ARI result wrapper 的边界。**
   - 风险是什么：有些方法当前 result 是裸对象（`agent/set` 返回 `AgentInfo`），有些是 wrapper（`agent/get` 返回 `{agent: ...}`、`agentrun/status` 返回 `{agentRun, shimState}`、`workspace/list` 返回 `{workspaces: [...]}`）。若只是机械替换类型，容易出现不一致或文档示例遗漏。
   - 最稳妥处理意见：在设计文档中逐方法列明新 result：例如 `agent/set` 是返回裸 `Agent` 还是 `{agent: Agent}`，`workspace/status.members` 是 `[]AgentRun` 还是保留轻量 summary。建议保持现有 wrapper 结构不变，只替换内部实体类型：`agent/get -> {agent: Agent}`、`agent/list -> {agents: Agent[]}`、`agentrun/status -> {agentRun: AgentRun, shimState?}`、`agentrun/list -> {agentRuns: AgentRun[]}`、`workspace/list -> {workspaces: Workspace[]}`；`agent/set` 若要避免裸/wrapper 混杂，也可引入 `{agent: Agent}`，但必须同步文档。

3. **RISK：domain shape 会暴露当前 store/runtime 内部字段，需要显式确认哪些字段属于 public ARI。**
   - 风险是什么：`AgentRunStatus` 当前包含 `ShimSocketPath`、`ShimStateDir`、`ShimPID`、`BootstrapConfig` 等字段；直接作为 ARI wire shape 可能暴露内部路径、PID、bootstrap config。管理员已要求 wire contract 改动，但不等于所有内部字段都应公开。
   - 最稳妥处理意见：在 `api/ari/domain.go` 中把 public ARI domain shape 与 private store-only fields 分开，或为敏感字段加 `json:"-"` / 移到 internal status 结构。设计文档必须明确 `AgentRun.status`、`Workspace.status`、`Agent.spec` 暴露字段；`agentrun/attach` 仍是公开 shim socket path 的受控入口。

4. **RISK：notification queue 写成“无界 buffered channel”在 Go 中不是一个实际类型。**
   - 风险是什么：Go channel 容量固定，不能天然无界；如果实现者只建 `make(chan notificationMsg, 256)`，慢 handler 时第 257 条会阻塞 jsonrpc2 Handler，从而重新阻塞 read loop。
   - 最稳妥处理意见：实现时二选一并测试：要么明确使用固定 256 buffer 并接受满队列后阻塞（文档改成“bounded buffer with backpressure”）；要么实现真正的无界队列 goroutine（input channel 快速接收，内部 slice/list 缓冲，worker drain）。考虑 shim event 流可能持续产生，建议先采用 bounded buffer + 明确背压，避免无界内存增长；`TestClient_SlowNotificationHandler` 应覆盖 buffer 未满时不阻塞 response，另加满队列行为测试。

5. **RISK：Phase 2 的 golden test 目标需要随 contract 方向更新。**
   - 风险是什么：v5 测试计划仍写 “ARI JSON shape 与迁移前一致”。在管理员澄清后，这个断言会阻止正确的 breaking change。
   - 最稳妥处理意见：把 Phase 2/3 的 golden test 改为“与更新后的 `docs/design/agentd/ari-spec.md` 示例一致”，而不是“与迁移前一致”。Phase 2a 的纯 rename/move 仍可要求 JSON 不变；Phase 2b contract convergence 必须允许并验证新 JSON shape。

#### 结论

按协作协议第3轮强制收敛：批准进入执行，但以上 RISK 必须作为执行约束处理。最关键的收敛路径是：先同步更新 `docs/design/agentd/ari-spec.md`，明确新的 ARI domain wire shape 和公开字段边界，再按新设计文档修改 `api/ari`、service interface、typed client、server implementation 和 golden tests。

---

## 最终方案

> 整合 v5 基础方案 + codex 第3轮 RISK 约束。所有 Phase 内容以本章节为准。

### 总体设计决策

1. **ARI wire contract**: Clean-break convergence — 删除 Info DTO，直接使用 domain 类型 (Agent/AgentRun/Workspace) 作为 ARI wire format，采用 `metadata/spec/status` 嵌套结构。同步更新 `docs/design/agentd/ari-spec.md`。
2. **JSON-RPC 框架**: 封装 `sourcegraph/jsonrpc2 v0.2.1`，不手写协议核心。
3. **Notification 分发**: Bounded buffer (256) + FIFO worker goroutine + 明确背压。
4. **Streaming**: Peer 抽象 (Notify + DisconnectNotify)，替代 raw conn 暴露。
5. **错误语义**: RPCError 类型 + 标准映射策略。

---

### Phase 1: Transport-agnostic JSON-RPC 框架 (`pkg/jsonrpc/`)

**内容**：与 v5 一致（Server + Client + RPCError + Peer + FIFO notification worker）。

**Notification 队列修正**（RISK 4）：
- 使用 **bounded buffer channel `make(chan notificationMsg, 256)`**
- 满队列行为：**backpressure** — jsonrpc2 Handler 投递时阻塞（阻塞 read loop），直到 worker 消费腾出空间
- 这是合理的：如果 256 条 notification 积压说明 handler 严重落后，暂停读取是安全行为
- Close 行为：关闭 channel，worker drain 剩余消息后退出

**测试补充**（RISK 4）：
- `TestClient_NotificationBackpressure` — buffer 满时 handler 投递阻塞，worker 消费后恢复

**涉及文件**：

| 操作 | 文件 |
|------|------|
| 新建 | `pkg/jsonrpc/server.go` — Server + ServiceDesc + Interceptor |
| 新建 | `pkg/jsonrpc/client.go` — Client (封装 jsonrpc2, bounded FIFO notification worker) |
| 新建 | `pkg/jsonrpc/errors.go` — RPCError + 标准错误码 + 映射策略 |
| 新建 | `pkg/jsonrpc/peer.go` — Peer (Notify, DisconnectNotify) |
| 新建 | `pkg/jsonrpc/server_test.go` |
| 新建 | `pkg/jsonrpc/client_test.go` |

---

### Phase 2a: 纯 rename/move（不改变任何 wire format）

**内容**：与 v5 一致。

- `api/spec/` → `api/runtime/`
- `pkg/shimapi/` → `api/shim/`

**验收标准**：所有 JSON output 与重构前完全一致。纯 import 路径变更。

---

### Phase 2b: ARI clean-break contract convergence（RISK 1 + 2 + 3）

> 这是 v5→最终方案的最大变化。从"保留 Info DTO"改为"删除 Info DTO，使用 domain 类型作为 wire format"。

**步骤 1：更新设计文档 `docs/design/agentd/ari-spec.md`**

将所有 `AgentInfo` / `AgentRunInfo` / `WorkspaceInfo` 替换为 `Agent` / `AgentRun` / `Workspace` 的 `metadata/spec/status` 嵌套结构。

逐方法定义新 result schema（RISK 2）：

| Method | 旧 Result | 新 Result |
|--------|-----------|-----------|
| `agent/set` | `AgentInfo` (裸) | `{agent: Agent}` (统一 wrapper) |
| `agent/get` | `{agent: AgentInfo}` | `{agent: Agent}` |
| `agent/list` | `{agents: AgentInfo[]}` | `{agents: Agent[]}` |
| `agent/delete` | `{}` | `{}` (不变) |
| `agentrun/create` | `{workspace, name, state}` | `{agentRun: AgentRun}` (统一 wrapper) |
| `agentrun/status` | `{agentRun: AgentRunInfo, shimState?}` | `{agentRun: AgentRun, shimState?: ShimStateInfo}` |
| `agentrun/list` | `{agentRuns: AgentRunInfo[]}` | `{agentRuns: AgentRun[]}` |
| `agentrun/prompt` | `{}` | `{}` (不变) |
| `agentrun/cancel` | `{}` | `{}` (不变) |
| `agentrun/stop` | `{}` | `{}` (不变) |
| `agentrun/delete` | `{}` | `{}` (不变) |
| `agentrun/restart` | `{agentRun: AgentRunInfo}` | `{agentRun: AgentRun}` |
| `agentrun/attach` | `{socketPath}` | `{socketPath}` (不变，受控入口) |
| `workspace/create` | `{name, phase}` | `{workspace: Workspace}` (统一 wrapper) |
| `workspace/status` | `{name, phase, path?, members[]}` | `{workspace: Workspace, members: AgentRun[]}` |
| `workspace/list` | `{workspaces: WorkspaceInfo[]}` | `{workspaces: Workspace[]}` |
| `workspace/delete` | `{}` | `{}` (不变) |
| `workspace/send` | `{delivered: bool}` | `{delivered: bool}` (不变) |

设计原则：
- 返回实体的方法统一使用 `{entityName: Entity}` wrapper，不使用裸对象
- `agent/set` 从裸返回改为 `{agent: Agent}` wrapper，保持一致性
- `agentrun/create` 和 `workspace/create` 从轻量返回改为完整 wrapper
- 纯操作方法（delete/prompt/cancel/stop/send）保持空或轻量返回

**步骤 2：分离 public/private 字段（RISK 3）**

在 `api/ari/domain.go` 中，为 ARI 不应暴露的内部字段添加 `json:"-"`：

```go
// AgentRunStatus — 公开字段 + 内部字段隔离
type AgentRunStatus struct {
    State        api.Status `json:"state"`
    ErrorMessage string     `json:"errorMessage,omitempty"`

    // Internal fields — not exposed in ARI wire format
    ShimSocketPath  string          `json:"-"` // exposed only via agentrun/attach
    ShimStateDir    string          `json:"-"` // internal filesystem path
    ShimPID         int             `json:"-"` // exposed only in ShimStateInfo
    BootstrapConfig json.RawMessage `json:"-"` // internal bootstrap config
}

// WorkspaceSpec — Hooks 不通过 ARI 暴露
type WorkspaceSpec struct {
    Source json.RawMessage `json:"source,omitempty"`
    Hooks  json.RawMessage `json:"-"` // internal, not exposed via ARI
}
```

**需要确认的字段暴露决策**：
- `AgentRunSpec.Description` — 建议 **暴露**（`json:"description,omitempty"`），客户端有用
- `Workspace.Metadata.CreatedAt/UpdatedAt` — 建议 **暴露**，WorkspaceInfo 当前不含但有价值
- `Workspace.Metadata.Labels` — 建议 **暴露**（`json:"labels,omitempty"`），与 AgentRun 一致

**步骤 3：更新 `api/ari/types.go`**

- 删除 `AgentInfo`, `AgentRunInfo`, `WorkspaceInfo` 类型
- 删除 `api/ari/adapter.go`（不再需要转换函数）
- RPC Result 类型直接引用 domain 类型：

```go
type AgentSetResult struct {
    Agent Agent `json:"agent"`
}
type AgentGetResult struct {
    Agent Agent `json:"agent"`
}
type AgentListResult struct {
    Agents []Agent `json:"agents"`
}
type AgentRunCreateResult struct {
    AgentRun AgentRun `json:"agentRun"`
}
type AgentRunStatusResult struct {
    AgentRun  AgentRun      `json:"agentRun"`
    ShimState *ShimStateInfo `json:"shimState,omitempty"`
}
type AgentRunListResult struct {
    AgentRuns []AgentRun `json:"agentRuns"`
}
type WorkspaceCreateResult struct {
    Workspace Workspace `json:"workspace"`
}
type WorkspaceStatusResult struct {
    Workspace Workspace  `json:"workspace"`
    Members   []AgentRun `json:"members"`
}
type WorkspaceListResult struct {
    Workspaces []Workspace `json:"workspaces"`
}
```

**步骤 4：删除转换层**

- 删除 `pkg/ari/server.go` 中的 `agentRunToInfo()`, `agentToInfo()`
- 不需要 `api/ari/adapter.go`
- Server handler 直接返回 domain 对象

**涉及文件**：

| 操作 | 文件 |
|------|------|
| 更新 | `docs/design/agentd/ari-spec.md` — 新 wire schema + result 示例 |
| 移动 | `api/meta/types.go` 类型 → `api/ari/domain.go` (+ json:"-" 标记) |
| 更新 | `api/ari/types.go` — 删除 Info 类型，Result 引用 domain 类型 |
| 删除 | `api/meta/` 目录 |
| 更新 | ~20 files import `api/meta` → `api/ari` |
| 更新 | `pkg/ari/server.go` — 删除 agentRunToInfo/agentToInfo，直接返回 domain 对象 |
| 更新 | `cmd/agentdctl/` — 适配新 Result 类型 |
| 更新 | `cmd/.../workspacemcp/` — 适配新 Result 类型 |

**验收标准**（RISK 5）：ARI JSON shape 与更新后的 `docs/design/agentd/ari-spec.md` 一致。

---

### Phase 3: Service Interface + Register + typed Clients

**变更**（相对 v5）：所有 Service Interface 返回 domain 类型，不再返回 Info DTO。

```go
// api/ari/service.go

type WorkspaceService interface {
    Create(ctx context.Context, req *WorkspaceCreateParams) (*WorkspaceCreateResult, error)
    Status(ctx context.Context, req *WorkspaceStatusParams) (*WorkspaceStatusResult, error)
    List(ctx context.Context) (*WorkspaceListResult, error)
    Delete(ctx context.Context, req *WorkspaceDeleteParams) error
    Send(ctx context.Context, req *WorkspaceSendParams) (*WorkspaceSendResult, error)
}

type AgentRunService interface {
    Create(ctx context.Context, req *AgentRunCreateParams) (*AgentRunCreateResult, error)
    Prompt(ctx context.Context, req *AgentRunPromptParams) (*AgentRunPromptResult, error)
    Cancel(ctx context.Context, req *AgentRunCancelParams) error
    Stop(ctx context.Context, req *AgentRunStopParams) error
    Delete(ctx context.Context, req *AgentRunDeleteParams) error
    Restart(ctx context.Context, req *AgentRunRestartParams) (*AgentRunRestartResult, error)
    List(ctx context.Context, req *AgentRunListParams) (*AgentRunListResult, error)
    Status(ctx context.Context, req *AgentRunStatusParams) (*AgentRunStatusResult, error)
    Attach(ctx context.Context, req *AgentRunAttachParams) (*AgentRunAttachResult, error)
}

type AgentService interface {
    Set(ctx context.Context, req *AgentSetParams) (*AgentSetResult, error)
    Get(ctx context.Context, req *AgentGetParams) (*AgentGetResult, error)
    List(ctx context.Context) (*AgentListResult, error)
    Delete(ctx context.Context, req *AgentDeleteParams) error
}
```

Shim Service Interface 与 v5 一致（Subscribe 使用 Peer 抽象）。

**涉及文件**：与 v5 一致。

---

### Phase 4: 实现迁移

**变更**（相对 v5）：Server 实现直接返回 domain 对象，无需 adapter 转换。

```go
// pkg/ari/server/workspace.go
func (s *WorkspaceServiceImpl) Create(ctx context.Context, req *ari.WorkspaceCreateParams) (*ari.WorkspaceCreateResult, error) {
    ws, err := s.wsMgr.Create(ctx, req.Name, ...)
    if err != nil { return nil, ... }
    return &ari.WorkspaceCreateResult{Workspace: *ws}, nil
}
```

其余与 v5 一致（cmd 入口组装、Shim server/client、ARI client）。

---

### Phase 5: 清理

与 v5 一致，额外删除：
- `api/ari/adapter.go`（不再需要）
- 确认无代码引用已删除的 `AgentInfo`/`AgentRunInfo`/`WorkspaceInfo` 类型

---

### 最终目录结构

```
api/                                  # 协议定义层
├── ari/                              # ARI 协议
│   ├── types.go                      # RPC Params/Results (引用 domain 类型)
│   ├── domain.go                     # Domain 类型 (Agent, AgentRun, Workspace) — 同时作为 ARI wire format
│   ├── service.go                    # Service Interfaces + RegisterXxxService
│   └── client.go                     # Typed Client wrappers
├── shim/                             # Shim RPC 协议
│   ├── types.go                      # Shim RPC Params/Results
│   ├── service.go                    # ShimService Interface + RegisterShimService
│   └── client.go                     # Typed ShimClient wrapper
├── runtime/                          # OAR Runtime Spec (原 api/spec)
│   ├── config.go                     # Config, AcpAgent, AcpProcess, etc.
│   └── state.go                      # State, LastTurn
├── methods.go                        # RPC method 常量
├── events.go                         # Event type 常量
└── types.go                          # Status, EnvVar

pkg/
├── jsonrpc/                          # JSON-RPC 框架 (transport-agnostic)
│   ├── server.go                     # Server + ServiceDesc + Interceptor
│   ├── client.go                     # Client (封装 jsonrpc2, bounded FIFO notification worker)
│   ├── errors.go                     # RPCError + 标准错误码
│   └── peer.go                       # Peer 抽象 (Notify, DisconnectNotify)
├── ari/                              # ARI 实现层
│   ├── server/
│   │   ├── workspace.go              # implements ari.WorkspaceService
│   │   ├── agentrun.go               # implements ari.AgentRunService
│   │   └── agent.go                  # implements ari.AgentService
│   ├── client/
│   │   └── client.go                 # Dial helper
│   └── registry.go                   # Workspace 元数据缓存
├── shim/                             # Shim 实现层
│   ├── server/
│   │   └── service.go                # implements shim.ShimService
│   └── client/
│       └── client.go                 # Dial helper
├── agentd/                           # 守护进程管理 (精简)
│   ├── agent.go
│   ├── process.go
│   ├── recovery.go
│   └── ...                           # shim_client.go 删除
├── events/                           # 事件系统 (不变)
├── runtime/                          # ACP 进程管理 (不变)
├── store/                            # 持久化 (不变)
└── workspace/                        # 工作空间管理 (不变)
```

---

### 最终测试计划

#### Phase 1: `pkg/jsonrpc/` 协议测试

| 测试 | 描述 |
|------|------|
| `TestServer_Dispatch` | 注册 ServiceDesc，验证 method 路由正确 |
| `TestServer_MethodNotFound` | 未注册 method 返回 -32601 |
| `TestServer_InvalidParams` | unmarshal 失败返回 -32602 |
| `TestServer_RPCError` | handler 返回 *RPCError 保留 code/message/data |
| `TestServer_PlainError` | handler 返回 plain error 映射为 -32603 |
| `TestServer_Interceptor` | 拦截器链正确执行 |
| `TestServer_PeerNotify` | handler 通过 PeerFromContext 发送 notification |
| `TestServer_PeerDisconnect` | client 断开后 DisconnectNotify channel 关闭 |
| `TestClient_Call` | 基本 Call round-trip |
| `TestClient_ConcurrentCall` | 并发 Call 正确 demux response |
| `TestClient_CallWithNotification` | Call 期间收到 notification 不阻塞 response |
| `TestClient_NotificationHandler` | inbound notification 正确分发到 handler |
| `TestClient_NotificationOrder` | notification 按接收顺序串行交付（FIFO 保序） |
| `TestClient_SlowNotificationHandler` | 慢 notification handler 不阻塞 Call response |
| `TestClient_NotificationBackpressure` | buffer 满时阻塞投递，worker 消费后恢复 |
| `TestClient_ContextCancel` | context cancel 后 Call 立即返回 ctx.Err()，后续 response 不破坏连接 |
| `TestClient_Close` | Close 唤醒所有 pending calls，notification worker 退出 |
| `TestClient_ResponseOutOfOrder` | response 乱序仍能正确匹配 |

#### Phase 2a: rename/move 验证

| 测试 | 描述 |
|------|------|
| 编译通过 | `make build` — 纯 import 路径变更 |
| 全量测试 | `go test ./...` — 无行为变更，JSON 不变 |

#### Phase 2b: ARI contract convergence 验证

| 测试 | 描述 |
|------|------|
| `TestAgentJSON` | Agent domain type marshal 输出 metadata/spec 嵌套结构 |
| `TestAgentRunJSON` | AgentRun marshal 输出 metadata/spec/status，敏感字段不出现 |
| `TestWorkspaceJSON` | Workspace marshal 输出正确，Hooks 不出现 |
| `TestSensitiveFieldsHidden` | ShimStateDir, BootstrapConfig, ShimSocketPath, ShimPID 不在 JSON 中 |
| ARI result golden | 所有 Result 类型的 JSON 与更新后 `ari-spec.md` 示例一致 |
| 全量测试 | `go test ./...` |

#### Phase 3: Service Interface round-trip 测试

与 v5 一致。

#### Phase 4: 行为迁移测试

与 v5 一致。

#### Phase 5: 清理验证

与 v5 一致 + 确认无引用 `AgentInfo`/`AgentRunInfo`/`WorkspaceInfo`。

---

### 执行顺序

```
Phase 1: pkg/jsonrpc/ 框架 (Server + Client + RPCError + Peer + bounded FIFO worker)
   ↓
Phase 2a: 纯 rename/move (api/spec → api/runtime, pkg/shimapi → api/shim)
   ↓
Phase 2b: ARI clean-break contract convergence
         (先更新 ari-spec.md → 再 api/meta → api/ari/domain.go + json:"-" → 删除 Info DTO → 更新 Result 类型)
   ↓
Phase 3: Service Interface + Register + typed Clients
   ↓
Phase 4: 实现迁移 (pkg/{ari,shim}/{server,client})
   ↓
Phase 5: 清理
```

每个 Phase 独立可提交。`make build` + `go test ./...` 每步通过。
