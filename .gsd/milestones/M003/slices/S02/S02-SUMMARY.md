---
id: S02
parent: M003
milestone: M003
provides:
  - ["Shim-vs-DB state reconciliation in recoverSession — downstream slices S03/S04 can rely on DB state being truthful after recovery", "TOCTOU-free ARI socket startup"]
requires:
  []
affects:
  - ["S03", "S04"]
key_files:
  - ["pkg/agentd/recovery.go", "cmd/agentd/main.go", "pkg/agentd/recovery_test.go"]
key_decisions:
  - ["D042: ErrInvalidTransition from Transition() logged at Warn and recovery proceeds — transition edge cases should not block reconnecting to a live shim", "D043: Unconditional os.Remove for socket cleanup eliminates TOCTOU race"]
patterns_established:
  - ["Shim-vs-DB reconciliation pattern: explicit switch on shim status with per-case handling, string comparison across type boundaries, catch-all for unknown mismatches", "TOCTOU-free socket cleanup: unconditional os.Remove ignoring os.ErrNotExist"]
observability_surfaces:
  - none
drill_down_paths:
  - [".gsd/milestones/M003/slices/S02/tasks/T01-SUMMARY.md", ".gsd/milestones/M003/slices/S02/tasks/T02-SUMMARY.md"]
duration: ""
verification_result: passed
completed_at: 2026-04-07T18:00:24.284Z
blocker_discovered: false
---

# S02: Live Shim Reconnect and Truthful Session Rebuild

**recoverSession now reconciles shim-reported status against DB session state before proceeding with recovery, and the ARI socket startup TOCTOU race is eliminated.**

## What Happened

This slice closed two gaps in agentd's recovery path: shim-vs-DB state reconciliation and a socket cleanup race condition.

**T01 — Shim-vs-DB State Reconciliation + Socket TOCTOU Fix**

Added a reconciliation switch block in `recoverSession` (pkg/agentd/recovery.go) between the existing `client.Status()` and `client.History()` calls. Three branches:

1. **Shim reports stopped** → close client, return error. This triggers the existing fail-closed path in `RecoverSessions` which marks the session stopped in DB. Previously, a shim reporting stopped would have been briefly treated as recovered before eventually failing.

2. **Shim running, DB says created** → call `m.sessions.Transition(ctx, session.ID, meta.SessionStateRunning)` to update DB to match shim truth. If `Transition()` returns `ErrInvalidTransition`, log at Warn and proceed (D042). This covers the scenario where agentd crashed between launching the shim and recording the created→running transition.

3. **Any other mismatch** (e.g. shim running, DB says paused:warm) → log at Warn with session_id, shim_status, db_state fields, but proceed with recovery. The shim is alive and reachable, so blocking recovery for a state mismatch would be worse than proceeding.

Also fixed the ARI socket startup TOCTOU race in `cmd/agentd/main.go`: replaced the `Stat→Remove→Serve` sequence with unconditional `os.Remove` that ignores `os.ErrNotExist` (D043).

**T02 — Three Unit Tests for Reconciliation Code Paths**

Added three new tests to `pkg/agentd/recovery_test.go` using the existing mock shim infrastructure:

- `TestRecoverSessions_ShimReportsStopped` — verifies fail-closed: session marked stopped in DB, not in process map, shim not subscribed.
- `TestRecoverSessions_ReconcileCreatedToRunning` — verifies DB transition from created→running when shim is ahead, session recovered into process map.
- `TestRecoverSessions_ShimMismatchLogsWarning` — verifies paused:warm vs running mismatch logs warning but recovery proceeds, session state unchanged in DB.

All existing tests continue to pass unchanged, including `TestRecoverSessions_LiveShim` (happy path) and `TestRecoverSessions_DeadShim` (fail-closed for unreachable shims).

## Verification

All verification commands pass:

1. `go build ./cmd/agentd/... ./pkg/agentd/...` — exit 0, zero errors
2. `go test ./pkg/agentd/... -count=1 -v` — exit 0, all tests pass including 3 new reconciliation tests
3. `go test ./pkg/ari/... -count=1` — exit 0, regression clean
4. `go vet ./pkg/agentd/... ./pkg/ari/... ./cmd/agentd/...` — exit 0, no issues
5. Targeted run of new tests (`-run ShimReportsStopped|ReconcileCreatedToRunning|ShimMismatchLogsWarning`) — all 3 PASS

## Requirements Advanced

- R044 — S02 delivers shim reconnect hardening (shim-vs-DB reconciliation, fail-closed for stopped shims) — one of the restart/reconnect hardening items tracked by R044

## Requirements Validated

None.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

None.

## Known Limitations

Reconciliation only covers the three most common mismatch scenarios (stopped, created→running, generic mismatch). Additional edge cases like shim reporting 'creating' when DB says 'running' are caught by the generic mismatch log-and-proceed path but don't have dedicated handling.

## Follow-ups

None.

## Files Created/Modified

- `pkg/agentd/recovery.go` — Added shim-vs-DB state reconciliation switch block between Status() and History() calls in recoverSession
- `cmd/agentd/main.go` — Replaced Stat→Remove TOCTOU with unconditional os.Remove for ARI socket cleanup
- `pkg/agentd/recovery_test.go` — Added 3 unit tests: ShimReportsStopped, ReconcileCreatedToRunning, ShimMismatchLogsWarning
