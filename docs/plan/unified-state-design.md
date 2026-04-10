# OAR Platform Terminal State Refactor

> Status: Draft
> Date: 2026-04-10

## Goal

本次重构是一次**整个平台终态重构**，目标是将 OAR agentd 切到一个清晰、可靠、可维护的终态设计。范围包含三类改造，一次性完成，不分批渐进，不保留兼容层：

1. **状态统一** — `spec.Status` 成为唯一运行态枚举；shim 是唯一运行态真相；消除双写、猜测式状态同步
2. **资源模型重写** — k8s 风格 `ObjectMeta/spec/status`；Room/Namespace + 独立 Workspace → 统一 Workspace 资源；Session/AgentRun 概念消除；Agent 直接对应 shim 实例
3. **存储引擎重写** — SQLite（CGo）→ bbolt（纯 Go）；删除所有 SQL schema 和 migration 逻辑

## Context

当前系统存在三套重叠的状态系统：

| 层级 | 类型 | 位置 | 状态值 |
|------|------|------|--------|
| shim/runtime | `spec.Status` | `pkg/spec/state_types.go` | creating, created, running, stopped |
| agentd DB | `meta.AgentState` | `pkg/meta/models.go` | creating, created, running, stopped, **error** |
| agentd DB | `meta.SessionState` | `pkg/meta/models.go` | creating, created, running, stopped, **error** |

此外还存在 Room 和 Workspace 两套独立资源，以及 Session 概念与 Agent 的 1:1 冗余影子关系。

**核心矛盾**：真正知道 agent 运行状态的是 shim，但当前 agentd 在 DB 里维护了独立的状态，通过"先猜后纠正"的模式同步。这种模式：

1. **崩溃时不一致** — agentd 重启后 DB 里可能还是 "running"，但 shim 已经 idle
2. **Session 冗余** — Session 与 Agent 1:1，状态机几乎完全重复
3. **API 混乱** — `AgentStatusResult` 同时返回 `Agent.State` 和 `ShimState.Status`，调用方需要理解两套状态
4. **Recovery 时以 shim 为准** — `recovery.go` 已经在做"读 shim 状态 → 覆盖 DB 状态"，说明 shim 才是真相
5. **`created` 语义模糊** — 改为 `idle` 语义更明确
6. **模型职责混乱** — Agent 混杂了配置字段和运行状态字段，Session 是 Agent 的重复影子

## Design Principles

1. **Shim 是唯一运行态真相，边界绝对** — shim 建立后，agentd 对运行态只读不写。任何运行态转换（`idle`/`running`/`stopped`/`error`）只能由 shim 通过 `runtime/stateChange` 通知驱动；agentd 仅在 shim 建立前（`creating` 及 pre-bootstrap `error`）和 shim 确认已死时（`stopped`）直接写 DB state。没有例外。
2. **Agent 直接对应一个 shim 实例**（类比 containerd Task），由 `(workspace, name)` 唯一标识，无需 UUID。身份模型锁死为 `(workspace, name)`，贯穿 API、存储键、路由模型。
3. **DB state 是 admission 的快速门，不是唯一决策者** — 热路径（prompt/cancel/stop）先以 DB 的 `Agent.Status.State` 做快速判断；若 DB 允许但 shim 拒绝（通知延迟导致 DB stale），agentd 即时从 shim 同步最新 state 并以 shim 结果为准返回给调用方。DB state 的作用是快速拦截明显无效请求，不是替代 shim 的真实决策。
4. **ACP sessionId 从 shim state 文件读取** — 不持久化到 DB；recovery 时按需读取，支持 `tryReload` 的 best-effort 恢复语义。
5. **终态直切，无兼容层** — 不保留兼容别名、兼容 API 或渐进迁移层。

## Target Model

### 1. 统一状态枚举

```go
// pkg/spec/state_types.go
type Status string

const (
    StatusCreating Status = "creating"
    StatusIdle     Status = "idle"    // was "created"
    StatusRunning  Status = "running"
    StatusStopped  Status = "stopped"
    StatusError    Status = "error"   // NEW
)
```

删除 `meta.AgentState` 和 `meta.SessionState`，全部使用 `spec.Status`。

### 2. 状态机

```
creating ──→ idle ──→ running ──→ idle     (turn 完成)
    │          │         │
    │          └──→ stopped    └──→ stopped
    │
    └──→ error            └──→ error

stopped ──→ creating  (restart)
error   ──→ creating  (restart)
```

| From | Valid To | Trigger |
|------|----------|---------|
| creating | idle, error | bootstrap complete / fail |
| idle | running, stopped | prompt dispatch / stop |
| running | idle, stopped, error | turn done / stop / fail |
| stopped | creating | restart |
| error | creating | restart |

### 3. 对象模型

