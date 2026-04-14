# Changelog

> Auto-generated from GSD milestone summaries. Do not edit directly.
> Last synced: 2026-04-14 after M012

## M012: Codebase Refactor: Service Interface + Unified RPC + Directory Restructure (2026-04-14)

Replaced three duplicated JSON-RPC implementations with a single transport-agnostic `pkg/jsonrpc/` framework, established typed Service Interfaces for ARI and Shim, performed clean-break ARI wire contract convergence, and restructured API packages for clarity and consistency.

### S01: pkg/jsonrpc/ Transport-Agnostic Framework
- Built shared JSON-RPC foundation: Server (ServiceDesc + interceptor chain), Client (256-buffer FIFO notification worker), RPCError, Peer; 18 protocol tests
- Key files: `pkg/jsonrpc/server.go`, `pkg/jsonrpc/client.go`, `pkg/jsonrpc/errors.go`, `pkg/jsonrpc/peer.go`

### S02: Phase 2a: Pure Rename/Move
- `api/spec` → `api/runtime`, `pkg/shimapi` → `api/shim`; 22 files updated; zero wire format changes
- Key files: `api/runtime/config.go`, `api/runtime/state.go`, `api/shim/types.go`

### S03: Phase 2b: ARI Clean-Break Contract Convergence
- ARI wire format uses Agent/AgentRun/Workspace domain shapes; ARIView() helpers strip internal fields; Info DTOs deleted; ari-spec.md updated
- Key files: `api/ari/domain.go`, `api/ari/types.go`, `docs/design/agentd/ari-spec.md`

### S04: Phase 3: Service Interface + Register + Typed Clients
- WorkspaceService/AgentRunService/AgentService interfaces with Register functions; typed Workspace/AgentRun/Agent/ShimClient clients
- Key files: `api/ari/service.go`, `api/ari/client.go`, `api/shim/service.go`, `api/shim/client.go`

### S05: Phase 4: Implementation Migration
- Adapter pattern for pkg/ari/server (resolves WorkspaceService.List vs AgentService.List signature conflict); pkg/ari/client ARIClient; cmd entrypoints + pkg/agentd migrated to typed service interfaces; all 18 packages pass
- Key files: `pkg/ari/server/server.go`, `pkg/ari/client/client.go`, `pkg/agentd/process.go`, `pkg/agentd/recovery.go`, `cmd/agentd/subcommands/server/command.go`

### S06: Phase 5: Cleanup
- Deleted `pkg/rpc/`, `pkg/agentd/shim_client.go`, `pkg/ari/server.go` monolith; test harness migrated to new API; mock infrastructure extracted to `mock_shim_server_test.go`; all 17 packages pass
- Key files: DELETED `pkg/rpc/`, `pkg/agentd/shim_client.go`, `pkg/ari/server.go` — new `pkg/agentd/mock_shim_server_test.go`, migrated `pkg/ari/server_test.go`

---

## M011: Reduce Shim Event Translation Overhead (2026-04-12)

ACP event translation now preserves full data fidelity — all 11 SessionUpdate branches translated, 5 new event types added, union types mirror ACP flat wire shape.

### S01: Core types, translator, and envelope
- Rewrote `pkg/events/types.go` with 15+ new support types; translate() covers all 11 SessionUpdate branches; 5 new constants in `api/events.go`; design docs updated
- Key files: `api/events.go`, `pkg/events/types.go`, `pkg/events/translator.go`, `pkg/events/envelope.go`

### S02: Fix and extend tests
- Fixed 6 broken tests; added 31 new tests covering all 22 plan matrix items; discovered ACP SDK strips `_meta` from ContentBlock union MarshalJSON
- Key files: `pkg/events/translate_rich_test.go`, `pkg/events/wire_shape_test.go`

---

## M010: CLI Consolidation: subcommands layout + workspace UX fixes (2026-04-11)

Moved cmd/agentd and cmd/agentdctl into subcommands/ layout and reshaped workspace CLI per review.

- S01: cmd/agentd → `subcommands/server`, `subcommands/shim`, `subcommands/workspacemcp`; main.go is 8 lines
- S02: cmd/agentdctl → `subcommands/{agent,agentrun,daemon,shim,workspace}` with shared cliutil; getClient closure injection; no package globals
- S03: workspace create split into local/git/empty/-f subcommands; workspace get added; workspace send made positional
- Key files: `cmd/agentd/main.go`, `cmd/agentdctl/main.go`, `cmd/agentdctl/subcommands/`

---

## M009: Simplify ACP Client in Runtime (2026-04-11)

Removed TerminalManager and fs/terminal implementations from pkg/runtime; ACP client now only handles SessionUpdate and RequestPermission.

- Deleted ~900 lines of terminal/fs code; Initialize no longer advertises fs capabilities; mockagent updated
- Key files: `pkg/runtime/client.go`, `pkg/runtime/runtime.go`

