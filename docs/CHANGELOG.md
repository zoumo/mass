> Auto-generated. Do not edit directly.
> Last updated: 2026-04-15 after M014

# Changelog

---

## M014: Enrich state.json + Session Metadata Pipeline (2026-04-15)

### S01: Dead code removal
- Removed `EventTypeFileWrite`, `EventTypeFileRead`, `EventTypeCommand` constants and `FileWriteEvent`, `FileReadEvent`, `CommandEvent` structs plus all decode/test references
- Pure deletions — no logic modifications; remaining event type surface accurately reflects ACP output
- Key files: `pkg/agentrun/api/event_constants.go`, `pkg/agentrun/api/event_types.go`, `pkg/agentrun/api/shim_event.go`

### S02: state.json type definitions
- Defined all session metadata types (`SessionState`, `AgentInfo`, `AgentCapabilities`, union types with custom MarshalJSON/UnmarshalJSON) in `pkg/runtime-spec/api`
- Extended `State` with `UpdatedAt`, `Session (*SessionState)`, `EventCounts (map[string]int)` — all with omitempty for backward compat
- Round-trip tests cover every field variant (Unstructured AvailableCommandInput, Grouped/Ungrouped ConfigSelectOptions, nested SessionForkCapabilities)
- Key files: `pkg/runtime-spec/api/session.go`, `pkg/runtime-spec/api/state.go`, `pkg/runtime-spec/state_test.go`

### S03: writeState read-modify-write refactor
- Refactored all 7 writeState call sites to closure pattern `func(*apiruntime.State)` — Kill, process-exit, prompt cycles never clobber Session metadata
- `UpdatedAt` stamped unconditionally (RFC3339Nano) on every write as a derived field
- Tests prove Session preservation across Kill() and process-exit; `errors.Is(err, os.ErrNotExist)` guards first-write vs update
- Key files: `pkg/agentrun/runtime/acp/runtime.go`, `pkg/agentrun/runtime/acp/runtime_test.go`

### S04: Translator eventCounts
- `eventCounts[ev.Type]++` in `broadcast()` after `nextSeq++`, before fan-out — single counting site covering all event origins
- Fail-closed: log-append failures exit before count increment; `EventCounts()` returns thread-safe map copy
- Key files: `pkg/agentrun/server/translator.go`, `pkg/agentrun/server/translator_test.go`

### S05: ACP bootstrap capabilities capture
- `convertInitializeToSession()` maps ACP InitializeResponse to `state.json.session` (agentInfo + capabilities) at bootstrap-complete
- Synthetic `NotifyStateChange("idle","idle",pid,"bootstrap-metadata",["agentInfo","capabilities"])` emitted after `Translator.Start()`
- `StateChangeEvent.SessionChanged []string` field added for metadata change events
- Key files: `pkg/agentrun/runtime/acp/runtime.go`, `pkg/agentrun/api/event_types.go`, `pkg/agentrun/server/translator.go`, `cmd/mass/commands/run/command.go`, `internal/testutil/mockagent/main.go`

### S06: Session metadata hook chain
- End-to-end pipeline: `Translator.maybeNotifyMetadata` (type-switch gate, 4 ACP types) → `Manager.UpdateSessionMetadata` → state.json + `state_change` event
- `SetEventCountsFn` injects Translator counts into Manager; EventCounts flushed on every `writeState` call
- Sort helpers (`sortCommandsByName`, `sortConfigOptionsByID`) ensure deterministic JSON output
- Key files: `pkg/agentrun/runtime/acp/runtime.go`, `pkg/agentrun/server/translator.go`, `cmd/mass/commands/run/session_update.go`, `cmd/mass/commands/run/command.go`

### S07: runtime/status overlay + doc updates
- `Status()` overlays real-time `Translator.EventCounts()` onto state.json snapshot — callers see authoritative counts, not stale disk values
- Design docs (`run-rpc-spec.md`, `runtime-spec.md`) updated with full M014 state schema (session, eventCounts, updatedAt, sessionChanged)
- Key files: `pkg/agentrun/server/service.go`, `pkg/agentrun/server/service_test.go`, `docs/design/runtime/run-rpc-spec.md`, `docs/design/runtime/runtime-spec.md`

