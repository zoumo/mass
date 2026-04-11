# Design / Implementation Drift Audit — 2026-04-12

## Scope

Reviewed every file under `docs/design/` against the current repository implementation.

Design documents reviewed:

- `docs/design/README.md`
- `docs/design/contract-convergence.md`
- `docs/design/roadmap.md`
- `docs/design/agentd/ari-spec.md`
- `docs/design/agentd/agentd.md`
- `docs/design/runtime/agent-shim.md`
- `docs/design/runtime/config-spec.md`
- `docs/design/runtime/design.md`
- `docs/design/runtime/runtime-spec.md`
- `docs/design/runtime/shim-rpc-spec.md`
- `docs/design/runtime/why-no-runa.md`
- `docs/design/workspace/workspace-spec.md`

Implementation evidence used:

- `cmd/agentd/**`
- `cmd/agentdctl/**`
- `pkg/ari/**`
- `pkg/agentd/**`
- `pkg/events/**`
- `pkg/meta/**`
- `pkg/rpc/**`
- `pkg/runtime/**`
- `pkg/spec/**`
- `pkg/workspace/**`
- `tests/integration/**`

## Summary

The design set is no longer aligned with the code in several high-impact areas:

1. The ARI runtime lifecycle surface in code is `agentrun/*`, while several design docs describe runtime lifecycle as `agent/*`.
2. The current `agent/*` ARI surface is AgentTemplate CRUD, which is mostly absent from the design docs.
3. The current identity model is `workspace + name`, not `room + name`; there is no implemented Room Manager or Room Spec under `docs/design/`.
4. The runtime status value for an idle bootstrapped shim is `idle`, not `created`.
5. The shim RPC implementation has already moved to `session/*` + `runtime/*`, but some design docs still say the repository is on legacy PascalCase / `$/event`.
6. The actual daemon layout co-locates shim state files with the bundle directory under the agentd bundle root, not `/run/agentd/shim/<id>/`.
7. The actual binaries are `agentd` and `agentdctl`; shim and workspace MCP are subcommands, not standalone `cmd/agent-shim` / `cmd/agent-shim-cli`.
8. The Workspace Spec and ARI docs omit or misstate implemented details around source defaults, hook persistence, cleanup, and AgentTemplate-provided runtime classes.

## Outdated Items

### 1. `docs/design/contract-convergence.md`

Outdated content:

- Claims agent identity is `room + name`.
- Claims all agents belong to a room.
- Claims orchestrator-facing lifecycle methods are `agent/create`, `agent/status`, `agent/prompt`, `agent/stop`, `agent/delete`, and `agent/restart`.
- Claims room desired state is owned by `docs/design/orchestrator/room-spec.md`.
- Claims `workspace/prepare` produces a `workspaceId`.
- Claims active agent state machine is `creating -> created -> running -> stopped`.

Current implementation:

- Runtime instances are stored as `meta.AgentRun` and identified by `Metadata.Workspace + Metadata.Name`.
- Public runtime lifecycle methods are `agentrun/create`, `agentrun/status`, `agentrun/prompt`, `agentrun/cancel`, `agentrun/stop`, `agentrun/delete`, `agentrun/restart`, `agentrun/list`, and `agentrun/attach`.
- `agent/*` methods manage AgentTemplate records: `agent/set`, `agent/get`, `agent/list`, `agent/delete`.
- There is no implemented Room Manager and no `docs/design/orchestrator/room-spec.md` file in the tree.
- Workspace creation is `workspace/create`, and the persisted workspace identity is the workspace name.
- Runtime/agent state uses `idle` for bootstrapped, prompt-ready state.

Suggested fix:

- Rewrite the authority map around the implemented split:
  - `agent/*` = AgentTemplate CRUD / runtime class template management.
  - `agentrun/*` = realized runtime agent process lifecycle.
  - `workspace/*` = workspace lifecycle and intra-workspace message routing.