---

## M008: CLI Consolidation + API Model Rename (2026-04-10)

Consolidated 5 binaries into 2 (agentd + agentdctl), eliminated config.yaml via --root flag, elevated RuntimeClass to DB-persisted AgentTemplate, renamed API model to agent=template / agentrun=running instance.

- S01: Binary skeleton — cobra tree for agentd (server/shim/workspace-mcp); agentdctl shim client; stub agentrun subcommands
- S02: `--root` config + Runtime entity + self-fork — `Options{Root}` with 5 deterministic path helpers; `meta.Runtime` in bbolt; `forkShim` self-forks via `os.Executable()`; `runtime/*` ARI CRUD
- S03: CLI grammar + socket validation — agentrun prompt flags; workspace create positional; socket path validated at agentrun/create entry (-32602 on overflow)
- S04: Three-layer rename — `meta.Runtime→AgentTemplate`, `meta.Agent→AgentRun`; ARI `runtime/*→agent/*`, `agent/*→agentrun/*`; all 8 integration tests pass
- Key files: `cmd/agentd/`, `cmd/agentdctl/`, `pkg/agentd/options.go`, `pkg/meta/models.go`, `pkg/ari/server.go`

---

## M007: OAR Platform Terminal State Refactor (2026-04-09)

Replaced SQLite with bbolt, unified to spec.Status single state enum, eliminated Session/Room concepts, unified Workspace as grouping+filesystem, established (workspace,name) agent identity, enforced shim as sole post-bootstrap write authority.

- S01: bbolt store + model foundation; StatusCreated→StatusIdle; Session/Room deleted; pkg/ari/server.go replaced with compilable stub; 37 bbolt unit tests
- S02: agentd core adaptation — D088 shim write authority boundary via buildNotifHandler; RestartPolicy tryReload/alwaysNew; Subscribe-before-Load ordering invariant
- S03: ARI handler rewrite — 946-line server; handleAgentCreate async pattern; agentToInfo helper; miniShimServer in ari_test; 22 handler tests
- S04: CLI + workspace-mcp-server + design docs — room-mcp-server→workspace-mcp-server; ari-spec.md + agentd.md fully rewritten
- S05: Integration tests — rewrote all integration files; fixed 3 pre-existing bugs in process.go (socket path mismatch, missed idle notification, stale socket cleanup)
- Key files: `pkg/meta/store.go`, `pkg/agentd/process.go`, `pkg/agentd/recovery.go`, `pkg/ari/server.go`, `cmd/workspace-mcp-server/main.go`, `docs/design/agentd/ari-spec.md`

---

## M006: Fix golangci-lint v2 issues (2026-04-09)

Eliminated all 202 golangci-lint v2 issues across 11 linter categories — codebase reports 0 issues.

- S01: gci + gofumpt (56 issues, auto-fixed via `golangci-lint fmt ./...`)
- S02: unconvert + copyloopvar + ineffassign (24 issues, mostly manual; gocritic --fix adds `errors.As` without `errors` import)
- S03: misspell + unparam (17 issues; unparam masks multiple findings per function)
- S04/S05: unused + errorlint — clean no-ops (M005 migration already cleaned these)
- S06: gocritic (13 active: filepathJoin, importShadow, appendAssign, exitAfterDefer, builtinShadowDecl)
- S07: testifylint (5 active: require-error guards)
- Key files: `pkg/agentd/process.go`, `pkg/ari/server.go`, `pkg/workspace/git.go`, `.golangci.yaml`

---

## M005: agentd Agent Model Refactoring (2026-04-08)

