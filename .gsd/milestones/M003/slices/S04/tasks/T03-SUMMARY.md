---
id: T03
parent: S04
milestone: M003
key_files:
  - pkg/ari/server.go
  - pkg/ari/server_test.go
key_decisions:
  - DB ref_count is the primary gate for cleanup; in-memory registry RefCount used only as fallback when store is nil
  - Recovery guard fires before param parsing in handleWorkspaceCleanup (fail-closed posture)
duration: 
verification_result: passed
completed_at: 2026-04-08T03:40:20.630Z
blocker_discovered: false
---

# T03: Changed handleWorkspaceCleanup to gate on persisted DB ref_count instead of volatile in-memory RefCount, added recovery-phase guard, and wrote 2 safety tests proving cleanup is blocked by active DB refs and during recovery.

**Changed handleWorkspaceCleanup to gate on persisted DB ref_count instead of volatile in-memory RefCount, added recovery-phase guard, and wrote 2 safety tests proving cleanup is blocked by active DB refs and during recovery.**

## What Happened

Modified handleWorkspaceCleanup in pkg/ari/server.go with two changes: (1) Added recoveryGuard at the top to block cleanup during active recovery phase, and (2) replaced the volatile meta.RefCount > 0 check with a DB-based check via store.GetWorkspace that survives daemon restarts. Updated the existing TestARIWorkspaceCleanupWithRefs to also set DB ref_count. Added two new integration tests: TestARIWorkspaceCleanupBlockedByDBRefCount and TestARIWorkspaceCleanupBlockedDuringRecovery.

## Verification

All 6 verification checks pass: 2 new targeted tests PASS, 3 slice-level tests from T01 PASS, all ARI tests PASS (no regressions), all meta tests PASS, go vet clean, full build succeeds.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go test ./pkg/ari/... -count=1 -run 'TestARIWorkspaceCleanupBlockedByDBRefCount|TestARIWorkspaceCleanupBlockedDuringRecovery' -v` | 0 | ✅ pass | 1036ms |
| 2 | `go test ./pkg/ari/... -count=1 -run 'TestARISessionNewAcquiresWorkspaceRef|TestARISessionRemoveReleasesWorkspaceRef|TestARIWorkspacePrepareSourcePersisted' -v` | 0 | ✅ pass | 618ms |
| 3 | `go test ./pkg/ari/... -count=1` | 0 | ✅ pass | 6354ms |
| 4 | `go test ./pkg/meta/... -count=1` | 0 | ✅ pass | 564ms |
| 5 | `go vet ./pkg/ari/... ./pkg/meta/...` | 0 | ✅ pass | 500ms |
| 6 | `go build ./cmd/agentd/... ./cmd/agent-shim/... ./pkg/...` | 0 | ✅ pass | 1000ms |

## Deviations

Updated existing TestARIWorkspaceCleanupWithRefs to also set DB ref_count via store.AcquireWorkspace (required a fake session row in DB). This was necessary because the new DB-first gate made the old registry-only ref invisible to the cleanup handler.

## Known Issues

None.

## Files Created/Modified

- `pkg/ari/server.go`
- `pkg/ari/server_test.go`
