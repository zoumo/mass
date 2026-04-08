---
id: S01
parent: M003
milestone: M003
provides:
  - ["RecoveryPhase type and atomic tracking on ProcessManager (SetRecoveryPhase, GetRecoveryPhase, IsRecovering)", "RecoveryInfo per-session metadata and SetSessionRecoveryInfo method", "recoveryGuard ARI handler helper with CodeRecoveryBlocked (-32001)", "SessionRecoveryInfo in ARI SessionStatusResult", "RecoverSessions phase lifecycle management (recovering→complete on all exit paths)"]
requires:
  []
affects:
  - ["S02 (Live Shim Reconnect) — builds on the recovery phase lifecycle wired into RecoverSessions", "S03 (Atomic Event Resume) — will use RecoveryInfo to track per-session event resume state", "S04 (Workspace Ref Truth) — recovery metadata informs cleanup safety decisions"]
key_files:
  - ["pkg/agentd/recovery_posture.go", "pkg/agentd/process.go", "pkg/agentd/recovery.go", "pkg/agentd/recovery_posture_test.go", "pkg/ari/types.go", "pkg/ari/server.go", "pkg/ari/server_test.go"]
key_decisions:
  - ["D039: Two-level recovery model — atomic RecoveryPhase for daemon-wide gating, per-session RecoveryInfo for detailed inspection", "D040: Block only session/prompt and session/cancel during recovery; allow status/list/stop/attach/detach", "D041: RecoverSessions always transitions to Complete on all exit paths to avoid permanent recovery-blocked state"]
patterns_established:
  - ["Atomic field for cross-goroutine state gating (recoveryPhase atomic.Int32)", "recoveryGuard helper pattern: check phase, return typed JSON-RPC error -32001 if recovering", "Two-level recovery meta daemon-wide phase for gating + per-session RecoveryInfo for inspection", "Guard placement: after param unmarshalling, before side-effects", "Safety exemption: session/stop always allowed regardless of recovery phase"]
observability_surfaces:
  - none
drill_down_paths:
  []
duration: ""
verification_result: passed
completed_at: 2026-04-07T17:41:16.835Z
blocker_discovered: false
---

# S01: Fail-Closed Recovery Posture and Discovery Contract

**Established an explicit fail-closed recovery posture that blocks operational ARI actions (prompt, cancel) while the daemon is recovering sessions, and surfaces per-session recovery metadata through session/status.**

## What Happened

This slice introduced the recovery posture mechanism that makes the daemon's startup recovery window safe and observable.

**T01 — Type Infrastructure.** Created `pkg/agentd/recovery_posture.go` with the `RecoveryPhase` type (idle/recovering/complete) and per-session `RecoveryInfo` struct (Recovered bool, RecoveredAt timestamp, Outcome string). Added `recoveryPhase atomic.Int32` to `ProcessManager` with `SetRecoveryPhase()`, `GetRecoveryPhase()`, `IsRecovering()`, and `SetSessionRecoveryInfo()` methods. On the ARI side, added `SessionRecoveryInfo` struct and `Recovery` field to `SessionStatusResult` in `pkg/ari/types.go`, maintaining proper package dependency direction (ari types don't import agentd).

**T02 — ARI Guards.** Added `CodeRecoveryBlocked` (-32001) JSON-RPC error code and a `recoveryGuard` helper on `connHandler` in `pkg/ari/server.go`. The guard is called at the top of `handleSessionPrompt` and `handleSessionCancel` — after param unmarshalling but before side-effects, so invalid params still get proper errors. `session/stop` is intentionally left unguarded (operators must always be able to stop a session). All read-only methods (`session/status`, `session/list`, `session/attach`, `session/detach`) remain unguarded. Modified `handleSessionStatus` to populate `RecoveryInfo` from the session's `ShimProcess` when recovery metadata exists.

**T03 — Wiring and Tests.** Modified `RecoverSessions` in `pkg/agentd/recovery.go` to manage the recovery phase lifecycle: sets `RecoveryPhaseRecovering` at entry and `RecoveryPhaseComplete` at every exit path (including systemic failures). For each successfully recovered session, stores `RecoveryInfo{Recovered: true, RecoveredAt: time.Now(), Outcome: "recovered"}`. Created 12 tests total: 6 unit tests in `recovery_posture_test.go` (default idle phase, transitions, IsRecovering semantics, phase lifecycle with live/dead/empty scenarios) and 6 ARI guard integration tests in `server_test.go` (prompt/cancel blocked during recovery, status/list/stop allowed during recovery, prompt unblocked after recovery completes).

All tests pass with zero regressions across both `pkg/agentd` and `pkg/ari`.

## Verification

**Build verification:** `go build ./pkg/agentd/... && go build ./pkg/ari/...` — exit 0.
**Vet verification:** `go vet ./pkg/agentd/... && go vet ./pkg/ari/...` — exit 0.
**Unit tests:** `go test ./pkg/agentd/... -count=1` — all pass (6 new recovery posture tests + existing tests).
**Integration tests:** `go test ./pkg/ari/... -count=1` — all pass (6 new ARI guard tests + existing tests).
**Full suite:** `go test ./pkg/agentd/... ./pkg/ari/... -count=1 -v` — 12 new tests pass, zero regressions across both packages.

## Requirements Advanced

None.

## Requirements Validated

None.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

TestARIRecoveryInfo_InSessionStatus from the plan was covered at the agentd level by TestRecoverSessions_PhaseTransitions_WithLiveShim instead. Three extra ARI guard tests were added beyond plan for completeness (cancel blocked, list allowed, stop allowed).

## Known Limitations

Recovery phase is purely in-memory — if the daemon crashes during recovery, the phase resets to idle on next startup (which is correct behavior since RecoverSessions runs again). There is no persistent recovery phase tracking, which is appropriate since recovery is a transient startup operation.

## Follow-ups

None.

## Files Created/Modified

- `pkg/agentd/recovery_posture.go` — New file: RecoveryPhase type, RecoveryOutcome type, RecoveryInfo struct
- `pkg/agentd/process.go` — Added recoveryPhase atomic.Int32 field and SetRecoveryPhase/GetRecoveryPhase/IsRecovering/SetSessionRecoveryInfo methods
- `pkg/agentd/recovery.go` — Wired recovery phase transitions (recovering→complete) and per-session RecoveryInfo into RecoverSessions
- `pkg/agentd/recovery_posture_test.go` — New file: 6 unit tests for phase tracking, IsRecovering semantics, RecoverSessions phase lifecycle
- `pkg/ari/types.go` — Added CodeRecoveryBlocked constant (-32001), SessionRecoveryInfo struct, Recovery field on SessionStatusResult
- `pkg/ari/server.go` — Added recoveryGuard helper, wired into handleSessionPrompt and handleSessionCancel, wired RecoveryInfo into handleSessionStatus
- `pkg/ari/server_test.go` — Added 6 ARI guard integration tests for recovery blocking behavior