采用 k8s 风格：公共 `ObjectMeta`，`spec` 存配置，`status` 存运行状态。

资源分类：
- **Cluster-scoped**：Workspace（原 Room/Namespace，同时承担组织分组和文件系统工作目录） — `ObjectMeta.Workspace` 为空
- **Workspace-scoped**：Agent — `ObjectMeta.Workspace` = 所属 workspace 名

Namespace 和独立 Workspace 资源均消除，合并为单一 Workspace 概念。Agent 属于某个 Workspace，自动继承其文件系统路径，无需额外引用。

#### ObjectMeta（所有资源共用）

```go
// pkg/meta/models.go
type ObjectMeta struct {
    Name      string            `json:"name"`
    Workspace string            `json:"workspace,omitempty"` // empty for cluster-scoped
    Labels    map[string]string `json:"labels,omitempty"`
    CreatedAt time.Time         `json:"createdAt"`
    UpdatedAt time.Time         `json:"updatedAt"`
}
```

#### Workspace（cluster-scoped，原 Room/Namespace + 文件系统工作目录）

```go
type Workspace struct {
    Metadata ObjectMeta      `json:"metadata"`
    Spec     WorkspaceSpec   `json:"spec"`
    Status   WorkspaceStatus `json:"status"`
}

type WorkspaceSpec struct {
    Source WorkspaceSource `json:"source"`
    Hooks  *WorkspaceHooks `json:"hooks,omitempty"`
}

type WorkspaceSource struct {
    Type     WorkspaceSourceType `json:"type"`
    Git      *GitSource          `json:"git,omitempty"`
    EmptyDir *EmptyDirSource     `json:"emptyDir,omitempty"`
    Local    *LocalSource        `json:"local,omitempty"`
}

type WorkspaceSourceType string

const (
    WorkspaceSourceTypeGit      WorkspaceSourceType = "git"
    WorkspaceSourceTypeEmptyDir WorkspaceSourceType = "emptyDir"
    WorkspaceSourceTypeLocal    WorkspaceSourceType = "local"
)

type GitSource struct {
    URL   string `json:"url"`
    Ref   string `json:"ref,omitempty"`   // branch / tag / commit
    Depth int    `json:"depth,omitempty"`
}

type EmptyDirSource struct{}

type LocalSource struct {
    Path string `json:"path"` // source path on host
}

type WorkspaceHooks struct {
    Setup    []HookCommand `json:"setup,omitempty"`
    Teardown []HookCommand `json:"teardown,omitempty"`
}

type HookCommand struct {
    Command     string   `json:"command"`
    Args        []string `json:"args,omitempty"`
    Description string   `json:"description,omitempty"`
}

// WorkspaceStatus is set by agentd after source preparation.
type WorkspaceStatus struct {
    Phase WorkspacePhase `json:"phase"`
    Path  string         `json:"path,omitempty"` // prepared filesystem path, set when ready
}

type WorkspacePhase string

const (
    WorkspacePhasePending WorkspacePhase = "pending"
    WorkspacePhaseReady   WorkspacePhase = "ready"
    WorkspacePhaseError   WorkspacePhase = "error"
)
```

**Workspace 生命周期**：`workspace/create` 触发后台 source 准备（git clone 等），`WorkspaceStatus.Phase` 从 `pending` 变为 `ready` 或 `error`。只有 `ready` 的 workspace 才允许创建 agent。

#### Agent（workspace-scoped，直接对应一个 shim 实例）

```go
type Agent struct {
    Metadata ObjectMeta  `json:"metadata"` // Metadata.Workspace = 所属 workspace
    Spec     AgentSpec   `json:"spec"`
    Status   AgentStatus `json:"status"`
}

type AgentSpec struct {
    RuntimeClass  string        `json:"runtimeClass"`
    RestartPolicy RestartPolicy `json:"restartPolicy"`
    Description   string        `json:"description,omitempty"`
    SystemPrompt  string        `json:"systemPrompt,omitempty"`
}

// RestartPolicy 决定 shim 死亡重启后的 ACP session 处理方式。
// 若 shim 仍存活则始终重连，与此字段无关。
type RestartPolicy string

const (
    // RestartPolicyTryReload 尝试恢复上一次对话历史：
    // 从 shim state 文件中读取 ACP sessionId，调 session/load。
    // 以下情况退化为 AlwaysNew：state 文件不存在、sessionId 找不到、
    // ACP runtime 不支持 session/load。
    RestartPolicyTryReload RestartPolicy = "tryReload"

    // RestartPolicyAlwaysNew 始终从零开始，不尝试恢复历史。
    RestartPolicyAlwaysNew RestartPolicy = "alwaysNew"
)

type AgentStatus struct {
    State        spec.Status `json:"state"`
    ErrorMessage string      `json:"errorMessage,omitempty"`

    // Shim process metadata — written by agentd at bootstrap, used for recovery.
    // BootstrapConfig is the spec.Config (config.json) generated by agentd and
    // passed to the shim at startup. Persisted here so recovery can restart the
    // shim with the identical config without re-deriving it.
    ShimSocketPath  string       `json:"shimSocketPath,omitempty"`
    ShimStateDir    string       `json:"shimStateDir,omitempty"`
    ShimPID         int          `json:"shimPid,omitempty"`
    BootstrapConfig *spec.Config `json:"bootstrapConfig,omitempty"`
}
```

