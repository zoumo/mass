---
estimated_steps: 10
estimated_files: 1
skills_used: []
---

# T02: Add reconciliation unit tests and verify full test suite

Add unit tests to `pkg/agentd/recovery_test.go` that exercise every reconciliation code path added in T01. Use the existing mock shim infrastructure (`newMockShimServer`, `setupRecoveryTest`, `createRecoveryTestSession`) — the mock shim server's `statusResult` field is directly settable to control what `runtime/status` returns.

New tests to add (follow the `TestRecoverSessions_*` naming pattern):

1. **TestRecoverSessions_ShimReportsStopped** — Create a running session in DB. Start mock shim with `statusResult.State.Status = spec.StatusStopped`. Run `RecoverSessions`. Assert: session is marked stopped in DB, session is NOT in the processes map, mock shim was NOT subscribed.

2. **TestRecoverSessions_ReconcileCreatedToRunning** — Create a `created` session in DB. Start mock shim with `statusResult.State.Status = spec.StatusRunning`. Run `RecoverSessions`. Assert: session state in DB is now `running` (transitioned from created), session IS in the processes map, mock shim WAS subscribed.

3. **TestRecoverSessions_ShimMismatchLogsWarning** — Create a `paused:warm` session in DB. Start mock shim with `statusResult.State.Status = spec.StatusRunning`. Run `RecoverSessions`. Assert: session IS in the processes map (recovery proceeded despite mismatch), session state in DB is still `paused:warm` (we don't attempt paused:warm→running since it's a valid recovery scenario — the shim may have resumed).

After adding tests, run the full verification suite:
- `go test ./pkg/agentd/... -count=1 -v` — all tests pass including new ones
- `go test ./pkg/ari/... -count=1` — regression check
- `go vet ./pkg/agentd/... ./pkg/ari/... ./cmd/agentd/...` — clean

Note: The existing `TestRecoverSessions_LiveShim` test already covers the happy path (shim running, DB running) and should continue to pass unchanged. The new tests cover the three gap scenarios identified in research.

## Inputs

- ``pkg/agentd/recovery.go` — the reconciliation logic added in T01`
- ``pkg/agentd/recovery_test.go` — existing test infrastructure (setupRecoveryTest, newMockShimServer, createRecoveryTestSession)`
- ``pkg/agentd/shim_client_test.go` — mock shim server implementation with configurable statusResult`
- ``pkg/spec/state_types.go` — spec.StatusStopped, spec.StatusRunning constants`
- ``pkg/meta/models.go` — meta.SessionStateCreated, meta.SessionStatePausedWarm constants`

## Expected Output

- ``pkg/agentd/recovery_test.go` — 3 new tests: TestRecoverSessions_ShimReportsStopped, TestRecoverSessions_ReconcileCreatedToRunning, TestRecoverSessions_ShimMismatchLogsWarning`

## Verification

go test ./pkg/agentd/... -count=1 -v && go test ./pkg/ari/... -count=1 && go vet ./pkg/agentd/... ./pkg/ari/... ./cmd/agentd/...
