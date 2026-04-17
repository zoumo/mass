# Permission Request Human-in-the-Loop 设计方案

> 状态：Draft | 日期：2026-04-16 | 依赖重构完成后实施

## Context

当前 Shim 层的 `acpClient.RequestPermission()` 对所有权限请求做了硬编码处理：
- `approve_all` → 自动选第一个 option
- `approve_reads` / `deny_all` → 直接返回 error

缺少一种 **"等待外部决策"** 的策略。当 Agent 执行敏感操作（如写文件、执行终端命令）时，
无法暂停执行等待人类或第三方 Agent 审批。

**目标**：新增 `manual` 权限策略 + `waiting_permission` 状态，
让权限请求通过 `runtime/event_update` 暴露给外部，通过新 RPC 方法接收审批结果。
无超时机制，纯阻塞等待直到外部 resolve 或 session/cancel。

---

## 1. 新增 PermissionPolicy: `manual`

**文件**: `pkg/runtime-spec/api/config.go`

```go
const (
    ApproveAll   PermissionPolicy = "approve_all"
    ApproveReads PermissionPolicy = "approve_reads"
    DenyAll      PermissionPolicy = "deny_all"
    Manual       PermissionPolicy = "manual"       // NEW
)
```

更新 `IsValid()` 方法包含 `Manual`。

---

## 2. 新增 Status: `waiting_permission`

**文件**: `pkg/runtime-spec/api/types.go`

```go
StatusWaitingPermission Status = "waiting_permission"
```

状态机新增转换：
```
running → waiting_permission    (RequestPermission 到达 + manual 策略)
waiting_permission → running    (外部 resolve / cancel)
```

这是 `running` 的子阶段 — Agent 仍在 turn 中，但阻塞等待权限审批。

**文件**: `docs/design/runtime/runtime-spec.md` — 更新 status 列表、生命周期图、权限策略表

---

## 3. 新增事件类型: `permission_request` / `permission_resolved`

### 设计决策

`permission_request` 作为独立的 session 事件类型（与 `tool_call` 同级），
**不**放入 `runtime_update`。原因：
- 它是 turn 内事件，需要携带 `turnId`
- 它需要外部交互（不是单向通知），语义上更接近 `tool_call` 而非 metadata 更新
- 状态变更 (`running → waiting_permission`) 仍通过 `runtime_update.status` 字段传递

### 事件定义

**文件**: `pkg/agentrun/api/event_constants.go`

```go
EventTypePermissionRequest  = "permission_request"
EventTypePermissionResolved = "permission_resolved"
```

**文件**: `pkg/agentrun/api/event_types.go`

```go
// PermissionRequestEvent — 需要外部决策的权限请求
type PermissionRequestEvent struct {
    RequestID string             `json:"requestId"`
    ToolCall  PermissionToolCall `json:"toolCall"`
    Options   []PermissionOption `json:"options"`
}

// PermissionToolCall — 触发权限请求的工具调用信息
type PermissionToolCall struct {
    ToolCallID string            `json:"toolCallId"`
    Title      *string           `json:"title,omitempty"`
    Kind       *string           `json:"kind,omitempty"`
    Content    []ToolCallContent `json:"content,omitempty"`
    RawInput   any               `json:"rawInput,omitempty"`
}

// PermissionOption — 权限选项
type PermissionOption struct {
    OptionID string `json:"optionId"`
    Name     string `json:"name"`
    Kind     string `json:"kind"` // allow_once, allow_always, reject_once, reject_always
}

// PermissionResolvedEvent — 权限决策结果
type PermissionResolvedEvent struct {
    RequestID  string `json:"requestId"`
    Resolution string `json:"resolution"` // "selected" | "cancelled"
    OptionID   string `json:"optionId,omitempty"`
}
```

**文件**: `pkg/agentrun/api/event.go` — `decodeEventPayload` 增加两个 case

### AgentRunEvent 类型表更新

| type | payload 字段 | 说明 |
|------|-------------|------|
| `permission_request` | `requestId`, `toolCall`, `options[]` | Agent 请求权限，等待外部决策 |
| `permission_resolved` | `requestId`, `resolution`, `optionId?` | 权限决策结果 |

---

## 4. 新增 RPC 方法: `session/resolve_permission`

**文件**: `pkg/agentrun/api/methods.go`

```go
MethodSessionResolvePermission = "session/resolve_permission"
```