Agent 的文件系统路径通过 `Workspace.Status.Path` 获取，不在 Agent 内冗余存储。

**移除**：`Agent.ID`、`Agent.State`（flat 字段）、`Session`/`AgentRun` 整个概念、`meta.AgentState`、`meta.SessionState`、`WorkspaceRef`、独立 Namespace 资源。

### 4. Storage: bbolt Bucket 结构

效仿 containerd 的嵌套 bucket 风格。

```
v1 (top-level bucket)
  ├── workspaces
  │   └── {name} → Workspace JSON
  │
  └── agents
      └── {workspace} (bucket)
          └── {name} → Agent JSON
```

Go 访问模式：

```go
func getAgentsBucket(tx *bbolt.Tx, workspace string) *bbolt.Bucket {
    b := tx.Bucket(bucketKeyV1)
    if b == nil { return nil }
    agents := b.Bucket(bucketKeyAgents)
    if agents == nil { return nil }
    return agents.Bucket([]byte(workspace))
}

// 写 agent
v1Bkt  := tx.Bucket(bucketKeyV1)
agBkt, _ := v1Bkt.CreateBucketIfNotExists(bucketKeyAgents)
wsBkt, _ := agBkt.CreateBucketIfNotExists([]byte(workspace))
wsBkt.Put([]byte(agentName), agentJSON)
```

**删除 workspace 前的引用检查**：扫描 `agents/{workspace}` sub-bucket，若仍有 agent 记录则拒绝删除。

### 5. API Surface

```go
// pkg/ari/types.go

type WorkspaceCreateParams struct {
    Name   string            `json:"name"`
    Source WorkspaceSource   `json:"source"`
    Hooks  *WorkspaceHooks   `json:"hooks,omitempty"`
    Labels map[string]string `json:"labels,omitempty"`
}

type WorkspaceInfo struct {
    Name      string            `json:"name"`
    Phase     string            `json:"phase"`  // pending / ready / error
    Path      string            `json:"path,omitempty"`
    Labels    map[string]string `json:"labels,omitempty"`
    CreatedAt time.Time         `json:"createdAt"`
    UpdatedAt time.Time         `json:"updatedAt"`
}

type AgentCreateParams struct {
    Workspace     string            `json:"workspace"`
    Name          string            `json:"name"`
    RuntimeClass  string            `json:"runtimeClass"`
    RestartPolicy RestartPolicy     `json:"restartPolicy,omitempty"`
    Description   string            `json:"description,omitempty"`
    SystemPrompt  string            `json:"systemPrompt,omitempty"`
    Labels        map[string]string `json:"labels,omitempty"`
}

// agent/prompt, cancel, stop, restart, status, delete, attach 全部用 workspace+name 定位
type AgentRef struct {
    Workspace string `json:"workspace"`
    Name      string `json:"name"`
}

type AgentInfo struct {
    Workspace     string            `json:"workspace"`
    Name          string            `json:"name"`
    RuntimeClass  string            `json:"runtimeClass"`
    RestartPolicy RestartPolicy     `json:"restartPolicy"`
    Description   string            `json:"description,omitempty"`
    SystemPrompt  string            `json:"systemPrompt,omitempty"`
    Labels        map[string]string `json:"labels,omitempty"`
    State         string            `json:"state"`
    ErrorMessage  string            `json:"errorMessage,omitempty"`
    RuntimePID    int               `json:"runtimePid,omitempty"`
    CreatedAt     time.Time         `json:"createdAt"`
    UpdatedAt     time.Time         `json:"updatedAt"`
}

type AgentStatusResult struct {
    Agent AgentInfo `json:"agent"`
}
```

### 6. 状态写入策略

**Shim 边界规则（绝对）**：

| 时机 | 写入者 | 允许写入的状态 |
|------|--------|---------------|
| shim 建立前 | agentd | `creating`（create/restart 时）、`error`（bootstrap 失败） |
| shim 建立后 | shim only | `idle`、`running`、`stopped`、`error` |
| shim 确认已死 | agentd | `stopped`（仅当 shim 进程不存在时） |

shim 建立后，agentd **绝不直接写运行态**，只通过 `runtime/stateChange` 被动同步到 DB。

