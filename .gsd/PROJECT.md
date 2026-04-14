# Project

## What This Is

Open Agent Runtime (OAR) is a layered runtime for headless coding agents. It manages ACP-speaking agent processes through `agentd` (daemon), `agentd shim` (inlined shim subcommand), and a future orchestrator layer for multi-agent coordination. Modeled after containerd/runc: spec-driven, layered, single-binary operational model.

## Core Value

Reliable, observable agent execution with truthful lifecycle and recovery semantics.

## Current State

**M014 in progress.** S01–S06 complete. S07 (runtime/status overlay + doc updates) remains.

### Active Milestone

**M014 — Enrich state.json + Session Metadata Pipeline**

| Slice | Title | Status |
|-------|-------|--------|
| S01 | Dead placeholder removal | ✅ done |
| S02 | State type enrichment (SessionState, ConfigOption, etc.) | ✅ done |
| S03 | writeState read-modify-write refactor | ✅ done |
| S04 | Translator eventCounts | ✅ done |
| S05 | ACP bootstrap capabilities capture | ✅ done |
| S06 | Session metadata hook chain | ✅ done |
| S07 | runtime/status overlay + doc updates | ⬜ pending (depends S04, S06) |

### Completed Milestones

| Milestone | Title | Summary |
|-----------|-------|---------|
| M001 | Core runtime foundation | agent-shim, agentd, ARI socket, workspace, metadata store, ACP handshake |
| M002 | Contract convergence | ARI client/server contract alignment, JSON-RPC lifecycle |
| M003 | Recovery hardening | Fail-closed recovery, shim-vs-DB reconciliation, atomic event resume, workspace cleanup |
| M004 | Room runtime | mesh/star/isolated room modes, room/send, room-mcp-server |
| M005 | Agent model refactoring | session→agent migration, async lifecycle, agent-centric ARI surface |
| M006 | Fix golangci-lint v2 issues | 202 → 0 issues across 11 linter categories; clean lint posture established |
| M007 | Platform terminal state refactor | bbolt storage, unified spec.Status (idle replaces created), (workspace,name) identity, shim write authority, Room/Session elimination; all integration tests pass; 0 lint issues |
| M008 | CLI consolidation + API model rename | 2-binary model (agentd + agentdctl), --root startup, DB-persisted AgentTemplate, resource-first grammar, agent=template/agentrun=instance rename; all 8 integration tests pass |
| M012 | Codebase Refactor: Service Interface + Unified RPC + Directory Restructure | Typed service interfaces, pkg/jsonrpc unified transport, ARI wire contract convergence, adapter pattern, legacy package cleanup; make build + go test ./... pass with zero legacy references |
| M013 | Package Restructure: Clean api/ Boundary + Event/Runtime Colocation | api/ deleted; pkg/ari/{api,server,client} + pkg/shim/{api,server,client,runtime/acp} canonical structure; pkg/events + pkg/runtime eliminated; make build + go test ./... + go vet (first-party) all pass |

### What's Implemented

- `agentd` manages agents with **(workspace, name)** identity (no UUID), async lifecycle, fail-closed recovery
- `agentd` is a **cobra tree**: `agentd server` (daemon, --root flag, no config.yaml), `agentd shim` (inlined from agent-shim), `agentd workspace-mcp` (inlined from workspace-mcp-server)
- `agentdctl` is a **full-featured CLI**: `agentdctl agent` (template CRUD: apply/get/list/delete) + `agentdctl agentrun` (lifecycle: create/list/status/prompt/stop/delete/restart/attach/cancel) + workspace/daemon/shim commands
- **ARI surface (final):** `workspace/*` (create/status/list/delete/send) + `agent/*` (set/get/list/delete — AgentTemplate CRUD) + `agentrun/*` (create/prompt/cancel/stop/delete/restart/list/status/attach — running instance lifecycle)
- **pkg/ari tri-split (M013/S02):** `pkg/ari/api` (pure types + ARI method constants), `pkg/ari/server` (interfaces, Registry, RPC dispatch), `pkg/ari/client` (typed ARIClient + simple Client); api/ari/ directory deleted
- **pkg/shim tri-split (M013/S03+S04):** `pkg/shim/api` (pure shim types + service interface + client + method constants + event wire types + EventType*/Category* constants), `pkg/shim/server` (shim service + Translator + EventLog), `pkg/shim/client` (dial helper), `pkg/shim/runtime/acp` (ACP runtime Manager and client)
- **writeState read-modify-write closure pattern (M014/S03):** All 7 writeState call sites use `func(*apiruntime.State)` closures; Session/EventCounts never clobbered by lifecycle writes; UpdatedAt stamped unconditionally on every write
- **State type enrichment (M014/S02):** SessionState, AgentInfo, AgentCapabilities, ConfigOption (with Select variant), AvailableCommand (with Unstructured input), EventCounts — all in pkg/runtime-spec/api/state.go with round-trip JSON fidelity
- **ACP bootstrap capabilities capture (M014/S05):** Manager.Create() captures InitializeResponse, converts ACP types to runtime-spec/api types via convertInitializeToSession(), writes Session to state.json at bootstrap-complete; synthetic bootstrap-metadata state_change event emitted after Translator.Start() with sessionChanged:["agentInfo","capabilities"]
- **Session metadata hook chain (M014/S06):** Translator.maybeNotifyMetadata fires for 4 metadata event types → Manager.UpdateSessionMetadata read-modify-writes state.json → state_change emitted with sessionChanged; buildSessionUpdate converts apishim→apiruntime types with sort helpers for deterministic output; command.go wires SetSessionMetadataHook + SetEventCountsFn
- **Translator eventCounts (M014/S04):** Translator tracks EventCounts in memory; counts flushed to state.json on every write via SetEventCountsFn
- **Dead placeholder removal (M014/S01):** EventTypeFileWrite, EventTypeFileRead, EventTypeCommand, and their wire types removed from codebase
- **api/ directory deleted (M013/S03):** No more `github.com/zoumo/oar/api` import targets
- **pkg/runtime-spec/api as sole Status/EnvVar home (M013/S01):** api/runtime/ deleted; all consumers use `apiruntime "github.com/zoumo/oar/pkg/runtime-spec/api"`
- **Typed Service Interfaces (M012):** ShimService interface in pkg/shim/api + ARI service interfaces in pkg/ari/server — all implementations satisfy typed contracts
- **pkg/jsonrpc unified transport (M012):** Single JSON-RPC transport used by both ARI server and shim server; legacy pkg/rpc, pkg/ari/server.go monolith, and pkg/agentd/shim_client.go all deleted
- **DB-persisted agent templates**: `meta.AgentTemplate` in `v1/agents` bbolt bucket; ARI `agent/set|get|list|delete`; `agentdctl agent apply -f` YAML-based CLI
- **bbolt metadata store**: `v1/workspaces/{name}` + `v1/agentruns/{workspace}/{name}` + `v1/agents/{name}` bucket layout
- **Self-fork shim**: `forkShim` uses `os.Executable()` for single-binary deployment; `OAR_SHIM_BINARY` env override for testing
- **agent-shim** starts ACP agent processes, performs the ACP handshake, exposes `session/*` + `runtime/*` shim RPC surface
- **spec.Status as sole state enum**: creating/idle/running/stopped/error
- **Socket path overflow guard**: `ValidateAgentSocketPath` in ProcessManager; early `-32602` at `agentrun/create` entry before any DB write; platform limits in build-tag files