**文件**: `pkg/agentrun/api/types.go`

```go
type SessionResolvePermissionParams struct {
    RequestID string `json:"requestId"`
    OptionID  string `json:"optionId,omitempty"` // 空/省略 = cancel
}

type SessionResolvePermissionResult struct{}
```

**Wire shape**:

```json
// Request
{
  "jsonrpc": "2.0", "id": 9,
  "method": "session/resolve_permission",
  "params": { "requestId": "perm-001", "optionId": "opt-allow-once" }
}

// Response
{ "jsonrpc": "2.0", "id": 9, "result": {} }
```

**方法速查表新增行**:

| 方法 | 方向 | 阻塞 | 说明 |
|------|------|------|------|
| `session/resolve_permission` | 请求 / 响应 | 否 | 解决挂起的权限请求 |

---

## 5. 核心实现：Manager 侧权限等待机制

**文件**: `pkg/agentrun/runtime/acp/runtime.go`

Manager 新增字段：

```go
type Manager struct {
    // ... existing fields ...
    permMu            sync.Mutex
    pendingPermission *pendingPerm
}

type pendingPerm struct {
    requestID string
    ch        chan permResolution
}

type permResolution struct {
    optionID  string
    cancelled bool
}
```

新增方法：

```go
// ResolvePermission 解决挂起的权限请求。
// requestID 不匹配时返回 error。
func (m *Manager) ResolvePermission(requestID, optionID string) error
```

**文件**: `pkg/agentrun/runtime/acp/client.go`

`RequestPermission` 增加 `manual` 分支：

```
RequestPermission 调用流（manual 策略）:

1. 生成 requestID (UUID)
2. 创建 chan permResolution (buffer=1) → m.pendingPermission
3. 写状态: running → waiting_permission （触发 runtime_update 事件）
4. 通过 PermissionHook 广播 permission_request 事件
5. select:
   - case resolution := <-ch → 构造 ACP Response
   - case <-ctx.Done()       → cancelled outcome
6. 清理 m.pendingPermission = nil
7. 写状态: waiting_permission → running
8. 通过 PermissionHook 广播 permission_resolved 事件
9. 返回 ACP Response
```

**关键约束**：ACP 协议中 `RequestPermission` 是同步阻塞调用，
Agent 在等待 Response 期间不会发送其他请求。因此 pendingPermission 最多只有一个。

---

## 6. Service 层接入

**文件**: `pkg/agentrun/server/service.go`

```go
func (s *Service) ResolvePermission(ctx context.Context, req *runapi.SessionResolvePermissionParams) (*runapi.SessionResolvePermissionResult, error) {
    if err := s.mgr.ResolvePermission(req.RequestID, req.OptionID); err != nil {
        return nil, jsonrpc.ErrInvalidParams(err.Error())
    }
    return &runapi.SessionResolvePermissionResult{}, nil
}
```

**文件**: `pkg/agentrun/server/register.go` — 注册 `session/resolve_permission` 方法

---

## 7. Client 层同步更新

**文件**: `pkg/agentrun/client/client.go`

```go
func (c *Client) ResolvePermission(ctx context.Context, requestID, optionID string) error
```

---

## 8. Cancel 联动

`session/cancel` 处理时需检查：如果有 pending permission request，
通过 channel 发送 cancelled resolution，使 `RequestPermission` 返回 cancelled outcome 给 ACP Agent。

---

## 9. 事件广播机制

Manager 需要持有 Translator 引用（或一个 broadcast callback）来广播 permission 事件。
当前 Manager 通过 `StateChangeHook` 做状态通知，但 permission 事件是 session 级事件，
需要走 Translator 的 broadcast 路径以确保：
- 分配 seq
- 携带 turnId
- 持久化到 events.jsonl
- 推送给所有 watcher

方案：Manager 接受一个 `PermissionHook func(Event)` callback，由 Service 层在初始化时注入，
callback 内部调用 `Translator.broadcastEvent()`。
这与现有 `StateChangeHook` / `sessionMetadataHook` 模式一致。

---

## 状态机全貌