**Admission 策略（带 stale 兜底）**：

热路径（`agent/prompt`、`agent/cancel`、`agent/stop`）按以下顺序处理：

```
1. 读 DB Agent.Status.State（快速门）
   - 明显无效（stopped/error/creating）→ 直接拒绝，无需询问 shim
   - 允许（idle/running）→ 继续

2. 向 shim 发送请求
   - shim 接受 → 正常处理
   - shim 拒绝（state mismatch）→ 即时从 shim 读取当前 state，
     更新 DB，以 shim 返回结果为准响应调用方
```

这样消除了 stale DB state 导致的误放行：shim 永远是最终裁决，DB 只是快速路径上的预筛选。

| 场景 | DB state | shim 实际 state | 结果 |
|------|----------|-----------------|------|
| DB stale（通知延迟） | running | idle | DB 拒绝，调用方重试后 DB 已更新 → 成功 |
| DB stale（通知延迟） | idle | running | DB 放行 → shim 拒绝 → 即时同步 DB → 返回 busy |
| 正常 | idle | idle | DB 放行 → shim 接受 → 成功 |

**状态变更明细**：

| 状态变更 | 写入者 | 说明 |
|----------|--------|------|
| → creating | agentd | agent/create 或 agent/restart 时 |
| → idle | shim → agentd 同步 | shim 完成 ACP handshake 后写 state.json；agentd 通过 `runtime/stateChange` 同步到 DB |
| → running | shim → agentd 同步 | shim 收到 prompt 开始处理时写 |
| → stopped | shim → agentd 同步 | 进程退出或 runtime/stop 完成时写 |
| → stopped | agentd（仅限） | shim 进程已死，agentd 直接写 |
| → error（pre-shim） | agentd | shim 建立前 bootstrap 失败 |
| → error（post-shim） | shim → agentd 同步 | shim 建立后只能由 shim 写 |

#### `agent/stop` 边界

| 场景 | 处理方式 |
|------|----------|
| shim 存活 | 调 `runtime/stop`，stopped 由 shim 通知驱动 |
| shim 已死 | agentd 直接将 Agent.Status.State 写为 stopped |
| 已处于 terminal | 幂等返回成功 |
| agent 不存在 | 返回 not found |

### 7. `deliverPromptAsync` 重构

```
agentd: 读取 DB Agent.Status.State，验证 == idle
agentd: dispatch prompt to shim
  shim: set state → running (写 state.json, 发 runtime/stateChange)
  shim: process turn
  shim: set state → idle (写 state.json, 发 runtime/stateChange)
agentd: 通过 runtime/stateChange 通知更新 DB Agent.Status.State
```

### 8. Recovery

agentd 重启后：

1. 扫描所有 `status.state IN (creating, idle, running)` 的 agents
2. 检查 `ShimPID` 对应进程是否存在：
   - 存活 → 重连 shim socket，同步当前 state
   - 已死 → 根据 `RestartPolicy`：
     - `tryReload`：重启 shim，尝试从 shim state 文件读取 ACP sessionId 并调 `session/load`；若 state 文件不存在、sessionId 缺失或 ACP 不支持 session/load，则退化为从零开始
     - `alwaysNew`：重启 shim，始终从零开始

## Implementation Plan

### Phase 0: bbolt 存储 + 统一模型（直接切终态）

**修改文件**：
- `go.mod` / `go.sum` — 加 `go.etcd.io/bbolt`，移除 `github.com/mattn/go-sqlite3`
- `pkg/spec/state_types.go` — `StatusCreated → StatusIdle`，添加 `StatusError`
- `pkg/meta/models.go` — 全部重写：ObjectMeta + Workspace（含 source/hooks/status）+ Agent，删除 Session/AgentState/SessionState/WorkspaceRef/Room/Namespace
- `pkg/meta/store.go` — `*sql.DB` → `*bbolt.DB`；`initSchema()` → `initBuckets()`；删除 `BeginTx()`/`DB()`
- `pkg/meta/schema.sql` — 删除
- `pkg/meta/session.go` — 删除
- `pkg/meta/room.go` — 删除（Room 概念消除）
- `pkg/meta/workspace.go` — 重写为 bbolt KV，CRUD 基于 `workspace.name`；删除 AcquireWorkspace/ReleaseWorkspace
- `pkg/meta/agent.go` — SQL CRUD → bbolt KV，全改用 workspace+name
- `pkg/meta/*_test.go` — 全部重写

**验收标准**：`pkg/meta/*_test.go` 全部通过。

### Phase 1: agentd 业务层适配

- `pkg/agentd/session.go` — 删除
- `pkg/agentd/agent.go` — AgentManager 去掉 UpdateState，Delete 改用 namespace+name
- `pkg/agentd/process.go` — Session → Agent.Status；`OAR_ROOM_AGENT` → `OAR_AGENT_NAME`
- `pkg/agentd/recovery.go` — 按新 RestartPolicy + ACPSessionID 逻辑重写

