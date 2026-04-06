# S04: Workspace Lifecycle

**Goal:** WorkspaceManager orchestrates full workspace lifecycle: Prepare (source preparation + setup hooks) and Cleanup (teardown hooks + managed directory deletion), with reference counting to prevent premature cleanup when multiple sessions share a workspace.
**Demo:** After this: WorkspaceManager Prepare/Cleanup work; ref counting prevents premature cleanup

## Tasks
- [x] **T01: WorkspaceManager Prepare implementation with source routing, hook execution, and managed cleanup** — Define WorkspaceManager struct with SourceHandler routing map, Prepare workflow, WorkspaceError type, and reference counting.

## Steps

1. Define WorkspaceError type in errors.go with fields: Phase (string), WorkspaceID (string), SourceType (SourceType), Managed (bool), Message (string), Err (error)
2. Implement WorkspaceError.Error() method joining parts with ': ' separator following GitError/HookError pattern
3. Implement WorkspaceError.Unwrap() returning Err for errors.Is/errors.As compatibility
4. Define WorkspaceManager struct with fields: handlers map[SourceType]SourceHandler, hookExecutor *HookExecutor, refCount map[string]int (workspaceID → count), mutex sync.Mutex for refCount
5. Implement NewWorkspaceManager() constructor that initializes handlers map with GitHandler, EmptyDirHandler, LocalHandler, creates HookExecutor, initializes empty refCount map
6. Implement Prepare(ctx context.Context, spec WorkspaceSpec, targetDir string) (workspacePath string, err error) method:
   - Validate spec via ValidateWorkspaceSpec(spec), return WorkspaceError if invalid
   - Route to handler via handlers[spec.Source.Type], call handler.Prepare(ctx, spec.Source, targetDir)
   - On handler failure: return WorkspaceError with Phase="prepare-source"
   - Call hookExecutor.ExecuteHooks(ctx, spec.Hooks.Setup, workspacePath, "setup")
   - On hook failure: if managed workspace, os.RemoveAll(targetDir) to clean up partial workspace; return WorkspaceError with Phase="prepare-hooks"
   - Track workspace: Acquire(targetDir) to increment ref count, store managed flag (true for Git/EmptyDir, false for Local)
   - Return workspacePath
7. Implement Acquire(workspaceID string) method: mutex.Lock, refCount[workspaceID]++ (default 0 + 1 = 1), mutex.Unlock
8. Implement isManaged(spec Source) bool helper: returns true for SourceTypeGit/SourceTypeEmptyDir, false for SourceTypeLocal
9. Write unit tests in manager_test.go: TestWorkspaceErrorStructure, TestWorkspaceErrorErrorMethod, TestWorkspaceErrorUnwrap, TestNewWorkspaceManager, TestWorkspaceManagerPrepareGitSource, TestWorkspaceManagerPrepareEmptyDirSource, TestWorkspaceManagerPrepareLocalSource, TestWorkspaceManagerPrepareInvalidSpec, TestWorkspaceManagerPrepareHookFailureCleanup

## Must-Haves

- [ ] WorkspaceError type defined with Phase, WorkspaceID, SourceType, Managed, Message, Err fields
- [ ] WorkspaceError.Error() method produces formatted string matching GitError/HookError pattern
- [ ] WorkspaceError.Unwrap() method returns Err for error chaining
- [ ] WorkspaceManager struct defined with handlers map, hookExecutor, refCount map, mutex
- [ ] NewWorkspaceManager() constructor initializes handlers for all 3 source types (Git, EmptyDir, Local)
- [ ] Prepare method routes to correct SourceHandler based on spec.Source.Type
- [ ] Prepare method calls HookExecutor.ExecuteHooks for setup hooks after source preparation
- [ ] Setup hook failure triggers os.RemoveAll for managed workspaces (Git/EmptyDir) to clean up partial state
- [ ] Acquire method increments refCount[workspaceID] under mutex lock
- [ ] isManaged helper returns true for Git/EmptyDir, false for Local
  - Estimate: 1.5h
  - Files: pkg/workspace/manager.go, pkg/workspace/errors.go, pkg/workspace/manager_test.go
  - Verify: go test ./pkg/workspace/... -v -run "Manager.*Prepare|WorkspaceError" -count=1
- [x] **T02: Implemented WorkspaceManager Cleanup workflow with Release method for reference counting, Cleanup method with teardown hooks and managed directory deletion, plus comprehensive integration tests proving lifecycle round-trips and reference counting semantics.** — Implement Cleanup workflow, Release method for reference counting, and integration tests.

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
  - Estimate: 1h
  - Files: pkg/workspace/manager.go, pkg/workspace/manager_test.go
  - Verify: go test ./pkg/workspace/... -v -run "Manager" -count=1
