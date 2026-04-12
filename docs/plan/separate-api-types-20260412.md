# 方案：API 类型定义与业务逻辑分离

## 方案

### 背景与目标

当前 `pkg/meta` 和 `pkg/spec` 将 API 类型定义（结构体、常量、类型别名）与业务逻辑（bbolt CRUD、文件 I/O）混合在一起：

- **pkg/meta**: `ObjectMeta`, `Agent`, `AgentRun`, `Workspace` 等结构体定义与 `Store` 的 bbolt CRUD 操作混在同一个包
- **pkg/spec**: `Config`, `State`, `Status`, `EnvVar` 等类型定义与 `ParseConfig`, `WriteState`, `ReadState` 等文件 I/O 操作混在同一个包

这导致：
1. 任何想复用类型定义的包都被迫引入存储依赖（bbolt）或文件 I/O 依赖
2. 类型无法在不同层之间最大化复用
3. 与 k8s 的 `api/` + `pkg/` 分层模式不一致

**目标**：参考 k8s 做法，创建顶层 `api/` 目录，只放结构体定义和必要的帮助函数（String、IsValid 等纯函数），实现最大程度的结构体复用。

### 新目录结构

```
api/                                   # NEW: 纯 API 类型定义，无 I/O，无业务逻辑
├── types.go                           # package api: 跨 group 共享的基础类型（Status, EnvVar）
├── meta/
│   └── types.go                       # package meta: 编排层 API 对象
└── spec/
    ├── types.go                       # package spec: 运行时 Config 规范类型
    └── state.go                       # package spec: 运行时 State 类型

pkg/
├── store/                             # RENAMED from pkg/meta: 仅保留 Store + CRUD
│   ├── store.go                       # Store 结构体、NewStore、Close
│   ├── agent.go                       # Agent 定义 CRUD（来自当前 runtime.go 中的 CRUD 部分）
│   ├── agentrun.go                    # AgentRun CRUD（来自当前 agent.go 中的全部内容）
│   └── workspace.go                   # Workspace CRUD
└── spec/                              # KEEP: 保留原包名，仅移除类型定义
    ├── config.go                      # ParseConfig, ValidateConfig, ResolveAgentRoot
    ├── state.go                       # WriteState, ReadState, DeleteState, StateDir, EventLogPath, ShimSocketPath, ValidateShimSocketPath
    ├── maxsockpath_darwin.go
    ├── maxsockpath_linux.go
    ├── testdata/                      # 保留：测试用的 example bundles
    ├── config_test.go
    ├── state_test.go
    └── example_bundles_test.go
```

### Import 路径

```go
// API 类型（无外部依赖，只有标准库）
import "github.com/zoumo/oar/api"          // api.Status, api.EnvVar
import "github.com/zoumo/oar/api/meta"      // meta.Agent, meta.ObjectMeta
import "github.com/zoumo/oar/api/spec"      // spec.Config, spec.State

// 业务逻辑
import "github.com/zoumo/oar/pkg/store"     // store.Store, store.NewStore
import "github.com/zoumo/oar/pkg/spec"      // spec.ParseConfig, spec.WriteState
```

**api/spec 与 pkg/spec 冲突处理**：`pkg/spec` 保留原名不重命名（原因见下方分析）。共 3 个文件需同时导入 `api/spec`（类型）和 `pkg/spec`（I/O 函数），统一使用 `apispec` 作为 `api/spec` 的别名：

```go
import (
    apispec "github.com/zoumo/oar/api/spec"
    "github.com/zoumo/oar/pkg/spec"
)
// 使用：apispec.Config, spec.ParseConfig()
```

### 不重命名 pkg/spec 的原因

Codex 正确指出：`pkg/spec/state.go` 中的 `WriteState`、`ReadState`、`DeleteState`、`StateDir`、`EventLogPath`、`ShimSocketPath`、`ValidateShimSocketPath` 操作的是 shim state dir、event log、Unix socket path，**不是 bundle 概念**。`state.json` 位于 shim state dir（`/run/agentd/shim/{id}/state.json`），不在 bundle 目录下。

因此 `pkg/bundle` 这个名字会引入概念错误，与 `docs/design/runtime/runtime-spec.md` 中 bundle/state/socket 的职责划分不一致。

保留 `pkg/spec` 作为 "OAR Runtime Spec 操作函数" 的包名是准确的——它提供对 spec 定义的文件格式（config.json、state.json）的读写操作。仅 3 个冲突文件需要 alias，成本可接受。