---

## M013: Package Restructure — Clean api/ Boundary + Event/Runtime Colocation (2026-04-14)

### S01: Runtime-spec consumer migration
- Migrated all consumers off `api/runtime/` and `api.Status`/`api.EnvVar` to `pkg/runtime-spec/api`
- Deleted `api/runtime/`, `api/types.go`, and two empty `pkg/agentd/runtimeclass` stubs
- Applied Pattern A (full replacement) and Pattern B (dual-import for files still needing Method/Category constants)
- Key files: `pkg/runtime/runtime.go`, `pkg/runtime/client.go`, `pkg/agentd/agent.go`, `pkg/agentd/process.go`, `api/ari/domain.go`, `api/ari/types.go`, `pkg/store/agentrun.go`, `pkg/ari/server/server.go`

### S02: ARI package restructure
- Established `pkg/ari/api/` (types, domain, methods), `pkg/ari/server/` (service interfaces + registry + dispatch), `pkg/ari/client/` (typed + simple clients)
- Deleted `api/ari/` directory and `pkg/ari` root-level files (`registry.go`, `client.go`)
- Migrated all 35+ consumers across 9 groups; moved 3 test files into new sub-packages
- Key files: `pkg/ari/api/types.go`, `pkg/ari/api/domain.go`, `pkg/ari/api/methods.go`, `pkg/ari/server/service.go`, `pkg/ari/server/registry.go`, `pkg/ari/client/typed.go`, `pkg/ari/client/simple.go`

### S03: Shim package restructure + api/ deletion
- Created `pkg/agentrun/api/` (methods, types, service, client) and `pkg/events/constants.go` (EventType*/Category* constants)
- Migrated all 19 consumer files across 6 groups; deleted `api/shim/`, `api/events.go`, `api/methods.go`, and the `api/` directory root
- Key files: `pkg/agentrun/api/methods.go`, `pkg/agentrun/api/types.go`, `pkg/agentrun/api/service.go`, `pkg/agentrun/api/client.go`, `pkg/events/constants.go`, `pkg/agentd/process.go`, `cmd/massctl/commands/agentrun/command.go`

### S04: Events impl + ACP runtime migration + final verification
- Moved `pkg/events/` event wire types into `pkg/agentrun/api/` (shim_event.go, event_types.go, event_constants.go)
- Moved translator + log from `pkg/events/` to `pkg/agentrun/server/`; moved `pkg/runtime/` to `pkg/agentrun/runtime/acp/`
- Deleted `pkg/events/` and `pkg/runtime/` packages entirely; added `EventTypeOf()` exported accessor for sealed interface cross-package access
- Key files: `pkg/agentrun/api/shim_event.go`, `pkg/agentrun/api/event_types.go`, `pkg/agentrun/server/translator.go`, `pkg/agentrun/server/log.go`, `pkg/agentrun/runtime/acp/runtime.go`, `pkg/agentrun/runtime/acp/client.go`

---

## M012: Codebase Refactor: Service Interface + Unified RPC + Directory Restructure (2026-04-14)

### S01: pkg/jsonrpc/ Transport-Agnostic Framework
- Built transport-agnostic JSON-RPC 2.0 server + client replacing three duplicated implementations
- Bounded 256-entry FIFO notification worker prevents slow handlers blocking response dispatch
- Key files: `pkg/jsonrpc/server.go`, `pkg/jsonrpc/client.go`, `pkg/jsonrpc/errors.go`, `pkg/jsonrpc/peer.go`

### S02: Pure Rename/Move (api/spec→api/runtime, pkg/shimapi→api/shim)
- Pure import path migration; created `api/runtime/` and `api/shim/types.go`; deleted `api/spec/` and `pkg/shimapi/`
- Key files: `api/runtime/config.go`, `api/runtime/state.go`, `api/shim/types.go`

### S03: ARI Clean-Break Contract Convergence
- Updated ARI spec with metadata/spec/status domain shapes; added `ARIView()` for sensitive field stripping at API boundary
- Created `api/ari/domain.go`; deleted `api/meta/`; `ARIView()` preferred over `json:"-"` to preserve bbolt persistence
- Key files: `api/ari/domain.go`, `api/ari/service.go`, `docs/design/mass/ari-spec.md`

