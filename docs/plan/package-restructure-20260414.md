# Package Restructure Plan

Date: 2026-04-14

## Context

当前项目结构存在以下问题：

1. **`api/` 职责越界** — 原本只应放 type 定义，但 `api/ari/` 和 `api/shim/` 中包含了 client、service registration 等实现代码
2. **`pkg/ari/` 和 `pkg/shim/` 不完整** — 重构时只完成了最小集，部分文件未迁移
3. **`pkg/events/` 定位错误** — 事件类型和翻译器本质上属于 shim 协议层
4. **`pkg/runtime/` 定位错误** — ACP runtime 实现应该是 shim 的一个 runtime 子包
5. **迁移后对应的测试文件被删除**

## Target Structure

原则：**api/ 子目录只放纯数据类型（struct/const/enum），interface 和 func 放到 server/ 或 client/**

```
pkg/
  runtime-spec/                 ← renamed from pkg/spec (package runtimespec)
    api/
      config.go           ← api/runtime/config.go (Config, Metadata, AgentRoot, etc.)
      state.go            ← api/runtime/state.go (State, LastTurn)
      types.go            ← api/types.go (Status, EnvVar)
    config.go             (existing — parsing, validation)
    state.go              (existing — I/O)
    maxsockpath_*.go      (existing)
    *_test.go             (existing)

  ari/
    api/
      types.go            ← api/ari/types.go (wire param/result structs)
      domain.go           ← api/ari/domain.go (Agent, AgentRun, Workspace domain types)
      methods.go          ← NEW: ARI method constants extracted from api/methods.go
    server/
      server.go           (existing — service implementation)
      service.go          ← api/ari/service.go (interfaces: WorkspaceService, AgentRunService,
                             AgentService + RegisterXxxService funcs + MapRPCError helper)
      registry.go         ← pkg/ari/registry.go
      registry_test.go    ← pkg/ari/registry_test.go
    client/
      client.go           (existing — ARIClient with typed sub-clients)
      typed.go            ← api/ari/client.go (WorkspaceClient, AgentRunClient, AgentClient
                             wrappers + callRPC/callRPCRaw helpers)
      simple.go           ← pkg/ari/client.go (low-level socket client)
      simple_test.go      ← pkg/ari/client_test.go

  shim/
    api/
      types.go            ← api/shim/types.go (session/runtime wire param/result structs)
      methods.go          ← NEW: shim method constants extracted from api/methods.go
      events.go           ← api/events.go (event type/category constants)
      event.go            ← pkg/events/shim_event.go (ShimEvent struct)
      event_types.go      ← pkg/events/types.go (Event interface, content blocks, typed events)
      wire_shape_test.go  ← pkg/events/wire_shape_test.go
    server/
      service.go          (existing — shim service implementation)
      interface.go        ← api/shim/service.go (ShimService interface + RegisterShimService)
      translator.go       ← pkg/events/translator.go
      translator_test.go  ← pkg/events/translator_test.go
      translate_rich_test.go ← pkg/events/translate_rich_test.go
      log.go              ← pkg/events/log.go
      log_test.go         ← pkg/events/log_test.go
    client/
      client.go           (existing — Dial, DialWithHandler, ParseShimEvent)
      typed.go            ← api/shim/client.go (ShimClient typed wrapper)
    runtime/
      acp/
        runtime.go        ← pkg/runtime/runtime.go
        client.go         ← pkg/runtime/client.go
        runtime_test.go   ← pkg/runtime/runtime_test.go
        client_test.go    ← pkg/runtime/client_test.go

# Deleted directories
api/                      (entire directory)
pkg/events/               (entire directory)
pkg/runtime/              (entire directory)
pkg/spec/                 (renamed to pkg/runtime-spec)

# Deleted empty files (no actual content, only package decl + "moved to" comments)
pkg/spec/types.go
pkg/spec/state_types.go
pkg/agentd/runtimeclass.go
pkg/agentd/runtimeclass_test.go
```

## Import Path Changes

| Old Import | New Import |
|---|---|
| `github.com/zoumo/oar/api` (Status, EnvVar) | `github.com/zoumo/oar/pkg/runtime-spec/api` |
| `github.com/zoumo/oar/api` (EventType*, Category*) | `github.com/zoumo/oar/pkg/shim/api` |
| `github.com/zoumo/oar/api` (ARI Method*) | `github.com/zoumo/oar/pkg/ari/api` |
| `github.com/zoumo/oar/api` (Shim Method*) | `github.com/zoumo/oar/pkg/shim/api` |
| `github.com/zoumo/oar/api/ari` (types) | `github.com/zoumo/oar/pkg/ari/api` |
| `github.com/zoumo/oar/api/ari` (interfaces) | `github.com/zoumo/oar/pkg/ari/server` |
| `github.com/zoumo/oar/api/ari` (clients) | `github.com/zoumo/oar/pkg/ari/client` |
| `github.com/zoumo/oar/api/shim` (types) | `github.com/zoumo/oar/pkg/shim/api` |
| `github.com/zoumo/oar/api/shim` (interface) | `github.com/zoumo/oar/pkg/shim/server` |
| `github.com/zoumo/oar/api/shim` (client) | `github.com/zoumo/oar/pkg/shim/client` |
| `github.com/zoumo/oar/api/runtime` | `github.com/zoumo/oar/pkg/runtime-spec/api` |
| `github.com/zoumo/oar/pkg/events` (types) | `github.com/zoumo/oar/pkg/shim/api` |
| `github.com/zoumo/oar/pkg/events` (Translator, EventLog) | `github.com/zoumo/oar/pkg/shim/server` |
| `github.com/zoumo/oar/pkg/runtime` | `github.com/zoumo/oar/pkg/shim/runtime/acp` |

## Method Constants Split

`api/methods.go` 拆分为两个文件：

**`pkg/ari/api/methods.go`** — ARI methods (orchestrator ↔ agentd):
- `MethodWorkspace*` (Create, Status, List, Delete, Send)
- `MethodAgentRun*` (Create, Prompt, Cancel, Stop, Delete, Restart, List, Status, Attach)
- `MethodAgent*` (Set, Get, List, Delete)

**`pkg/shim/api/methods.go`** — Shim methods (agent-shim ↔ agentd):
- `MethodSession*` (Prompt, Cancel, Load, Subscribe)
- `MethodRuntime*` (Status, History, Stop)
- `MethodShimEvent`

## Implementation Steps

### Step 1: Rename `pkg/spec/` → `pkg/runtime-spec/` + create api/

- Rename directory `pkg/spec/` → `pkg/runtime-spec/`, change package to `runtimespec`
- Delete empty files: `types.go`, `state_types.go`
- Create `pkg/runtime-spec/api/` subdirectory
- Move `api/runtime/config.go` → `pkg/runtime-spec/api/config.go`, change package to `api`
- Move `api/runtime/state.go` → `pkg/runtime-spec/api/state.go`, change package to `api`
- Move `api/types.go` → `pkg/runtime-spec/api/types.go`, change package to `api`
- Update: config.go/state.go 内部引用 `api.Status` 改为同包直接引用
- Update `pkg/runtime-spec/config.go` and `state.go`: import `api/runtime` → `pkg/runtime-spec/api`
- Update `pkg/runtime-spec/` package name from `spec` to `runtimespec`

### Step 2: Create `pkg/ari/api/` + 分发 service/client

从 `api/ari/` 迁移纯类型到 api，interface/client 分发到 server/client：
- Move `api/ari/types.go` → `pkg/ari/api/types.go`, change package to `api`
- Move `api/ari/domain.go` → `pkg/ari/api/domain.go`, change package to `api`
- Move `api/ari/service.go` → `pkg/ari/server/service.go`, change package to `server`
  (包含 WorkspaceService/AgentRunService/AgentService interfaces + RegisterXxx + MapRPCError)
- Move `api/ari/client.go` → `pkg/ari/client/typed.go`, change package to `client`
  (包含 WorkspaceClient/AgentRunClient/AgentClient wrappers + callRPC/callRPCRaw)
- Create `pkg/ari/api/methods.go` — extract ARI method constants
- Update imports: `api` → `pkg/runtime-spec/api`, `api/ari` → `pkg/ari/api`

### Step 3: Move `pkg/ari/` internal files

- Move `pkg/ari/registry.go` → `pkg/ari/server/registry.go`
- Move `pkg/ari/registry_test.go` → `pkg/ari/server/registry_test.go`
- Move `pkg/ari/client.go` → `pkg/ari/client/simple.go`
- Move `pkg/ari/client_test.go` → `pkg/ari/client/simple_test.go`
- Update package names and internal imports

### Step 4: Create `pkg/shim/api/` + 分发 service/client

从 `api/shim/`、`api/events.go`、`pkg/events/` 迁移：
- Move `api/shim/types.go` → `pkg/shim/api/types.go`, change package to `api`
- Move `api/shim/service.go` → `pkg/shim/server/interface.go`, change package to `server`
  (包含 ShimService interface + RegisterShimService)
- Move `api/shim/client.go` → `pkg/shim/client/typed.go`, change package to `client`
  (包含 ShimClient typed wrapper)
- Move `api/events.go` → `pkg/shim/api/events.go`, change package to `api`
- Create `pkg/shim/api/methods.go` — extract shim method constants
- Move `pkg/events/shim_event.go` → `pkg/shim/api/event.go`
- Move `pkg/events/types.go` → `pkg/shim/api/event_types.go`
- Move `pkg/events/wire_shape_test.go` → `pkg/shim/api/wire_shape_test.go`
- Update all imports:
  - `api/runtime` → `pkg/runtime-spec/api`
  - `pkg/events` → same package (no import needed for types in shim/api)
  - `api` (method/event constants) → same package

### Step 5: Move events implementation to `pkg/shim/server/`

- Move `pkg/events/translator.go` → `pkg/shim/server/translator.go`
- Move `pkg/events/translator_test.go` → `pkg/shim/server/translator_test.go`
- Move `pkg/events/translate_rich_test.go` → `pkg/shim/server/translate_rich_test.go`
- Move `pkg/events/log.go` → `pkg/shim/server/log.go`
- Move `pkg/events/log_test.go` → `pkg/shim/server/log_test.go`
- Update package to `server`, update imports:
  - `pkg/events` types → `pkg/shim/api`
  - `api` constants → `pkg/shim/api`
  - `pkg/ndjson` → `pkg/runtime-spec/ndjson` (if applicable, stays if independent)

### Step 6: Move `pkg/runtime/` → `pkg/shim/runtime/acp/`

- Move `pkg/runtime/runtime.go` → `pkg/shim/runtime/acp/runtime.go`
- Move `pkg/runtime/client.go` → `pkg/shim/runtime/acp/client.go`
- Move `pkg/runtime/runtime_test.go` → `pkg/shim/runtime/acp/runtime_test.go`
- Move `pkg/runtime/client_test.go` → `pkg/shim/runtime/acp/client_test.go`
- Change package to `acp`
- Update imports:
  - `api` → `pkg/runtime-spec/api`
  - `api/runtime` → `pkg/runtime-spec/api`
  - `pkg/spec` → `pkg/runtime-spec`

### Step 7: Update all consumers

Update imports in all consuming packages (约 50+ 文件):

- `pkg/agentd/` — update imports for ari api, shim api, runtime-spec api, events
  - Delete empty `runtimeclass.go` and `runtimeclass_test.go`
- `pkg/store/` — update imports for ari domain types, runtime-spec api
- `cmd/agentd/subcommands/server/` — update ari server, ari api imports
- `cmd/agentd/subcommands/shim/` — update shim server, runtime, events imports
- `cmd/agentdctl/subcommands/` — update ari api, shim api imports
- `tests/` — update integration test imports

### Step 8: Delete old directories

- Delete `api/` directory
- Delete `pkg/events/` directory
- Delete `pkg/runtime/` directory
- (empty files already deleted in Step 1 and Step 7)

### Step 9: Verify

- `make build` 编译通过
- `go test ./...` 所有测试通过
- `go vet ./...` 无警告

## Key Design Decisions

1. **api/ 只放纯类型** — struct/const/enum only，interface 和 func 放到 server/ 或 client/
2. **Shared types (Status, EnvVar) → `pkg/runtime-spec/api/`** — 这些类型是 runtime spec 定义的基础状态模型
3. **Event types → `pkg/shim/api/`** — 事件结构体是 shim 协议的一等公民
4. **测试跟随源文件** — 测试文件必须和被测源文件在同一目录下
5. **删除空文件** — spec/types.go, spec/state_types.go, agentd/runtimeclass.go 等无实际内容的文件直接删除
4. **Service interfaces + Register → server/** — WorkspaceService 等 interface 和 RegisterXxx 函数放 server/
5. **Typed client wrappers → client/** — WorkspaceClient 等 typed wrapper 放 client/
6. **Translator + EventLog → `pkg/shim/server/`** — 它们是 shim server 的实现细节
7. **`pkg/ari/client.go` (低级 socket client) → `pkg/ari/client/simple.go`** — 合并到 client 子包
8. **`pkg/ari/registry.go` → `pkg/ari/server/`** — registry 是 server 端的内存状态管理

## Dependency Graph (After)

```
pkg/runtime-spec/api          ← foundation: Status, EnvVar, Config, State (no internal deps)
    ↑
pkg/runtime-spec      ← config parsing, state I/O (imports runtime-spec/api)
    ↑
pkg/ari/api           ← ARI wire types only (imports runtime-spec/api)
pkg/shim/api          ← shim wire types + event types (imports runtime-spec/api, acp-go-sdk)
    ↑
pkg/ari/server        ← interfaces + impl (imports ari/api, jsonrpc, agentd, store, shim/client)
pkg/ari/client        ← typed + simple clients (imports ari/api, jsonrpc)
pkg/shim/server       ← interface + impl + translator (imports shim/api, jsonrpc, shim/runtime/acp)
pkg/shim/client       ← typed client + helpers (imports shim/api, jsonrpc)
pkg/shim/runtime/acp  ← ACP runtime (imports runtime-spec/api, runtime-spec, acp-go-sdk)
```
