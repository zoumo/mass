# S02 — Live Shim Reconnect and Truthful Session Rebuild — Research

**Date:** 2026-04-08
**Depth:** Light research — the reconnect implementation already exists and passes both unit and integration tests.

## Summary

**The core work described by this slice — live shim reconnect and truthful session rebuild — is already implemented.** S01 was scoped to deliver the recovery posture types and ARI guards, but the actual implementation went further: `RecoverSessions` in `pkg/agentd/recovery.go` already contains the full reconnect pipeline (dial → runtime/status → runtime/history → session/subscribe → register in process map → start watcher), and the integration test `TestAgentdRestartRecovery` already proves the complete round-trip: session A stays alive across daemon restart, session B (dead shim) gets marked stopped, and events are contiguous with no seq gaps after restart.

The remaining S02 work is therefore **verification, gap-closing, and hardening of the existing implementation** rather than new feature work. There are three concrete gaps to close:

1. **Session state reconciliation** — `recoverSession` succeeds when it can connect and subscribe, but it does not reconcile the shim's reported `runtime/status` state against the SQLite session state. If the shim reports `stopped` but the DB says `running`, the recovery silently proceeds rather than updating the DB or flagging the mismatch.

2. **ARI socket cleanup race** — `cmd/agentd/main.go` lines 98-103 use `Stat → Remove → Serve`, which creates a TOCTOU race. If another daemon instance starts between Stat and Remove, the socket could be deleted from under a live daemon. The recovery pass runs _before_ the ARI socket is created, so this is a startup correctness issue, not a recovery issue, but it's called out in the milestone research as directly relevant.

3. **Recovery metadata in session/status after restart** — The `handleSessionStatus` path in `pkg/ari/server.go` only returns `ShimState` when `session.State == meta.SessionStateRunning` AND the process is in the in-memory map. After recovery, this works correctly because `recoverSession` registers the process. But the `RecoveryInfo` field on `SessionStatusResult` — added by S01 — is only populated from `ShimProcess.Recovery`, and the recovery flow already sets this. **This path is complete.**

## Recommendation

Plan this slice as three tasks:

1. **State reconciliation in recoverSession** — After `runtime/status` returns, compare the shim's reported status against the session's DB state. If the shim reports a terminal status (stopped/exited), transition the session to stopped and skip the subscribe step. If there's a mismatch that isn't terminal (e.g., shim says "running" but DB says "created"), update the DB state to match the shim truth. Log mismatches at Warn level. Add unit tests for the reconciliation paths.

2. **ARI socket startup race fix** — Replace the `Stat → Remove → Serve` sequence in `cmd/agentd/main.go` with an unconditional `os.Remove` (ignore ENOENT) before calling `Serve()`. This is a one-line fix but it eliminates a documented race condition.

3. **Integration and verification** — Extend unit tests to cover the reconciliation paths (mock shim reporting stopped, mock shim reporting mismatched state). Verify existing integration test still passes. Verify all `pkg/agentd` and `pkg/ari` tests pass.

## Implementation Landscape

### Key Files

- **`pkg/agentd/recovery.go`** — The recovery pipeline. `RecoverSessions` iterates non-terminal sessions. `recoverSession` does the actual dial → status → history → subscribe → register flow. This is the file where state reconciliation belongs.

- **`pkg/agentd/process.go`** — `ProcessManager`, `ShimProcess`, the process map, and the existing recovery posture methods. Already complete for this slice's needs. The `watchRecoveredProcess` goroutine monitors recovered shims via `DisconnectNotify`.

- **`pkg/agentd/recovery_posture.go`** — `RecoveryPhase`, `RecoveryInfo`, `RecoveryOutcome` types. Already complete.

- **`pkg/agentd/shim_client.go`** — `ShimClient` with `Status()`, `History()`, `Subscribe()`. Complete RPC surface — no changes needed.

- **`pkg/ari/server.go`** — `handleSessionStatus` already surfaces `RecoveryInfo` and `ShimState` for recovered sessions. `recoveryGuard` blocks prompt/cancel during recovery. No changes needed.

- **`pkg/ari/types.go`** — `SessionRecoveryInfo`, `CodeRecoveryBlocked`. Complete.

- **`pkg/meta/session.go`** — `UpdateSession(ctx, id, state, labels)` for state transitions. `GetSession` for reads. Already used by recovery flow.

- **`cmd/agentd/main.go`** — Lines 98-103: the socket cleanup race. Single fix target.

- **`pkg/agentd/recovery_test.go`** — Existing test infrastructure: `setupRecoveryTest`, `createRecoveryTestSession`, `newMockShimServer`. **6 existing tests** that cover live shim, dead shim, no sessions, stopped sessions, mixed live/dead, no socket path. New reconciliation tests should follow the same pattern.