### S04: Service Interface + Register + Typed Clients
- Defined `WorkspaceService`/`AgentRunService`/`AgentService` interfaces with `Register` functions; added typed `ShimClient` wrapper
- Key files: `api/ari/service.go`, `api/ari/client.go`, `api/shim/service.go`, `api/shim/client.go`

### S05: Implementation Migration
- Migrated four concrete packages and three cmd entrypoints to typed Service Interface contracts
- Adapter pattern (three thin unexported adapters embedding `*Service`) to handle identical-signature multi-interface constraint
- Key files: `pkg/ari/server/server.go`, `pkg/agentrun/server/service.go`, `pkg/ari/client/client.go`, `cmd/mass/commands/server/command.go`

### S06: Legacy Cleanup
- Deleted `pkg/rpc/` (844 lines), `pkg/agentd/shim_client.go`, `pkg/ari/server.go` (1235 lines); extracted shared mock infra to `mock_shim_server_test.go`
- Key files: `pkg/agentd/mock_shim_server_test.go`, `pkg/ari/server/server_test.go`

---

## M011: Reduce Shim Event Translation Overhead (2026-04-12)

### S01: Full ACP event translation coverage
- Eliminated silent discard of 5 ACP `SessionUpdate` branches; added 5 new event types and 15+ support types
- All 11 `translate()` branches covered; `decodeEventPayload` handles 17 types; full ACP field fidelity preserved
- Key files: `pkg/events/types.go`, `pkg/events/translator.go`, `pkg/events/translate_rich_test.go`, `pkg/events/wire_shape_test.go`

### S02: Test coverage for all 22 plan matrix items
- Added 31 new tests; documented ACP SDK ContentBlock union `_meta` strip behavior
- Key files: `pkg/events/translate_rich_test.go`, `pkg/events/wire_shape_test.go`

---

## M010: CLI Consolidation: subcommands layout + workspace UX fixes (2026-04-11)

### S01: cmd/agentd subcommands layout
- Refactored into `subcommands/server`, `subcommands/shim`, `subcommands/workspacemcp`; `main.go` reduced to 8 lines
- Key files: `cmd/mass/main.go`, `cmd/mass/commands/root.go`

### S02: cmd/massctl subcommands layout
- Refactored into `subcommands/{agent,agentrun,daemon,shim,workspace}` with shared `cliutil`; eliminated package globals
- Key files: `cmd/massctl/main.go`, `cmd/massctl/commands/root.go`, `cmd/massctl/commands/cliutil/cliutil.go`

### S03: workspace CLI reshape
- `create` split into `local/git/empty/-f` subcommands; `workspace get` added; `workspace send` made positional
- Key files: `cmd/massctl/commands/workspace/command.go`, `cmd/massctl/commands/workspace/create/command.go`

---

## M009: Simplify ACP Client in Runtime (2026-04-11)

### S01: Remove TerminalManager + fs/terminal implementations
- Deleted ~900 lines of terminal and fs code from `pkg/runtime/`; ACP client now only handles `SessionUpdate` and `RequestPermission`
- `Initialize` no longer advertises fs capabilities; 7 methods return not-supported
- Key files: `pkg/runtime/client.go`, `pkg/runtime/runtime.go`, `internal/testutil/mockagent/main.go`

---

## M008: CLI Consolidation + API Model Rename (2026-04-10)

### S01: Binary skeleton reorganization
- Replaced flat flag-based `cmd/mass/main.go` with cobra tree (`server/shim/workspace-mcp`); inlined old binaries as subcommands
- Key files: `cmd/mass/main.go`, `cmd/massctl/main.go`, `Makefile`

### S02: --root config + Runtime entity + self-fork
- Eliminated `config.yaml`; `Options{Root}` derives all paths deterministically; `meta.Runtime` in bbolt `v1/runtimes`; `ProcessManager` self-forks via `os.Executable()`
- Key files: `pkg/agentd/options.go`, `pkg/meta/runtime.go`

