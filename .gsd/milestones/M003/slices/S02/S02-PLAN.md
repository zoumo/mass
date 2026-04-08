# S02: Live Shim Reconnect and Truthful Session Rebuild

**Goal:** recoverSession reconciles shim-reported status against DB session state — a shim that reports stopped is fail-closed instead of briefly appearing recovered, and a shim that reports running when the DB says created gets its DB state updated. The ARI socket startup TOCTOU race is eliminated.
**Demo:** After this: TBD

## Tasks
- [x] **T01: Added shim-vs-DB state reconciliation to recoverSession and replaced Stat→Remove TOCTOU with unconditional os.Remove for socket cleanup** — Add state reconciliation logic to `recoverSession` in `pkg/agentd/recovery.go` that compares the shim's `runtime/status` response against the session's DB state. After the existing `client.Status(ctx)` call and before `client.History(ctx, ...)`, insert a reconciliation check:

1. If `status.State.Status == spec.StatusStopped` → close client, return error ("shim reports stopped"). This triggers the existing fail-closed path in `RecoverSessions` which marks the session stopped.
2. If `status.State.Status == spec.StatusRunning` and `session.State == meta.SessionStateCreated` → call `m.sessions.Transition(ctx, session.ID, meta.SessionStateRunning)` to update DB to match shim truth. Log at Info level.
3. For any other mismatch between shim status and DB state → log at Warn level with session_id, shim_status, db_state fields, but proceed with recovery (the shim is alive and reachable).

Also fix the ARI socket startup TOCTOU race in `cmd/agentd/main.go` lines 98-103: replace the `Stat → Remove → Serve` sequence with an unconditional `os.Remove` that ignores `os.ErrNotExist`.

Constraints:
- Do not change the `RecoverSessions` outer loop — only modify `recoverSession`.
- The reconciliation must happen AFTER `client.Status()` succeeds and BEFORE `client.History()` is called.
- Use `m.sessions.Transition()` for state changes (it enforces the valid transitions table).
- If `Transition()` returns `ErrInvalidTransition`, log at Warn and proceed — don't fail recovery for a transition error.
- The shim `spec.Status` values are: `creating`, `created`, `running`, `stopped`. The meta `SessionState` values are: `created`, `running`, `paused:warm`, `paused:cold`, `stopped`.
  - Estimate: 30m
  - Files: pkg/agentd/recovery.go, cmd/agentd/main.go
  - Verify: go build ./cmd/agentd/... ./pkg/agentd/... && go vet ./cmd/agentd/... ./pkg/agentd/...
- [x] **T02: Added 3 unit tests covering every shim-vs-DB reconciliation code path: shim-reports-stopped fail-close, created→running DB reconciliation, and paused:warm mismatch log-and-proceed** — Add unit tests to `pkg/agentd/recovery_test.go` that exercise every reconciliation code path added in T01. Use the existing mock shim infrastructure (`newMockShimServer`, `setupRecoveryTest`, `createRecoveryTestSession`) — the mock shim server's `statusResult` field is directly settable to control what `runtime/status` returns.

New tests to add (follow the `TestRecoverSessions_*` naming pattern):

1. **TestRecoverSessions_ShimReportsStopped** — Create a running session in DB. Start mock shim with `statusResult.State.Status = spec.StatusStopped`. Run `RecoverSessions`. Assert: session is marked stopped in DB, session is NOT in the processes map, mock shim was NOT subscribed.

2. **TestRecoverSessions_ReconcileCreatedToRunning** — Create a `created` session in DB. Start mock shim with `statusResult.State.Status = spec.StatusRunning`. Run `RecoverSessions`. Assert: session state in DB is now `running` (transitioned from created), session IS in the processes map, mock shim WAS subscribed.

3. **TestRecoverSessions_ShimMismatchLogsWarning** — Create a `paused:warm` session in DB. Start mock shim with `statusResult.State.Status = spec.StatusRunning`. Run `RecoverSessions`. Assert: session IS in the processes map (recovery proceeded despite mismatch), session state in DB is still `paused:warm` (we don't attempt paused:warm→running since it's a valid recovery scenario — the shim may have resumed).

After adding tests, run the full verification suite:
- `go test ./pkg/agentd/... -count=1 -v` — all tests pass including new ones
- `go test ./pkg/ari/... -count=1` — regression check
- `go vet ./pkg/agentd/... ./pkg/ari/... ./cmd/agentd/...` — clean

Note: The existing `TestRecoverSessions_LiveShim` test already covers the happy path (shim running, DB running) and should continue to pass unchanged. The new tests cover the three gap scenarios identified in research.
  - Estimate: 30m
  - Files: pkg/agentd/recovery_test.go
  - Verify: go test ./pkg/agentd/... -count=1 -v && go test ./pkg/ari/... -count=1 && go vet ./pkg/agentd/... ./pkg/ari/... ./cmd/agentd/...
