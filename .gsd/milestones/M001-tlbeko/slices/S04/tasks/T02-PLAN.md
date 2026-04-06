---
estimated_steps: 32
estimated_files: 2
skills_used: []
---

# T02: WorkspaceManager Cleanup implementation

Implement Cleanup workflow, Release method for reference counting, and integration tests.

## Steps

1. Implement Release(workspaceID string) method: mutex.Lock, if refCount[workspaceID] > 0 then refCount[workspaceID]--, count = refCount[workspaceID], mutex.Unlock, return count
2. Implement Cleanup(ctx context.Context, workspaceID string, spec WorkspaceSpec) error method:
   - Release(workspaceID) to decrement ref count
   - If count > 0: return nil (workspace still in use by other sessions)
   - If count == 0: proceed with cleanup
   - Call hookExecutor.ExecuteHooks(ctx, spec.Hooks.Teardown, workspaceID, "teardown")
   - On teardown hook failure: log error but continue (best-effort cleanup)
   - If isManaged(spec.Source): os.RemoveAll(workspaceID) to delete managed directory
   - Return nil (or WorkspaceError with Phase="cleanup-delete" if os.RemoveAll fails)
3. Implement cleanupOnHookFailure(ctx context.Context, targetDir string) helper for Prepare hook failure cleanup: if managed, os.RemoveAll(targetDir)
4. Write integration tests in manager_test.go:
   - TestWorkspaceManagerLifecycleGit: Prepare → workspace exists → Cleanup → workspace deleted
   - TestWorkspaceManagerLifecycleEmptyDir: Prepare → workspace exists → Cleanup → workspace deleted
   - TestWorkspaceManagerLifecycleLocal: Prepare → workspace exists → Cleanup → workspace NOT deleted
   - TestWorkspaceManagerReferenceCounting: Acquire twice → Release once → count=1 → Release again → count=0 → Cleanup triggered
   - TestWorkspaceManagerCleanupHookFailure: Prepare → setup hook succeeds → Cleanup → teardown hook fails → workspace still deleted
   - TestWorkspaceManagerPrepareHookFailureCleanupManaged: Prepare → setup hook fails → workspace directory cleaned up (not left behind)
   - TestWorkspaceManagerMultipleSessions: Prepare → Acquire twice (2 sessions) → Release once → count=1 → workspace NOT deleted → Release again → Cleanup triggered
5. Run full test suite: go test ./pkg/workspace/... -v -count=1

## Must-Haves

- [ ] Release method decrements refCount[workspaceID] under mutex lock, returns count after decrement
- [ ] Cleanup method checks ref count, only proceeds if count == 0
- [ ] Cleanup method calls HookExecutor.ExecuteHooks for teardown hooks before directory deletion
- [ ] Teardown hook failure does not prevent managed directory deletion (best-effort cleanup)
- [ ] Cleanup method skips os.RemoveAll for Local workspaces (isManaged returns false)
- [ ] Integration test proves Prepare → Cleanup round-trip for Git source
- [ ] Integration test proves Prepare → Cleanup round-trip for EmptyDir source
- [ ] Integration test proves Local workspace NOT deleted on Cleanup
- [ ] Reference counting test proves Cleanup only triggered when count reaches zero
- [ ] Setup hook failure test proves managed workspace cleaned up (no partial state left)

## Inputs

- `pkg/workspace/manager.go`
- `pkg/workspace/errors.go`
- `pkg/workspace/hook.go`
- `pkg/workspace/git.go`
- `pkg/workspace/emptydir.go`
- `pkg/workspace/local.go`

## Expected Output

- `pkg/workspace/manager.go`
- `pkg/workspace/manager_test.go`

## Verification

go test ./pkg/workspace/... -v -run "Manager" -count=1

## Observability Impact

WorkspaceError Phase values extended: "cleanup-hooks" for teardown hook failure, "cleanup-delete" for directory deletion failure. Teardown hook failure logged but cleanup continues. Best-effort cleanup semantics observable via test assertions.