### 类型迁移详情

#### api/types.go（package api）

来源 `pkg/spec/state_types.go`：
- `Status` 类型 + 常量（`StatusCreating`, `StatusIdle`, `StatusRunning`, `StatusStopped`, `StatusError`）
- `Status.String()` 方法

来源 `pkg/spec/types.go`：
- `EnvVar` 结构体

**放在根 api 包的原因**：`Status` 被 `api/meta`（AgentRunStatus.State, AgentRunFilter.State）和 `api/spec`（State.Status）同时使用；`EnvVar` 被 `api/meta`（AgentSpec.Env）和 `api/spec`（McpServer.Env）同时使用。放在根包可避免 meta↔spec 循环依赖。

#### api/meta/types.go（package meta）

来源 `pkg/meta/models.go`：
- `ObjectMeta` 结构体
- `AgentRun`, `AgentRunSpec`, `AgentRunStatus`, `AgentRunFilter` 结构体
- `Workspace`, `WorkspaceSpec`, `WorkspaceStatus`, `WorkspaceFilter` 结构体
- `WorkspacePhase` 类型 + 常量（`WorkspacePhasePending`, `WorkspacePhaseReady`, `WorkspacePhaseError`）
- `RestartPolicyTryReload`, `RestartPolicyAlwaysNew` 常量

来源 `pkg/meta/runtime.go`（仅类型定义）：
- `Agent`, `AgentSpec` 结构体

**依赖**：`import "github.com/zoumo/oar/api"` — 仅依赖 api.Status 和 api.EnvVar。

#### api/spec/types.go（package spec）

来源 `pkg/spec/types.go`：
- `Config`, `AgentRoot`, `Metadata`, `AcpAgent`, `AcpProcess`, `AcpSession`, `McpServer` 结构体
- `PermissionPolicy` 类型 + 常量（`ApproveAll`, `ApproveReads`, `DenyAll`）+ `IsValid()`, `String()` 方法

**依赖**：`import "github.com/zoumo/oar/api"` — 仅依赖 api.EnvVar（在 McpServer.Env 中使用）。

#### api/spec/state.go（package spec）

来源 `pkg/spec/state_types.go`：
- `State`, `LastTurn` 结构体

**依赖**：`import "github.com/zoumo/oar/api"` — 仅依赖 api.Status（在 State.Status 中使用）。

### pkg/store 方法签名（pkg/meta → pkg/store）

**原则**：`pkg/store` 不定义任何业务类型 alias，所有 CRUD 方法直接引用 `api/meta` 和 `api` 包的类型。

#### Store 生命周期（store.go）

```go
package store

import "github.com/zoumo/oar/api/meta"

type Store struct { ... }  // bbolt 连接 + logger

func NewStore(path string) (*Store, error)
func (s *Store) Close() error
```

#### Agent 定义 CRUD（agent.go）

```go
package store

import (
    "context"
    "github.com/zoumo/oar/api/meta"
)

func (s *Store) SetAgent(ctx context.Context, ag *meta.Agent) error
func (s *Store) GetAgent(ctx context.Context, name string) (*meta.Agent, error)
func (s *Store) ListAgents(ctx context.Context) ([]*meta.Agent, error)
func (s *Store) DeleteAgent(ctx context.Context, name string) error
```

#### AgentRun CRUD（agentrun.go）

```go
package store

import (
    "context"
    "github.com/zoumo/oar/api"
    "github.com/zoumo/oar/api/meta"
)

func (s *Store) CreateAgentRun(ctx context.Context, agent *meta.AgentRun) error
func (s *Store) GetAgentRun(ctx context.Context, workspace, name string) (*meta.AgentRun, error)
func (s *Store) ListAgentRuns(ctx context.Context, filter *meta.AgentRunFilter) ([]*meta.AgentRun, error)
func (s *Store) UpdateAgentRunStatus(ctx context.Context, workspace, name string, status meta.AgentRunStatus) error
func (s *Store) TransitionAgentRunState(ctx context.Context, workspace, name string, expected, next api.Status) (bool, error)
func (s *Store) DeleteAgentRun(ctx context.Context, workspace, name string) error
```

#### Workspace CRUD（workspace.go）

