---
estimated_steps: 32
estimated_files: 2
skills_used: []
---

# T03: Gate workspace/cleanup on DB ref_count and recovery phase, add safety tests

## Description

Currently `handleWorkspaceCleanup` gates on volatile `registry.RefCount` (empty after restart). This task changes it to check DB `ref_count` (persisted truth) and adds a recovery-phase guard. The `WorkspaceManager.Cleanup` internal refcount gate is bypassed by checking DB first in the handler.

## Steps

1. In `pkg/ari/server.go` `handleWorkspaceCleanup`, add recovery guard at the top (like `handleSessionPrompt` uses `h.recoveryGuard`). This blocks cleanup during active recovery phase.

2. In `handleWorkspaceCleanup`, after getting workspace from registry, replace the `if meta.RefCount > 0` check with a DB-based check: call `h.srv.store.GetWorkspace(ctx, p.WorkspaceId)` and check `dbWorkspace.RefCount > 0`. If the store is nil (shouldn't happen in practice), fall back to the registry check. If DB says ref_count > 0, return the existing error message. This makes cleanup safe after restart because DB ref_count survives.

3. Also handle the case where the workspace exists in DB but not in registry (after restart, before rebuild, or if rebuild failed). If `registry.Get` returns nil but DB has the workspace, load spec from DB and proceed with cleanup (using `workspace.WorkspaceManager.Cleanup` with the spec reconstructed from DB source). However, for simplicity and given that T02 rebuilds the registry, keeping the registry-not-found check is acceptable — just make sure the DB ref_count check happens when the workspace IS in the registry.

4. Add test `TestARIWorkspaceCleanupBlockedByDBRefCount` in `pkg/ari/server_test.go`: prepare workspace, create session (which now acquires DB ref via T01), then call `workspace/cleanup` — should fail with "active references". Then stop + remove the session, call cleanup again — should succeed.

5. Add test `TestARIWorkspaceCleanupBlockedDuringRecovery` in `pkg/ari/server_test.go`: set the ProcessManager to recovering state, then call `workspace/cleanup` — should return `CodeRecoveryBlocked`. The existing `recoveryGuard` pattern returns this code. To set recovering state, call `processes.SetRecoveryPhase(agentd.RecoveryPhaseRecovering)` (this API exists from S01).

6. Run the full test suite to confirm no regressions.

## Negative Tests

- **Cleanup with active refs**: `workspace/cleanup` returns error when DB ref_count > 0
- **Cleanup during recovery**: `workspace/cleanup` returns CodeRecoveryBlocked
- **Cleanup after ref release**: `workspace/cleanup` succeeds when ref_count == 0 and recovery complete

## Must-Haves

- [ ] `handleWorkspaceCleanup` checks DB `ref_count` instead of volatile registry RefCount
- [ ] `handleWorkspaceCleanup` is blocked during recovery phase via recoveryGuard
- [ ] Test proves cleanup blocked with active DB refs
- [ ] Test proves cleanup blocked during recovery
- [ ] All existing workspace cleanup tests continue to pass

## Verification

- `go test ./pkg/ari/... -count=1 -run 'TestARIWorkspaceCleanupBlockedByDBRefCount|TestARIWorkspaceCleanupBlockedDuringRecovery' -v` — both PASS
- `go test ./pkg/ari/... -count=1` — all tests pass (including existing cleanup tests)
- `go test ./pkg/meta/... -count=1` — regression clean
- `go vet ./pkg/ari/... ./pkg/meta/...` — clean
- `go build ./cmd/agentd/... ./cmd/agent-shim/... ./pkg/...` — full build passes

## Inputs

- `pkg/ari/server.go` — handleWorkspaceCleanup to modify, T01's AcquireWorkspace wiring
- `pkg/ari/server_test.go` — T01's new test patterns for session+workspace lifecycle
- `pkg/agentd/process.go` — SetRecoveryPhase API from S01

## Expected Output

- `pkg/ari/server.go` — modified: handleWorkspaceCleanup uses DB ref_count + recovery guard
- `pkg/ari/server_test.go` — modified: 2 new safety tests added

## Inputs

- ``pkg/ari/server.go` — handleWorkspaceCleanup to modify (T01 already wired AcquireWorkspace)`
- ``pkg/ari/server_test.go` — T01's new test patterns for session+workspace lifecycle`
- ``pkg/agentd/process.go` — SetRecoveryPhase and IsRecovering from S01`

## Expected Output

- ``pkg/ari/server.go` — handleWorkspaceCleanup gates on DB ref_count + recovery guard`
- ``pkg/ari/server_test.go` — 2 new tests: TestARIWorkspaceCleanupBlockedByDBRefCount, TestARIWorkspaceCleanupBlockedDuringRecovery`

## Verification

go test ./pkg/ari/... -count=1 -run 'TestARIWorkspaceCleanupBlockedByDBRefCount|TestARIWorkspaceCleanupBlockedDuringRecovery' -v && go test ./pkg/ari/... -count=1 && go test ./pkg/meta/... -count=1 && go vet ./pkg/ari/... ./pkg/meta/... && go build ./cmd/agentd/... ./cmd/agent-shim/... ./pkg/...