### Phase 2: ARI 层适配

- `pkg/ari/types.go` — 全部重写：namespace+name 替代 agentId，去掉 ShimState/Session 相关类型
- `pkg/ari/server.go` — `deliverPromptAsync` 去掉手动状态；admission 改用 DB state；所有 handler 改用 namespace+name

### Phase 3: CLI + 集成测试适配

- `cmd/agentdctl/*.go` — CLI 输出适配
- `tests/integration/*.go` — 测试适配

## Risk & Mitigation

| 风险 | 缓解 |
|------|------|
| shim 通知丢失导致 DB state 滞后 | agentd 定期 poll shim `runtime/status` 做 reconciliation |
| bbolt 写事务串行 | agentd 是单进程 daemon，并发写极少，无实际瓶颈 |
| 旧 SQLite 数据丢失 | 不做迁移，首次启动全新初始化 |
| workspace 删除检查 | 扫描 agents，运行期数量极少，线性扫描无瓶颈 |

---

## Design Doc Updates

以下各节给出 `docs/design/` 下受影响文件的**完整替换内容**，实现时直接覆盖对应文件。

### `docs/design/agentd/agentd.md`

```markdown
# agentd — runtime realization daemon

agentd is the daemon that realizes already-decided runtime objects.
It owns workspaces, agents, process supervision, and the realized workspace projection needed for routing and inspection.
It does **not** own orchestrator intent.

## Desired vs Realized

| Concern | Primary owner | agentd role |
|---|---|---|
| Which Workspace should exist | caller (orchestrator / CLI) | realize it when asked |
| Which agents should exist | caller | store realized `workspace` / `name` on agents |
| When work is complete | caller | expose runtime state only |
| Workspace source preparation and cleanup | Workspace Manager | authoritative runtime execution |
| Agent lifecycle and identity | Agent Manager | external lifecycle authority |
| ACP protocol details | runtime / shim | hidden behind shim and translated for ARI |

## Internal Subsystems

### Workspace Manager

Workspace Manager realizes a workspace spec into a runtime workspace path.
It owns:

- source realization (`git`, `emptyDir`, `local`);
- canonical path registration;
- hook execution;
- cleanup rules for managed vs unmanaged workspaces.

Important boundary rules:

- **local workspace** paths are host paths and must be validated/canonicalized before use;
- workspace hooks are host commands, not in-agent work;
- managed workspaces may be deleted on cleanup, local workspaces may not.

### Agent Manager

Agent Manager owns the **external** lifecycle of agents.
An agent directly corresponds to one shim instance (analogous to a containerd Task).
It records:

- identity: `workspace` + `name` (unique key — all agents belong to a workspace);
- selected `runtimeClass`;
- `description` and bootstrap inputs (`systemPrompt`, `labels`);
- `restartPolicy` for recovery behavior;
- runtime state (`creating`, `idle`, `running`, `stopped`, `error`);
- shim process metadata for recovery (`shimSocketPath`, `shimStateDir`, `shimPID`, `bootstrapConfig`).

Agent identity (`workspace` + `name`) is stable across restarts.
The agent's filesystem path is inherited from `Workspace.Status.Path` — not stored redundantly on the agent.
There is no separate internal session or run concept — the agent record IS the runtime instance.

### Process Manager

Process Manager realizes an agent into an actual runtime process through the shim.
It owns:

- bundle materialization;
- runtime startup and shutdown;
- runtime-state inspection;
- reconnect to existing shim processes;
- typed event subscription.

## Agent Identity

All agents must belong to a workspace.
The `(workspace, name)` pair is the stable external identity for an agent.

- `workspace` — the Workspace this agent is a member of;
- `name` — the member name inside that Workspace (e.g. `architect`, `coder`).

Together they form a unique key within agentd.
External callers refer to agents by `(workspace, name)`.
There is no opaque `agentId` UUID.

## Agent State Machine

```
creating ──→ idle ──→ running ──→ idle     (turn complete)
    │          │         │
    │          └──→ stopped    └──→ stopped
    │
    └──→ error            └──→ error

