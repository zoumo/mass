---
estimated_steps: 11
estimated_files: 2
skills_used: []
---

# T01: Implement shim state reconciliation and fix socket startup race

Add state reconciliation logic to `recoverSession` in `pkg/agentd/recovery.go` that compares the shim's `runtime/status` response against the session's DB state. After the existing `client.Status(ctx)` call and before `client.History(ctx, ...)`, insert a reconciliation check:

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

## Inputs

- ``pkg/agentd/recovery.go` — existing recoverSession pipeline to modify`
- ``cmd/agentd/main.go` — socket cleanup code at lines 98-103`
- ``pkg/agentd/session.go` — SessionManager.Transition and validTransitions table`
- ``pkg/spec/state_types.go` — spec.StatusStopped, spec.StatusRunning constants`
- ``pkg/meta/models.go` — meta.SessionState constants`

## Expected Output

- ``pkg/agentd/recovery.go` — recoverSession now reconciles shim status against DB state before proceeding to history/subscribe`
- ``cmd/agentd/main.go` — socket cleanup uses unconditional os.Remove instead of Stat→Remove`

## Verification

go build ./cmd/agentd/... ./pkg/agentd/... && go vet ./cmd/agentd/... ./pkg/agentd/...