### S03: CLI grammar alignment + socket validation
- `agentrun prompt` positional flags; `workspace create` positional `<type> <name>`; socket path overflow validated at `agentrun/create` entry (-32602)
- Key files: `pkg/spec/maxsockpath_darwin.go`, `pkg/spec/maxsockpath_linux.go`

### S04: Cleanup + API rename (agent→AgentTemplate, agentrun→running instance)
- Three-layer simultaneous rename: meta DB layer, ARI types, ARI server dispatch + CLI; deleted three obsolete cmd directories
- Key files: `pkg/meta/models.go`, `pkg/ari/types.go`, `pkg/ari/server.go`, `cmd/massctl/agent_template.go`, `cmd/massctl/agent.go`

---

## M007: MASS Platform Terminal State Refactor (2026-04-09)

### S01: Storage + Model Foundation
- Replaced SQLite/CGo with pure-Go bbolt; deleted Session/Room/AgentState/SessionState; `StatusIdle` replaces `StatusCreated`
- Key files: `pkg/meta/models.go`, `pkg/meta/store.go`, `pkg/meta/agent.go`, `pkg/meta/workspace.go`, `pkg/spec/state_types.go`

### S02: agentd Core Adaptation
- Enforced shim write authority boundary (`startEventConsumer`); best-effort session recovery with correct Subscribe-before-Load ordering
- Key files: `pkg/agentd/process.go`, `pkg/agentd/recovery.go`, `pkg/agentd/shim_boundary_test.go`

### S03: ARI Handler Rewrite
- Full 946-line JSON-RPC 2.0 server for all `workspace/*` and `agent/*` methods; `agentToInfo` centralizes `AgentInfo` construction; `miniShimServer` for test injection
- Key files: `pkg/ari/server.go`, `pkg/ari/server_test.go`, `pkg/ari/types.go`

### S04: CLI + workspace-mcp-server + Design Docs
- Renamed `room-mcp-server` → `workspace-mcp-server`; `room_send` → `workspace_send`; full design doc rewrite
- Key files: `cmd/workspace-mcp-server/main.go`, `cmd/massctl/workspace.go`, `docs/design/mass/ari-spec.md`, `docs/design/mass/agentd.md`

### S05: Integration Tests + Final Verification
- Full integration test suite rewrite for M007 ARI surface; fixed 3 pre-existing bugs (socket path mismatch, missed idle notification, stale socket cleanup)
- Key files: `tests/integration/session_test.go`, `tests/integration/e2e_test.go`, `tests/integration/restart_test.go`

---

## M006: Fix golangci-lint v2 issues (2026-04-09)

### S01–S07: Full lint cleanup (202 → 0 issues)
- Auto-fixed gci + gofumpt (56 issues via `golangci-lint fmt`); manually fixed unconvert/copyloopvar/ineffassign (24); misspell/unparam (17); gocritic (13 active); testifylint (5 active)
- S04 (unused) and S05 (errorlint) were clean no-ops — M005 migration had already eliminated targets
- Key files: `pkg/agentd/process.go`, `pkg/runtime/terminal.go`, `pkg/workspace/git.go`, `.golangci.yaml`

---

## M005: agentd Agent Model Refactoring (2026-04-08)

### S01: Design Contract First
- Rewrote all 7 authority docs (`ari-spec.md`, `agentd.md`, `run-rpc-spec.md`, `room-spec.md`, `contract-convergence.md`); `scripts/verify-m005-s01-contract.sh` as mechanical proof
- Key files: `docs/design/mass/ari-spec.md`, `docs/design/runtime/run-rpc-spec.md`, `scripts/verify-m005-s01-contract.sh`

### S02: Metadata Layer (agents + sessions tables)
- New `agents` table with `(room, name)` UNIQUE key; `sessions.agent_id` FK; `AgentState` (5 states: creating/created/running/stopped/error); `paused:warm/paused:cold` retired
- Key files: `pkg/meta/schema.sql`, `pkg/meta/models.go`, `pkg/meta/agent.go`, `pkg/meta/session.go`

### S03: ARI Handler Migration (agent/* + room/*)
- Full `agent/*` handler set (create/prompt/stop/remove/restart/status/list); `room/delete` auto-deletes stopped agents; `deliverPrompt` shared helper
- Key files: `pkg/ari/server.go`, `pkg/ari/types.go`