Refactored agentd from session-centric to agent-centric: new agents table (room+name identity), 10-method agent/* ARI surface, async lifecycle, turn-aware event ordering (turnId/streamSeq), SDK-based room-mcp-server, fail-safe daemon recovery.

- S01: Design contract — 7 authority docs updated; contract verifier script; ARI events renamed agent/update + agent/stateChange at orchestrator boundary
- S02: State machine — AgentState (creating/created/running/stopped/error); paused:warm/paused:cold retired; agents + sessions tables with FK
- S03: ARI handlers — agent/* surface; async agent/create; handleRoomDelete guards on agent state
- S04: Async lifecycle — agent/create goroutine; agent/restart; OAR_AGENT_ID / OAR_AGENT_NAME env vars
- S05: Turn-aware events — turnId/streamSeq/*int; Translator state mutations under mu.Lock
- S06: room-mcp-server SDK migration — go-sdk/mcp; room_send/room_status tools
- S07: Recovery + integration tests — recoverSession returns (spec.Status, error); TestAgentdRestartRecovery; kill-all-shims strategy
- Key files: `pkg/meta/agent.go`, `pkg/agentd/agent.go`, `pkg/ari/server.go`, `pkg/events/envelope.go`, `cmd/room-mcp-server/main.go`, `tests/integration/`

---

## M004: Realized Room Runtime and Routing (2026-04-08)

Turned the Room from design-only contract into working runtime with ARI lifecycle, point-to-point routing via room/send and room-mcp-server, and end-to-end multi-agent integration proof across 3 agents.

- S01: Room lifecycle — room/create, room/status, room/delete; mesh/star/isolated vocabulary; room-existence validation; active-member guards
- S02: Routing + MCP injection — deliverPrompt helper (shared by session/prompt + room/send); room-mcp-server hand-rolled; MCP injection in generateConfig
- S03: Multi-agent proof — TestARIMultiAgentRoundTrip (3 agents, bidirectional A↔B + A→C); TestARIRoomTeardownGuards; 47-test ARI suite
- Key files: `pkg/ari/server.go`, `pkg/meta/room.go`, `pkg/spec/types.go`, `cmd/room-mcp-server/main.go`

---

## M003: Recovery and Safety Hardening (2026-04-08)

Hardened agentd against daemon restarts: fail-closed recovery posture, truthful shim-vs-DB reconciliation, atomic event resume, damaged-tail tolerance, DB-backed workspace cleanup safety.

- S01: Recovery posture — atomic `RecoveryPhase` gates prompt/cancel; always transitions to Complete on all exit paths; 12 tests
- S02: Live shim reconnect — stopped shims fail-closed; state mismatches reconciled; TOCTOU socket race eliminated (unconditional os.Remove)
- S03: Atomic event resume — SubscribeFromSeq holds Translator mutex during log read + subscription (eliminates History→Subscribe gap); two-pass damaged-tail classification
- S04: Workspace cleanup safety — workspace/cleanup gates on DB ref_count; Registry.RebuildFromDB + WorkspaceManager.InitRefCounts after restart
- Key files: `pkg/agentd/recovery.go`, `pkg/events/log.go`, `pkg/events/translator.go`, `pkg/ari/server.go`, `pkg/ari/registry.go`

---

## M002: Contract Convergence and ACP Runtime Truthfulness (2026-04-07)

Converged all design docs onto one authority map; migrated to clean-break shim protocol (session/* + runtime/*); implemented session config persistence and daemon restart recovery; proven with real CLI integration tests.

- S01: Design contract — 5 design docs converged onto one authority map; contract verifier script; example bundle tests
- S02: Protocol migration — PascalCase/$/event shim methods replaced with session/* + runtime/*; events.Envelope with monotonic seq
- S03: Session recovery persistence — discrete DB columns for hot recovery fields; schema v1→v2 migration; TestAgentdRestartRecovery
- S04: Real CLI proof — TestRealCLI_GsdPi and TestRealCLI_ClaudeCode exercise full ARI session lifecycle
- Key files: `docs/design/`, `scripts/verify-m002-s01-contract.sh`, `pkg/agentd/recovery.go`, `pkg/rpc/server.go`, `pkg/meta/schema.sql`

---

## M001-tvc4z0: Phase 2 — agentd Core (2026-04-06)

Built agentd daemon core: ARI JSON-RPC server over Unix socket, session lifecycle (9 methods), ProcessManager with shim forking, SQLite metadata store, RuntimeClass registry, 27 ARI integration tests.

- S01: ARI server — Unix socket; SIGTERM/SIGINT graceful shutdown; socket file removal for crash recovery
- S02: SQLite metadata — WAL mode with foreign keys; embedded schema via go:embed
- S03: RuntimeClass registry — os.Expand for env substitution; thread-safe registry
- S04-S08: Session state machine; session/prompt auto-start; 27 integration tests including concurrent sessions and restart recovery foundation
- Key files: `cmd/agentd/main.go`, `pkg/ari/server.go`, `pkg/agentd/session.go`, `pkg/agentd/process.go`, `pkg/meta/store.go`

---

## M001-tlbeko: Declarative Workspace Provisioning (2026-04-03)

Workspace Manager prepares workspaces from spec (Git/EmptyDir/Local), executes hooks sequentially with abort-on-failure, tracks references to prevent premature cleanup, exposes ARI JSON-RPC workspace/* methods — 79+ tests pass.

- S01-S02: Workspace spec — Source discriminated union JSON; GitHandler (shallow clone); EmptyDirHandler; LocalHandler (unmanaged, returns path directly)
- S03: Hook executor — sequential abort-on-failure; HookError with HookIndex
- S04: Workspace lifecycle — WorkspaceError with Phase; best-effort teardown cleanup; reference counting (Acquire/Release)
- S05: ARI integration — workspace/prepare, workspace/list, workspace/cleanup; Registry; 4 integration tests
- Key files: `pkg/workspace/spec.go`, `pkg/workspace/git.go`, `pkg/workspace/handler.go`, `pkg/workspace/hook.go`, `pkg/workspace/manager.go`, `pkg/ari/server.go`
