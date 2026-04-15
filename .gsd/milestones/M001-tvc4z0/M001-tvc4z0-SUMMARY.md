---
id: M001-tvc4z0
title: "Phase 2 — agentd Core"
status: complete
completed_at: 2026-04-06T18:44:02.758Z
key_decisions:
  - D006: ExitCode as optional pointer field — ExitCode is *int because it's only meaningful after process exits. Nil means process is running, non-nil means exited with that code.
  - D007: Socket file removal before Listen() for unclean shutdown recovery — Unix socket files persist after daemon crashes, must be removed before listening.
  - D008: Graceful shutdown with SIGTERM/SIGINT signal handling — srv.Shutdown() allows in-flight requests to complete before closing connections.
  - D009: Optional metadata store initialization — Daemon can run without SQLite persistence, enabling ephemeral mode for testing.
  - D010: SQLite WAL journal mode with foreign keys — Better concurrency (readers don't block writers) and data integrity enforcement.
  - D011: Embedded SQL schema with go:embed — Single-binary deployment without external schema files.
  - Env substitution at registry creation time — os.Expand resolves ${VAR} patterns once at startup for consistent resolved values.
  - session/prompt auto-start behavior — session/prompt automatically starts shim process when session.State == 'created', simplifying CLI UX.
  - exec.Command (not CommandContext) for shim processes — Shim must run independently of request context; lifecycle managed by Stop/watchProcess.
key_files:
  - cmd/agentd/main.go
  - cmd/agentdctl/main.go
  - cmd/agentdctl/session.go
  - cmd/agentdctl/workspace.go
  - pkg/agentd/config.go
  - pkg/agentd/process.go
  - pkg/agentd/runtimeclass.go
  - pkg/agentd/session.go
  - pkg/agentd/shim_client.go
  - pkg/ari/client.go
  - pkg/ari/server.go
  - pkg/ari/types.go
  - pkg/meta/models.go
  - pkg/meta/schema.sql
  - pkg/meta/session.go
  - pkg/meta/store.go
  - pkg/meta/workspace.go
  - tests/integration/e2e_test.go
  - tests/integration/session_test.go
  - tests/integration/restart_test.go
  - tests/integration/concurrent_test.go
lessons_learned:
  - K023: exec.CommandContext kills process when context cancelled — NEVER use it for long-running daemon processes that should outlive the request. Use exec.Command and manage lifecycle explicitly.
  - K024: JSON-RPC error code semantics — Use CodeInvalidParams for client-provided invalid state, CodeInternalError for server-side failures. Error code choice matters for client debugging.
  - K025: macOS Unix socket path limitation — 104-char limit on macOS. Use short paths like /tmp/mass-{pid}-{counter}.sock for integration tests, never t.TempDir().
  - K026: ARI client serialization — JSON-RPC clients are not thread-safe for concurrent calls. The mutex only protects ID generation. Serialize full request/response cycle or use separate clients.
  - K027: Integration test cleanup with pkill — Tests that spawn processes need aggressive cleanup. Use pkill in cleanup function to ensure clean state for subsequent tests.
---

# M001-tvc4z0: Phase 2 — agentd Core

**Built agentd daemon with session/process management, ARI service, CLI, and 8 integration tests proving full agentd → agent-shim → mockagent pipeline works end-to-end**

## What Happened

Milestone M001-tvc4z0 delivered Phase 2 of the Open Agent Runtime architecture, transitioning from single-agent management (Phase 1 agent-shim layer) to multi-agent management through the agentd daemon.

**S01: Scaffolding + Phase 1.3 exitCode**
Established agentd daemon foundation: YAML config parsing (Socket, WorkspaceRoot, MetaDB fields), workspace manager and registry initialization, ARI server bootstrap, and graceful shutdown with SIGTERM/SIGINT handling. Added ExitCode field to shim State/GetStateResult — pointer type (*int) with omitempty because exit code only exists after process exits.

**S02: Metadata Store (SQLite)**
Implemented SQLite-based metadata store with WAL journal mode, foreign key constraints, and embedded schema (go:embed). Full CRUD operations for Session, Workspace, Room entities with transaction support (BeginTx). Reference counting pattern for workspace acquisition/release. Optional initialization enables ephemeral daemon mode.

**S03: RuntimeClass Registry**
Created RuntimeClassRegistry with thread-safe Get/List methods (sync.RWMutex). Env substitution with os.Expand resolves ${VAR} patterns at registry creation time. Validation ensures Command field is required. Capabilities defaults: Streaming=true, SessionLoad=false, ConcurrentSessions=1.

**S04: Session Manager**
Implemented SessionManager with CRUD operations and state machine validation. Five session states: created, running, paused:warm, paused:cold, stopped. Nine valid transitions defined in declarative transition table. Delete protection blocks removal of running and paused:warm sessions. Custom error types (ErrInvalidTransition, ErrDeleteProtected) with context.

**S05: Process Manager**
Built ProcessManager orchestrating full session startup: resolve runtimeClass → generate config.json → create bundle → fork shim → wait for socket → connect ShimClient → subscribe events. Critical bug fix: Changed from exec.CommandContext to exec.Command so shim process runs independently of request context. Stop method: Shutdown RPC → wait for exit (10s timeout) → kill if needed.

**S06: ARI Service**
Extended ARI JSON-RPC server with 9 session/* handlers (new/prompt/cancel/stop/remove/list/status/attach/detach). session/prompt auto-starts shim if state==created. Error handling with appropriate JSON-RPC codes (InvalidParams for client errors, InternalError for system failures). 27 ARI tests pass including 10 session tests.

**S07: agentdctl CLI**
Built CLI tool with spf13/cobra: 11 subcommands (7 session, 3 workspace, 1 daemon). Created pkg/ari/client.go — simplified JSON-RPC client for single-shot RPC calls over Unix sockets. Type-specific flag validation before RPC calls. Pretty-printed JSON output.

**S08: Integration Tests**
Created 8 integration tests proving full pipeline agentd → agent-shim → mockagent works end-to-end. Tests cover: full lifecycle (TestEndToEndPipeline), state machine transitions (TestSessionLifecycle), error handling (TestSessionPromptStoppedSession, TestSessionRemoveRunningSession), listing (TestSessionList), restart behavior (TestAgentdRestartRecovery reveals reconnection not yet implemented), concurrent sessions (TestMultipleConcurrentSessions, TestConcurrentPromptsSameSession).

**Discovery**: Restart recovery (shim socket reconnection after agentd restart) is future work. Session metadata persists, shim process survives, but agentd cannot reconnect to existing shim sockets on restart.

## Success Criteria Results

### Success Criterion 1: agentd daemon starts with config.yaml and listens on ARI socket
**Status**: ✅ PASSED  
**Evidence**: S01 implementation verified. cmd/agentd/main.go parses YAML config with Socket/WorkspaceRoot/MetaDB fields, initializes workspace manager and registry, creates ARI server, removes existing socket file (unclean shutdown recovery), listens on Unix socket. Integration tests start agentd daemon successfully. Build: `go build -o bin/agentd ./cmd/agentd` succeeds.

### Success Criterion 2: SQLite metadata store with CRUD operations
**Status**: ✅ PASSED  
**Evidence**: S02 implementation verified. pkg/meta provides Store with Session/Workspace/Room CRUD operations. 26 unit tests + 2 integration tests pass. WAL journal mode, foreign key constraints, embedded schema (go:embed). Transaction support via BeginTx.

### Success Criterion 3: RuntimeClass registry with env substitution
**Status**: ✅ PASSED  
**Evidence**: S03 implementation verified. pkg/agentd/runtimeclass.go provides RuntimeClassRegistry with Get/List methods. Env substitution uses os.Expand. 6 unit tests pass covering: valid config, Get found/not found, env substitution, Command required validation, Capabilities defaults, List functionality.

### Success Criterion 4: Session Manager with state machine
**Status**: ✅ PASSED  
**Evidence**: S04 implementation verified. pkg/agentd/session.go provides SessionManager with CRUD and state machine. Five states (created, running, paused:warm, paused:cold, stopped), nine valid transitions defined in declarative table. 12 tests pass covering CRUD round-trips, valid/invalid transitions, delete protection.

### Success Criterion 5: Process Manager with shim lifecycle
**Status**: ✅ PASSED  
**Evidence**: S05 implementation verified. pkg/agentd/process.go provides ProcessManager with Start/Stop/State/Connect methods. Start workflow: resolve runtimeClass → generate config → create bundle → fork shim → wait for socket → connect client → subscribe events. Stop: Shutdown RPC → wait 10s → kill if needed. Tests verify full lifecycle with mockagent.

### Success Criterion 6: ARI JSON-RPC server with session/* methods
**Status**: ✅ PASSED  
**Evidence**: S06 implementation verified. pkg/ari/server.go exposes 9 session/* methods: new/prompt/cancel/stop/remove/list/status/attach/detach. session/prompt auto-starts when state==created. 27 ARI tests pass including 10 session tests. Integration tests verify over actual Unix socket.

### Success Criterion 7: agentdctl CLI for session management
**Status**: ✅ PASSED  
**Evidence**: S07 implementation verified. cmd/agentdctl provides CLI with 11 subcommands: session (new/list/status/prompt/stop/remove/attach), workspace (prepare/list/cleanup), daemon (status). Uses spf13/cobra. Build verified: `go build ./cmd/agentdctl` produces executable.

### Success Criterion 8: Full pipeline integration tests pass
**Status**: ✅ PASSED  
**Evidence**: S08 implementation verified. 8 integration tests pass: TestEndToEndPipeline (full lifecycle), TestSessionLifecycle (state machine), TestSessionPromptStoppedSession (error handling), TestSessionRemoveRunningSession (protected deletion), TestSessionList (listing), TestAgentdRestartRecovery (restart behavior documented), TestMultipleConcurrentSessions (3 concurrent sessions), TestConcurrentPromptsSameSession (concurrent prompts). Verified by running `go test ./tests/integration/... -v`.

## Definition of Done Results

### All 8 slices complete
All slices marked ✅ in roadmap: S01, S02, S03, S04, S05, S06, S07, S08

### All slice summaries exist
All slices have SUMMARY.md and UAT.md files:
- S01-SUMMARY.md, S01-UAT.md ✓
- S02-SUMMARY.md, S02-UAT.md ✓
- S03-SUMMARY.md, S03-UAT.md ✓
- S04-SUMMARY.md, S04-UAT.md ✓
- S05-SUMMARY.md, S05-UAT.md ✓
- S06-SUMMARY.md, S06-UAT.md ✓
- S07-SUMMARY.md, S07-UAT.md ✓
- S08-SUMMARY.md, S08-UAT.md ✓

### Integration tests pass
All 8 integration tests pass (verified by running `go test ./tests/integration/... -v`):
- TestEndToEndPipeline (0.17s) — Full agentd → shim → mockagent lifecycle
- TestSessionLifecycle (0.23s) — State machine created → running → stopped
- TestSessionPromptStoppedSession (0.23s) — Error handling for invalid operations
- TestSessionRemoveRunningSession (0.25s) — Protected deletion semantics
- TestSessionList (0.25s) — Listing with count verification
- TestAgentdRestartRecovery (0.33s) — Documents restart behavior (reconnection not implemented)
- TestMultipleConcurrentSessions (0.36s) — 3 concurrent sessions respond independently
- TestConcurrentPromptsSameSession (0.25s) — Same session handles concurrent prompts

### Cross-slice integration works
TestEndToEndPipeline proves full pipeline integration:
1. agentd daemon startup with config
2. workspace/prepare creates workspace
3. session/new creates session with state=created
4. session/prompt auto-starts shim, returns response
5. session/status verifies running state
6. session/stop stops shim gracefully
7. session/remove deletes session
8. workspace/cleanup removes workspace
9. agentd shutdown cleanly

## Requirement Outcomes

### R001 → validated
**Previous status**: active  
**New status**: validated  
**Evidence**: S01 tests pass. Daemon starts successfully with minimal config.yaml (socket, workspaceRoot, metaDB fields), initializes workspace manager and registry, creates ARI server, listens on Unix socket, handles SIGTERM graceful shutdown. Build verified with `go build -o bin/agentd ./cmd/agentd`.

### R002 → validated
**Previous status**: active  
**New status**: validated  
**Evidence**: S03 tests pass (6 unit tests: TestNewRuntimeClassRegistryValidConfig, TestGetFoundAndNotFound, TestEnvSubstitution, TestCommandRequired, TestCapabilitiesDefaults, TestList). RuntimeClass registry resolves names to launch configs with ${VAR} env substitution, validates Command required, applies Capabilities defaults.

### R003 → validated
**Previous status**: active  
**New status**: validated  
**Evidence**: S02 tests pass (26 unit tests + 2 integration tests). SQLite metadata store with WAL mode, foreign keys, embedded schema. CRUD operations for Session, Workspace, Room. Transaction support via BeginTx. Daemon lifecycle integration verified.

### R004 → validated
**Previous status**: active  
**New status**: validated  
**Evidence**: S04 tests pass (12 SessionManager tests). CRUD operations work. State machine validates 9 valid transitions (created→running, created→stopped, running→paused:warm, running→stopped, paused:warm→running, paused:warm→paused:cold, paused:warm→stopped, paused:cold→running, paused:cold→stopped). Delete protection blocks running/paused:warm sessions.

### R005 → validated
**Previous status**: active  
**New status**: validated  
**Evidence**: S05 tests pass. ProcessManager.Start forks shim, connects socket, subscribes events. ProcessManager.Stop gracefully shuts down. ShimClient provides RPC communication. mockagent responds to prompts. Integration tests verify full lifecycle.

### R006 → validated
**Previous status**: active  
**New status**: validated  
**Evidence**: S06 tests pass (27 ARI tests including 10 session tests). All 9 session/* methods implemented: new/prompt/cancel/stop/remove/list/status/attach/detach. Auto-start on prompt. Error handling with JSON-RPC error codes. Integration tests verify over Unix socket.

### R007 → validated
**Previous status**: active  
**New status**: validated  
**Evidence**: S07 build verified. agentdctl CLI has 11 subcommands (7 session, 3 workspace, 1 daemon). CLI help output confirms all commands. Functional verification: CLI executes, error handling works, cobra validation works.

### R008 → remains validated
**Status**: validated (already)  
**Evidence**: S08 integration tests pass (8 tests). Full pipeline agentd → agent-shim → mockagent works: TestEndToEndPipeline proves complete lifecycle from create to cleanup.

## Deviations

None. All 8 slices completed as planned. The only notable discovery was that restart recovery (shim socket reconnection after agentd restart) is not yet implemented — this was documented in S08/T03 as future work, not a blocker.

## Follow-ups

1. Restart recovery — Shim socket reconnection after agentd restart (documented in S08/T03). Session metadata persists in SQLite, shim process survives agentd exit, but agentd cannot reconnect to existing shim sockets. Future implementation needs socket discovery in /tmp/agentd-shim/{sessionId}/ and reconnect logic.

2. Session label filtering — session/list does not support label filtering. The meta.SessionFilter struct only supports State, WorkspaceID, Room, and HasRoom filters.
