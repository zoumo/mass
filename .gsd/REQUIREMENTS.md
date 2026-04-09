# Requirements

This file is the explicit capability and coverage contract for the project.

## Active

### R020 — CreateTerminal executes a shell command in agent workspace with cwd/env/output capture
- Class: core-capability
- Status: active
- Description: CreateTerminal executes a shell command in agent workspace with cwd/env/output capture
- Why it matters: Enables shell command execution in agent workspace
- Source: execution
- Primary owning slice: M001-terminal/S01
- Supporting slices: none
- Validation: unmapped
- Notes: Phase 1.1 — ACP CreateTerminalRequest/Response implementation

### R026 — TerminalOutput returns captured stdout/stderr with exit status and truncation flag
- Class: core-capability
- Status: active
- Description: TerminalOutput returns captured stdout/stderr with exit status and truncation flag
- Why it matters: Enables agents to read command output after execution
- Source: execution
- Primary owning slice: M001-terminal/S02
- Supporting slices: none
- Validation: unmapped
- Notes: Phase 1.1 — ACP TerminalOutputRequest/Response implementation

### R027 — KillTerminalCommand sends SIGTERM/SIGKILL to running terminal command
- Class: core-capability
- Status: active
- Description: KillTerminalCommand sends SIGTERM/SIGKILL to running terminal command
- Why it matters: Enables agents to stop long-running or stuck commands
- Source: execution
- Primary owning slice: M001-terminal/S03
- Supporting slices: none
- Validation: unmapped
- Notes: Phase 1.1 — ACP KillTerminalCommandRequest/Response implementation

### R028 — ReleaseTerminal frees terminal resources (process, buffers) after command completion
- Class: core-capability
- Status: active
- Description: ReleaseTerminal frees terminal resources (process, buffers) after command completion
- Why it matters: Prevents resource leaks from abandoned terminals
- Source: execution
- Primary owning slice: M001-terminal/S04
- Supporting slices: none
- Validation: unmapped
- Notes: Phase 1.1 — ACP ReleaseTerminalRequest/Response implementation

### R029 — WaitForTerminalExit blocks until command exits, returns exit status
- Class: core-capability
- Status: active
- Description: WaitForTerminalExit blocks until command exits, returns exit status
- Why it matters: Enables synchronous command execution patterns
- Source: execution
- Primary owning slice: M001-terminal/S05
- Supporting slices: none
- Validation: unmapped
- Notes: Phase 1.1 — ACP WaitForTerminalExitRequest/Response implementation

### R044 — Additional restart, replay, cleanup, and cross-client hardening that does not fit the primary convergence milestone remains planned follow-on work.
- Class: quality-attribute
- Status: active
- Description: Additional restart, replay, cleanup, and cross-client hardening that does not fit the primary convergence milestone remains planned follow-on work.
- Why it matters: M002 should converge the contract first, not absorb all remaining hardening work.
- Source: inferred
- Primary owning slice: M002-q9r6sg/S02
- Supporting slices: M002-q9r6sg/S01, M002-q9r6sg/S03, M002-q9r6sg/S04
- Validation: mapped
- Notes: M003 delivered: fail-closed recovery posture (S01), shim-vs-DB reconciliation (S02), atomic event resume and damaged-tail tolerance (S03), DB-backed workspace cleanup safety (S04). Remaining follow-on: real CLI restart recovery tests, cross-client hardening.

## Validated

### R001 — agentd daemon can start, parse config.yaml, and listen on ARI Unix socket
- Class: launchability
- Status: validated
- Description: agentd daemon can start, parse config.yaml, and listen on ARI Unix socket
- Why it matters: Foundation for all agentd functionality
- Source: execution
- Primary owning slice: M001-tvc4z0/S01
- Supporting slices: none
- Validation: S01 tests pass. Daemon starts successfully with minimal config.yaml (socket, workspaceRoot, metaDB fields), initializes workspace manager and registry, creates ARI server, listens on Unix socket, handles SIGTERM graceful shutdown. Build verified with `go build -o bin/agentd ./cmd/agentd`.
- Notes: Includes project scaffolding, config parsing, signal handling

### R002 — RuntimeClass registry can resolve runtimeClass name to command/args/env/capabilities
- Class: core-capability
- Status: validated
- Description: RuntimeClass registry can resolve runtimeClass name to command/args/env/capabilities
- Why it matters: Enables declarative agent type selection (K8s RuntimeClass pattern)
- Source: execution
- Primary owning slice: M001-tvc4z0/S03
- Supporting slices: none
- Validation: S03 tests pass (6 unit tests). RuntimeClass registry resolves names to launch configs with ${VAR} env substitution, validates Command required, applies Capabilities defaults (Streaming=true, SessionLoad=false, ConcurrentSessions=1).
- Notes: Config parsing, ${VAR} substitution, validation

### R003 — SQLite-based metadata store persists session/workspace/room records with CRUD operations
- Class: core-capability
- Status: validated
- Description: SQLite-based metadata store persists session/workspace/room records with CRUD operations
- Why it matters: Required for session/workspace/room management and agentd restart recovery
- Source: execution
- Primary owning slice: M001-tvc4z0/S02
- Supporting slices: none
- Validation: S02 tests pass (26 unit tests + 2 integration tests). SQLite metadata store with WAL mode, foreign keys, embedded schema. CRUD operations for Session, Workspace, Room. Transaction support via BeginTx. Daemon lifecycle integration verified.
- Notes: Schema: sessions, workspaces, rooms tables; transaction support