```go
package store

import (
    "context"
    "github.com/zoumo/oar/api/meta"
)

func (s *Store) CreateWorkspace(ctx context.Context, ws *meta.Workspace) error
func (s *Store) GetWorkspace(ctx context.Context, name string) (*meta.Workspace, error)
func (s *Store) ListWorkspaces(ctx context.Context, filter *meta.WorkspaceFilter) ([]*meta.Workspace, error)
func (s *Store) UpdateWorkspaceStatus(ctx context.Context, name string, status meta.WorkspaceStatus) error
func (s *Store) DeleteWorkspace(ctx context.Context, name string) error
```

### 消费者更新（影响范围）

| 消费者包 | 当前导入 | 变更后导入 |
|---------|---------|----------|
| pkg/agentd/agent.go | `pkg/meta`, `pkg/spec` | `api`, `api/meta`, `pkg/store` |
| pkg/agentd/process.go | `pkg/meta`, `pkg/spec` | `api`, `api/meta`, `apispec "api/spec"`, `pkg/store`, `pkg/spec` |
| pkg/agentd/recovery.go | `pkg/meta`, `pkg/spec` | `api`, `api/meta`, `pkg/store` |
| pkg/agentd/shim_client.go | `pkg/spec` | `api` |
| pkg/ari/server.go | `pkg/meta`, `pkg/spec` | `api`, `api/meta`, `pkg/store` |
| pkg/ari/registry.go | `pkg/meta` | `api/meta`, `pkg/store` |
| pkg/ari/types.go | `pkg/spec` | `api` |
| pkg/workspace/manager.go | `pkg/meta` | `api/meta`, `pkg/store` |
| pkg/rpc/server.go | `pkg/spec` | `api`, `api/spec` |
| pkg/runtime/client.go | `pkg/spec` | `api`, `api/spec` |
| pkg/runtime/runtime.go | `pkg/spec` | `api`, `apispec "api/spec"`, `pkg/spec` |
| cmd/agentd/.../server/command.go | `pkg/meta` | `pkg/store` |
| cmd/agentd/.../shim/command.go | `pkg/spec` | `apispec "api/spec"`, `pkg/spec` |
| cmd/agentdctl/.../agent/command.go | `pkg/meta` | `api/meta` |

**api/spec vs pkg/spec 冲突文件**（使用 `apispec` alias）：
- `pkg/agentd/process.go` — 需要 `apispec.Config` (类型) + `spec.ValidateConfig()` (函数)
- `pkg/runtime/runtime.go` — 需要 `apispec.Config` (类型) + `spec.ValidateConfig()` (函数)
- `cmd/agentd/.../shim/command.go` — 需要 `apispec.Config` (类型) + `spec.ParseConfig()` (函数)

### 执行步骤

**Phase 1: 创建 api/ 目录并迁移类型**
1. 创建 `api/types.go` — 移入 Status（类型 + 5 个常量 + String 方法）、EnvVar
2. 创建 `api/meta/types.go` — 移入 ObjectMeta, Agent, AgentSpec, AgentRun, AgentRunSpec, AgentRunStatus, AgentRunFilter, Workspace, WorkspaceSpec, WorkspaceStatus, WorkspaceFilter, WorkspacePhase + 全部常量
3. 创建 `api/spec/types.go` — 移入 Config, AgentRoot, Metadata, AcpAgent, AcpProcess, AcpSession, McpServer, PermissionPolicy + 常量 + IsValid/String 方法
4. 创建 `api/spec/state.go` — 移入 State, LastTurn

**Phase 2: 重命名 pkg/meta → pkg/store**
5. 将 `pkg/meta/` 目录重命名为 `pkg/store/`
6. 修改所有 `pkg/store/*.go` 文件的 package 声明为 `package store`
7. 删除 `pkg/store/models.go`（类型已迁到 api/meta）
8. 清理 `pkg/store/agent.go`（原 runtime.go，仅保留 Agent CRUD 方法，删除 Agent/AgentSpec 类型定义）
9. 重命名 `pkg/store/agent.go`（原 agent.go，AgentRun CRUD）为 `pkg/store/agentrun.go`
10. 更新 `pkg/store/` 所有文件的 import，引用 `api` 和 `api/meta`

