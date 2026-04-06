---
id: S02
parent: M001-tlbeko
milestone: M001-tlbeko
provides:
  - EmptyDirHandler implementation for SourceTypeEmptyDir
  - LocalHandler implementation for SourceTypeLocal
  - Complete source type coverage (Git + EmptyDir + Local)
requires:
  - slice: S01
    provides: SourceHandler interface pattern, Source types (SourceTypeGit, SourceTypeEmptyDir, SourceTypeLocal), GitHandler reference implementation
affects:
  - S04
key_files:
  - pkg/workspace/emptydir.go
  - pkg/workspace/emptydir_test.go
  - pkg/workspace/local.go
  - pkg/workspace/local_test.go
key_decisions:
  - Followed GitHandler pattern exactly for consistency across source handlers
  - LocalHandler returns source.Local.Path (NOT targetDir) because local workspaces are unmanaged by agentd
patterns_established:
  - SourceHandler interface pattern reinforced with EmptyDirHandler and LocalHandler implementations
  - Managed vs unmanaged workspace semantics: Git/EmptyDir are managed (created/deleted by agentd), Local is unmanaged (validated only)
observability_surfaces:
  - none
drill_down_paths:
  - .gsd/milestones/M001-tlbeko/slices/S02/tasks/T01-SUMMARY.md
  - .gsd/milestones/M001-tlbeko/slices/S02/tasks/T02-SUMMARY.md
duration: ""
verification_result: passed
completed_at: 2026-04-02T18:03:56.786Z
blocker_discovered: false
---

# S02: EmptyDir + Local Handlers

**EmptyDirHandler creates managed directories; LocalHandler validates existing paths and returns them directly (unmanaged semantics)**

## What Happened

Implemented EmptyDirHandler and LocalHandler following the SourceHandler interface pattern established in S01. EmptyDirHandler creates empty directories with os.MkdirAll(targetDir, 0755) and returns targetDir. LocalHandler validates existing paths exist and are directories via os.Stat(), returning source.Local.Path directly (NOT targetDir) because local workspaces are unmanaged by agentd. Both handlers include context cancellation checks before their operations. The handlers complete source type coverage for workspace provisioning: Git (managed, clone), EmptyDir (managed, create), Local (unmanaged, validate). All 60 workspace tests pass.

## Verification

Full workspace test suite executed: go test ./pkg/workspace/... -v -count=1. All 60 tests pass including EmptyDirHandler (7 tests: type rejection for git/local/unknown sources, directory creation, nested paths, existing directory handling, context cancellation) and LocalHandler (9 tests: type rejection for git/emptyDir/unknown sources, path doesn't exist error, path is file error, returns source.Local.Path vs targetDir, nested directories, context cancellation, permission denied). Handlers follow SourceHandler interface pattern consistently with proper error message formatting.

## Requirements Advanced

- R009 — Partial advance - EmptyDirHandler and LocalHandler implementations ready for WorkspaceManager integration

## Requirements Validated

None.

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

- `pkg/workspace/emptydir.go` — EmptyDirHandler implementation: type check, directory creation, returns targetDir
- `pkg/workspace/emptydir_test.go` — EmptyDirHandler tests: type rejection, directory creation, nested paths, context cancellation
- `pkg/workspace/local.go` — LocalHandler implementation: type check, path validation, returns source.Local.Path
- `pkg/workspace/local_test.go` — LocalHandler tests: type rejection, path validation errors, correct return value
