# S01: Fail-Closed Recovery Posture and Discovery Contract — UAT

**Milestone:** M003
**Written:** 2026-04-07T17:41:16.836Z

## Preconditions
- Go toolchain installed (1.22+)
- Repository checked out at post-S01 state
- All binaries buildable: `go build ./...`

---

## UAT-1: Recovery phase starts idle on fresh ProcessManager

**Steps:**
1. Run `go test ./pkg/agentd/... -run TestRecoveryPhase_DefaultIsIdle -v -count=1`

**Expected:**
- Test passes
- A new ProcessManager reports `GetRecoveryPhase() == RecoveryPhaseIdle`
- `IsRecovering()` returns false

---

## UAT-2: Recovery phase transitions work correctly

**Steps:**
1. Run `go test ./pkg/agentd/... -run TestRecoveryPhase_TransitionsWork -v -count=1`

**Expected:**
- Test passes
- Phase transitions idle → recovering → complete succeed
- `GetRecoveryPhase()` returns correct value after each transition
- `IsRecovering()` is true only during RecoveryPhaseRecovering

---

## UAT-3: session/prompt blocked during recovery with JSON-RPC error -32001

**Steps:**
1. Run `go test ./pkg/ari/... -run TestARIRecoveryGuard_BlocksPromptDuringRecovery -v -count=1`

**Expected:**
- Test passes
- When ProcessManager.IsRecovering() is true, session/prompt returns JSON-RPC error
- Error code is -32001 (CodeRecoveryBlocked)
- Error message indicates daemon is recovering sessions

---

## UAT-4: session/cancel blocked during recovery with JSON-RPC error -32001

**Steps:**
1. Run `go test ./pkg/ari/... -run TestARIRecoveryGuard_BlocksCancelDuringRecovery -v -count=1`

**Expected:**
- Test passes
- When ProcessManager.IsRecovering() is true, session/cancel returns JSON-RPC error code -32001

---

## UAT-5: session/status remains accessible during recovery

**Steps:**
1. Run `go test ./pkg/ari/... -run TestARIRecoveryGuard_AllowsStatusDuringRecovery -v -count=1`

**Expected:**
- Test passes
- session/status responds normally even while recovery phase is active
- Operators can inspect session state during recovery window

---

## UAT-6: session/list remains accessible during recovery

**Steps:**
1. Run `go test ./pkg/ari/... -run TestARIRecoveryGuard_AllowsListDuringRecovery -v -count=1`

**Expected:**
- Test passes
- session/list responds normally even while recovery phase is active

---

## UAT-7: session/stop remains accessible during recovery (safety)

**Steps:**
1. Run `go test ./pkg/ari/... -run TestARIRecoveryGuard_AllowsStopDuringRecovery -v -count=1`

**Expected:**
- Test passes
- session/stop is never blocked — operators can always stop a session, even during recovery

---

## UAT-8: session/prompt unblocked after recovery completes

**Steps:**
1. Run `go test ./pkg/ari/... -run TestARIRecoveryGuard_AllowsPromptAfterRecovery -v -count=1`

**Expected:**
- Test passes
- After recovery phase transitions from recovering → complete, session/prompt is no longer blocked by the guard
- Any further errors are normal operational errors, not recovery-blocked errors

---

## UAT-9: RecoverSessions sets per-session RecoveryInfo on recovered sessions

**Steps:**
1. Run `go test ./pkg/agentd/... -run TestRecoverSessions_PhaseTransitions_WithLiveShim -v -count=1`

**Expected:**
- Test passes
- After RecoverSessions completes, recovered ShimProcess has RecoveryInfo populated:
  - `Recovered == true`
  - `RecoveredAt` is non-nil and recent
  - `Outcome == "recovered"`

---

## UAT-10: RecoverSessions phase lifecycle — empty DB case

**Steps:**
1. Run `go test ./pkg/agentd/... -run TestRecoverSessions_PhaseTransitions_EmptyDB -v -count=1`

**Expected:**
- Test passes
- Phase transitions idle → recovering → complete even when no sessions exist
- Daemon does not get stuck in recovering phase on clean startup

---

## UAT-11: Full test suite regression check

**Steps:**
1. Run `go test ./pkg/agentd/... ./pkg/ari/... -count=1`

**Expected:**
- All tests pass
- Zero regressions from the recovery posture changes
- Guards are no-ops when phase is idle, so pre-existing tests are unaffected

---

## Edge Cases

### EC-1: Recovery guard placement relative to param validation
The recovery guard fires after JSON param unmarshalling but before side-effects. This means:
- A request with invalid params during recovery still gets a proper InvalidParams error (guard not reached)
- A request with valid params during recovery gets the -32001 recovery-blocked error

### EC-2: Systemic failure during RecoverSessions
If ListSessions fails (e.g., metadata DB unreachable), RecoverSessions still transitions phase to Complete. Verified by `TestRecoverSessions_PhaseTransitions_EmptyDB` and the design decision D041.

### EC-3: Concurrent phase reads from multiple ARI handlers
RecoveryPhase uses `atomic.Int32`, not the process map RWMutex. Multiple handlers can check `IsRecovering()` concurrently without lock contention.