**Phase 3: 更新 pkg/spec（移除类型定义，保留 I/O 函数）**
11. 从 `pkg/spec/types.go` 删除所有类型和常量定义（Config, EnvVar, PermissionPolicy 等已迁到 api/）
12. 从 `pkg/spec/state_types.go` 删除所有类型和常量定义（Status, State, LastTurn 已迁到 api/）
13. 如果 `types.go` 和 `state_types.go` 变为空文件则删除
14. 更新 `pkg/spec/config.go` 和 `pkg/spec/state.go` 的 import，引用 `api` 和 `api/spec`（此处 pkg/spec 内部引用 api/spec 需要 alias：`apispec "github.com/zoumo/oar/api/spec"`）

**Phase 4: 更新所有消费者 import**
15. 按上方消费者表逐文件更新 import 和类型引用
16. 更新所有 `_test.go` 文件的 import（包括 `pkg/store/*_test.go`, `pkg/spec/*_test.go`, `tests/integration/*_test.go`）

**Phase 5: 同步设计文档和约定文档**
17. 更新 `docs/design/roadmap.md`：
    - `pkg/spec` 行改为：`api/spec + pkg/spec  — API types in api/spec; config parsing & state I/O in pkg/spec`
    - `pkg/meta` 行改为：`api/meta + pkg/store  — API types in api/meta; bbolt persistence in pkg/store`
    - 新增 `api/` 行说明：`api/               — Pure API type definitions (Status, EnvVar, meta objects, spec objects)`
18. 更新 `docs/CONVENTIONS.md`：
    - K030 的测试命令不变：`go test ./pkg/spec -run TestExampleBundlesAreValid`（pkg/spec 保留原名）
    - K005 的引用路径不变：`pkg/spec/config.go`（config.go 留在 pkg/spec）

**Phase 6: 构建和验证**
19. 运行以下命令，全部通过才算完成：

```bash
# 1. 编译验证
make build

# 2. 全量单元测试
go test ./...

# 3. Contract/example bundle 专项验证（路径不变，因为 pkg/spec 保留）
go test ./pkg/spec -run TestExampleBundlesAreValid

# 4. 验证 api/ 包无外部依赖（应只有标准库和当前 module）
#    期望输出为空；若非空，则说明 api/ 引入了模块外第三方依赖
go list -deps -f '{{if and (not .Standard) (not (eq .Module.Path "github.com/zoumo/oar"))}}{{.ImportPath}}{{end}}' ./api/... | sed '/^$/d'

# 5. 验证 contract 脚本（如存在）
scripts/verify-m002-s01-contract.sh
```

### 依赖关系图（变更后）

```
api/               ← 无外部依赖（仅标准库）
  ↑
api/meta/          ← 依赖 api/（使用 api.Status, api.EnvVar）
  ↑
api/spec/          ← 依赖 api/（使用 api.EnvVar, api.Status）
  ↑
pkg/store/         ← 依赖 api/, api/meta, bbolt
pkg/spec/          ← 依赖 api/, api/spec（alias: apispec）, 标准库 I/O
  ↑
pkg/agentd/        ← 依赖 api/, api/meta, api/spec(alias), pkg/store, pkg/spec
pkg/ari/           ← 依赖 api/, api/meta, pkg/store
pkg/workspace/     ← 依赖 api/meta, pkg/store
pkg/runtime/       ← 依赖 api/, api/spec(alias), pkg/spec
```

### 风险与取舍

1. **大量 import 路径变更**：约 14 个非测试文件 + 对应测试文件需要更新 import。但由于不考虑兼容性，这是纯机械替换工作。

2. **api/spec 与 pkg/spec 同名**：仅 3 个文件（`pkg/agentd/process.go`, `pkg/runtime/runtime.go`, `cmd/agentd/.../shim/command.go`）需要同时导入两者，统一使用 `apispec` alias。其余文件只导入其中一个，无冲突。不重命名 `pkg/spec` 是因为其职责（OAR spec 文件的 I/O 操作）与包名匹配，而 `pkg/bundle` 会引入概念错误。

3. **pkg/meta → pkg/store 重命名**：变更范围较大（10 个非测试文件），但消除了与 `api/meta` 的包名冲突，长期维护更清晰。`store.Store` 读起来略有冗余，但 `store.NewStore()` 在 Go 中是惯用模式。

4. **文件名调整（agent.go↔agentrun.go）**：当前 `pkg/meta/agent.go` 实际是 AgentRun 的 CRUD，`pkg/meta/runtime.go` 包含 Agent 的 CRUD。趁重命名时修正文件名：AgentRun CRUD → `agentrun.go`，Agent CRUD → `agent.go`。

## 审查记录

### codex 第1轮

#### 认可项