### R004 — Session Manager provides Create/Get/List/Update/Delete with state machine (Created → Running → Paused:Warm → Paused:Cold → Stopped)
- Class: core-capability
- Status: validated
- Description: Session Manager provides Create/Get/List/Update/Delete with state machine (Created → Running → Paused:Warm → Paused:Cold → Stopped)
- Why it matters: Core session lifecycle management
- Source: execution
- Primary owning slice: M001-tvc4z0/S04
- Supporting slices: none
- Validation: S04 tests pass (12 SessionManager tests). CRUD operations work. State machine validates 9 valid transitions (created→running, created→stopped, running→paused:warm, running→stopped, paused:warm→running, paused:warm→paused:cold, paused:warm→stopped, paused:cold→running, paused:cold→stopped). Delete protection blocks running/paused:warm sessions.
- Notes: Label-based filtering, prevent Delete on running sessions

### R005 — Process Manager can fork agent-shim, connect to shim socket, subscribe to events, and manage process lifecycle (Start/Stop/State/Connect)
- Class: core-capability
- Status: validated
- Description: Process Manager can fork agent-shim, connect to shim socket, subscribe to events, and manage process lifecycle (Start/Stop/State/Connect)
- Why it matters: Enables actual agent execution through shim layer
- Source: execution
- Primary owning slice: M001-tvc4z0/S05
- Supporting slices: none
- Validation: S05 tests pass. ProcessManager.Start forks shim, connects socket, subscribes events. ProcessManager.Stop gracefully shuts down with Shutdown RPC + 10s wait + kill. ShimClient provides RPC communication. Integration tests verify full lifecycle with mockagent.
- Notes: Start workflow: resolve runtimeClass → generate config.json → create bundle → fork shim → connect socket → subscribe events