- Replace `room + name` identity with `workspace + name`.
- Move Room material into an explicit future-work section until a real Room Spec and implementation exist.
- Replace `workspaceId` with workspace name unless a distinct ID is reintroduced in code.
- Replace `created` with `idle` for the current daemon-facing runtime lifecycle.

### 2. `docs/design/README.md`

Outdated content:

- Maps `Pod -> Room` and describes Room as an active part of the architecture.
- Says agentd manages an external `agent/*` API object and Session Manager is internal.
- Lists `docs/design/orchestrator/room-spec.md` in the documentation index even though that file does not exist.
- Says `agent-shim-cli` is the management CLI.
- The architecture diagram and “authority” prose assume `agent/*` lifecycle semantics rather than `agentrun/*`.

Current implementation:

- There is no Room package, no Room ARI method, and no Room design file in the repo.
- `pkg/ari/server.go` exposes `agentrun/*` for realized process lifecycle and `agent/*` for AgentTemplate CRUD.
- The built binaries are `bin/agentd` and `bin/agentdctl`.
- Direct shim interaction is an `agentdctl shim` subcommand.
- The shim runtime process is launched via `agentd shim` self-fork.

Suggested fix:

- Update the architecture overview to name `Workspace`, `AgentTemplate`, and `AgentRun` as the implemented concepts.
- Remove or clearly mark Room references as future design only.
- Fix the document index to remove the nonexistent orchestrator Room Spec or add a placeholder only if the project intentionally wants it.
- Replace `agent-shim-cli` references with `agentdctl shim`.
- Explain that external runtime lifecycle is currently `agentrun/*`, while `agent/*` configures templates.

### 3. `docs/design/roadmap.md`

Outdated content:

- Says only the agent-shim layer is implemented.
- Says `agentd` is not implemented.
- Lists standalone `cmd/agent-shim` and `cmd/agent-shim-cli`.
- Phase 2 scaffolding still asks to create `cmd/agentd/main.go` and multiple `pkg/agentd/...` packages that already exist or were implemented under different package names.
- Still plans `session/*` ARI methods (`session/new`, `session/prompt`, etc.) even though the code uses `agentrun/*`.
- Says terminal operations are stubs in `pkg/runtime/client.go`. The file exists and does contain terminal method stubs returning `not supported`, but they are part of the ACP client callback implementation (`acpClient`), not the standalone terminal client implied by the roadmap's higher-level framing.
- Says `session/load` is not wired, while `pkg/agentd/shim_client.go` and recovery tests include `session/load` attempts for `tryReload`.
- Says metadata store should use SQLite, while the implementation uses bbolt.
- Says workspace methods should be `workspace/prepare` and `workspace/cleanup`, while the implementation uses `workspace/create` and `workspace/delete`.

Current implementation:

- `cmd/agentd/main.go`, `cmd/agentdctl/main.go`, `pkg/agentd`, `pkg/ari`, `pkg/meta`, `pkg/workspace`, and integration tests exist.
- `Makefile` builds `agentd` and `agentdctl`.
- Metadata storage uses `go.etcd.io/bbolt`.
- ARI includes `workspace/*`, `agentrun/*`, and `agent/*`.
- Recovery logic attempts `session/load` only under the `tryReload` restart policy.

Suggested fix:

- Replace the roadmap with a status-based roadmap that reflects completed scaffolding and implemented subsystems.
- Move old `session/*` ARI items to historical notes or delete them.
- Rename lifecycle work to `agentrun/*`.
- Record bbolt as the current metadata backend.
- Update future work around terminal ops and `session/load` to match the current code paths and tests.
- Use `make build` as the validation command for binary build checks.

### 4. `docs/design/agentd/ari-spec.md`

Outdated content:

- Defines runtime lifecycle methods as `agent/create`, `agent/prompt`, `agent/cancel`, `agent/stop`, `agent/delete`, `agent/restart`, `agent/list`, `agent/status`, and `agent/attach`.
- Omits implemented AgentTemplate methods `agent/set`, `agent/get`, `agent/list`, and `agent/delete`.
- Reuses `agent/list` and `agent/delete` for realized runtime agents, conflicting with current template CRUD.
- Says `agent/status` returns runtime agent state, but code exposes `agentrun/status`.
- Says `agent/attach` returns shim socket path, but code exposes `agentrun/attach`.
- Says `agent/update` and `agent/stateChange` are ARI events. The current attach model returns the shim socket; live events on that socket are `session/update` and `runtime/stateChange`.
- Mentions `agentID` in workspace MCP logging. Current workspace MCP environment and logging use workspace and agent name; the generated env vars are `OAR_WORKSPACE_NAME`, `OAR_AGENT_NAME`, `OAR_AGENTD_SOCKET`, and `OAR_STATE_DIR`.

Current implementation:

- `pkg/ari/server.go` dispatches:
  - `workspace/create`, `workspace/status`, `workspace/list`, `workspace/delete`, `workspace/send`
  - `agentrun/create`, `agentrun/prompt`, `agentrun/cancel`, `agentrun/stop`, `agentrun/delete`, `agentrun/restart`, `agentrun/list`, `agentrun/status`, `agentrun/attach`
  - `agent/set`, `agent/get`, `agent/list`, `agent/delete`
- `pkg/ari/types.go` names wire structs as `AgentRun*` for runtime instances and `AgentTemplate*` for templates.

Suggested fix:

- Split the ARI spec into three sections matching code:
  - Workspace methods.
  - AgentTemplate methods under `agent/*`.
  - AgentRun methods under `agentrun/*`.
- Keep `workspace + name` as AgentRun identity.
- Rename all runtime lifecycle examples from `agent/*` to `agentrun/*`.
- Add AgentTemplate request/result schemas.
- Replace `agent/update` / `agent/stateChange` with the implemented attach-to-shim model unless ARI-level fanout is added in code.

### 5. `docs/design/agentd/agentd.md`

Outdated content:

- Describes the external lifecycle object as `Agent` rather than `AgentRun`.
- Says `agent/create` is the external entry point.
- Says `agent/prompt`, `agent/stop`, `agent/delete`, and `agent/restart` are runtime lifecycle operations.
- Says runtime classes are selected by runtimeClass but does not describe the implemented AgentTemplate store and `agent/set` API.
- Describes persisted recovery state as including shim socket path, state directory, PID, and bootstrap config, which is now accurate, but still frames it through `agent/*` instead of `agentrun/*`.

Current implementation:

- The persistent runtime object is `meta.AgentRun`.
- Agent launch templates are `meta.AgentTemplate`.
- Agent Manager wraps AgentRun CRUD; Process Manager resolves `AgentTemplate` by `AgentRun.Spec.RuntimeClass`.
- `agentdctl agent` manages templates; `agentdctl agentrun` manages running instances.

Suggested fix:

- Rename the external runtime object to AgentRun throughout this document.
- Add an AgentTemplate subsection:
  - identity `metadata.name`
  - `spec.command`, `spec.args`, `spec.env`, optional `startupTimeoutSeconds`
  - managed via `agent/set|get|list|delete`
- Rename runtime lifecycle operations to `agentrun/*`.
- Keep `workspace + name` as AgentRun identity.
- Keep recovery persistence text, but tie it to AgentRun.

### 6. `docs/design/runtime/runtime-spec.md`

Outdated content:

- Defines the idle bootstrapped runtime status as `created`.
- Says `pid` is required when status is `created` or `running`.
- Uses lifecycle diagrams and state mapping with `created`.
- Says socket/state layout is `/run/agentd/shim/<agent-id>/`.
- Says socket path convention is `/run/agentd/shim/<session-id>/agent-shim.sock`.
- Says `delete` removes `/run/agentd/shim/<agent-id>/`.

Current implementation:

- `pkg/spec/state_types.go` defines `creating`, `idle`, `running`, `stopped`, and `error`.
- `pkg/runtime.Manager.Create` writes `StatusIdle` after ACP bootstrap succeeds.
- `pkg/agentd/process.go` co-locates state files with the bundle directory:
  - bundle path: `<bundleRoot>/<workspace>-<name>`
  - state dir: same as bundle path
  - socket: `<bundleRoot>/<workspace>-<name>/agent-shim.sock`
- `cmd/agentd shim` defaults to `/run/agentd/shim`, but agentd self-fork passes `--state-dir <bundleRoot>` and `--id <workspace>-<name>`.
- `State` also includes implemented fields `lastTurn` and `exitCode`.

Suggested fix:

- Replace `created` with `idle` in the runtime state model, lifecycle, state mapping, operation preconditions, examples, and notification examples.
- Document `error`, `lastTurn`, and `exitCode` if they are intended wire fields.
- Update file-system layout to describe the current agentd deployment:
  - shim state files are co-located with the bundle under bundle root.
  - `agentd shim` standalone defaults can remain as a separate CLI default note.
- Update recovery text to say agentd primarily uses persisted `ShimSocketPath` from metadata, not only scanning `/run/agentd/shim/*`.

### 7. `docs/design/runtime/shim-rpc-spec.md`

Outdated content:

- “Implementation lag” section says `pkg/rpc`, `pkg/agentd/shim_client.go`, and `cmd/agent-shim-cli` still implement legacy PascalCase / `$/event`.
- Examples use runtime status `created`.
- Says `runtime/history` `fromSeq` defaults to `1`, while current server effectively defaults to `0`.
- Specifies `session/subscribe(afterSeq)` only, while the implementation also supports `fromSeq` atomic backfill in `session/subscribe`.
- Says turn fields `turnId`, `streamSeq`, and `phase` are added; current types have all three fields, but current CLI prints `turnId`/`streamSeq` and omits `phase`.
- Says `session/prompt` corresponds to upper ARI `session/prompt`; current upper ARI is `agentrun/prompt` or `workspace/send`.

Current implementation:

- `pkg/rpc/server.go` implements clean-break methods:
  - `session/prompt`
  - `session/cancel`
  - `session/subscribe`
  - `runtime/status`
  - `runtime/history`
  - `runtime/stop`
- Notifications are `session/update` and `runtime/stateChange`.
- `pkg/agentd/shim_client.go` uses clean-break methods and supports `session/load`.
- `cmd/agentdctl subcommands/shim` uses clean-break methods.

Suggested fix:

- Remove the stale implementation-lag statement or replace it with “implementation now uses clean-break surface.”
- Replace `created` examples with `idle`.
- Document both `afterSeq` and implemented `fromSeq` semantics for `session/subscribe`.
- Decide whether `runtime/history` should default to `0` or `1`; align docs and code.
- Update upper-boundary references from `session/prompt` to `agentrun/prompt`.
- Either add `phase` rendering to `agentdctl shim` later or mark it as wire-supported but not displayed by CLI.

### 8. `docs/design/runtime/agent-shim.md`

Outdated content:

- Says each OAR session corresponds to one agent-shim process.
- Says M005 only enhances agent-shim with event ordering.
- Says current implementation still uses legacy PascalCase / `$/event`.
- Says runtime state/sockets are under `/run/agentd/shim/*` by implication through referenced recovery flow.
- Mentions `session/load` as a shim responsibility, but the current shim RPC server does not implement `session/load`; the agentd client attempts it and treats errors as fallback behavior.

Current implementation:

- The code names the outer runtime object AgentRun, while some internal comments still use session terminology.
- `pkg/rpc/server.go` is clean-break.
- `pkg/agentd/shim_client.go` includes a `Load` client call, but the shim server dispatch does not include `session/load`.
- State dir is co-located with bundle root in normal agentd launches.

Suggested fix:

- Replace “session” with “AgentRun runtime instance” where describing the external daemon model; reserve “session” for the shim RPC and ACP protocol boundary.
- Remove stale legacy-implementation paragraph.
- Document that `session/load` is currently client-side recovery intent with fallback, not a supported shim RPC method, unless implementing it is part of the next slice.
- Align path examples with the current bundle/state co-location.

### 9. `docs/design/runtime/config-spec.md`

Outdated content:

- Says MCP server `type` values are only `"http"` and `"sse"`.
- Says `acpAgent.systemPrompt` is not a hidden work turn and must be realized as bootstrap semantics before prompt-ready state.
- Examples say later work enters through ARI `session/prompt`.

Current implementation:

- `pkg/spec.McpServer` supports `http`, `sse`, and `stdio`.
- `pkg/agentd/process.go` injects a stdio MCP server named `workspace` into every generated config.
- `pkg/runtime.Manager.Create` implements `systemPrompt` by sending an ACP `Prompt` during bootstrap after `session/new`.
- Public daemon prompt entry is `agentrun/prompt`; direct shim prompt is `session/prompt`.

Suggested fix:

- Add `stdio` MCP server schema (`name`, `command`, `args`, `env`) and note that `args`/`env` are emitted as explicit arrays for ACP compatibility.
- Document the current systemPrompt implementation honestly:
  - either classify the current prompt-based seeding as implementation drift to be fixed in runtime code;
  - or update the design to allow bootstrap-compatible seeding via an internal prompt before exposing `idle`.
- Replace ARI `session/prompt` references with `agentrun/prompt`, while keeping shim `session/prompt` as the internal boundary.

### 10. `docs/design/runtime/design.md`

Outdated content:

- Uses `ARI session/new` and `ARI session/prompt` in the bootstrap flow.
- Describes agentd session lifecycle states including `paused:warm` and `paused:cold`.
- Says runtime status is `created` after bootstrap.
- Says S03 still needs durable bundle path and shim socket path, bootstrap config snapshot, and last known runtime/process state transition metadata.

Current implementation:

- ARI bootstrap is `agentrun/create`; work entry is `agentrun/prompt`.
- The active state machine is `creating`, `idle`, `running`, `stopped`, `error`; warm/cold pause is not implemented.
- Bootstrap success state is `idle`.
- AgentRun status persists `ShimSocketPath`, `ShimStateDir`, `ShimPID`, and `BootstrapConfig`.
- Some durability gaps remain, but several listed gaps have been partly implemented.

Suggested fix:

- Rename ARI flow to `workspace/create` -> `agentrun/create` -> `agentrun/status` -> `agentrun/prompt`.
- Remove active warm/cold pause state from current design; keep as future work only if needed.
- Replace `created` with `idle`.
- Update the durable-state gaps table to distinguish already implemented fields from remaining gaps.

### 11. `docs/design/runtime/why-no-runa.md`

Outdated content:

- Says lifecycle operations are `Prompt` / `Cancel` / `Shutdown` / `GetState`.
- Links to `config.md`, but the actual file is `config-spec.md`.

Current implementation:

- Shim RPC methods are `session/prompt`, `session/cancel`, `session/subscribe`, `runtime/status`, `runtime/history`, and `runtime/stop`.
- Direct shim CLI is `agentdctl shim`.

Suggested fix:

- Replace legacy lifecycle operation names with clean-break method names.
- Fix the link text and target to `config-spec.md`.

### 12. `docs/design/workspace/workspace-spec.md`

Outdated content:

- Says the Workspace Spec declares preparation for “one or more sessions.”
- Says ARI `session/new` env overrides do not flow into hooks.
- Says agent process env precedence includes `session/new` env overrides.
- Says one workspace may be attached to multiple sessions and Room members sharing `workspaceId`.
- Says hook stdout/stderr and failure status must be observable to the caller, but ARI currently only stores `phase=error` and does not persist hook output in workspace status.
- The top-level shape says `metadata` and `source` are `{}` in an example, but implementation requires `metadata.name` and `source.type`.

Current implementation:

- The current external runtime object is AgentRun.
- Workspace create request accepts `name`, raw `source`, and `labels`; hooks exist in `meta.WorkspaceSpec` and `workspace.WorkspaceSpec`, but `handleWorkspaceCreate` only passes source to `workspace.WorkspaceSpec` and does not pass hooks from ARI params.
- AgentRun create params do not include env overrides.
- Runtime env is currently inherited process env plus AgentTemplate env.
- Shared workspace membership is through multiple AgentRuns using the same workspace name.
- Hook failures affect phase, but hook output is not returned or persisted in the ARI workspace status model.

Suggested fix:

- Replace session wording with AgentRun wording.
- Replace `session/new` env override text with current AgentTemplate/runtime class env behavior.
- Add a current limitation stating AgentRun-specific env overrides are not implemented.
- Update shared workspace text to `workspace + AgentRun`, not Room/`workspaceId`.
- Either add hook fields to ARI `workspace/create` and persist hook output, or mark hook output observability as a target gap rather than an implemented guarantee.
- Make examples include required `metadata.name` and `source.type`.

## Cross-Document Repair Plan

### Phase A — Establish current terminology

1. Define these terms once and use them consistently:
   - AgentTemplate: launch template / runtime class record managed by `agent/*`.
   - AgentRun: realized runtime instance managed by `agentrun/*`.
   - Workspace: named realized workspace managed by `workspace/*`.
   - Shim session: internal `session/*` + `runtime/*` boundary between agentd and shim.
2. Reserve Room for future work until code and a real Room Spec exist.
3. Reserve ACP session for the inner protocol identity.

### Phase B — Rewrite public ARI design

1. Rewrite `docs/design/agentd/ari-spec.md` around implemented method groups:
   - `workspace/*`
   - `agent/*` AgentTemplate CRUD
   - `agentrun/*` AgentRun lifecycle
2. Update request and response schemas from `pkg/ari/types.go`.
3. Remove ARI-level `agent/update` / `agent/stateChange` unless fanout is implemented.
4. Add direct-attach semantics: `agentrun/attach` returns the shim socket and the caller consumes shim RPC events.

### Phase C — Align runtime state and shim docs

1. Replace `created` with `idle` wherever describing current code.
2. Document `error`, `lastTurn`, and `exitCode`.
3. Update runtime file layout to bundle/state co-location under agentd bundle root.
4. Remove stale legacy shim RPC implementation notes.
5. Document implemented `session/subscribe` `fromSeq` behavior.
6. Decide and align `runtime/history` default `fromSeq` boundary.

### Phase D — Align workspace docs

1. Update workspace terminology from session/Room to AgentRun/workspace.
2. Document implemented sources and validation requirements.
3. Clarify that `workspace/create` currently does not accept hooks through the ARI wire params even though lower-level workspace types support hooks.
4. Clarify current env precedence:
   - inherited shim/daemon environment
   - AgentTemplate env
   - no AgentRun-specific env override yet
5. Mark hook output observability as a gap unless implemented.

### Phase E — Replace roadmap

1. Convert `docs/design/roadmap.md` from an old phase plan into a status matrix.
2. Mark implemented:
   - `agentd` daemon
   - `agentdctl`
   - bbolt metadata store
   - workspace create/status/list/delete/send
   - AgentTemplate CRUD
   - AgentRun lifecycle
   - clean-break shim RPC
   - recovery pass with `session/subscribe(fromSeq=0)` and optional `session/load` attempt
3. Mark remaining:
   - true `session/load` shim server support
   - hook output persistence
   - AgentRun-specific env overrides, if desired
   - Room model, if still desired
   - ARI-level event fanout, if still desired

## Proposed Execution Order