1. **分离方向合理**：把 `pkg/meta`、`pkg/spec` 中可复用的结构体、常量和纯方法迁到顶层 `api/`，可以消除类型消费者被迫依赖 bbolt / 文件 I/O 的问题，方向与 k8s `api/` + `pkg/` 分层一致。
2. **`pkg/meta` 重命名为 `pkg/store` 的动机成立**：当前 `pkg/meta` 同时承担 API 对象和 bbolt CRUD，重命名后职责更清楚，也避免与 `api/meta` 同名造成长期 alias 成本。
3. **共享 `Status` / `EnvVar` 的循环依赖分析基本成立**：`AgentRunStatus`、runtime `State`、Agent `Env` 和 MCP server `Env` 确实跨 meta/spec 使用，放到根 `api` 包可以避免 `api/meta` 与 `api/spec` 互相依赖。

#### 问题项

1. **方案对 `pkg/spec` 是否重命名没有收敛，文档内部自相矛盾。**
   - 问题是什么：前面的"新目录结构""pkg 层变更""消费者更新"多处写 `pkg/spec` 保留；后面的冲突分析又确认 3 个文件会同时导入 `api/spec` 和 `pkg/spec`，并建议重命名为 `pkg/bundle`；执行步骤和依赖图则按 `pkg/bundle` 执行。
   - 为什么是问题：执行者无法判断最终目标是"保留 `pkg/spec` + alias"还是"重命名为 `pkg/bundle`"。这会影响目录移动、包名、import 替换、测试包路径、文档更新和后续 API 命名。
   - 期望如何解决：在修订方案中明确选择一个最终方案，并把目录结构、Import 路径、消费者表、执行步骤、依赖图、风险与测试命令全部统一。如果选择 `pkg/spec` 保留，需要明确 3 个冲突文件的 alias 规则；如果选择重命名，需要继续解决下一个问题中的职责边界。

2. **`pkg/spec` → `pkg/bundle` 的命名和职责与现有代码事实不匹配。**
   - 问题是什么：现有 `pkg/spec/state.go` 包含 `WriteState`、`ReadState`、`DeleteState`、`StateDir`、`EventLogPath`、`ShimSocketPath`、`ValidateShimSocketPath`。这些函数操作的是 shim state dir、event log、Unix socket path，不是 bundle。方案却把整个 `pkg/spec` 重命名为 `pkg/bundle`，并描述为"主要操作 bundle 目录下的 config.json 和 state.json"。
   - 为什么是问题：`state.json` 当前不是 bundle 下的文件，socket/event-log 路径也不是 bundle 概念。把这些 API 放进 `pkg/bundle` 会引入新的概念错误，和 `docs/design/runtime/runtime-spec.md`、`docs/design/agentd/agentd.md` 中 bundle/state/socket 的职责划分不一致。
   - 期望如何解决：二选一：保留 `pkg/spec` 并仅在冲突处 alias；或者拆成更准确的包，例如 config/bundle I/O 与 runtime state/socket I/O 分开。若仍坚持 `pkg/bundle`，必须重写职责说明并说明为什么 state/socket 属于 bundle，且同步设计文档。

3. **`pkg/store` 的 CRUD 方法签名没有明确，Go 代码无法按当前描述直接落地。**
   - 问题是什么：方案说 `pkg/store` "仅保留 Store + CRUD" 并 import `api/meta`，但没有明确方法签名是直接使用 `*meta.AgentRun` / `meta.AgentRunStatus` / `*meta.Workspace`，还是在 `pkg/store` 中定义 type alias（如 `type AgentRun = meta.AgentRun`）。
   - 为什么是问题：这决定所有消费者如何调用 store。若不定义 alias，`CreateAgentRun` 等方法签名必须显式写成 `func (s *Store) CreateAgentRun(ctx context.Context, agent *meta.AgentRun) error`；若定义 alias，`pkg/store` 又会重新暴露 API 类型，削弱"仅 CRUD"的边界。当前方案没有给出可验证的目标 API。
   - 期望如何解决：明确 `pkg/store` 不定义业务类型 alias，并列出关键方法的新签名；或明确选择 alias 并解释为什么不违反分层目标。建议优先采用直接引用 `api/meta` 类型的签名。

