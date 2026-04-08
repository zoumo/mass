# S02: Live Shim Reconnect and Truthful Session Rebuild — UAT

**Milestone:** M003
**Written:** 2026-04-07T18:00:24.285Z

## UAT: Live Shim Reconnect and Truthful Session Rebuild

### Preconditions
- Go toolchain available (`go build` succeeds)
- Repository at commit with M003/S02 changes applied
- `pkg/agentd/recovery_test.go` contains the three new test functions

---

### Test Case 1: Shim-reports-stopped triggers fail-closed path

**Scenario:** A shim process has exited (reports stopped) but the DB still shows the session as running.

**Steps:**
1. Run: `go test ./pkg/agentd/... -run TestRecoverSessions_ShimReportsStopped -v -count=1`
2. Verify test output shows `PASS`
3. Verify log output contains `"shim reports stopped"` for the session
4. Verify the test asserts: session is marked `stopped` in DB, session is NOT in the process map, mock shim was NOT subscribed

**Expected:** Test passes. The recovery path correctly fail-closes the session instead of briefly treating it as recovered.

---

### Test Case 2: DB state reconciled from created→running when shim is ahead

**Scenario:** agentd crashed between launching a shim and recording the created→running transition. The shim is running, but the DB says created.

**Steps:**
1. Run: `go test ./pkg/agentd/... -run TestRecoverSessions_ReconcileCreatedToRunning -v -count=1`
2. Verify test output shows `PASS`
3. Verify log output contains `"reconciled session state created→running"`
4. Verify the test asserts: DB state is now `running`, session IS in the process map, mock shim WAS subscribed

**Expected:** Test passes. The DB is updated to match shim truth, and recovery proceeds normally.

---

### Test Case 3: Shim/DB state mismatch logs warning but proceeds

**Scenario:** A session is paused:warm in DB but the shim reports running (valid scenario — shim may have resumed before agentd restarted).

**Steps:**
1. Run: `go test ./pkg/agentd/... -run TestRecoverSessions_ShimMismatchLogsWarning -v -count=1`
2. Verify test output shows `PASS`
3. Verify log output contains `"shim status differs from DB state (proceeding)"` with `shim_status=running db_state=paused:warm`
4. Verify the test asserts: session IS in the process map (recovery succeeded), DB state is still `paused:warm` (not mutated)

**Expected:** Test passes. Recovery proceeds despite the mismatch — the shim is alive.

---

### Test Case 4: Existing recovery tests unbroken (regression)

**Steps:**
1. Run: `go test ./pkg/agentd/... -run TestRecoverSessions -v -count=1`
2. Verify all `TestRecoverSessions_*` tests pass, including pre-existing `TestRecoverSessions_LiveShim` and `TestRecoverSessions_DeadShim`

**Expected:** All recovery tests pass. No regressions.

---

### Test Case 5: ARI package regression clean

**Steps:**
1. Run: `go test ./pkg/ari/... -count=1`
2. Verify exit code 0

**Expected:** All ARI tests pass unchanged.

---

### Test Case 6: Build and vet clean

**Steps:**
1. Run: `go build ./cmd/agentd/... ./pkg/agentd/...`
2. Run: `go vet ./pkg/agentd/... ./pkg/ari/... ./cmd/agentd/...`
3. Verify both exit code 0

**Expected:** No build errors, no vet warnings.

---

### Test Case 7: Socket TOCTOU fix (code review)

**Steps:**
1. Open `cmd/agentd/main.go` and locate the socket cleanup code (around line 98-99)
2. Verify the code is: `os.Remove(cfg.Socket)` with `!os.IsNotExist(err)` guard — NOT a `Stat→Remove` sequence
3. Confirm there is no `os.Stat` call before `os.Remove` for the socket path

**Expected:** Unconditional `os.Remove` with `os.ErrNotExist` tolerance. No TOCTOU window.

---

### Edge Cases

- **ErrInvalidTransition during reconciliation:** If `sessions.Transition()` rejects created→running (e.g. concurrent state change), recovery logs at Warn and continues. Verified by D042 decision — the shim being alive is more important than a transition edge case.
- **Shim in 'creating' state:** Falls through to the generic mismatch catch-all — logged at Warn, recovery proceeds.
