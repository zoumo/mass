# Requirements

This file is the explicit capability and coverage contract for the project.

Use it to track what is actively in scope, what has been validated by completed work, what is intentionally deferred, and what is explicitly out of scope.

Guidelines:
- Keep requirements capability-oriented, not a giant feature wishlist.
- Requirements should be atomic, testable, and stated in plain language.
- Every **Active** requirement should be mapped to a slice, deferred, blocked with reason, or moved out of scope.
- Each requirement should have one accountable primary owner and may have supporting slices.
- Research may suggest requirements, but research does not silently make them binding.
- Validation means the requirement was actually proven by completed work and verification, not just discussed.

## Active

### R001 — agentd daemon launchability
- Class: launchability
- Status: active
- Description: agentd daemon can start, parse config.yaml, and listen on ARI Unix socket
- Why it matters: Foundation for all agentd functionality
- Source: execution
- Primary owning slice: M001-tvc4z0/S01
- Supporting slices: none
- Validation: unmapped
- Notes: Includes project scaffolding, config parsing, signal handling

### R002 — RuntimeClass registry
- Class: core-capability
- Status: active
- Description: RuntimeClass registry can resolve runtimeClass name to command/args/env/capabilities
- Why it matters: Enables declarative agent type selection (K8s RuntimeClass pattern)
- Source: execution
- Primary owning slice: M001-tvc4z0/S03
- Supporting slices: none
- Validation: unmapped
- Notes: Config parsing, ${VAR} substitution, validation

### R003 — Metadata Store persistence
- Class: core-capability
- Status: active
- Description: SQLite-based metadata store persists session/workspace/room records with CRUD operations
- Why it matters: Required for session/workspace/room management and agentd restart recovery
- Source: execution
- Primary owning slice: M001-tvc4z0/S02
- Supporting slices: none
- Validation: unmapped
- Notes: Schema: sessions, workspaces, rooms tables; transaction support

### R004 — Session Manager CRUD + state machine
- Class: core-capability
- Status: active
- Description: Session Manager provides Create/Get/List/Update/Delete with state machine (Created → Running → Paused:Warm → Paused:Cold → Stopped)
- Why it matters: Core session lifecycle management
- Source: execution
- Primary owning slice: M001-tvc4z0/S04
- Supporting slices: none
- Validation: unmapped
- Notes: Label-based filtering, prevent Delete on running sessions

### R005 — Process Manager lifecycle
- Class: core-capability
- Status: active
- Description: Process Manager can fork agent-shim, connect to shim socket, subscribe to events, and manage process lifecycle (Start/Stop/State/Connect)
- Why it matters: Enables actual agent execution through shim layer
- Source: execution
- Primary owning slice: M001-tvc4z0/S05
- Supporting slices: none
- Validation: unmapped
- Notes: Start workflow: resolve runtimeClass → generate config.json → create bundle → fork shim → connect socket → subscribe events