4. **文档/设计一致性更新缺失。**
   - 问题是什么：仓库约束要求 Code changes 必须与 docs/design 对齐。当前方案只修改代码和测试，没有列出需要更新的设计/约定文档。实际引用包括 `docs/design/roadmap.md` 中的 `pkg/spec` / `pkg/meta` 目录说明，以及 `docs/CONVENTIONS.md` 的 `go test ./pkg/spec -run TestExampleBundlesAreValid`、`pkg/spec/config.go` 约定。
   - 为什么是问题：如果代码迁到 `api/`、`pkg/store`、可能的 `pkg/bundle`，设计文档会立即过期；如果测试命令路径仍指向旧包，后续验证也会误导执行者。
   - 期望如何解决：新增一个 Phase 或步骤，列出必须同步更新的文档和验证命令，至少覆盖 `docs/design/roadmap.md`、`docs/CONVENTIONS.md`，以及任何仍引用旧包路径的设计/脚本说明。若某些生成的 slide HTML 不打算维护，也需要明确排除理由。

5. **迁移后的验证矩阵不够精确。**
   - 问题是什么：方案最后只写"运行 `make build`"和"运行测试"，但这次重命名会影响包路径、测试目录和 contract/conventions 中的特定验证命令。
   - 为什么是问题：只跑笼统测试不便于确认设计约束是否仍成立，尤其是如果 `pkg/spec` 重命名，原 `go test ./pkg/spec -run TestExampleBundlesAreValid` 不再存在。
   - 期望如何解决：明确最终验证命令，例如 `make build`、`go test ./...`，以及替代后的 contract/example bundle 测试命令。若仍保留 `pkg/spec`，则保留现有专项命令；若重命名，必须写出新包路径。

### claude-code 第2轮

逐条回应 codex 的反馈：

**问题 1（pkg/spec 是否重命名没有收敛）**：接受。修订方案中明确选择 **保留 `pkg/spec` 原名**，统一了目录结构、Import 路径、消费者表、执行步骤、依赖图、风险和测试命令。3 个冲突文件统一使用 `apispec` alias。

**问题 2（pkg/bundle 命名与职责不匹配）**：接受。完全放弃 `pkg/bundle` 方案。`pkg/spec` 中的 state/socket/event-log 函数确实不是 bundle 概念，保留 `pkg/spec` 原名并在冲突处 alias 是正确做法。

**问题 3（pkg/store CRUD 方法签名不明确）**：接受。修订方案中明确：`pkg/store` 不定义任何业务类型 alias，所有 CRUD 方法签名直接引用 `api/meta` 和 `api` 包的类型。已列出全部 15 个方法的完整签名。

**问题 4（文档/设计一致性更新缺失）**：接受。新增 Phase 5 覆盖 `docs/design/roadmap.md` 和 `docs/CONVENTIONS.md`。由于 `pkg/spec` 保留原名，CONVENTIONS 中的 K030 测试命令和 K005 引用路径不需要修改（路径不变）。

**问题 5（验证矩阵不够精确）**：接受。Phase 6 现在列出了 5 项具体验证命令，包括 `make build`、`go test ./...`、专项 `TestExampleBundlesAreValid`（路径不变）、api/ 包无外部依赖检查、contract 脚本。

### codex 第2轮

#### ✅ 认可项

1. **`pkg/spec` 决策已收敛**：明确保留 `pkg/spec`，并只在 `pkg/agentd/process.go`、`pkg/runtime/runtime.go`、`cmd/agentd/.../shim/command.go` 使用 `apispec` alias，解决了上一轮的方案分叉问题。
2. **放弃 `pkg/bundle` 是正确取舍**：修订方案已说明 state/socket/event-log 不属于 bundle 概念，避免了把 runtime state 职责错误归入 bundle 的设计偏差。
3. **`pkg/store` API 边界已明确**：不定义业务类型 alias、CRUD 方法直接引用 `api/meta` 和 `api.Status`，边界清楚且可落地。
4. **文档同步范围基本足够**：新增 Phase 5 覆盖 `docs/design/roadmap.md` 和 `docs/CONVENTIONS.md`，与当前“保留 `pkg/spec`”的最终决策一致。

#### ❌ 问题项