### S04: Async agent/create
- `agent/create` returns `creating` immediately; background goroutine handles shim bootstrap; workspace refs acquired using linked session ID
- Key files: `pkg/agentd/agent.go`, `pkg/agentd/process.go`

### S05: Turn-aware event ordering
- `turnId` assigned at `turn_start`, cleared at `turn_end`; `streamSeq` (`*int`) resets 0 per turn; `runtime/stateChange` excluded from turn ordering
- Key files: `pkg/events/envelope.go`, `pkg/events/translator.go`, `pkg/rpc/server.go`

### S06: room-mcp-server SDK upgrade
- Replaced hand-rolled MCP with mcp-go SDK (`server.AddTool` with `json.RawMessage` InputSchema to preserve custom schemas)
- Key files: `cmd/room-mcp-server/main.go`

### S07: CLI + integration tests
- `massctl agent` and `massctl agentrun` subcommands; consolidated integration test helpers; recovery test uses kill-all-shims strategy
- Key files: `cmd/massctl/agent.go`, `cmd/massctl/helpers.go`, `tests/integration/`

---

## M004: Realized Room Runtime and Routing (2026-04-08)

### S01: Room Lifecycle and ARI Surface
- Converged communication vocabulary (mesh/star/isolated replaces broadcast/direct/hub); `room/create`, `room/status`, `room/delete` ARI handlers; room-existence validation in `session/new`
- Key files: `pkg/ari/server.go`, `pkg/ari/types.go`, `pkg/meta/models.go`, `pkg/meta/room.go`

### S02: Routing Engine and MCP Tool Injection
- `room/send` ARI handler; `deliverPrompt` shared helper; `room-mcp-server` hand-rolled MCP binary; automatic MCP injection in `generateConfig` for room sessions
- Key files: `cmd/room-mcp-server/main.go`, `pkg/agentd/process.go`, `pkg/spec/types.go`

### S03: End-to-End Multi-Agent Integration Proof
- `TestARIMultiAgentRoundTrip`: 3 agents, bidirectional A↔B + A→C messaging, full teardown; `TestARIRoomTeardownGuards`: adversarial ordering proof
- Key files: `pkg/ari/server_test.go`

---

## M003: Recovery and Safety Hardening (2026-04-08)

### S01: Fail-Closed Recovery Posture
- `RecoveryPhase` atomic type (idle/recovering/complete); `recoveryGuard` blocks `session/prompt`+`session/cancel` with JSON-RPC -32001; always transitions to Complete on all exit paths
- Key files: `pkg/agentd/recovery_posture.go`, `pkg/agentd/recovery.go`

### S02: Live Shim Reconnect and Truthful Session Rebuild
- Shim-vs-DB state reconciliation in `recoverSession`; TOCTOU-free socket cleanup (`os.Remove` unconditional)
- Key files: `pkg/agentd/recovery.go`, `cmd/mass/main.go`

### S03: Atomic Event Resume and Damaged-Tail Tolerance
- `ReadEventLog` rewritten with `bufio.Scanner` + two-pass damaged-tail classification; `Translator.SubscribeFromSeq` holds mutex during log read + subscription
- Key files: `pkg/events/log.go`, `pkg/events/translator.go`, `pkg/rpc/server.go`

### S04: Reconciled Workspace Ref Truth and Safe Cleanup
- `handleWorkspaceCleanup` gates on DB `ref_count`; `AcquireWorkspace` called at `session/new`; `Registry.RebuildFromDB` + `WorkspaceManager.InitRefCounts` after restart
- Key files: `pkg/ari/registry.go`, `pkg/workspace/manager.go`, `pkg/meta/workspace.go`, `cmd/mass/main.go`

---

## M002: Contract Convergence and ACP Runtime Truthfulness (2026-04-07)

### S01: Design Contract Convergence
- Produced `docs/design/contract-convergence.md` authority map; `session/new` = config-only bootstrap; contract verifier script + example bundle validation tests as mechanical proof surface
- Key files: `docs/design/contract-convergence.md`, `docs/design/runtime/run-rpc-spec.md`, `scripts/verify-m002-s01-contract.sh`

