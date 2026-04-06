---
id: T01
parent: S04
milestone: M001-tlbeko
provides: []
requires: []
affects: []
key_files: ["pkg/workspace/manager.go", "pkg/workspace/errors.go", "pkg/workspace/manager_test.go"]
key_decisions: ["WorkspaceError follows GitError/HookError pattern with Phase field for targeted diagnostics", "Managed flag (true for Git/EmptyDir, false for Local) determines cleanup behavior on hook failure", "LocalHandler returns source.Local.Path directly (unmanaged), GitHandler/EmptyDirHandler return targetDir (managed)"]
patterns_established: []
drill_down_paths: []
observability_surfaces: []
duration: ""
verification_result: "All 9 required tests pass: TestWorkspaceErrorStructure, TestWorkspaceErrorErrorMethod, TestWorkspaceErrorUnwrap, TestNewWorkspaceManager, TestWorkspaceManagerPrepareGitSource, TestWorkspaceManagerPrepareEmptyDirSource, TestWorkspaceManagerPrepareLocalSource, TestWorkspaceManagerPrepareInvalidSpec, TestWorkspaceManagerPrepareHookFailureCleanup. Verification command executed successfully: go test ./pkg/workspace/... -v -run "Manager.*Prepare|WorkspaceError" -count=1 (exit 0, 2.55s). Additional tests for Acquire and isManaged also pass (exit 0, 0.53s)."
completed_at: 2026-04-02T19:00:34.574Z
blocker_discovered: false
---

# T01: WorkspaceManager Prepare implementation with source routing, hook execution, and managed cleanup

> WorkspaceManager Prepare implementation with source routing, hook execution, and managed cleanup

## What Happened
---
id: T01
parent: S04
milestone: M001-tlbeko
key_files:
  - pkg/workspace/manager.go
  - pkg/workspace/errors.go
  - pkg/workspace/manager_test.go
key_decisions:
  - WorkspaceError follows GitError/HookError pattern with Phase field for targeted diagnostics
  - Managed flag (true for Git/EmptyDir, false for Local) determines cleanup behavior on hook failure
  - LocalHandler returns source.Local.Path directly (unmanaged), GitHandler/EmptyDirHandler return targetDir (managed)
duration: ""
verification_result: passed
completed_at: 2026-04-02T19:00:34.576Z
blocker_discovered: false
---

# T01: WorkspaceManager Prepare implementation with source routing, hook execution, and managed cleanup

**WorkspaceManager Prepare implementation with source routing, hook execution, and managed cleanup**

## What Happened

Implemented WorkspaceManager Prepare functionality: WorkspaceError type with Phase/WorkspaceID/SourceType/Managed fields following GitError/HookError pattern; WorkspaceManager struct with handlers map routing to Git/EmptyDir/Local handlers, hookExecutor, refCount map, mutex; Prepare method validates spec, routes to handler, executes setup hooks, cleans up managed workspaces on hook failure; Acquire method increments refCount under mutex; isManaged helper returns true for Git/EmptyDir, false for Local. All 9 required tests pass covering error structure, routing, validation failures, cleanup behavior, and reference counting.

## Verification

All 9 required tests pass: TestWorkspaceErrorStructure, TestWorkspaceErrorErrorMethod, TestWorkspaceErrorUnwrap, TestNewWorkspaceManager, TestWorkspaceManagerPrepareGitSource, TestWorkspaceManagerPrepareEmptyDirSource, TestWorkspaceManagerPrepareLocalSource, TestWorkspaceManagerPrepareInvalidSpec, TestWorkspaceManagerPrepareHookFailureCleanup. Verification command executed successfully: go test ./pkg/workspace/... -v -run "Manager.*Prepare|WorkspaceError" -count=1 (exit 0, 2.55s). Additional tests for Acquire and isManaged also pass (exit 0, 0.53s).

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go test ./pkg/workspace/... -v -run "Manager.*Prepare|WorkspaceError" -count=1` | 0 | ✅ pass | 2551ms |
| 2 | `go test ./pkg/workspace/... -v -run "TestNewWorkspaceManager|TestWorkspaceManagerAcquire|TestIsManaged" -count=1` | 0 | ✅ pass | 527ms |


## Deviations

None.

## Known Issues

None.

## Files Created/Modified

- `pkg/workspace/manager.go`
- `pkg/workspace/errors.go`
- `pkg/workspace/manager_test.go`


## Deviations
None.

## Known Issues
None.