### R006 — ARI Service session methods
- Class: integration
- Status: active
- Description: ARI JSON-RPC server exposes session/* methods (new/prompt/cancel/stop/remove/list/status/attach/detach)
- Why it matters: Primary interface for orchestrator and CLI to manage sessions
- Source: execution
- Primary owning slice: M001-tvc4z0/S06
- Supporting slices: none
- Validation: unmapped
- Notes: Event notifications: session/update, session/stateChange

### R007 — agentdctl CLI
- Class: admin/support
- Status: active
- Description: CLI tool for ARI operations: session new/list/status/prompt/stop/remove, daemon status
- Why it matters: Operator interface for agentd management
- Source: execution
- Primary owning slice: M001-tvc4z0/S07
- Supporting slices: none
- Validation: unmapped
- Notes: Extends agent-shim-cli or separate agentdctl binary

### R008 — End-to-end integration
- Class: integration
- Status: validated
- Description: Full pipeline agentd → agent-shim → mockagent works: create → prompt → stop → remove
- Why it matters: Proves the assembled system works end-to-end
- Source: execution
- Primary owning slice: M001-tvc4z0/S08
- Supporting slices: none
- Validation: S08 Integration Tests: 11 tests pass — TestEndToEndPipeline (full lifecycle), TestSessionLifecycle (state machine), TestSessionPromptStoppedSession (error handling), TestSessionRemoveRunningSession (protected deletion), TestSessionList (listing), TestAgentdRestartRecovery (restart test reveals reconnection not yet implemented), TestMultipleConcurrentSessions (concurrent sessions), TestConcurrentPromptsSameSession (concurrent prompts same session)
- Notes: Restart recovery (shim reconnection after agentd restart) identified as future enhancement — test documents current behavior

### R009 — Workspace Manager prepare/cleanup
- Class: core-capability
- Status: validated
- Description: Workspace Manager can prepare workspace from spec (Git/EmptyDir/Local) and cleanup with reference counting
- Why it matters: Enables declarative workspace provisioning
- Source: execution
- Primary owning slice: M001-tlbeko/S04
- Supporting slices: M001-tlbeko/S01, M001-tlbeko/S02
- Validation: S04 WorkspaceManager tests: 13 tests pass, Prepare→Cleanup round-trips for Git/EmptyDir/Local, reference counting prevents premature cleanup (TestWorkspaceManagerReferenceCounting), hook failure handling verified
- Notes: Phase 3 requirement — validated by M001-tlbeko

### R010 — Git source handler
- Class: core-capability
- Status: validated
- Description: Git source handler clones repository with ref/depth support
- Why it matters: Primary workspace source type for agent work
- Source: execution
- Primary owning slice: M001-tlbeko/S01
- Supporting slices: none
- Validation: S01 GitHandler integration tests: 6 tests pass on github.com/octocat/Hello-World.git — default clone, shallow depth=1, branch ref='test', commit SHA checkout, context cancellation, invalid URL error handling
- Notes: Phase 3 requirement — validated by M001-tlbeko

### R011 — Hook execution
- Class: core-capability
- Status: validated
- Description: Setup/teardown hooks execute sequentially with failure handling and output capture
- Why it matters: Enables workspace initialization and cleanup customization
- Source: execution
- Primary owning slice: M001-tlbeko/S03
- Supporting slices: none
- Validation: S03 HookExecutor tests: 17 tests pass — sequential execution, abort-on-failure (TestExecuteHooksSequentialAbort proves marker file not created after first failure), output capture (HookError.Output), context cancellation
- Notes: Phase 3 requirement — validated by M001-tlbeko

### R012 — ARI workspace methods
- Class: integration
- Status: validated
- Description: ARI workspace/* methods (prepare/list/cleanup) exposed
- Why it matters: Primary interface for workspace management
- Source: execution
- Primary owning slice: M001-tlbeko/S05
- Supporting slices: none
- Validation: S05 ARI integration tests: 16 tests pass over JSON-RPC — workspace/prepare (UUID generation, Registry tracking), workspace/list (tracked workspaces), workspace/cleanup (RefCount validation, lifecycle round-trip)
- Notes: Phase 3 requirement — validated by M001-tlbeko

## Deferred

### R020 — Terminal operations
- Class: core-capability
- Status: deferred
- Description: Implement terminal/execute and terminal/read_output in agent-shim
- Why it matters: Enables shell command execution in agent workspace
- Source: execution
- Primary owning slice: none
- Supporting slices: none
- Validation: unmapped
- Notes: Phase 1.1 — currently stubs returning "not supported"

### R021 — Session load (warm resume)
- Class: continuity
- Status: deferred
- Description: Implement session/load support for warm resume
- Why it matters: Enables conversation history restoration
- Source: execution
- Primary owning slice: none
- Supporting slices: none
- Validation: unmapped
- Notes: Phase 1.2 — not yet wired

### R022 — Event log rotation
- Class: operability
- Status: deferred
- Description: Event log rotation or size limit
- Why it matters: Prevents unbounded disk growth
- Source: execution
- Primary owning slice: none
- Supporting slices: none
- Validation: unmapped
- Notes: Phase 1.4 — currently unbounded append

### R023 — GetHistory filtering
- Class: operability
- Status: deferred
- Description: GetHistory with time-range and event-type filters
- Why it matters: Reduces unnecessary data transfer
- Source: execution
- Primary owning slice: none
- Supporting slices: none
- Validation: unmapped
- Notes: Phase 1.4 — currently returns all events

### R024 — Room Manager
- Class: differentiator
- Status: deferred
- Description: Room Manager with member tracking, MCP tool injection, message routing
- Why it matters: Enables multi-agent collaboration
- Source: execution
- Primary owning slice: none
- Supporting slices: none
- Validation: unmapped
- Notes: Phase 4 — depends on Phase 3 for shared workspace

### R025 — Warm/Cold pause lifecycle
- Class: continuity
- Status: deferred
- Description: Warm idle timeout → Cold pause, cold restart with session/load
- Why it matters: Optimizes resource usage
- Source: execution
- Primary owning slice: none
- Supporting slices: none
- Validation: unmapped
- Notes: Phase 5 — builds on Phase 2 session infrastructure

## Out of Scope

### R030 — Interactive permission approval
- Class: anti-feature
- Status: out-of-scope
- Description: Runtime interactive approval for fs/terminal operations
- Why it matters: agentd manages headless sessions; interactive approval is for tools like toad
- Source: execution
- Primary owning slice: none
- Supporting slices: none
- Validation: n/a
- Notes: Use toad/acpx for interactive approval scenarios

### R031 — ACP direct manipulation
- Class: anti-feature
- Status: out-of-scope
- Description: Direct ACP message manipulation (bypassing typed events)
- Why it matters: ACP is shim implementation detail; agentd consumes typed events
- Source: execution
- Primary owning slice: none
- Supporting slices: none
- Validation: n/a
- Notes: Typed events are the core protocol

## Traceability

| ID | Class | Status | Primary owner | Supporting | Proof |
|---|---|---|---|---|---|
| R001 | launchability | active | M001-tvc4z0/S01 | none | unmapped |
| R002 | core-capability | active | M001-tvc4z0/S03 | none | unmapped |
| R003 | core-capability | active | M001-tvc4z0/S02 | none | unmapped |
| R004 | core-capability | active | M001-tvc4z0/S04 | none | unmapped |
| R005 | core-capability | active | M001-tvc4z0/S05 | none | unmapped |
| R006 | integration | active | M001-tvc4z0/S06 | none | unmapped |
| R007 | admin/support | active | M001-tvc4z0/S07 | none | unmapped |
| R008 | integration | validated | M001-tvc4z0/S08 | none | S08: 11 integration tests pass |
| R009 | core-capability | validated | M001-tlbeko/S01 | none | S04 WorkspaceManager tests |
| R010 | core-capability | validated | M001-tlbeko/S02 | none | S01 GitHandler tests |
| R011 | core-capability | validated | M001-tlbeko/S03 | none | S03 HookExecutor tests |
| R012 | integration | validated | M001-tlbeko/S04 | none | S05 ARI integration tests |
| R020 | core-capability | deferred | none | none | unmapped |
| R021 | continuity | deferred | none | none | unmapped |
| R022 | operability | deferred | none | none | unmapped |
| R023 | operability | deferred | none | none | unmapped |
| R024 | differentiator | deferred | none | none | unmapped |
| R025 | continuity | deferred | none | none | unmapped |
| R030 | anti-feature | out-of-scope | none | none | n/a |
| R031 | anti-feature | out-of-scope | none | none | n/a |

## Coverage Summary

- Active requirements: 7
- Mapped to slices: 12
- Validated: 5
- Unmapped active requirements: 0
- Unmapped active requirements: 0