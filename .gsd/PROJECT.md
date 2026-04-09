# Project

## What This Is

Open Agent Runtime (OAR) is a layered runtime for headless coding agents. It manages ACP-speaking agent processes through `agent-shim`, a higher-level daemon in `agentd`, and a future orchestrator layer for multi-agent coordination.

## Core Value

The thing that must stay true is reliable, observable agent execution with truthful lifecycle and recovery semantics. If scope has to shrink, the runtime still needs to launch real ACP agents, manage them cleanly, and tell the truth about their state, ownership boundaries, and restart behavior.

## Current State

### Completed Milestones

**M001 — Core Runtime Implementation.** Built the foundational layers: agent-shim process management, agentd daemon with ARI JSON-RPC server, workspace preparation (Git/EmptyDir/Local), session lifecycle with state machine, metadata persistence in SQLite, and CLI tooling. Integration tests prove the full pipeline `agentd → agent-shim → mockagent`.

**M002 — Contract Convergence.** Converged the design contract across Room, Session, Runtime, Workspace, and shim recovery semantics into one non-conflicting authority map. Replaced legacy PascalCase shim methods with clean-break `session/*` + `runtime/*` surface. Added schema v2 with bootstrap config persistence, `RecoverSessions` startup pass for live shim reconnection, and real CLI integration tests for `gsd-pi` and `claude-code`.

**M003 — Recovery and Safety Hardening.** Hardened daemon recovery with fail-closed posture (RecoveryPhase atomic tracking blocks prompt/cancel during recovery), shim-vs-DB state reconciliation, atomic event resume (SubscribeFromSeq eliminates History→Subscribe gap structurally), damaged-tail tolerant log reads, and DB-backed workspace cleanup safety (ref_count gates, Registry/WorkspaceManager rebuild from DB, recovery-phase guard on cleanup).

**M004 — Realized Room Runtime and Routing.** Turned the Room from a design-only contract into a working runtime. All 3 slices complete:
- **S01:** Room Lifecycle and ARI Surface — room/create, room/status, room/delete handlers. Communication vocabulary converged to mesh/star/isolated. Room-existence validation enforced on session/new. 5 integration tests.
- **S02:** Routing Engine and MCP Tool Injection — room/send ARI handler for orchestrator-driven messaging and room-mcp-server MCP binary for agent-driven messaging. deliverPrompt helper shared between session/prompt and room/send. 12 integration tests.
- **S03:** End-to-End Multi-Agent Integration Proof — TestARIMultiAgentRoundTrip (3-agent bootstrap, bidirectional A↔B + A→C delivery, state transitions, clean teardown) and TestARIRoomTeardownGuards (delete-with-active-members guard, session/remove-on-running guard, correct ordering succeeds). 2 capstone integration tests. 47 total ARI integration tests.

**M005 — agentd Agent Model Refactoring.** ✅ Complete. Transformed agentd's external object model from session-centric to agent-centric across 7 slices and 40 changed files (2697 insertions, 4194 deletions). All 6 requirements (R047–R052) validated.