### R006 — ARI JSON-RPC server exposes session/* methods (new/prompt/cancel/stop/remove/list/status/attach/detach)
- Class: integration
- Status: validated
- Description: ARI JSON-RPC server exposes session/* methods (new/prompt/cancel/stop/remove/list/status/attach/detach)
- Why it matters: Primary interface for orchestrator and CLI to manage sessions
- Source: execution
- Primary owning slice: M001-tvc4z0/S06
- Supporting slices: none
- Validation: S06 tests pass (27 ARI tests including 10 session tests). All 9 session/* methods implemented: new/prompt/cancel/stop/remove/list/status/attach/detach. Auto-start on prompt. Error handling with JSON-RPC error codes. Integration tests verify over Unix socket.
- Notes: Event notifications: session/update, session/stateChange

### R007 — CLI tool for ARI operations: session new/list/status/prompt/stop/remove, daemon status
- Class: admin/support
- Status: validated
- Description: CLI tool for ARI operations: session new/list/status/prompt/stop/remove, daemon status
- Why it matters: Operator interface for agentd management
- Source: execution
- Primary owning slice: M001-tvc4z0/S07
- Supporting slices: none
- Validation: S07 build verified. agentdctl CLI has 11 subcommands (7 session, 3 workspace, 1 daemon). CLI help output confirms all commands. Functional verification: CLI executes, error handling works, cobra validation works for missing required flags.
- Notes: Extends agent-shim-cli or separate agentdctl binary

### R008 — Full pipeline agentd → agent-shim → mockagent works: create → prompt → stop → remove
- Class: integration
- Status: validated
- Description: Full pipeline agentd → agent-shim → mockagent works: create → prompt → stop → remove
- Why it matters: Proves the assembled system works end-to-end
- Source: execution
- Primary owning slice: M001-tvc4z0/S08
- Supporting slices: none
- Validation: S08 Integration Tests: 8 tests pass — TestEndToEndPipeline (full lifecycle), TestSessionLifecycle (state machine), TestSessionPromptStoppedSession (error handling), TestSessionRemoveRunningSession (protected deletion), TestSessionList (listing), TestAgentdRestartRecovery (restart test reveals reconnection not yet implemented), TestMultipleConcurrentSessions (concurrent sessions), TestConcurrentPromptsSameSession (concurrent prompts same session)
- Notes: Restart recovery (shim reconnection after agentd restart) identified as future enhancement — test documents current behavior

### R009 — Workspace Manager can prepare workspace from spec (Git/EmptyDir/Local) and cleanup with reference counting
- Class: core-capability
- Status: validated
- Description: Workspace Manager can prepare workspace from spec (Git/EmptyDir/Local) and cleanup with reference counting
- Why it matters: Enables declarative workspace provisioning
- Source: execution
- Primary owning slice: M001-tlbeko/S04
- Supporting slices: M001-tlbeko/S01, M001-tlbeko/S02
- Validation: S04 WorkspaceManager tests: 13 tests pass, Prepare→Cleanup round-trips for Git/EmptyDir/Local, reference counting prevents premature cleanup (TestWorkspaceManagerReferenceCounting), hook failure handling verified
- Notes: Phase 3 requirement — validated by M001-tlbeko

### R010 — Git source handler clones repository with ref/depth support
- Class: core-capability
- Status: validated
- Description: Git source handler clones repository with ref/depth support
- Why it matters: Primary workspace source type for agent work
- Source: execution
- Primary owning slice: M001-tlbeko/S01
- Supporting slices: none
- Validation: S01 GitHandler integration tests: 6 tests pass on github.com/octocat/Hello-World.git — default clone, shallow depth=1, branch ref='test', commit SHA checkout, context cancellation, invalid URL error handling
- Notes: Phase 3 requirement — validated by M001-tlbeko

### R011 — Setup/teardown hooks execute sequentially with failure handling and output capture
- Class: core-capability
- Status: validated
- Description: Setup/teardown hooks execute sequentially with failure handling and output capture
- Why it matters: Enables workspace initialization and cleanup customization
- Source: execution
- Primary owning slice: M001-tlbeko/S03
- Supporting slices: none
- Validation: S03 HookExecutor tests: 17 tests pass — sequential execution, abort-on-failure (TestExecuteHooksSequentialAbort proves marker file not created after first failure), output capture (HookError.Output), context cancellation
- Notes: Phase 3 requirement — validated by M001-tlbeko

### R012 — ARI workspace/* methods (prepare/list/cleanup) exposed
- Class: integration
- Status: validated
- Description: ARI workspace/* methods (prepare/list/cleanup) exposed
- Why it matters: Primary interface for workspace management
- Source: execution
- Primary owning slice: M001-tlbeko/S05
- Supporting slices: none
- Validation: S05 ARI integration tests: 16 tests pass over JSON-RPC — workspace/prepare (UUID generation, Registry tracking), workspace/list (tracked workspaces), workspace/cleanup (RefCount validation, lifecycle round-trip)
- Notes: Phase 3 requirement — validated by M001-tlbeko

### R032 — `docs/design/*` must define one non-conflicting contract for Room, Session, Runtime, Workspace, and recovery semantics.
- Class: core-capability
- Status: validated
- Description: `docs/design/*` must define one non-conflicting contract for Room, Session, Runtime, Workspace, and recovery semantics.
- Why it matters: Further implementation work is unsafe while the design contract still contradicts itself.
- Source: user
- Primary owning slice: M002-ssi4mk/S01
- Supporting slices: none
- Validation: M002/S01 final verifier passed: `bash scripts/verify-m002-s01-contract.sh`; bundle proof passed: `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1`. The design set now defines one non-conflicting contract across Room, Session, Runtime, Workspace, and shim recovery semantics.
- Notes: Validated by the converged authority map and clean-break shim contract established in S01.

### R033 — `agentRoot.path`, resolved `cwd`, `session/new`, `systemPrompt`, and bootstrap behavior must have one authoritative meaning.
- Class: integration
- Status: validated
- Description: `agentRoot.path`, resolved `cwd`, `session/new`, `systemPrompt`, and bootstrap behavior must have one authoritative meaning.
- Why it matters: Startup ambiguity makes state rebuild, client compatibility, and recovery behavior untrustworthy.
- Source: user
- Primary owning slice: M002-ssi4mk/S01
- Supporting slices: M002-ssi4mk/S02
- Validation: T02 converged `agentRoot.path`, resolved `cwd`, `session/new`, `systemPrompt`, and bootstrap semantics across runtime-spec, config-spec, design.md, and contract-convergence.md. Final slice verifier passed at S01 close.
- Notes: S03 still owns durable persistence of bootstrap/recovery state, but the design meaning is now singular and validated.

### R034 — The shim surface must stop carrying the legacy PascalCase / `$/event` contract and expose one clean-break protocol aligned with the converged design.
- Class: integration
- Status: validated
- Description: The shim surface must stop carrying the legacy PascalCase / `$/event` contract and expose one clean-break protocol aligned with the converged design.
- Why it matters: The current split naming and event model adds protocol drift exactly where ACP compatibility matters most.
- Source: user
- Primary owning slice: M002-ssi4mk/S02
- Supporting slices: none
- Validation: S02 replaced all legacy PascalCase shim methods and `$/event` notifications with the clean-break `session/*` + `runtime/*` surface. No-legacy-name grep gate passes: `! rg '"Prompt"|"Cancel"|"Subscribe"|"GetState"|"GetHistory"|"Shutdown"|"$/event"'` in non-test sources across pkg/rpc, pkg/agentd, pkg/ari, cmd/agent-shim-cli returns zero matches. Full test suite passes. D027 records the validation decision.
- Notes: No backward-compatibility burden is required for obsolete names or event shapes.

### R035 — Runtime event recovery must offer a single resume path that closes the current gap between history replay and live subscription.
- Class: continuity
- Status: validated
- Description: Runtime event recovery must offer a single resume path that closes the current gap between history replay and live subscription.
- Why it matters: Agentd restart and reconnect logic cannot be trusted if events can be silently missed.
- Source: user
- Primary owning slice: M002-q9r6sg/S03
- Supporting slices: M002-ssi4mk/S02, M002-ssi4mk/S03, M002-q9r6sg/S02
- Validation: M003/S03 upgraded the resume path: Translator.SubscribeFromSeq reads log + registers subscription under a single mutex hold, eliminating the History→Subscribe gap structurally. RecoverSession now uses atomic Subscribe(fromSeq=0) instead of separate History+Subscribe calls. Proven by TestSubscribeFromSeq_BackfillAndLive (contiguous seq, no gap), TestShimClientSubscribeFromSeq, and full recovery test suite.
- Notes: Recovery hardening ownership moved to M002-q9r6sg for atomic resume and damaged-tail tolerance beyond the M002 baseline. M003/S03 completed the structural fix.

### R036 — The runtime must preserve enough session configuration and identity to rebuild truthful state after restart or reconnect.
- Class: continuity
- Status: validated
- Description: The runtime must preserve enough session configuration and identity to rebuild truthful state after restart or reconnect.
- Why it matters: A session that restarts without durable config becomes metadata theater instead of real recovery.
- Source: inferred
- Primary owning slice: M002-q9r6sg/S02
- Supporting slices: M002-ssi4mk/S03, M002-q9r6sg/S01
- Validation: TestAgentdRestartRecovery proves bootstrap_config, socket_path, state_dir, PID persist in schema v2. RecoverSessions rebuilds truthful state: live shim reconnected, dead shim marked stopped.
- Notes: Truthful restart/state rebuild now completes in M002-q9r6sg through live reconnect and explicit recovery posture.

### R037 — Workspace identity, reuse rules, cleanup boundaries, and shared access expectations must be explicit in both design and implementation direction.
- Class: core-capability
- Status: validated
- Description: Workspace identity, reuse rules, cleanup boundaries, and shared access expectations must be explicit in both design and implementation direction.
- Why it matters: Shared or reused workspaces become unsafe quickly if hooks, cleanup, or path identity are ambiguous.
- Source: user
- Primary owning slice: M002-q9r6sg/S04
- Supporting slices: M002-ssi4mk/S03, M002-q9r6sg/S02, M002-q9r6sg/S03
- Validation: S04 implemented DB-backed ref_count as cleanup gate (store.GetWorkspace ref_count check in handleWorkspaceCleanup), recovery-phase guard blocking cleanup during recovery, Registry.RebuildFromDB for workspace identity persistence across restarts, and WorkspaceManager.InitRefCounts for refcount consistency. 7 integration tests prove: ref_count increments on session/new (TestARISessionNewAcquiresWorkspaceRef), decrements on session/remove (TestARISessionRemoveReleasesWorkspaceRef), Source spec persisted (TestARIWorkspacePrepareSourcePersisted), cleanup blocked by DB refs (TestARIWorkspaceCleanupBlockedByDBRefCount), cleanup blocked during recovery (TestARIWorkspaceCleanupBlockedDuringRecovery), registry rebuild from DB (TestRegistryRebuildFromDB), and manager refcount init (TestWorkspaceManagerInitRefCounts).
- Notes: Workspace safety moves from design intent to restart-safe enforced cleanup semantics in M003. Registry rebuild does not verify on-disk workspace path existence (stale workspace detection).

### R038 — Local workspace attachment, hook execution, environment injection, and shared workspace access must have explicit boundary rules now, not only in a later readiness phase.
- Class: compliance/security
- Status: validated
- Description: Local workspace attachment, hook execution, environment injection, and shared workspace access must have explicit boundary rules now, not only in a later readiness phase.
- Why it matters: These are already-open runtime entry points with real host impact.
- Source: research
- Primary owning slice: M002-q9r6sg/S01
- Supporting slices: M002-ssi4mk/S03, M002-q9r6sg/S02, M002-q9r6sg/S04
- Validation: T03 documented explicit host-impact rules for local workspace attachment, hooks, env precedence, and shared workspace reuse across the authoritative design docs. Final slice verifier passed with these boundary rules in place.
- Notes: This validates the design boundary contract; runtime enforcement and recovery hardening continue in later slices.

### R039 — The converged contract must be exercised with the real bundle surfaces for `gsd-pi` and `claude-code`, not only mock agents.
- Class: integration
- Status: validated
- Description: The converged contract must be exercised with the real bundle surfaces for `gsd-pi` and `claude-code`, not only mock agents.
- Why it matters: The project’s ACP claims are only useful if they survive contact with real clients.
- Source: user
- Primary owning slice: M002-ssi4mk/S04
- Supporting slices: none
- Validation: S04 created TestRealCLI_GsdPi and TestRealCLI_ClaudeCode exercising full ARI session lifecycle with real runtime class configs. Tests skip gracefully when ANTHROPIC_API_KEY is absent. Timeout infrastructure (start=30s, prompt=120s, waitForSocket=20s) tuned for real CLI startup. The setupAgentdTestWithRuntimeClass helper proves the converged contract supports arbitrary runtime classes beyond mockagent.
- Notes: Existing bundle references under `bin/bundles/*` are the starting point for this proof.

### R041 — The project must eventually support a realized Room runtime with explicit ownership, routing, and delivery semantics rather than leaving Room as conflicting partial intent.
- Class: differentiator
- Status: validated
- Description: The project must eventually support a realized Room runtime with explicit ownership, routing, and delivery semantics rather than leaving Room as conflicting partial intent.
- Why it matters: Multi-agent coordination is a central differentiator for OAR, but it must land on a stable contract.
- Source: user
- Primary owning slice: M003-c761yf (provisional)
- Supporting slices: none
- Validation: Fully realized in M004: room/create, room/status, room/delete ARI handlers (ownership); room/send with target resolution and sender attribution (routing); deliverPrompt helper with auto-start semantics (delivery). Proven by TestARIMultiAgentRoundTrip — 3-agent bidirectional messaging end-to-end.
- Notes: This supersedes vague Room ambition with a concrete future capability target.

### R047 — agentd exposes agent/* ARI methods as external surface; session/* is internal only. Agent identified by room+name unique key.
- Class: core-capability
- Status: validated
- Description: agentd exposes agent/* ARI methods as external surface; session/* is internal only. Agent identified by room+name unique key.
- Why it matters: Users operate on agents, not sessions. The external model must match the user's mental model to reduce cognitive load and API confusion.
- Source: docs/plan/agent-runtime-alignment-plan.md
- Primary owning slice: M005/S03
- Supporting slices: M005/S01, M005/S02
- Validation: 10 agent/* dispatch cases in pkg/ari/server.go; TestARISessionMethodsRemoved confirms all 8 session/* methods return MethodNotFound; 64+ pkg/ari tests pass; grep count=25 for agent/* methods in ari-spec.md

### R048 — agent/create uses async semantics — returns creating state immediately, bootstrap completes in background. Callers poll agent/status for created/error.
- Class: core-capability
- Status: validated
- Description: agent/create uses async semantics — returns creating state immediately, bootstrap completes in background. Callers poll agent/status for created/error.
- Why it matters: ACP bootstrap can take 10-30 seconds. Synchronous blocking on create is unacceptable for orchestrator responsiveness.
- Source: docs/plan/agent-runtime-alignment-plan.md
- Primary owning slice: M005/S04
- Supporting slices: M005/S03
- Validation: TestARIAgentCreateAsync: create returns creating → poll status → transitions to created. TestARIAgentCreateAsyncErrorState: create returns creating → poll status → transitions to error with non-empty ErrorMessage. Both integration tests use real mockagent shim. Full suite (go test ./pkg/ari/... -count=1) passes.

### R049 — Agent state machine uses creating/created/running/stopped/error. paused:warm and paused:cold are removed from active state machine.
- Class: core-capability
- Status: validated
- Description: Agent state machine uses creating/created/running/stopped/error. paused:warm and paused:cold are removed from active state machine.
- Why it matters: paused:warm/paused:cold are implementation details of future checkpoint/recovery, not natural states for current user-facing agent model.
- Source: docs/plan/agent-runtime-alignment-plan.md
- Primary owning slice: M005/S02
- Validation: State transition unit tests in pkg/agentd/session_test.go cover all 5 states and explicitly reject paused:warm/paused:cold transitions. rg confirms zero remaining references to PausedWarm/PausedCold/paused:warm/paused:cold in production Go source. All 102 tests in pkg/meta/... and pkg/agentd/... pass (exit=0).

### R050 — Event envelopes carry turnId, streamSeq, and phase for turn-aware ordering. Global seq retained as log sequence. Chat/replay orders by (turnId, streamSeq).
- Class: core-capability
- Status: validated
- Description: Event envelopes carry turnId, streamSeq, and phase for turn-aware ordering. Global seq retained as log sequence. Chat/replay orders by (turnId, streamSeq).
- Why it matters: Current event ordering is receive-order, not causal-order. Events appear scrambled in chat/replay because seq only reflects when agentd received them, not their logical position in a turn.
- Source: docs/plan/agent-runtime-alignment-plan.md
- Primary owning slice: M005/S05
- Supporting slices: M005/S01
- Validation: S05/T01 added TurnId/StreamSeq/*int/Phase to SessionUpdateParams; Translator mutates turn state atomically under mu.Lock inside broadcastEnvelope callbacks. 7 new TestTurnAwareEnvelope_* tests prove: TurnId assigned on turn_start and propagated to all mid-turn events, streamSeq monotonic within turn and reset to 0 on new turn, turn_end carries TurnId before clearing, stateChange events excluded from turn fields, JSON round-trip correct, replay ordering invariants hold across two turns. S05/T02 wires NotifyTurnStart/NotifyTurnEnd into handlePrompt; RPC integration tests updated to 6-event model with turn field assertions. All 8 packages pass: go test ./pkg/... -count=1.

### R051 — room-mcp-server rewritten with modelcontextprotocol/go-sdk. Environment variables switch from OAR_SESSION_ID to OAR_AGENT_NAME/OAR_AGENT_ID/OAR_ROOM_NAME.
- Class: integration
- Status: validated
- Description: room-mcp-server rewritten with modelcontextprotocol/go-sdk. Environment variables switch from OAR_SESSION_ID to OAR_AGENT_NAME/OAR_AGENT_ID/OAR_ROOM_NAME.
- Why it matters: Current hand-rolled MCP server (497 lines) couples protocol and business logic. SDK migration separates concerns and aligns env vars with agent identity model.
- Source: docs/plan/agent-runtime-alignment-plan.md
- Primary owning slice: M005/S06
- Validation: room-mcp-server fully rewritten with modelcontextprotocol/go-sdk v0.8.0 (StdioTransport + server.AddTool). OAR_SESSION_ID and OAR_ROOM_AGENT removed from process.go env injections. Config now reads OAR_AGENT_ID/OAR_AGENT_NAME. go build ./cmd/room-mcp-server passes; TestGenerateConfigWithRoomMCPInjection (3 subtests) passes; go test ./pkg/agentd/... passes (M005/S06/T02).

### R052 — Recovery operates externally by agent identity (room+name), internally by session/shim handle. Agent identity survives daemon restart.
- Class: continuity
- Status: validated
- Description: Recovery operates externally by agent identity (room+name), internally by session/shim handle. Agent identity survives daemon restart.
- Why it matters: Recovery must use stable identity. Session UUIDs are internal handles; agent room+name is the stable external key that orchestrators and operators reference.
- Source: docs/plan/agent-runtime-alignment-plan.md
- Primary owning slice: M005/S07
- Supporting slices: M005/S02, M005/S04
- Validation: TestAgentdRestartRecovery (7-phase integration test): agent-A and agent-B created with room+name identity. After daemon restart with all shims killed, both agents are in error state but their agentId, room, and name are identical to pre-restart values. agent/list returns both agents with intact room identity. Test passes in 4.47s.

## Deferred

### R021 — Implement session/load support for warm resume
- Class: continuity
- Status: deferred
- Description: Implement session/load support for warm resume
- Why it matters: Enables conversation history restoration
- Source: execution
- Primary owning slice: none
- Supporting slices: none
- Validation: unmapped
- Notes: Phase 1.2 — not yet wired

### R022 — Event log rotation or size limit
- Class: operability
- Status: deferred
- Description: Event log rotation or size limit
- Why it matters: Prevents unbounded disk growth
- Source: execution
- Primary owning slice: none
- Supporting slices: none
- Validation: unmapped
- Notes: Phase 1.4 — currently unbounded append

### R023 — GetHistory with time-range and event-type filters
- Class: operability
- Status: deferred
- Description: GetHistory with time-range and event-type filters
- Why it matters: Reduces unnecessary data transfer
- Source: execution
- Primary owning slice: none
- Supporting slices: none
- Validation: unmapped
- Notes: Phase 1.4 — currently returns all events

### R024 — Room Manager with member tracking, MCP tool injection, message routing
- Class: differentiator
- Status: deferred
- Description: Room Manager with member tracking, MCP tool injection, message routing
- Why it matters: Enables multi-agent collaboration
- Source: execution
- Primary owning slice: none
- Supporting slices: none
- Validation: unmapped
- Notes: Phase 4 — depends on Phase 3 for shared workspace

### R025 — Warm idle timeout → Cold pause, cold restart with session/load
- Class: continuity
- Status: deferred
- Description: Warm idle timeout → Cold pause, cold restart with session/load
- Why it matters: Optimizes resource usage
- Source: execution
- Primary owning slice: none
- Supporting slices: none
- Validation: unmapped
- Notes: Phase 5 — builds on Phase 2 session infrastructure

### R040 — Codex compatibility must stay in the contract and the planned sequence, even if M002 does not require full real end-to-end proof for it.
- Class: integration
- Status: deferred
- Description: Codex compatibility must stay in the contract and the planned sequence, even if M002 does not require full real end-to-end proof for it.
- Why it matters: The runtime boundary should not converge around only two clients and make the third an afterthought.
- Source: user
- Primary owning slice: none
- Supporting slices: none
- Validation: deferred by user
- Notes: Deferred by user during M002-q9r6sg planning. Codex end-to-end validation is removed from this milestone and will return in a later roadmap decision.

### R042 — The project may later evaluate BoltDB or another backend, or abstract the metadata store, but this is not part of M002.
- Class: constraint
- Status: deferred
- Description: The project may later evaluate BoltDB or another backend, or abstract the metadata store, but this is not part of M002.
- Why it matters: Storage direction should be a deliberate decision, not accidental scope creep inside convergence work.
- Source: user
- Primary owning slice: none
- Supporting slices: none
- Validation: unmapped
- Notes: Current recommendation is to retain SQLite because the model already relies on relational features.

### R043 — Terminal work can return only after the converged runtime contract is stable enough to place it correctly.
- Class: constraint
- Status: deferred
- Description: Terminal work can return only after the converged runtime contract is stable enough to place it correctly.
- Why it matters: Prevents the roadmap from reviving a cancelled direction prematurely.
- Source: user
- Primary owning slice: none
- Supporting slices: none
- Validation: unmapped
- Notes: Keeps terminal explicitly out of the near-term roadmap without banning it forever.

## Out of Scope

### R030 — Runtime interactive approval for fs/terminal operations
- Class: anti-feature
- Status: out-of-scope
- Description: Runtime interactive approval for fs/terminal operations
- Why it matters: agentd manages headless sessions; interactive approval is for tools like toad
- Source: execution
- Primary owning slice: none
- Supporting slices: none
- Validation: n/a
- Notes: Use toad/acpx for interactive approval scenarios

### R031 — Direct ACP message manipulation (bypassing typed events)
- Class: anti-feature
- Status: out-of-scope
- Description: Direct ACP message manipulation (bypassing typed events)
- Why it matters: ACP is shim implementation detail; agentd consumes typed events
- Source: execution
- Primary owning slice: none
- Supporting slices: none
- Validation: n/a
- Notes: Typed events are the core protocol

### R045 — The convergence wave does not preserve compatibility with old contract shapes that the user no longer wants to carry.
- Class: anti-feature
- Status: out-of-scope
- Description: The convergence wave does not preserve compatibility with old contract shapes that the user no longer wants to carry.
- Why it matters: Prevents the roadmap from paying complexity for compatibility the sole operator explicitly does not need.
- Source: user
- Primary owning slice: none
- Supporting slices: none
- Validation: n/a
- Notes: This is the explicit “不需要考虑兼容性的问题” decision.

### R046 — The cancelled `M001-terminal` direction is not treated as an obligation in the new roadmap.
- Class: anti-feature
- Status: out-of-scope
- Description: The cancelled `M001-terminal` direction is not treated as an obligation in the new roadmap.
- Why it matters: Prevents the new milestone sequence from inheriting a plan the user explicitly rejected.
- Source: user
- Primary owning slice: none
- Supporting slices: none
- Validation: n/a
- Notes: Terminal is deferred by choice, not carried as hidden debt.

## Traceability

| ID | Class | Status | Primary owner | Supporting | Proof |
|---|---|---|---|---|---|
| R001 | launchability | validated | M001-tvc4z0/S01 | none | S01 tests pass. Daemon starts successfully with minimal config.yaml (socket, workspaceRoot, metaDB fields), initializes workspace manager and registry, creates ARI server, listens on Unix socket, handles SIGTERM graceful shutdown. Build verified with `go build -o bin/agentd ./cmd/agentd`. |
| R002 | core-capability | validated | M001-tvc4z0/S03 | none | S03 tests pass (6 unit tests). RuntimeClass registry resolves names to launch configs with ${VAR} env substitution, validates Command required, applies Capabilities defaults (Streaming=true, SessionLoad=false, ConcurrentSessions=1). |
| R003 | core-capability | validated | M001-tvc4z0/S02 | none | S02 tests pass (26 unit tests + 2 integration tests). SQLite metadata store with WAL mode, foreign keys, embedded schema. CRUD operations for Session, Workspace, Room. Transaction support via BeginTx. Daemon lifecycle integration verified. |
| R004 | core-capability | validated | M001-tvc4z0/S04 | none | S04 tests pass (12 SessionManager tests). CRUD operations work. State machine validates 9 valid transitions (created→running, created→stopped, running→paused:warm, running→stopped, paused:warm→running, paused:warm→paused:cold, paused:warm→stopped, paused:cold→running, paused:cold→stopped). Delete protection blocks running/paused:warm sessions. |
| R005 | core-capability | validated | M001-tvc4z0/S05 | none | S05 tests pass. ProcessManager.Start forks shim, connects socket, subscribes events. ProcessManager.Stop gracefully shuts down with Shutdown RPC + 10s wait + kill. ShimClient provides RPC communication. Integration tests verify full lifecycle with mockagent. |
| R006 | integration | validated | M001-tvc4z0/S06 | none | S06 tests pass (27 ARI tests including 10 session tests). All 9 session/* methods implemented: new/prompt/cancel/stop/remove/list/status/attach/detach. Auto-start on prompt. Error handling with JSON-RPC error codes. Integration tests verify over Unix socket. |
| R007 | admin/support | validated | M001-tvc4z0/S07 | none | S07 build verified. agentdctl CLI has 11 subcommands (7 session, 3 workspace, 1 daemon). CLI help output confirms all commands. Functional verification: CLI executes, error handling works, cobra validation works for missing required flags. |
| R008 | integration | validated | M001-tvc4z0/S08 | none | S08 Integration Tests: 8 tests pass — TestEndToEndPipeline (full lifecycle), TestSessionLifecycle (state machine), TestSessionPromptStoppedSession (error handling), TestSessionRemoveRunningSession (protected deletion), TestSessionList (listing), TestAgentdRestartRecovery (restart test reveals reconnection not yet implemented), TestMultipleConcurrentSessions (concurrent sessions), TestConcurrentPromptsSameSession (concurrent prompts same session) |
| R009 | core-capability | validated | M001-tlbeko/S04 | M001-tlbeko/S01, M001-tlbeko/S02 | S04 WorkspaceManager tests: 13 tests pass, Prepare→Cleanup round-trips for Git/EmptyDir/Local, reference counting prevents premature cleanup (TestWorkspaceManagerReferenceCounting), hook failure handling verified |
| R010 | core-capability | validated | M001-tlbeko/S01 | none | S01 GitHandler integration tests: 6 tests pass on github.com/octocat/Hello-World.git — default clone, shallow depth=1, branch ref='test', commit SHA checkout, context cancellation, invalid URL error handling |
| R011 | core-capability | validated | M001-tlbeko/S03 | none | S03 HookExecutor tests: 17 tests pass — sequential execution, abort-on-failure (TestExecuteHooksSequentialAbort proves marker file not created after first failure), output capture (HookError.Output), context cancellation |
| R012 | integration | validated | M001-tlbeko/S05 | none | S05 ARI integration tests: 16 tests pass over JSON-RPC — workspace/prepare (UUID generation, Registry tracking), workspace/list (tracked workspaces), workspace/cleanup (RefCount validation, lifecycle round-trip) |
| R020 | core-capability | active | M001-terminal/S01 | none | unmapped |
| R021 | continuity | deferred | none | none | unmapped |
| R022 | operability | deferred | none | none | unmapped |
| R023 | operability | deferred | none | none | unmapped |
| R024 | differentiator | deferred | none | none | unmapped |
| R025 | continuity | deferred | none | none | unmapped |
| R026 | core-capability | active | M001-terminal/S02 | none | unmapped |
| R027 | core-capability | active | M001-terminal/S03 | none | unmapped |
| R028 | core-capability | active | M001-terminal/S04 | none | unmapped |
| R029 | core-capability | active | M001-terminal/S05 | none | unmapped |
| R030 | anti-feature | out-of-scope | none | none | n/a |
| R031 | anti-feature | out-of-scope | none | none | n/a |
| R032 | core-capability | validated | M002-ssi4mk/S01 | none | M002/S01 final verifier passed: `bash scripts/verify-m002-s01-contract.sh`; bundle proof passed: `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1`. The design set now defines one non-conflicting contract across Room, Session, Runtime, Workspace, and shim recovery semantics. |
| R033 | integration | validated | M002-ssi4mk/S01 | M002-ssi4mk/S02 | T02 converged `agentRoot.path`, resolved `cwd`, `session/new`, `systemPrompt`, and bootstrap semantics across runtime-spec, config-spec, design.md, and contract-convergence.md. Final slice verifier passed at S01 close. |
| R034 | integration | validated | M002-ssi4mk/S02 | none | S02 replaced all legacy PascalCase shim methods and `$/event` notifications with the clean-break `session/*` + `runtime/*` surface. No-legacy-name grep gate passes: `! rg '"Prompt"|"Cancel"|"Subscribe"|"GetState"|"GetHistory"|"Shutdown"|"$/event"'` in non-test sources across pkg/rpc, pkg/agentd, pkg/ari, cmd/agent-shim-cli returns zero matches. Full test suite passes. D027 records the validation decision. |
| R035 | continuity | validated | M002-q9r6sg/S03 | M002-ssi4mk/S02, M002-ssi4mk/S03, M002-q9r6sg/S02 | M003/S03 upgraded the resume path: Translator.SubscribeFromSeq reads log + registers subscription under a single mutex hold, eliminating the History→Subscribe gap structurally. RecoverSession now uses atomic Subscribe(fromSeq=0) instead of separate History+Subscribe calls. Proven by TestSubscribeFromSeq_BackfillAndLive (contiguous seq, no gap), TestShimClientSubscribeFromSeq, and full recovery test suite. |
| R036 | continuity | validated | M002-q9r6sg/S02 | M002-ssi4mk/S03, M002-q9r6sg/S01 | TestAgentdRestartRecovery proves bootstrap_config, socket_path, state_dir, PID persist in schema v2. RecoverSessions rebuilds truthful state: live shim reconnected, dead shim marked stopped. |
| R037 | core-capability | validated | M002-q9r6sg/S04 | M002-ssi4mk/S03, M002-q9r6sg/S02, M002-q9r6sg/S03 | S04 implemented DB-backed ref_count as cleanup gate (store.GetWorkspace ref_count check in handleWorkspaceCleanup), recovery-phase guard blocking cleanup during recovery, Registry.RebuildFromDB for workspace identity persistence across restarts, and WorkspaceManager.InitRefCounts for refcount consistency. 7 integration tests prove: ref_count increments on session/new (TestARISessionNewAcquiresWorkspaceRef), decrements on session/remove (TestARISessionRemoveReleasesWorkspaceRef), Source spec persisted (TestARIWorkspacePrepareSourcePersisted), cleanup blocked by DB refs (TestARIWorkspaceCleanupBlockedByDBRefCount), cleanup blocked during recovery (TestARIWorkspaceCleanupBlockedDuringRecovery), registry rebuild from DB (TestRegistryRebuildFromDB), and manager refcount init (TestWorkspaceManagerInitRefCounts). |
| R038 | compliance/security | validated | M002-q9r6sg/S01 | M002-ssi4mk/S03, M002-q9r6sg/S02, M002-q9r6sg/S04 | T03 documented explicit host-impact rules for local workspace attachment, hooks, env precedence, and shared workspace reuse across the authoritative design docs. Final slice verifier passed with these boundary rules in place. |
| R039 | integration | validated | M002-ssi4mk/S04 | none | S04 created TestRealCLI_GsdPi and TestRealCLI_ClaudeCode exercising full ARI session lifecycle with real runtime class configs. Tests skip gracefully when ANTHROPIC_API_KEY is absent. Timeout infrastructure (start=30s, prompt=120s, waitForSocket=20s) tuned for real CLI startup. The setupAgentdTestWithRuntimeClass helper proves the converged contract supports arbitrary runtime classes beyond mockagent. |
| R040 | integration | deferred | none | none | deferred by user |
| R041 | differentiator | validated | M003-c761yf (provisional) | none | Fully realized in M004: room/create, room/status, room/delete ARI handlers (ownership); room/send with target resolution and sender attribution (routing); deliverPrompt helper with auto-start semantics (delivery). Proven by TestARIMultiAgentRoundTrip — 3-agent bidirectional messaging end-to-end. |
| R042 | constraint | deferred | none | none | unmapped |
| R043 | constraint | deferred | none | none | unmapped |
| R044 | quality-attribute | active | M002-q9r6sg/S02 | M002-q9r6sg/S01, M002-q9r6sg/S03, M002-q9r6sg/S04 | mapped |
| R045 | anti-feature | out-of-scope | none | none | n/a |
| R046 | anti-feature | out-of-scope | none | none | n/a |
| R047 | core-capability | validated | M005/S03 | M005/S01, M005/S02 | 10 agent/* dispatch cases in pkg/ari/server.go; TestARISessionMethodsRemoved confirms all 8 session/* methods return MethodNotFound; 64+ pkg/ari tests pass; grep count=25 for agent/* methods in ari-spec.md |
| R048 | core-capability | validated | M005/S04 | M005/S03 | TestARIAgentCreateAsync: create returns creating → poll status → transitions to created. TestARIAgentCreateAsyncErrorState: create returns creating → poll status → transitions to error with non-empty ErrorMessage. Both integration tests use real mockagent shim. Full suite (go test ./pkg/ari/... -count=1) passes. |
| R049 | core-capability | validated | M005/S02 | none | State transition unit tests in pkg/agentd/session_test.go cover all 5 states and explicitly reject paused:warm/paused:cold transitions. rg confirms zero remaining references to PausedWarm/PausedCold/paused:warm/paused:cold in production Go source. All 102 tests in pkg/meta/... and pkg/agentd/... pass (exit=0). |
| R050 | core-capability | validated | M005/S05 | M005/S01 | S05/T01 added TurnId/StreamSeq/*int/Phase to SessionUpdateParams; Translator mutates turn state atomically under mu.Lock inside broadcastEnvelope callbacks. 7 new TestTurnAwareEnvelope_* tests prove: TurnId assigned on turn_start and propagated to all mid-turn events, streamSeq monotonic within turn and reset to 0 on new turn, turn_end carries TurnId before clearing, stateChange events excluded from turn fields, JSON round-trip correct, replay ordering invariants hold across two turns. S05/T02 wires NotifyTurnStart/NotifyTurnEnd into handlePrompt; RPC integration tests updated to 6-event model with turn field assertions. All 8 packages pass: go test ./pkg/... -count=1. |
| R051 | integration | validated | M005/S06 | none | room-mcp-server fully rewritten with modelcontextprotocol/go-sdk v0.8.0 (StdioTransport + server.AddTool). OAR_SESSION_ID and OAR_ROOM_AGENT removed from process.go env injections. Config now reads OAR_AGENT_ID/OAR_AGENT_NAME. go build ./cmd/room-mcp-server passes; TestGenerateConfigWithRoomMCPInjection (3 subtests) passes; go test ./pkg/agentd/... passes (M005/S06/T02). |
| R052 | continuity | validated | M005/S07 | M005/S02, M005/S04 | TestAgentdRestartRecovery (7-phase integration test): agent-A and agent-B created with room+name identity. After daemon restart with all shims killed, both agents are in error state but their agentId, room, and name are identical to pre-restart values. agent/list returns both agents with intact room identity. Test passes in 4.47s. |

## Coverage Summary

- Active requirements: 6
- Mapped to slices: 6
- Validated: 27 (R001, R002, R003, R004, R005, R006, R007, R008, R009, R010, R011, R012, R032, R033, R034, R035, R036, R037, R038, R039, R041, R047, R048, R049, R050, R051, R052)
- Unmapped active requirements: 0
