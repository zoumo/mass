---
id: S02
parent: M007
milestone: M007
provides:
  - ["buildNotifHandler pattern available for any new DialWithHandler caller in agentd", "RestartPolicyTryReload/AlwaysNew constants in pkg/meta for use by ARI create/restart handlers (S03)", "Start() no longer writes StatusRunning — S03 ARI agent/create handler must poll for StatusIdle via DB after the call, not assume StatusRunning was set synchronously"]
requires:
  []
affects:
  - ["S03 — ARI agent/create must poll DB for StatusIdle after calling Start(), not assume synchronous StatusRunning write"]
key_files:
  - ["pkg/agentd/process.go", "pkg/agentd/shim_boundary_test.go", "pkg/agentd/shim_client.go", "pkg/agentd/shim_client_test.go", "pkg/agentd/recovery.go", "pkg/agentd/recovery_test.go", "pkg/meta/models.go"]
key_decisions:
  - ["D088 enforced: buildNotifHandler extracted as shared method; direct UpdateStatus(StatusRunning) removed from Start() — shim stateChange is now the sole post-bootstrap DB write path", "D089 implemented: tryReload block placed AFTER atomic Subscribe to avoid missing immediate stateChange response from session/load; falls back silently on all failure modes", "RestartPolicy constants RestartPolicyTryReload/AlwaysNew added to pkg/meta/models.go and old never/on-failure/always comment corrected"]
patterns_established:
  - ["buildNotifHandler: shared notification handler extracted as ProcessManager method; both Start() and recoverAgent() use it — eliminates closure duplication and enables unit-testing the handler in isolation", "tryReload fallback chain: state file read → sessionId empty check → Load() call → log+continue on any failure — recoverAgent() always completes regardless of tryReload outcome", "Background context for async notification writes: stateChange handler uses context.WithTimeout(context.Background(), 5s) not the request context, which may have ended by the time the shim emits stateChange"]
observability_surfaces:
  - ["slog INFO 'stateChange: updating DB state' keyed by agent_key — grep-able signal for every post-bootstrap DB state transition", "slog WARN 'stateChange: malformed notification dropped' keyed by agent_key — diagnostic for shim protocol violations", "slog INFO 'tryReload: session/load succeeded' / 'tryReload: session/load failed, continuing' / 'tryReload: could not read sessionId from state file, skipping' keyed by agent_key — full tryReload decision trace"]
drill_down_paths:
  - [".gsd/milestones/M007/slices/S02/tasks/T01-SUMMARY.md", ".gsd/milestones/M007/slices/S02/tasks/T02-SUMMARY.md"]
duration: ""
verification_result: passed
completed_at: 2026-04-09T21:00:14.867Z
blocker_discovered: false
---

# S02: agentd Core Adaptation

**Enforced D088 shim write authority boundary by wiring runtime/stateChange notifications to DB state updates and removing direct post-bootstrap UpdateStatus(StatusRunning) from Start(); implemented RestartPolicy tryReload/alwaysNew in recoverAgent(); 10 new unit tests all pass.**

## What Happened

S02 delivered two closely-coupled capabilities: (1) enforcement of the D088 shim write authority boundary, and (2) RestartPolicy tryReload/alwaysNew recovery semantics.

**T01 — Wire runtime/stateChange; remove direct post-bootstrap state writes**

The pre-S02 `Start()` called `UpdateStatus(StatusRunning)` directly after Subscribe — this violated D088 (shim should be the sole source of truth for post-bootstrap state). T01 removed that call and replaced both the Start() and recoverAgent() inline notification closures with a shared `buildNotifHandler` method on ProcessManager. The handler dispatches on method: `events.MethodSessionUpdate` routes to `shimProc.Events` (unchanged); `events.MethodRuntimeStateChange` parses the notification with `ParseRuntimeStateChange(params)` and calls `m.agents.UpdateStatus` with a 5-second background context (decoupled from the request context, which may have ended by the time the shim emits stateChange).

Malformed stateChange notifications are logged at WARN and dropped cleanly. Log line `"stateChange: updating DB state"` with `agent_key` field satisfies the slice verification contract.

Four unit tests in `pkg/agentd/shim_boundary_test.go` prove the boundary without a real shim binary:
- `TestStateChange_CreatingToIdle_UpdatesDB` — shim emits creating→idle; DB reads StatusIdle ✅
- `TestStateChange_RunningToIdle_UpdatesDB` — two successive stateChange notifications drive both transitions ✅
- `TestStart_DoesNotWriteStatusRunning` — DB never shows StatusRunning before a stateChange arrives ✅
- `TestStateChange_MalformedParamsDropped` — malformed params log WARN and do not panic (bonus test) ✅

**T02 — ShimClient.Load(); RestartPolicy tryReload/alwaysNew**