1. **api 无外部依赖检查命令不能证明目标，可能漏报外部依赖。**
   - 问题是什么：Phase 6 中的命令 `go list -deps ./api/... | grep -v 'github.com/open-agent-d' | grep -v '^[a-z]'` 会过滤掉所有以小写字母开头的 import path。Go 标准库以小写字母开头，但外部依赖（例如 `go.etcd.io/bbolt`、`golang.org/x/...`）同样以小写字母开头，因此也会被过滤掉。
   - 为什么是问题：方案声明 `api/` 必须无外部依赖，这是本次分层的核心约束之一。当前命令即使 `api/meta` 误引入了 bbolt，也可能输出 OK，给出错误的验证信号。
   - 期望如何解决：将该验证命令替换为基于 `go list -deps -f` 的标准库判断，例如对 `./api/...` 统一执行：

```bash
go list -deps -f '{{if and (not .Standard) (not (eq .Module.Path "github.com/zoumo/oar"))}}{{.ImportPath}}{{end}}' ./api/... | sed '/^$/d'
```

   期望输出为空；若非空，则说明 `api/` 引入了模块外部依赖。也可以使用等价脚本，但必须明确能区分标准库、当前 module 和第三方 module。

### claude-code 第3轮

**问题 1（api 无外部依赖检查命令不能证明目标）**：接受。已将 Phase 6 的验证命令替换为 `go list -deps -f '{{if and (not .Standard) (not (eq .Module.Path "..."))}}...'` 模式，能正确区分标准库、当前 module 和第三方 module。期望输出为空，非空即说明 api/ 引入了外部依赖。

### codex 第3轮

#### ✅ 认可项

1. **第2轮剩余问题已解决**：Phase 6 的 `api/` 依赖检查已替换为基于 `go list -deps -f`、`.Standard` 和 `.Module.Path` 的命令，能够正确识别模块外第三方依赖，满足“api/ 仅依赖标准库和当前 module”的验证目标。
2. **方案整体已收敛且可执行**：最终方案明确保留 `pkg/spec`、重命名 `pkg/meta` 为 `pkg/store`、将纯类型迁入 `api/`，并补齐消费者更新、文档同步和验证矩阵。当前剩余风险属于执行阶段的机械迁移风险，可通过列出的 `make build`、`go test ./...` 和专项验证覆盖。

#### ❌ 问题项

无阻塞问题。

## 最终方案

审查通过，以下为最终可执行方案。

### 目录结构

```
api/                                   # NEW: 纯 API 类型定义，无 I/O，无业务逻辑
├── types.go                           # package api: Status + 5 常量 + String(), EnvVar
├── meta/
│   └── types.go                       # package meta: ObjectMeta, Agent, AgentSpec, AgentRun,
│                                      #   AgentRunSpec, AgentRunStatus, AgentRunFilter,
│                                      #   Workspace, WorkspaceSpec, WorkspaceStatus,
│                                      #   WorkspaceFilter, WorkspacePhase + 全部常量
└── spec/
    ├── types.go                       # package spec: Config, AgentRoot, Metadata, AcpAgent,
    │                                  #   AcpProcess, AcpSession, McpServer, PermissionPolicy + 常量 + IsValid/String
    └── state.go                       # State, LastTurn

pkg/
├── store/                             # RENAMED from pkg/meta
│   ├── store.go                       # Store 结构体、NewStore、Close
│   ├── agent.go                       # Agent 定义 CRUD (SetAgent, GetAgent, ListAgents, DeleteAgent)
│   ├── agentrun.go                    # AgentRun CRUD (CreateAgentRun, GetAgentRun, ListAgentRuns, UpdateAgentRunStatus, TransitionAgentRunState, DeleteAgentRun)
│   └── workspace.go                   # Workspace CRUD (CreateWorkspace, GetWorkspace, ListWorkspaces, UpdateWorkspaceStatus, DeleteWorkspace)
└── spec/                              # KEEP: 保留原包名，仅移除类型定义
    ├── config.go                      # ParseConfig, ValidateConfig, ResolveAgentRoot
    ├── state.go                       # WriteState, ReadState, DeleteState, StateDir, EventLogPath, ShimSocketPath, ValidateShimSocketPath
    ├── maxsockpath_darwin.go
    └── maxsockpath_linux.go
```

### pkg/store 方法签名

`pkg/store` 不定义任何业务类型 alias，所有方法直接引用 `api/meta` 和 `api` 类型：