### S02: Shim-RPC Clean Break
- Replaced all legacy PascalCase methods + `$/event` with `session/*` + `runtime/*`; `events.Envelope{Method, Seq, Params}` as single live+replay shape; monotonic seq in `Translator`
- Key files: `pkg/events/envelope.go`, `pkg/events/translator.go`, `pkg/rpc/server.go`, `pkg/agentd/shim_client.go`

### S03: Recovery and Persistence Truth-Source
- Schema v2 (`bootstrap_config`, `shim_socket_path`, `shim_state_dir`, `shim_pid` columns); `RecoverSessions` startup pass with `SubscribeFromSeq` for gap-free event resume; fail-closed for dead shims
- Key files: `pkg/meta/schema.sql`, `pkg/meta/session.go`, `pkg/agentd/recovery.go`, `tests/integration/restart_test.go`

### S04: Real CLI Integration Verification
- Reusable test harness for full ARI session lifecycle with real `gsd-pi` and `claude-code` runtime classes; generous timeouts (30s start, 120s prompt); graceful skip without API keys
- Key files: `tests/integration/real_cli_test.go`

---

## M001-tvc4z0: Phase 2 — agentd Core (2026-04-06)

### S01: Scaffolding + exitCode
- `mass` daemon foundation: YAML config, workspace manager init, ARI server bootstrap, graceful shutdown; `ExitCode *int` added to shim state
- Key files: `cmd/mass/main.go`, `pkg/agentd/config.go`

### S02: Metadata Store (SQLite)
- SQLite WAL mode, FK constraints, embedded schema (`go:embed`); full CRUD for Session/Workspace/Room; optional init for ephemeral mode
- Key files: `pkg/meta/store.go`, `pkg/meta/schema.sql`, `pkg/meta/session.go`

### S03–S04: RuntimeClass Registry + Session Manager
- Thread-safe registry with env substitution (`os.Expand`); 5-state session machine (created/running/paused:warm/paused:cold/stopped) with 9 valid transitions
- Key files: `pkg/agentd/runtimeclass.go`, `pkg/agentd/session.go`

### S05–S06: Process Manager + ARI Service
- Full session startup: runtimeClass → config.json → bundle → fork shim → socket wait → connect; 9 `session/*` handlers; `session/prompt` auto-starts on `created`
- Key files: `pkg/agentd/process.go`, `pkg/ari/server.go`, `pkg/ari/types.go`

### S07–S08: massctl CLI + Integration Tests
- 11-command CLI; `pkg/ari/client.go` single-shot RPC client; 8 integration tests proving full `mass → agent-run → mockagent` pipeline
- Key files: `cmd/massctl/main.go`, `pkg/ari/client.go`, `tests/integration/`

---

## M001-tlbeko: Declarative Workspace Provisioning (2026-04-03)

### S01: Workspace Spec + Git Handler
- Discriminated union `Source` (git/emptyDir/local) with custom JSON marshaling; `GitHandler` with ref/depth clone; `GitError` structured type with Phase field
- Key files: `pkg/workspace/spec.go`, `pkg/workspace/git.go`, `pkg/workspace/errors.go`

### S02: EmptyDir + Local Handlers
- `EmptyDirHandler` (managed) and `LocalHandler` (unmanaged — returns `source.Local.Path` directly, not targetDir); managed/unmanaged semantics established
- Key files: `pkg/workspace/emptydir.go`, `pkg/workspace/local.go`, `pkg/workspace/handler.go`

### S03: Hook Execution
- `HookExecutor` sequential abort-on-failure; `HookError` with Phase/HookIndex; stdout+stderr capture; context cancellation support
- Key files: `pkg/workspace/hook.go`

### S04: Workspace Lifecycle
- `WorkspaceManager` Prepare/Cleanup with reference counting (Acquire/Release); best-effort teardown semantics; `WorkspaceError` Phase field
- Key files: `pkg/workspace/manager.go`

### S05: ARI Workspace Methods
- `workspace/prepare|list|cleanup` JSON-RPC handlers over Unix socket; UUID workspace IDs; `Registry` with `RefCount=0` cleanup guard
- Key files: `pkg/ari/server.go`, `pkg/ari/registry.go`, `pkg/ari/types.go`