1. Update `contract-convergence.md` first so it becomes the source of truth for the current implemented vocabulary.
2. Update `ari-spec.md` and `agentd.md` together because their method names and object model must match.
3. Update runtime docs (`runtime-spec.md`, `shim-rpc-spec.md`, `agent-shim.md`, `config-spec.md`, `design.md`, `why-no-runa.md`) in one pass to avoid reintroducing `created` / legacy shim names.
4. Update `workspace-spec.md` after the ARI vocabulary is stable.
5. Rewrite `README.md` and `roadmap.md` last so their summaries match the corrected lower-level docs.
6. Run `make build`.
7. Run focused tests:
   - `go test ./pkg/spec ./pkg/runtime ./pkg/rpc ./pkg/events`
   - `go test ./pkg/ari ./pkg/agentd ./pkg/workspace ./pkg/meta`
   - Integration tests only if the executor has the required local runtime binaries and credentials.

## Open Decisions

1. Should the project keep the implemented `agentrun/*` split, or should code be renamed back to `agent/*` lifecycle and move AgentTemplate elsewhere?
   - This audit assumes docs should be updated to match current implementation.
2. Should `systemPrompt` seeding by internal ACP prompt during create be considered acceptable bootstrap implementation?
   - If not, runtime code needs a future fix; docs should call it out as implementation drift rather than documenting it as intended behavior.
3. Should shim state remain co-located with bundles?
   - Current code does this intentionally for lifecycle locality. If `/run/agentd/shim/*` remains the desired design, code must change instead.
4. Should Room remain in the active design set?
   - Current implementation has workspace-level multi-agent routing via `workspace/send`, but no Room abstraction.

## Claude Validation

Status: requested via workspace message.

Requested validation from `claude-code` with instructions to explicitly report any disagreement, missing item, evidence path, and proposed correction.

`claude-code` reviewed the report and found one factual error in Item 3:

- Original report incorrectly said `pkg/runtime/client.go` does not exist.
- Correction: `pkg/runtime/client.go` exists and contains terminal operation stubs returning `not supported`, but those methods are part of the ACP client callback implementation (`acpClient`), not a standalone terminal client surface.

Resolution: accepted. Item 3 has been corrected. `claude-code` otherwise validated the report as accurate and complete.

## Final Execution Handoff

Status: blocked by target agent state after repeated attempts.

Handoff to `gsd-pi` was attempted through the workspace tool multiple times, including after the `claude-code` validation correction was incorporated, but the target rejected the message because it was not idle:

```text
workspace/send failed: jsonrpc2: code -32001 message: target agent not in idle state: running
```

Retry when `gsd-pi` becomes idle. The exact handoff instruction to send is:

```text
请执行文档 docs/plan/2026-04-12-design-implementation-drift-audit.md 中的修复计划。要求：严格按文档的 Cross-Document Repair Plan 和 Proposed Execution Order 操作；先更新 docs/design/ 下的设计文档以对齐当前代码实现，不要改代码，除非文档中明确要求做代码实现取舍；执行后运行文档列出的验证命令（至少 make build；能跑则跑 focused go test）；把完成情况、修改文件、验证结果、任何阻塞或需要决策的 Open Decisions 回信给我。注意：文档中记录已请求 claude-code 验证但未收到可读取回信；如执行期间收到 claude-code 的明确异议，需要把异议和处理结果写回同一 plan 文档。
```

Updated handoff instruction after `claude-code` validation:

```text
请执行文档 docs/plan/2026-04-12-design-implementation-drift-audit.md 中的修复计划。要求：严格按文档的 Cross-Document Repair Plan 和 Proposed Execution Order 操作；先更新 docs/design/ 下的设计文档以对齐当前代码实现，不要改代码，除非文档中明确要求做代码实现取舍；执行后运行文档列出的验证命令（至少 make build；能跑则跑 focused go test）；把完成情况、修改文件、验证结果、任何阻塞或需要决策的 Open Decisions 回信给我。注意：claude-code 已验证该报告，指出并已更正 1 处事实性错误（pkg/runtime/client.go 存在且包含 terminal stubs）；其余条目经 claude-code 验证准确。执行过程中如有新的异议或发现，请写回同一 plan 文档。
```
