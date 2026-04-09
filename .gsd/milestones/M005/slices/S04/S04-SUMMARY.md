---
id: S04
parent: M005
milestone: M005
provides:
  - ["Async agent/create → background goroutine bootstrap → creating/created/error state machine", "Real async agent/restart (replaces MethodNotFound stub)", "creating-state guard on agent/prompt", "OAR_AGENT_ID and OAR_AGENT_NAME in MCP server env block", "agentdctl restart subcommand", "AgentInfo.ErrorMessage field on agent/status response", "pollAgentUntilReady test helper for all downstream async tests"]
requires:
  - slice: S01
    provides: agent state machine design (creating/created/running/stopped/error)
  - slice: S03
    provides: ARI agent surface handlers (handleAgentCreate, handleAgentStop, handleAgentDelete, handleAgentStatus)
affects:
  - ["S05 — turn-aware event ordering (agent state transitions now include creating→created)", "S06 — room+MCP agent alignment (must remove OAR_SESSION_ID/OAR_ROOM_AGENT deprecated aliases)", "S07 — recovery proof (restart goroutine pattern is the recovery entry point)"]
key_files:
  - ["pkg/ari/server.go", "pkg/ari/server_test.go", "pkg/ari/types.go", "pkg/agentd/process.go", "cmd/agentdctl/agent.go"]
key_decisions:
  - ["Async create: goroutine uses context.Background()+90s timeout (not request ctx, which is dead after Reply)", "handleRoomDelete guards on non-stopped agents (not just sessions) to close async-create race window (D074)", "handleAgentRestart deletes old session inside goroutine to keep Reply latency minimal; creating state blocks prompts via T01 guard (D075)", "agents.UpdateState has no transition validation — stopped→creating and error→creating work as-is", "OAR_SESSION_ID/OAR_ROOM_AGENT retained as deprecated aliases alongside new OAR_AGENT_ID/OAR_AGENT_NAME until S06 (D076)"]
patterns_established:
  - ["Async RPC reply + background goroutine bootstrap: create agent record in creating state → reply immediately → goroutine creates session + starts process → update agent to created/error. Both create and restart follow this pattern.", "pollAgentUntilReady(t, conn, agentId, maxWait, interval) helper centralizes polling logic across all tests requiring async agent readiness.", "Two-state guard pair: handleAgentPrompt blocks on creating; handleRoomDelete blocks on non-stopped agents — together they close all concurrency windows around async bootstrap."]
observability_surfaces:
  - ["slog Info: 'ari: agent bootstrap complete' with agentId+sessionId on successful async create", "slog Error: 'ari: agent bootstrap: failed to start process' with agentId+sessionId+error on bootstrap failure", "slog Info: 'ari: agent restart complete' with agentId+oldSessionId+newSessionId on successful restart", "AgentInfo.ErrorMessage field on agent/status response surfaces bootstrap failure reason to orchestrators"]
drill_down_paths:
  []
duration: ""
verification_result: passed
completed_at: 2026-04-08T20:05:11.603Z
blocker_discovered: false
---

# S04: Agent Lifecycle — Async Create, Stop/Delete Separation, Restart

**Replaced synchronous agent/create with async semantics (creating state + background bootstrap), implemented real async agent/restart (was MethodNotFound stub), added OAR_AGENT_ID/OAR_AGENT_NAME env vars to generateConfig, and wired agentdctl restart subcommand — all tests pass.**

## What Happened

## S04: Agent Lifecycle — Async Create, Stop/Delete Separation, Restart

S04 delivered three focused changes to the agent lifecycle surface that complete the async create/restart model introduced by the M005 design contract.

### T01: Async agent/create with background goroutine bootstrap

`handleAgentCreate` was refactored from a synchronous operation (block until shim starts) to an async pattern:

1. Create the agent DB record in `AgentStateCreating`.
2. Reply immediately with `{agentId, state: "creating"}`.
3. Launch a background goroutine bounded by `context.WithTimeout(context.Background(), 90*time.Second)` that: creates the session row, acquires workspace/registry refs, calls `processes.Start`, and transitions the agent to `created` (success) or `error` (failure with cleanup + ErrorMessage populated).

A creating-state guard was added to `handleAgentPrompt`: if the agent is still `creating`, the call returns `CodeInvalidParams` with a clear message instructing the caller to poll `agent/status`.

`handleRoomDelete` was enhanced beyond the task plan to block on non-stopped agents (not just sessions), closing the async race where the goroutine hadn't yet created the session row.

`AgentInfo.ErrorMessage` was added to `types.go` so bootstrap failures are visible via `agent/status` responses.

20+ existing tests were updated to use a new `pollAgentUntilReady` helper. Two new integration tests were added using `newSessionTestHarness` (real mockagent shim): `TestARIAgentCreateAsync` and `TestARIAgentCreateAsyncErrorState`. Both pass with real end-to-end lifecycle verification.

### T02: Real async agent/restart (replaces MethodNotFound stub)

`AgentRestartResult` was added to `types.go`. The stub `handleAgentRestart` was replaced with a full async implementation:

- Validates agent is `stopped` or `error` (CodeInvalidParams otherwise).
- Pre-fetches the linked session (may be nil).
- Transitions agent to `creating` synchronously.
- Replies immediately: `{agentId, state: "creating"}`.
- Background goroutine (90s timeout): delete old session, release workspace refs, create new session with fresh UUID, acquire workspace/registry, start process, transition to `created` / `error` with structured slog logging including agentId, oldSessionId, newSessionId.

Key finding: `AgentManager.UpdateState` has no transition validation (unlike `SessionManager`), so `stopped→creating` and `error→creating` work without any changes to `pkg/agentd/agent.go`.

`TestARIAgentRestartAsync` replaces the old `TestARIAgentRestartStub` and exercises the full lifecycle: create → poll until created → prompt (verify functional) → stop → restart → poll until created → prompt (verify restart completed) → stop → delete.

`agentRestartCmd` was added to `cmd/agentdctl/agent.go` with a descriptive Long text advising callers to poll status after restart.

### T03: OAR_AGENT_ID / OAR_AGENT_NAME env vars in generateConfig

Two new entries were added to the `mcpServers` env block in `pkg/agentd/process.go`:
- `OAR_AGENT_ID` = `session.AgentID`
- `OAR_AGENT_NAME` = `session.RoomAgent`

The pre-existing `OAR_SESSION_ID` and `OAR_ROOM_AGENT` are retained with deprecation comments pointing to S06 for removal (when `room-mcp-server` is rewritten to use the canonical names). The change is purely additive — no existing tests required updates.

### Verification summary

All verification commands from the slice plan passed:
- `go test ./pkg/ari/... -count=1 -timeout 120s` → PASS (13s)
- `go test ./pkg/ari/... -run TestARIAgentCreateAsync -v -timeout 60s` → PASS
- `go test ./pkg/ari/... -run TestARIAgentCreateAsyncErrorState -v -timeout 60s` → PASS
- `go test ./pkg/ari/... -run TestARIAgentRestartAsync -v -timeout 90s` → PASS
- `go test ./pkg/agentd/... -count=1 -timeout 60s` → PASS
- `go build ./...` → clean
- `agentdctl agent restart --help` → correct output with polling guidance

## Verification

All slice plan verification commands executed and passed:
1. go test ./pkg/ari/... -count=1 -timeout 120s → ok (13.049s)
2. go test ./pkg/ari/... -run TestARIAgentCreateAsync -v → PASS (0.24s) — create returns creating, background goroutine bootstraps to created, stop+delete cleanup works
3. go test ./pkg/ari/... -run TestARIAgentCreateAsyncErrorState -v → PASS (0.03s) — invalid runtimeClass transitions agent to error state with ErrorMessage populated
4. go test ./pkg/ari/... -run TestARIAgentRestartAsync -v -timeout 90s → PASS (0.45s) — full lifecycle: create→prompt→stop→restart→poll→prompt→stop→delete
5. go test ./pkg/agentd/... -count=1 -timeout 60s → ok (6.494s)
6. go build ./... → exit 0 (clean build, 18s)
7. agentdctl agent restart --help → correct output with async polling guidance in Long description

## Requirements Advanced

- R044 — async restart is now implemented; remaining follow-on work is cross-client hardening deferred to S07

## Requirements Validated

- R048 — TestARIAgentCreateAsync: create returns creating → poll status → transitions to created. TestARIAgentCreateAsyncErrorState: create returns creating → poll status → transitions to error with non-empty ErrorMessage. Both use real mockagent shim.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

T01 added handleRoomDelete agent-state guard (not in task plan) to close the async-create race window. T01 added AgentInfo.ErrorMessage to types.go (not in task plan) to surface bootstrap failures. T01 migrated 4 tests from newTestHarness to newSessionTestHarness. All deviations are improvements that strengthen the async semantics.

## Known Limitations

No concurrent-create deduplication: calling agent/create twice with the same room+name within the creating window creates two agents (DB unique constraint prevents this at the agents table level — verified existing). OAR_SESSION_ID/OAR_ROOM_AGENT are still injected as deprecated aliases until S06 cleans them up.

## Follow-ups

S06 must remove OAR_SESSION_ID and OAR_ROOM_AGENT from generateConfig once room-mcp-server is rewritten. S07 recovery proof should exercise restart after daemon restart to verify the goroutine pattern survives the recovery window correctly.

## Files Created/Modified

- `pkg/ari/server.go` — handleAgentCreate async refactor, creating-state guard on handleAgentPrompt, handleRoomDelete agent-state guard, real async handleAgentRestart implementation
- `pkg/ari/server_test.go` — 20+ tests updated with pollAgentUntilReady, added TestARIAgentCreateAsync, TestARIAgentCreateAsyncErrorState, TestARIAgentRestartAsync (replaces TestARIAgentRestartStub)
- `pkg/ari/types.go` — Added AgentRestartResult struct, AgentInfo.ErrorMessage field
- `pkg/agentd/process.go` — Added OAR_AGENT_ID and OAR_AGENT_NAME to generateConfig mcpServers env block with deprecation comments on old aliases
- `cmd/agentdctl/agent.go` — Added agentRestartCmd with ExactArgs(1), Long description with polling guidance, runAgentRestart implementation
