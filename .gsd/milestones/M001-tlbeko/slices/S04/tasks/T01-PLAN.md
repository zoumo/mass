---
estimated_steps: 29
estimated_files: 3
skills_used: []
---

# T01: WorkspaceManager Prepare implementation

Define WorkspaceManager struct with SourceHandler routing map, Prepare workflow, WorkspaceError type, and reference counting.

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

## Inputs

- `pkg/workspace/handler.go`
- `pkg/workspace/spec.go`
- `pkg/workspace/git.go`
- `pkg/workspace/emptydir.go`
- `pkg/workspace/local.go`
- `pkg/workspace/hook.go`

## Expected Output

- `pkg/workspace/manager.go`
- `pkg/workspace/errors.go`
- `pkg/workspace/manager_test.go`

## Verification

go test ./pkg/workspace/... -v -run "Manager.*Prepare|WorkspaceError" -count=1

## Observability Impact

WorkspaceError type with Phase field (prepare-source, prepare-hooks) for targeted diagnostics. Error message includes WorkspaceID (targetDir), SourceType, Managed flag. Setup hook failure cleanup logged via os.RemoveAll call.
