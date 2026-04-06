---
id: S04
parent: M001-tlbeko
milestone: M001-tlbeko
provides:
  - WorkspaceManager.Prepare for full workspace preparation workflow
  - WorkspaceManager.Cleanup for full workspace cleanup workflow
  - WorkspaceManager.Acquire/Release for reference counting
  - WorkspaceError structured error type with Phase field
requires:
  - slice: S01
    provides: WorkspaceSpec types, SourceHandler interface, GitHandler implementation
  - slice: S02
    provides: EmptyDirHandler, LocalHandler implementations with managed/unmanaged semantics
  - slice: S03
    provides: HookExecutor for setup/teardown hook execution
affects:
  - S05
key_files:
  - pkg/workspace/manager.go
  - pkg/workspace/errors.go
  - pkg/workspace/manager_test.go
key_decisions:
  - WorkspaceError follows GitError/HookError pattern with Phase field for targeted diagnostics
  - Managed flag (true for Git/EmptyDir, false for Local) determines cleanup behavior on hook failure
  - Teardown hook failures use best-effort cleanup semantics: logged but cleanup continues to ensure managed directories are deleted
  - Release returns count after decrement (0 if workspaceID not tracked), enabling callers to check if cleanup should proceed
patterns_established:
  - WorkspaceError Phase field pattern for lifecycle diagnostics
  - Best-effort teardown cleanup pattern
  - Reference counting pattern with Acquire/Release under mutex protection
observability_surfaces:
  - WorkspaceError Phase field enables targeted diagnostics
  - Teardown hook failures logged for debugging
drill_down_paths:
  - .gsd/milestones/M001-tlbeko/slices/S04/tasks/T01-SUMMARY.md
  - .gsd/milestones/M001-tlbeko/slices/S04/tasks/T02-SUMMARY.md
duration: ""
verification_result: passed
completed_at: 2026-04-02T19:13:21.038Z
blocker_discovered: false
---

# S04: Workspace Lifecycle

**WorkspaceManager orchestrates full workspace lifecycle with Prepare/Cleanup workflows, reference counting, and comprehensive error handling with structured diagnostics**

## What Happened

Slice S04 delivered the WorkspaceManager lifecycle orchestration layer that ties together all previous workspace components. The Prepare workflow routes to appropriate SourceHandler (Git/EmptyDir/Local), executes setup hooks, and cleans up managed workspaces on hook failure. The Cleanup workflow executes teardown hooks with best-effort semantics (failures logged but cleanup continues), then deletes managed directories. Reference counting via Acquire/Release prevents premature cleanup when multiple sessions share a workspace. WorkspaceError type provides structured diagnostics with Phase field identifying exactly where failures occurred (prepare-source, prepare-hooks, cleanup-delete). Comprehensive integration tests prove: lifecycle round-trips for all source types, reference counting semantics, hook failure handling, and multiple session scenarios. All 79 workspace tests pass, including 13 Manager-specific tests.

## Verification

Slice verification complete. All 13 Manager tests pass: TestNewWorkspaceManager, TestWorkspaceManagerPrepareGitSource (with subtests), TestWorkspaceManagerPrepareEmptyDirSource, TestWorkspaceManagerPrepareLocalSource, TestWorkspaceManagerPrepareInvalidSpec (4 subtests), TestWorkspaceManagerPrepareHookFailureCleanup (2 subtests), TestWorkspaceManagerAcquire, TestWorkspaceManagerRelease, TestWorkspaceManagerLifecycleGit, TestWorkspaceManagerLifecycleEmptyDir, TestWorkspaceManagerLifecycleLocal, TestWorkspaceManagerReferenceCounting, TestWorkspaceManagerCleanupHookFailure, TestWorkspaceManagerPrepareHookFailureCleanupManaged, TestWorkspaceManagerMultipleSessions. Full workspace test suite: 79 tests pass (15.919s). Integration tests prove: Prepare→Cleanup round-trips for Git/EmptyDir/Local, reference counting prevents premature cleanup, hook failure cleanup behavior (managed vs unmanaged), teardown hook best-effort semantics.

## Requirements Advanced

- R009 — WorkspaceManager Prepare/Cleanup implementation complete with reference counting

## Requirements Validated

- R009 — Integration tests prove Prepare→Cleanup round-trips for all source types, reference counting prevents premature cleanup, hook failure handling works correctly

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

None.

## Known Limitations

None.

## Follow-ups

None.

## Files Created/Modified

- `pkg/workspace/manager.go` — WorkspaceManager struct with handlers map, hookExecutor, refCount; Prepare/Cleanup/Acquire/Release methods; isManaged helper
- `pkg/workspace/errors.go` — WorkspaceError type with Phase/WorkspaceID/SourceType/Managed/Message/Err fields; Error() and Unwrap() methods
- `pkg/workspace/manager_test.go` — 13 Manager tests covering Prepare routing, Cleanup workflow, reference counting, hook failure handling, lifecycle integration tests