- **`pkg/agentd/recovery_posture_test.go`** — 6 phase lifecycle tests from S01.

- **`tests/integration/restart_test.go`** — Full integration test: `TestAgentdRestartRecovery` — 6 phases proving restart, reconnect, dead-shim fail-closed, and event continuity.

### What Already Works

- **Full reconnect pipeline**: `recoverSession` connects to live shim sockets, calls `runtime/status`, replays `runtime/history`, subscribes with `afterSeq`, and registers in the process map.
- **Dead shim fail-closed**: Sessions with unreachable sockets are marked stopped.
- **Recovery phase lifecycle**: `RecoveryPhaseIdle → Recovering → Complete` with atomic tracking.
- **ARI guard**: `session/prompt` and `session/cancel` blocked during recovery.
- **Per-session RecoveryInfo**: Set on successful recovery, surfaced through `session/status`.
- **Bootstrap persistence**: `UpdateSessionBootstrap` persists socket path, state dir, PID, and bootstrap config JSON in SQLite v2 schema.
- **Integration test**: Proves daemon restart → live reconnect → dead shim stopped → event continuity.

### What's Missing

1. **State reconciliation**: `recoverSession` trusts the DB state and doesn't compare it against what the shim reports. If the shim says it's stopped but the DB says running, recovery connects successfully (the socket may still be open briefly after the shim process exits), subscribes, and then the watcher will eventually catch the disconnect. This is not catastrophic but is not truthful recovery — the session briefly appears recovered when it's actually dying.

2. **Socket startup race**: `Stat → Remove` in `main.go` is a TOCTOU race (documented in milestone research and unified-modification-plan). Simple fix: unconditional `os.Remove` with error ignored.

3. **No test for shim reporting non-running state**: Current tests use mock shims that always report `spec.StatusRunning`. No test covers a mock shim reporting `spec.StatusStopped` or a state mismatch between shim and DB.

### What's Out of Scope

- Event-log damage tolerance (S03)
- Workspace refcount reconciliation (S04)
- Codex prompt round-trip (S05/future)
- Socket-based shim discovery by filesystem scan (the current approach uses persisted `shim_socket_path` from SQLite, which is the correct M003 approach)

### Build Order

1. **Socket race fix first** (smallest, independent, unblocks clean startup).
2. **State reconciliation in recoverSession** (main work of this slice).
3. **Tests for reconciliation paths** (verifies the new behavior).

### Verification Approach

- `go build ./cmd/agentd/...` — build verification.
- `go vet ./pkg/agentd/... ./cmd/agentd/...` — static checks.
- `go test ./pkg/agentd/... -count=1` — unit tests including new reconciliation tests.
- `go test ./pkg/ari/... -count=1` — ARI tests (regression check).
- Existing integration test: `go test ./tests/integration/ -run TestAgentdRestartRecovery -count=1` (if binaries are built).

### Existing Test Infrastructure

The `pkg/agentd` test package has excellent infrastructure for this work:

- `setupRecoveryTest(t)` → creates ProcessManager with real SQLite store
- `newMockShimServer(t)` → starts a mock shim with configurable status/history/subscribe responses
- `createRecoveryTestSession(t, ctx, store, wsID, state, socketPath)` → creates a test session
- `createTestWorkspace(t, ctx, store)` → creates a test workspace

New tests should follow the existing `TestRecoverSessions_*` pattern.

### Requirements Targeted

| Requirement | Relevance | How This Slice Advances It |
|-------------|-----------|---------------------------|
| R035 (event recovery) | supporting | State reconciliation ensures recovered sessions truthfully reflect shim state, so the history→subscribe resume path doesn't run on dead-but-connected sessions |
| R036 (session config/identity for restart) | supporting | Reconciliation completes the truth loop: persisted metadata is verified against live shim truth during recovery |
| R044 (restart/cleanup hardening) | primary | This is the main slice delivering restart hardening beyond M002 proof level — reconciliation + socket race fix |

## Constraints

- The mock shim server in `shim_client_test.go` supports configurable `statusResult` — new tests can set `srv.statusResult.State.Status = spec.StatusStopped` to test reconciliation.
- `meta.SessionState` values: `created`, `running`, `paused:warm`, `paused:cold`, `stopped`. The shim's `spec.Status` values: `creating`, `created`, `running`, `stopped`. The mapping is: shim `stopped` → session `stopped`; shim `running` → session `running`.
- `SessionManager.Transition` enforces valid transitions. `running → stopped` is valid. `created → running` is valid. Invalid transitions will error — reconciliation must only attempt valid transitions.

## Skills Discovered

| Technology | Skill | Status |
|------------|-------|--------|
| Go | none relevant | not needed — straightforward Go patterns already in codebase |