```
         create
           │
           ▼
      ┌──────────┐
      │ creating  │
      └────┬──────┘
           │ process started + ACP initialized
           ▼
      ┌──────────┐        session/prompt        ┌──────────┐
      │   idle    │ ─────────────────────────► │  running   │
      │           │ ◄───────────────────────── │            │
      └────┬──────┘        turn end             └───┬───┬───┘
           │                                        │   │
           │                                        │   │ permission request (manual)
           │                                        │   ▼
           │                                        │ ┌───────────────────┐
           │                                        │ │waiting_permission │
           │                                        │ │  (human review)   │
           │                                        │ └────────┬──────────┘
           │                                        │          │ resolved / cancelled
           │                                        │◄─────────┘
           │ kill / exit / error                    │ kill / exit / error
           ▼                                        ▼
      ┌──────────┐                             ┌──────────┐
      │ stopped   │                             │ stopped   │
      └──────────┘                             └──────────┘
```

---

## 事件流示例

```
turn_start
agent_thinking        {status: "start", content: "Let me edit..."}
agent_thinking        {status: "end"}
tool_call             {id: "tc-1", kind: "file", title: "Write auth.go"}

                      ← Agent 触发 RequestPermission (manual 策略)

runtime_update        {status: {previousStatus: "running", status: "waiting_permission", reason: "permission-requested"}}
permission_request    {
                        requestId: "perm-001",
                        toolCall: {toolCallId: "tc-1", title: "Write auth.go", kind: "file"},
                        options: [
                          {optionId: "opt-1", name: "Allow once", kind: "allow_once"},
                          {optionId: "opt-2", name: "Allow always", kind: "allow_always"},
                          {optionId: "opt-3", name: "Reject", kind: "reject_once"}
                        ]
                      }

                      ← 人类调用 session/resolve_permission(requestId: "perm-001", optionId: "opt-1")

permission_resolved   {requestId: "perm-001", resolution: "selected", optionId: "opt-1"}
runtime_update        {status: {previousStatus: "waiting_permission", status: "running", reason: "permission-resolved"}}
tool_result           {id: "tc-1", status: "success"}
agent_message         {status: "start", content: "Done..."}
agent_message         {status: "end"}
turn_end              {stopReason: "end_turn"}
```

---

## 权限策略表更新

| 策略 | 行为 |
|------|------|
| `approve_all` | 所有操作自动批准 |
| `approve_reads` | 只读操作批准，写操作返回 deny |
| `deny_all` | 所有操作返回 deny |
| `manual` | 权限请求转发给外部决策者，阻塞等待 resolve |

---

## 修改文件清单

| 文件 | 变更 |
|------|------|
| `pkg/runtime-spec/api/types.go` | +`StatusWaitingPermission` |
| `pkg/runtime-spec/api/config.go` | +`Manual` PermissionPolicy, 更新 `IsValid()` |
| `pkg/agentrun/api/event_constants.go` | +`EventTypePermissionRequest`, +`EventTypePermissionResolved` |
| `pkg/agentrun/api/event_types.go` | +`PermissionRequestEvent`, +`PermissionResolvedEvent`, +支持类型 |
| `pkg/agentrun/api/event.go` | `decodeEventPayload` 增加 permission 事件 case |
| `pkg/agentrun/api/methods.go` | +`MethodSessionResolvePermission` |
| `pkg/agentrun/api/types.go` | +`SessionResolvePermissionParams/Result` |
| `pkg/agentrun/runtime/acp/client.go` | `RequestPermission()` 增加 `manual` 分支 |
| `pkg/agentrun/runtime/acp/runtime.go` | +`pendingPerm` 结构, +`ResolvePermission()`, +`PermissionHook` |
| `pkg/agentrun/server/service.go` | +`ResolvePermission()` handler |
| `pkg/agentrun/server/register.go` | 注册 `session/resolve_permission` |
| `pkg/agentrun/client/client.go` | +`ResolvePermission()` client 方法 |
| `docs/design/runtime/shim-rpc-spec.md` | 新增方法 + 事件 + 示例 |
| `docs/design/runtime/runtime-spec.md` | 状态机 + 权限策略表更新 |

---

## 验证方案

1. **单测**: `client_test.go` — `TestAcpClient_RequestPermission_Manual`
   - goroutine 模拟异步 resolve → 验证阻塞等待 → 正确 Response
   - ctx cancel → 验证 cancelled outcome
2. **单测**: `runtime_test.go` — 状态转换 running ↔ waiting_permission
3. **单测**: `client_test.go` (shim client) — ResolvePermission RPC 调用
4. **编译验证**: `make build`
5. **事件流验证**: `runtime/watch_event` 观察 permission_request / permission_resolved 正确产出