Added `SessionLoadParams` struct and `Load(ctx, sessionID) error` to ShimClient. Added `RestartPolicyTryReload = "tryReload"` and `RestartPolicyAlwaysNew = "alwaysNew"` constants to `pkg/meta/models.go`, correcting the AgentSpec.RestartPolicy comment which previously described the old `never/on-failure/always` semantics.

In `recoverAgent()`, a tryReload block fires AFTER atomic Subscribe (ordering is critical — Subscribe must be established before session/load so any immediate stateChange from the shim isn't missed). The block: reads the ACP sessionId from `state.json` via `readStateSessionID(stateDir)`; calls `client.Load(ctx, sessionId)`; falls back silently on any failure (missing state file, empty sessionId, shim rejects). `recoverAgent()` always completes regardless of tryReload outcome.

Log line `"tryReload: session/load failed, continuing"` keyed by `agent_key` satisfies the slice verification contract. The `alwaysNew` path (or empty RestartPolicy) simply skips the tryReload block — no `session/load` is issued.

Six tests prove all paths: `TestShimClient_Load_Success`, `TestShimClient_Load_RpcError`, `TestRecovery_TryReload_AttemptsSessionLoad`, `TestRecovery_TryReload_FallsBackOnLoadFailure`, `TestRecovery_TryReload_FallsBackOnMissingStateFile`, `TestRecovery_AlwaysNew_SkipsSessionLoad` — all pass ✅.

**Full suite:** 68 tests pass; only the pre-existing `TestProcessManagerStart` fails (requires a real shim binary — pre-existing before S02). `go build ./...` is clean.

## Verification

All slice verification checks passed:

1. `go test ./pkg/agentd/... -run 'TestStateChange' -count=1 -timeout 30s` → PASS (3 tests)
2. `go test ./pkg/agentd/... -run 'TestRecovery_TryReload|TestRecovery_AlwaysNew|TestShimClient_Load' -count=1 -timeout 30s` → PASS (6 tests)
3. `go test ./pkg/agentd/... -count=1 -timeout 60s` → 68 PASS, 1 pre-existing FAIL (TestProcessManagerStart — requires real shim binary, not introduced by S02)
4. `go build ./...` → exit 0, no errors
5. Log lines confirmed in test output: "stateChange: updating DB state" (agent_key keyed) and "tryReload: session/load failed, continuing" (agent_key keyed)
6. No Session concept (as grouping/Room concept) anywhere in pkg/agentd non-test files
7. RestartPolicy constants and comment updated in pkg/meta/models.go

## Requirements Advanced

None.

## Requirements Validated

- R044 — RestartPolicy tryReload/alwaysNew implemented with graceful fallback; shim write authority boundary enforced — establishes the contract that M007 converges before further hardening

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

T01 added a 4th test TestStateChange_MalformedParamsDropped (not in plan) to cover the WARN path. T02 used log wording 'tryReload: session/load failed, continuing' (slice plan) rather than 'falling back' (task plan wording) to satisfy the slice-level verification grep contract. buildNotifHandler method name follows the 'test-visible accessor' hint from the plan.

## Known Limitations

Full end-to-end tryReload proof (session/load through a real shim SDK) requires S05 integration tests — the S02 tests prove the agentd side against mockShimServer. TestProcessManagerStart pre-exists as a known non-passing test requiring a real shim binary; not introduced by S02.

## Follow-ups

S03 must poll DB for StatusIdle post agent/create (Start() no longer writes StatusRunning synchronously). S05 integration tests should include a tryReload scenario with a real shim to close the end-to-end proof of D089.

## Files Created/Modified

- `pkg/agentd/process.go` — Extracted buildNotifHandler method; added runtime/stateChange branch calling agents.UpdateStatus; removed direct UpdateStatus(StatusRunning) from Start() step 9
- `pkg/agentd/shim_boundary_test.go` — New file: 4 unit tests proving D088 shim write authority boundary (stateChange→DB, no direct StatusRunning write, malformed notification drop)
- `pkg/agentd/shim_client.go` — Added SessionLoadParams struct and Load(ctx, sessionID) method for session/load RPC
- `pkg/agentd/shim_client_test.go` — Extended mockShimServer with loadCalled/loadCalledWith/loadSessionErr fields; added TestShimClient_Load_Success and TestShimClient_Load_RpcError
- `pkg/agentd/recovery.go` — Added tryReload block after Subscribe; added readStateSessionID helper; removed unused encoding/json import
- `pkg/agentd/recovery_test.go` — 4 new tests: TryReload_AttemptsSessionLoad, TryReload_FallsBackOnLoadFailure, TryReload_FallsBackOnMissingStateFile, AlwaysNew_SkipsSessionLoad
- `pkg/meta/models.go` — Added RestartPolicyTryReload/AlwaysNew constants; updated AgentSpec.RestartPolicy comment from never/on-failure/always to tryReload/alwaysNew
