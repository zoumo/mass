---
id: T01
parent: S04
milestone: M003
key_files:
  - pkg/ari/server.go
  - pkg/ari/server_test.go
key_decisions:
  - AcquireWorkspace failure logged but does not fail session/new RPC (mirrors existing error-tolerance pattern)
  - No explicit ReleaseWorkspace in handleSessionRemove — DeleteSession already cascades workspace_refs
duration: 
verification_result: passed
completed_at: 2026-04-08T03:23:08.686Z
blocker_discovered: false
---

# T01: Wired handleSessionNew to call store.AcquireWorkspace + registry.Acquire and handleWorkspacePrepare to persist full Source JSON to DB, with 3 integration tests proving ref_count tracking and source persistence.

**Wired handleSessionNew to call store.AcquireWorkspace + registry.Acquire and handleWorkspacePrepare to persist full Source JSON to DB, with 3 integration tests proving ref_count tracking and source persistence.**

## What Happened

Modified handleWorkspacePrepare to serialize p.Spec.Source via json.Marshal into the meta.Workspace.Source field before CreateWorkspace (previously defaulted to "{}").\n\nModified handleSessionNew to call store.AcquireWorkspace and registry.Acquire after successful session creation, recording the session→workspace ref in DB and keeping the in-memory registry consistent. AcquireWorkspace failures are logged but don't fail the RPC.\n\nNo explicit ReleaseWorkspace was added to handleSessionRemove — DeleteSession already cascades workspace_refs rows via trigger.\n\nAdded 3 integration tests: TestARISessionNewAcquiresWorkspaceRef (verifies ref_count increments), TestARISessionRemoveReleasesWorkspaceRef (verifies ref_count decrements on removal), TestARIWorkspacePrepareSourcePersisted (verifies Source is serialized, not "{}").

## Verification

All 3 new tests pass, all existing tests still pass, go vet clean.\n- go test ./pkg/ari/... -count=1 -run 3 targeted tests → PASS\n- go test ./pkg/ari/... -count=1 → PASS (5.887s)\n- go vet ./pkg/ari/... → clean

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go test ./pkg/ari/... -count=1 -run 'TestARISessionNewAcquiresWorkspaceRef|TestARISessionRemoveReleasesWorkspaceRef|TestARIWorkspacePrepareSourcePersisted' -v` | 0 | ✅ pass | 1284ms |
| 2 | `go test ./pkg/ari/... -count=1` | 0 | ✅ pass | 5887ms |
| 3 | `go vet ./pkg/ari/...` | 0 | ✅ pass | 500ms |

## Deviations

None.

## Known Issues

None.

## Files Created/Modified

- `pkg/ari/server.go`
- `pkg/ari/server_test.go`