- **S01 ✅ — Design Contract — Agent Model Convergence.** All 7 authority documents rewritten to agent-first model. agent/* replaces session/* in external ARI surface. 5-state machine (creating/created/running/stopped/error) established. Turn-aware event ordering (turnId/streamSeq/phase) specified. Contract verifier script (`scripts/verify-m005-s01-contract.sh`) passes.

- **S02 ✅ — Schema & State Machine.** `agents` table (schema v3/v4) with room+name UNIQUE key. `meta.Agent`, `meta.AgentState`, full CRUD. `sessions.agent_id` FK column (DEFAULT NULL, ON DELETE SET NULL). `SessionStateCreating`/`SessionStateError` added; paused:* fully removed. 5-state transitions enforced. 102 tests pass.

- **S03 ✅ — ARI Agent Surface — Method Migration.** 10 `agent/*` handler methods replace all 9 `session/*` dispatch cases. `AgentManager` with domain error types. All Agent* request/response types in `pkg/ari/types.go`. `agentdctl` CLI migrated from `session/*` to `agent/*`. 64 pkg/ari tests pass.

- **S04 ✅ — Agent Lifecycle — Async Create, Stop/Delete Separation, Restart.** Async `agent/create` returns creating immediately; background goroutine (context.Background() + 90s timeout) bootstraps shim and transitions to created/error. `handleAgentPrompt` guards creating state. Real async `handleAgentRestart` (was MethodNotFound stub). `OAR_AGENT_ID`/`OAR_AGENT_NAME` injected into shim env.

- **S05 ✅ — Event Ordering — Turn-Aware Envelope Enhancement.** `TurnId`/`StreamSeq *int`/`Phase` fields on `SessionUpdateParams`. Translator assigns turn context atomically under lock. `handlePrompt` calls `NotifyTurnStart`/`NotifyTurnEnd`. StreamSeq is *int (not int) to preserve omitempty semantics for zero value. 7 unit tests + RPC integration tests pass. R050 validated.

- **S06 ✅ — Room & MCP Agent Alignment.** `handleRoomStatus` queries agents table; `handleRoomSend` guards on agent state and calls UpdateState(running) post-delivery. `room-mcp-server` rewritten with `modelcontextprotocol/go-sdk v0.8.0` (hand-rolled 497-line JSON-RPC loop deleted). Deprecated `OAR_SESSION_ID`/`OAR_ROOM_AGENT` env vars removed. R051 validated.

- **S07 ✅ — Recovery & Integration Proof.** Injected `AgentManager` into `ProcessManager`; `RecoverSessions` reconciles agent states post-restart: dead-shim→error, live-shim-running→running, creating-cleanup pass handles bootstrap races. `TestAgentdRestartRecovery` (7-phase integration test, 4.47s) proves R052: agentId/room/name persist through daemon restart. All 7 integration tests pass (2 skip, no API key). Zero session/* calls in non-CLI integration test files. R052 validated.

### What's Implemented

- `agent-shim` starts ACP agent processes, performs the ACP handshake, and exposes the clean-break `session/*` + `runtime/*` shim RPC surface
- `agentd` manages **agents** (external, identified by room+name) and sessions (internal runtime instances), with full CRUD, async lifecycle, and fail-closed daemon restart recovery
- **Agent-centric ARI surface**: `agent/create`, `agent/prompt`, `agent/cancel`, `agent/stop`, `agent/delete`, `agent/restart`, `agent/list`, `agent/status`, `agent/attach`, `agent/detach` — 10 methods; session/* is now internal-only
- **Turn-aware event ordering**: TurnId/StreamSeq/Phase on all session/update envelopes; runtime/stateChange excluded; global seq retained for cross-turn replay
- **Room runtime**: mesh/star/isolated communication modes, orchestrator-driven room/send, agent-driven room MCP tool injection (SDK-based)
- **Workspace preparation** for Git / EmptyDir / Local sources, with hooks and reference tracking
- **Integration tests** prove the assembled path `agentd → agent-shim → mockagent` including async lifecycle, daemon restart recovery, and multi-agent coordination
- **CLI tooling** (`agentdctl`) with agent/workspace/daemon subcommands

### ARI External Surface (as of M005)

```
agent/*      — create, prompt, cancel, stop, delete, restart, list, status, attach, detach
room/*       — create, status, send, delete
workspace/*  — prepare, list, cleanup
```

### Next Steps

M005 is complete. The agent model refactoring is fully implemented and integration-proven. The stable agent-centric ARI surface is ready for future milestones. Known cleanup items:
- Remove `handleSessionRemove` dead code in `pkg/ari/server.go`
- Implement `agent/detach` (currently no-op stub)
- Populate `Phase` field on `SessionUpdateParams` once phase semantics are defined