## Architecture / Key Patterns

- **2-binary model:** `agentd` (server/shim/workspace-mcp subcommands) + `agentdctl` (agent/agentrun/workspace/shim resource commands)
- **ARI protocol:** JSON-RPC 2.0 over Unix domain socket
- **OCI-inspired layering:** workspace=rootfs, agent (AgentTemplate)=container definition, agentrun=task/running instance, shim=runc equivalent
- **writeState closure pattern (M014/S03, D119):** writeState accepts `func(*apiruntime.State)` — reads existing state (or zero on first write), applies closure, stamps UpdatedAt, writes atomically; callers mutate only the fields they care about
- **Session metadata hook chain (M014/S06):** Translator.maybeNotifyMetadata (type-switch on 4 metadata types) → Manager.UpdateSessionMetadata (read-modify-write + state_change emit) → state.json updated; lock order: Translator.mu → release → Manager.mu → release → Translator.mu (D120)
- **ACP type conversion pattern (M014/S05):** convertInitializeToSession maps ACP SDK types (Implementation, AgentCapabilities) to runtime-spec/api types (AgentInfo, AgentCapabilities) — reusable pattern for future ACP→state.json field mappings
- **Synthetic metadata event pattern (M014/S05, D124):** idle→idle state_change with reason + sessionChanged for metadata-only events; emitted after trans.Start() so subscribers discover via history backfill (fromSeq=0)
- **pkg/ari tri-split pattern (M013/S02):** api/ for pure types+constants, server/ for interfaces+dispatch, client/ for dial helpers
- **pkg/shim tri-split pattern (M013/S03+S04):** api/ for pure shim types+service interface+event wire types+method constants, server/ for implementation+translator+eventlog, client/ for dial helper, runtime/acp/ for ACP runtime
- **Event wire types location (M013/S04):** All event wire types live in `pkg/shim/api`; Translator and EventLog in `pkg/shim/server`
- **Sealed interface cross-package accessor (M013/S04, D118):** EventTypeOf(ev Event) in pkg/shim/api/event_types.go
- **Typed service interface pattern (M012):** pkg/shim/api.ShimService + pkg/ari/server.*Service interfaces
- **Adapter pattern for conflicting method names (M012):** Three thin unexported adapter structs for WorkspaceService.List and AgentService.List (K077, D112)
- **errors.Is vs os.IsNotExist (K081):** spec.ReadState wraps with fmt.Errorf — must use errors.Is(err, os.ErrNotExist), not os.IsNotExist
- **bbolt buckets:** `v1/workspaces/{name}`, `v1/agentruns/{workspace}/{name}`, `v1/agents/{name}`
- **--root derived paths**: Options.SocketPath(), WorkspaceRoot(), BundleRoot(), MetaDBPath() — no config file needed
- **macOS socket path limit**: t.TempDir() paths often exceed 104-byte Unix socket limit; use os.MkdirTemp("/tmp", "oar-*") for socket-sensitive tests (K075)
- **pkg/jsonrpc notifCh race**: Pre-existing send-on-closed-channel race visible under -count=3; single-run go test ./... is the acceptance bar (K078)
- **rg exit code semantics**: exit 1 = no matches (not a failure); zero-match verification gates must use `! rg PATTERN` (K082)

## Milestone Sequence

- [x] M001: Core runtime foundation
- [x] M002: Contract convergence
- [x] M003: Recovery hardening
- [x] M004: Room runtime
- [x] M005: Agent model refactoring
- [x] M006: Fix golangci-lint v2 issues
- [x] M007: Platform terminal state refactor
- [x] M008: CLI consolidation + API model rename
- [x] M012: Codebase Refactor: Service Interface + Unified RPC + Directory Restructure
- [x] M013: Package Restructure: Clean api/ Boundary + Event/Runtime Colocation
- [ ] M014: Enrich state.json + Session Metadata Pipeline — S01–S06 done; S07 remaining