stopped ──→ creating  (restart)
error   ──→ creating  (restart)
```

| State | Meaning |
|---|---|
| `creating` | `agent/create` accepted; background bootstrap in progress |
| `idle` | Bootstrap complete; agent is idle, ready for a prompt |
| `running` | Agent is processing an active prompt turn |
| `stopped` | Agent process is stopped; state is preserved |
| `error` | Bootstrap or runtime failure; agent is not operational |

Transition rules:

| From | Valid To | Trigger |
|---|---|---|
| `creating` | `idle`, `error` | bootstrap complete / fail |
| `idle` | `running`, `stopped` | `agent/prompt` dispatch / `agent/stop` |
| `running` | `idle`, `stopped`, `error` | turn done / `agent/stop` / runtime fail |
| `stopped` | `creating` | `agent/restart` |
| `error` | `creating` | `agent/restart` |

**State write authority**:
- `creating` is written by agentd at `agent/create` or `agent/restart`;
- all runtime states (`idle`, `running`, `stopped`, `error` post-shim) are written by the shim via `runtime/stateChange` notification; agentd syncs passively to DB.
- pre-shim `error` (bootstrap failure before shim is alive) is written by agentd.

## Bootstrap Contract

1. `workspace/create` prepares the source and sets `Workspace.Status.Phase = ready` and `Workspace.Status.Path`.
2. `agent/create` is **async configuration-only bootstrap**.
3. agentd validates `workspace` (must be `ready`), `name`, `runtimeClass`, and records agent metadata with `state: creating`. The workspace path is read from `Workspace.Status.Path` at bundle generation time.
4. agentd writes the bundle (`config.json`) and wires `agentRoot.path` to the workspace path. The `bootstrapConfig` (`spec.Config`) is persisted in `Agent.Status`.
5. The runtime performs ACP bootstrap (`initialize`, ACP `session/new`), and reaches bootstrap-complete / idle state. Shim writes `runtime/stateChange → idle`.
6. Actual work arrives later through `agent/prompt`.
7. Callers poll `agent/status` until state transitions out of `creating`.

`agent/create` returns immediately with `state: "creating"`.

## Async Create Semantics

`agent/create` is non-blocking.
The response is:

```json
{ "workspace": "backend-refactor", "name": "architect", "state": "creating" }
```

The caller polls `agent/status` to determine when the agent is ready:

```json
{ "workspace": "backend-refactor", "name": "architect", "state": "idle" }
```

Bootstrap errors surface as:

```json
{ "workspace": "backend-refactor", "name": "architect", "state": "error", "errorMessage": "..." }
```

## Stop and Delete Separation

| Operation | Effect | Requires |
|---|---|---|
| `agent/stop` | Stops the runtime process; preserves agent metadata and state | Agent in `idle` or `running` state |
| `agent/delete` | Removes agent record | Agent must be in `stopped` or `error` state |

Agents already in `error` may be deleted directly.

## Restart

`agent/restart` re-bootstraps an agent from `stopped` or `error` state:

1. Validates agent is `stopped` or `error`.
2. Transitions agent to `creating`.
3. Triggers background re-bootstrap using existing agent metadata.
4. Restart behavior is governed by `Agent.Spec.RestartPolicy`:
   - `tryReload`: After shim starts, attempt to read the previous ACP `sessionId` from the shim state file and call `session/load`. Falls back to a new session if the state file is missing, the `sessionId` is absent, or ACP does not support `session/load`.
   - `alwaysNew`: Always start a fresh ACP session regardless of prior state.
5. Caller polls `agent/status` until `idle` or `error`.

## `agent/stop` Boundary

| Scenario | Handling |
|---|---|
| shim alive | Call `runtime/stop`; `stopped` state driven by shim `runtime/stateChange` notification |
| shim already dead | agentd writes `Agent.Status.State = stopped` directly |
| agent already in terminal state | Idempotent; return success |
| agent not found | Return not found |

## Error State Contract

- `agent/prompt` is rejected for `error` agents;
- `agent/cancel` is rejected for `error` agents;
- `workspace/send` is rejected when the target agent is in `error`.

Restart preserves `workspace`, `name`, and bootstrap configuration.
It does not create a new agent identity.

## Recovery and Persistence Posture

After daemon restart, agent identity (`workspace` + `name`) is the recovery key.

Persisted recovery state per agent:

- `workspace`, `name`, `runtimeClass`, `restartPolicy`, bootstrap configuration (`bootstrapConfig`);
- shim socket path, state directory, shim PID for live process reconnect.

On daemon restart:

1. Scan all agents with `status.state IN (creating, idle, running)`.
2. For each, check whether `ShimPID` process is alive:
   - **Alive** → reconnect shim socket, sync current state via `runtime/status`.
   - **Dead** → apply `RestartPolicy`:
     - `tryReload`: restart shim; read ACP `sessionId` from shim state file; call `session/load`; fall back to new session on any failure.
     - `alwaysNew`: restart shim; always start a new ACP session.

## Environment and Capability Posture

- ACP remains the inner protocol between shim and agent;
- agentd exposes a curated ARI surface (`agent/*`, `workspace/*`, attach notifications);
- raw ACP client responsibilities remain behind the shim boundary.

## Security Boundary Summary

- local path attachment is host-impacting and must be canonicalized before registration;
- hooks execute as host commands and can have host-side effects before any agent prompt runs;
- agents in the same workspace share the same host path — no filesystem isolation;
- ACP capability exposure is intentionally narrower at the ARI boundary than at the shim boundary.
```

---

### `docs/design/agentd/ari-spec.md`

```markdown
# ARI — Agent Runtime Interface

ARI is the local control API between the orchestrator and agentd.
It is a runtime API for **realized objects**. It does not replace the orchestrator's desired-state contracts.

## Desired vs Realized

| Concept | Desired-state authority | Realized-state authority |
|---|---|---|
| Workspace intent | `docs/design/orchestrator/workspace-spec.md` | `workspace/*` in ARI |
| Agent bootstrap contract | runtime/config design docs | `agent/create` in ARI |
| Work execution | orchestrator policy | `agent/prompt` in ARI |

## Transport

- protocol: JSON-RPC 2.0 over Unix domain socket
- default path: `/run/agentd/agentd.sock`

## Workspace Methods

Workspace is the top-level resource. It serves as both the filesystem working directory and the agent grouping boundary.

### `workspace/create`

Create a workspace and trigger background source preparation.
Returns immediately; callers poll `workspace/status` until `phase` is `ready` or `error`.

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "workspace/create",
  "params": {
    "name": "backend-refactor",
    "source": {
      "type": "local",
      "local": { "path": "/home/user/project" }
    },
    "labels": { "project": "auth-service" }
  }
}
```

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "name": "backend-refactor",
    "phase": "pending"
  }
}
```

### `workspace/status`

Return workspace status including prepared path and member agents.

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "workspace/status",
  "params": { "name": "backend-refactor" }
}
```

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "name": "backend-refactor",
    "phase": "ready",
    "path": "/home/user/project",
    "members": [
      { "name": "architect", "runtimeClass": "claude", "state": "running" },
      { "name": "coder",     "runtimeClass": "codex",  "state": "idle" }
    ]
  }
}
```

### `workspace/list`

List all workspaces with their current phase.

### `workspace/delete`

Delete a workspace. Blocked if any agents still exist within it.
Runs teardown hooks; deletes managed directories; detaches local paths.

## Agent Methods

Agents are identified by `(workspace, name)` — there is no opaque `agentId`.

### `agent/create`

`agent/create` is **async configuration-only bootstrap**.
Returns immediately with `state: "creating"`.
Requires the workspace to be in `phase: ready`.
Callers poll `agent/status` until state transitions to `idle` or `error`.

```json
{
  "jsonrpc": "2.0",
  "id": 10,
  "method": "agent/create",
  "params": {
    "workspace": "backend-refactor",
    "name": "architect",
    "description": "Designs the module structure.",
    "runtimeClass": "claude",
    "restartPolicy": "tryReload",
    "systemPrompt": "You are a coding agent.",
    "labels": { "task": "auth-refactor" }
  }
}
```

```json
{
  "jsonrpc": "2.0",
  "id": 10,
  "result": {
    "workspace": "backend-refactor",
    "name": "architect",
    "state": "creating"
  }
}
```

| Field | Type | Required | Meaning |
|---|---|---|---|
| `workspace` | string | yes | Target Workspace (must be `phase: ready`) |
| `name` | string | yes | Member name inside that Workspace (unique within the workspace) |
| `description` | string | no | Human-readable description of the agent's role |
| `runtimeClass` | string | yes | Selects the registered runtime-class configuration |
| `restartPolicy` | string | no | `tryReload` (default) or `alwaysNew` |
| `systemPrompt` | string | no | Bootstrap configuration for ACP session creation |
| `labels` | map | no | Arbitrary metadata |

### `agent/prompt`

Work-entry path for an idle agent.

```json
{
  "jsonrpc": "2.0",
  "id": 11,
  "method": "agent/prompt",
  "params": {
    "workspace": "backend-refactor",
    "name": "architect",
    "prompt": "Refactor the auth module to use JWT tokens."
  }
}
```

Rejected when the agent is in `creating`, `stopped`, or `error`.

### `agent/cancel`

```json
{
  "jsonrpc": "2.0",
  "id": 12,
  "method": "agent/cancel",
  "params": { "workspace": "backend-refactor", "name": "architect" }
}
```

### `agent/stop`

```json
{
  "jsonrpc": "2.0",
  "id": 13,
  "method": "agent/stop",
  "params": { "workspace": "backend-refactor", "name": "architect" }
}
```

### `agent/delete`

Requires the agent to be in `stopped` or `error` state.

```json
{
  "jsonrpc": "2.0",
  "id": 14,
  "method": "agent/delete",
  "params": { "workspace": "backend-refactor", "name": "architect" }
}
```

### `agent/restart`

Transitions back to `creating`; callers poll until `idle` or `error`.

```json
{
  "jsonrpc": "2.0",
  "id": 15,
  "method": "agent/restart",
  "params": { "workspace": "backend-refactor", "name": "architect" }
}
```

### `agent/list`

```json
{
  "jsonrpc": "2.0",
  "id": 16,
  "method": "agent/list",
  "params": { "workspace": "backend-refactor", "state": "running" }
}
```

### `agent/status`

```json
{
  "jsonrpc": "2.0",
  "id": 17,
  "method": "agent/status",
  "params": { "workspace": "backend-refactor", "name": "architect" }
}
```

```json
{
  "jsonrpc": "2.0",
  "id": 17,
  "result": {
    "workspace": "backend-refactor",
    "name": "architect",
    "description": "Designs the module structure.",
    "runtimeClass": "claude",
    "restartPolicy": "tryReload",
    "state": "running",
    "runtimePid": 12345,
    "labels": { "task": "auth-refactor" },
    "createdAt": "2026-04-10T10:00:00Z",
    "updatedAt": "2026-04-10T10:01:00Z"
  }
}
```

Agent state values: `creating`, `idle`, `running`, `stopped`, `error`.

### `agent/attach`

```json
{
  "jsonrpc": "2.0",
  "id": 18,
  "method": "agent/attach",
  "params": { "workspace": "backend-refactor", "name": "architect" }
}
```

## Events

### `agent/update`

Typed runtime event stream for attached ARI clients.

### `agent/stateChange`

Agent lifecycle transition notification for attached ARI clients.

## Capability Posture

- exposed: workspace lifecycle, agent bootstrap, prompt delivery, cancellation, status, attach notifications;
- not exposed: raw ACP negotiation, `fs/*`, `terminal/*`, or other client-side ACP duties.
```


---

### `docs/DECISIONS.md` — superseded decisions

在 `## Superseded Decisions` 下新增：

```markdown
### D009: Metadata backend direction during contract convergence *(superseded)*

- **Superseded by:** OAR Platform Terminal State Refactor (`docs/plan/unified-state-design.md`)
- **Original choice:** Retain SQLite as the metadata backend.
- **Why superseded:** The terminal state refactor replaces SQLite (CGo) with bbolt (pure Go) as part of the full storage engine rewrite. All SQL schema, migration logic, and `mattn/go-sqlite3` dependency are removed.

### D018: Runtime bootstrap and identity contract *(superseded)*

- **Superseded by:** OAR Platform Terminal State Refactor (`docs/plan/unified-state-design.md`)
- **Original choice:** Document `session/new` as configuration-only bootstrap; keep OAR `sessionId` distinct from ACP `sessionId`; defer durable ID/bootstrap persistence.
- **Why superseded:** The terminal state refactor eliminates the Session concept entirely. Agent directly corresponds to one shim instance. ACP `sessionId` is not persisted to DB; it is read from the shim state file at recovery time.

### D015: Room ownership model *(superseded)*

- **Superseded by:** OAR Platform Terminal State Refactor (`docs/plan/unified-state-design.md`)
- **Original choice:** Treat Room Spec as orchestrator-owned desired state; agentd owns realized `room/*` projection.
- **Why superseded:** Room concept is eliminated. Workspace is now the unified grouping + filesystem working directory resource. `docs/design/orchestrator/room-spec.md` is deleted.

### D019: Room ownership and room/* API semantics *(superseded)*

- **Superseded by:** OAR Platform Terminal State Refactor (`docs/plan/unified-state-design.md`)
- **Original choice:** Treat Room Spec as orchestrator-owned desired state; `room/*` as realized runtime projection; `session/new` configuration-only.
- **Why superseded:** Room/Namespace is renamed to Workspace. Workspace is now the unified grouping + filesystem working directory resource. `room/*` / `namespace/*` APIs become `workspace/*`. The Session concept is eliminated; identity is `(workspace, name)` with no opaque UUID.

### D032: Session recovery config persistence strategy *(superseded)*

- **Superseded by:** OAR Platform Terminal State Refactor (`docs/plan/unified-state-design.md`)
- **Original choice:** Add discrete columns plus JSON blob column to sessions table.
- **Why superseded:** Sessions table is deleted. Recovery state (`shimSocketPath`, `shimStateDir`, `shimPID`, `bootstrapConfig`) is stored directly in `Agent.Status` in bbolt.

### D033: Recovery failure posture for unreachable shims *(superseded)*

- **Superseded by:** OAR Platform Terminal State Refactor (`docs/plan/unified-state-design.md`)
- **Original choice:** Mark sessions as stopped when shim socket fails during recovery.
- **Why superseded:** No Session concept. Recovery operates on Agent records directly. Same fail-closed posture is preserved: unreachable shim → Agent state set to `stopped`. RestartPolicy governs whether and how the shim is restarted.
```