```go
// store.go
func NewStore(path string) (*Store, error)
func (s *Store) Close() error

// agent.go
func (s *Store) SetAgent(ctx context.Context, ag *meta.Agent) error
func (s *Store) GetAgent(ctx context.Context, name string) (*meta.Agent, error)
func (s *Store) ListAgents(ctx context.Context) ([]*meta.Agent, error)
func (s *Store) DeleteAgent(ctx context.Context, name string) error

// agentrun.go
func (s *Store) CreateAgentRun(ctx context.Context, agent *meta.AgentRun) error
func (s *Store) GetAgentRun(ctx context.Context, workspace, name string) (*meta.AgentRun, error)
func (s *Store) ListAgentRuns(ctx context.Context, filter *meta.AgentRunFilter) ([]*meta.AgentRun, error)
func (s *Store) UpdateAgentRunStatus(ctx context.Context, workspace, name string, status meta.AgentRunStatus) error
func (s *Store) TransitionAgentRunState(ctx context.Context, workspace, name string, expected, next api.Status) (bool, error)
func (s *Store) DeleteAgentRun(ctx context.Context, workspace, name string) error

// workspace.go
func (s *Store) CreateWorkspace(ctx context.Context, ws *meta.Workspace) error
func (s *Store) GetWorkspace(ctx context.Context, name string) (*meta.Workspace, error)
func (s *Store) ListWorkspaces(ctx context.Context, filter *meta.WorkspaceFilter) ([]*meta.Workspace, error)
func (s *Store) UpdateWorkspaceStatus(ctx context.Context, name string, status meta.WorkspaceStatus) error
func (s *Store) DeleteWorkspace(ctx context.Context, name string) error
```

### api/spec vs pkg/spec 冲突规则

`pkg/spec` 保留原名。3 个需同时导入的文件统一使用 `apispec` alias：
- `pkg/agentd/process.go`
- `pkg/runtime/runtime.go`
- `cmd/agentd/.../shim/command.go`

```go
import (
    apispec "github.com/zoumo/oar/api/spec"
    "github.com/zoumo/oar/pkg/spec"
)
```

### 执行步骤

**Phase 1: 创建 api/ 目录并迁移类型**
1. 创建 `api/types.go` — Status（类型 + 5 常量 + String）、EnvVar
2. 创建 `api/meta/types.go` — ObjectMeta, Agent, AgentSpec, AgentRun, AgentRunSpec, AgentRunStatus, AgentRunFilter, Workspace, WorkspaceSpec, WorkspaceStatus, WorkspaceFilter, WorkspacePhase + 全部常量
3. 创建 `api/spec/types.go` — Config, AgentRoot, Metadata, AcpAgent, AcpProcess, AcpSession, McpServer, PermissionPolicy + 常量 + IsValid/String
4. 创建 `api/spec/state.go` — State, LastTurn

**Phase 2: 重命名 pkg/meta → pkg/store**
5. 将 `pkg/meta/` 目录重命名为 `pkg/store/`
6. 修改所有 `pkg/store/*.go` 的 package 声明为 `package store`
7. 删除 `pkg/store/models.go`（类型已迁到 api/meta）
8. 清理 `pkg/store/agent.go`（原 runtime.go，删除 Agent/AgentSpec 类型定义，仅保留 CRUD）
9. 重命名 `pkg/store/agent.go`（原 agent.go，AgentRun CRUD）为 `pkg/store/agentrun.go`
10. 更新 `pkg/store/` 所有文件的 import

**Phase 3: 更新 pkg/spec（移除类型定义，保留 I/O 函数）**
11. 从 `pkg/spec/types.go` 删除所有类型和常量（已迁到 api/）
12. 从 `pkg/spec/state_types.go` 删除所有类型和常量（已迁到 api/）
13. 空文件则删除
14. 更新 `pkg/spec/config.go` 和 `pkg/spec/state.go` 的 import（`apispec "...api/spec"`）

**Phase 4: 更新所有消费者 import**
15. 按消费者表逐文件更新 import 和类型引用
16. 更新所有 `_test.go` 文件的 import

**Phase 5: 同步设计文档**
17. 更新 `docs/design/roadmap.md`：更新 pkg/spec、pkg/meta 行，新增 api/ 行
18. `docs/CONVENTIONS.md` 中 K030 和 K005 路径不变（pkg/spec 保留）

**Phase 6: 构建和验证**
19. 全部通过才算完成：
```bash
make build
go test ./...
go test ./pkg/spec -run TestExampleBundlesAreValid
go list -deps -f '{{if and (not .Standard) (not (eq .Module.Path "github.com/zoumo/oar"))}}{{.ImportPath}}{{end}}' ./api/... | sed '/^$/d'
scripts/verify-m002-s01-contract.sh
```
