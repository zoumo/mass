---
estimated_steps: 18
estimated_files: 3
skills_used: []
---

# T03: Integrate recovery phase lifecycle into startup and write comprehensive tests

Wire the recovery phase transitions into the daemon startup flow and RecoverSessions, track per-session recovery outcomes, and write tests that prove the posture works end-to-end.

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

## Inputs

- `pkg/agentd/recovery.go`
- `pkg/agentd/recovery_posture.go`
- `pkg/agentd/process.go`
- `pkg/ari/server.go`
- `pkg/ari/types.go`
- `pkg/agentd/shim_client_test.go`
- `pkg/agentd/recovery_test.go`

## Expected Output

- `pkg/agentd/recovery.go`
- `pkg/agentd/recovery_posture_test.go`
- `pkg/ari/server_test.go`

## Verification

go test ./pkg/agentd/... ./pkg/ari/... -count=1 -v 2>&1 | tail -30
