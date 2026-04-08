# S01: Fail-Closed Recovery Posture and Discovery Contract

**Goal:** Establish an explicit fail-closed recovery posture that blocks operational actions (prompt, cancel) while the daemon is recovering sessions, and surfaces recovery state through session/status so operators know whether a session is healthy, recovered, or blocked.
**Demo:** After this: TBD

## Tasks
- [x] **T01: Added RecoveryPhase atomic tracking and RecoveryInfo per-session metadata to ProcessManager, plus SessionRecoveryInfo on ARI SessionStatusResult** — Add the type infrastructure for expressing recovery state at both the daemon level and per-session level. This task introduces:

1. `RecoveryPhase` type (idle/recovering/complete) as an atomic field on `ProcessManager`
2. `RecoveryInfo` struct with `Recovered bool`, `RecoveredAt *time.Time`, `Outcome string` (recovered/failed/pending)
3. Per-session recovery metadata stored on `ShimProcess`
4. Methods on `ProcessManager`: `SetRecoveryPhase()`, `RecoveryPhase()`, `IsRecovering()`, `SetSessionRecoveryInfo()`
5. `RecoveryInfo` field added to `SessionStatusResult` in ARI types

Steps:
1. Open `pkg/agentd/process.go`, add `recoveryPhase` atomic field to `ProcessManager` struct and `RecoveryPhase` type constants
2. Add `IsRecovering()`, `SetRecoveryPhase()`, `GetRecoveryPhase()` methods
3. Add `RecoveryInfo` field to `ShimProcess` struct
4. Create new file `pkg/agentd/recovery_posture.go` for the `RecoveryPhase` type, `RecoveryInfo` struct, and constants
5. Open `pkg/ari/types.go`, add `RecoveryInfo` field to `SessionStatusResult`
6. Ensure all new types have proper JSON tags and documentation
  - Estimate: 45m
  - Files: pkg/agentd/recovery_posture.go, pkg/agentd/process.go, pkg/ari/types.go
  - Verify: go build ./pkg/agentd/... && go build ./pkg/ari/... && go vet ./pkg/agentd/... && go vet ./pkg/ari/...
- [x] **T02: Added recoveryGuard to block session/prompt and session/cancel during daemon recovery, and wired per-session RecoveryInfo into session/status response** — Implement the actual fail-closed behavior: when the daemon is in recovery phase, operational methods (session/prompt, session/cancel) are refused with a clear JSON-RPC error, while read-only methods (session/status, session/list, session/attach, session/detach) continue working. Also wire RecoveryInfo into the session/status response.

Steps:
1. Open `pkg/ari/server.go`, add a `recoveryGuard` helper that checks `processes.IsRecovering()` and returns a JSON-RPC error (code -32001, message 'daemon is recovering sessions, operational actions are blocked') if true
2. Call `recoveryGuard` at the top of `handleSessionPrompt` and `handleSessionCancel` — return early if it fires
3. Modify `handleSessionStatus` to include `RecoveryInfo` from the `ShimProcess` in the response when the session has recovery metadata
4. Do NOT guard `handleSessionStop` — stopping must always work for safety (an operator must be able to stop a session even during recovery)
5. Do NOT guard `handleSessionStatus`, `handleSessionList`, `handleSessionAttach`, `handleSessionDetach` — these are read-only inspection methods
6. Define a custom JSON-RPC error code constant for recovery-blocked errors
7. Verify existing test suite still passes with the guards in place (guards are no-ops when phase is idle)
  - Estimate: 45m
  - Files: pkg/ari/server.go, pkg/ari/types.go
  - Verify: go test ./pkg/ari/... -count=1 -v 2>&1 | tail -20
- [x] **T03: Wired RecoveryPhase transitions and per-session RecoveryInfo into RecoverSessions, plus 12 comprehensive tests for phase tracking, ARI guard blocking, and recovery metadata** — Wire the recovery phase transitions into the daemon startup flow and RecoverSessions, track per-session recovery outcomes, and write tests that prove the posture works end-to-end.

Steps:
1. Open `pkg/agentd/recovery.go`, modify `RecoverSessions` to:
   - Call `m.SetRecoveryPhase(RecoveryPhaseRecovering)` at the start
   - Store `RecoveryInfo` on each recovered `ShimProcess` (outcome=recovered, recoveredAt=time.Now())
   - Call `m.SetRecoveryPhase(RecoveryPhaseComplete)` at the end
2. Open `cmd/agentd/main.go` — no changes needed since RecoverSessions now manages its own phase transitions
3. Write unit tests in `pkg/agentd/recovery_posture_test.go`:
   - `TestRecoveryPhase_DefaultIsIdle` — new ProcessManager starts in idle phase
   - `TestRecoveryPhase_TransitionsWork` — set/get phase transitions
   - `TestIsRecovering_TrueOnlyDuringRecovery` — IsRecovering returns true only when phase == recovering
4. Write guard test in `pkg/ari/server_test.go`:
   - `TestARIRecoveryGuard_BlocksPromptDuringRecovery` — set recovery phase, call session/prompt, verify JSON-RPC error -32001
   - `TestARIRecoveryGuard_AllowsStatusDuringRecovery` — set recovery phase, call session/status, verify success
   - `TestARIRecoveryGuard_AllowsPromptAfterRecovery` — set phase to idle, call session/prompt, verify no guard error
5. Verify recovery info appears in session/status for recovered sessions:
   - `TestARIRecoveryInfo_InSessionStatus` — recover a session via RecoverSessions with a mock shim, call session/status, verify RecoveryInfo fields are populated
6. Run full test suite to confirm no regressions
  - Estimate: 1h
  - Files: pkg/agentd/recovery.go, pkg/agentd/recovery_posture_test.go, pkg/ari/server_test.go
  - Verify: go test ./pkg/agentd/... ./pkg/ari/... -count=1 -v 2>&1 | tail -30
