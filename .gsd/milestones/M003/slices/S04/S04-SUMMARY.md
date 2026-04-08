---
id: S04
parent: M003
milestone: M003
provides:
  - ["DB-backed workspace ref_count tracking through session lifecycle", "Registry and WorkspaceManager rebuild from DB after restart", "Safe workspace cleanup gated on persisted ref_count + recovery phase", "Store.ListWorkspaceRefs API for querying session→workspace refs"]
requires:
  []
affects:
  []
key_files:
  - ["pkg/ari/server.go", "pkg/ari/server_test.go", "pkg/ari/registry.go", "pkg/ari/registry_test.go", "pkg/workspace/manager.go", "pkg/workspace/manager_test.go", "pkg/meta/workspace.go", "cmd/agentd/main.go"]
key_decisions:
  - ["D049: Workspace cleanup gates on persisted DB ref_count, not volatile in-memory RefCount", "D050: Registry and WorkspaceManager refcounts rebuilt from DB after daemon restart (non-fatal failures)", "AcquireWorkspace failure logged but does not fail session/new RPC (error-tolerance pattern)", "No explicit ReleaseWorkspace in handleSessionRemove — DeleteSession cascade handles it", "Recovery guard fires before param parsing in handleWorkspaceCleanup (fail-closed posture)"]
patterns_established:
  - ["DB-as-truth for cleanup gating: volatile in-memory state is not trusted for destructive operations — DB ref_count is the authoritative gate", "Rebuild-from-DB on startup: Registry.RebuildFromDB and WorkspaceManager.InitRefCounts populate in-memory state from DB after recovery, before the ARI server starts serving", "Non-fatal rebuild: startup rebuild failures are logged but don't block daemon start — the daemon can still function with reduced workspace awareness", "Recovery-phase guard on destructive ops: workspace/cleanup joins session/prompt and session/cancel in being blocked during recovery phase"]
observability_surfaces:
  - ["agentd startup logs: 'registry rebuilt from database' and 'workspace refcount init' messages after recovery pass", "Non-fatal error logs for rebuild/init failures", "Recovery guard returns CodeRecoveryBlocked (-32001) JSON-RPC error for cleanup attempts during recovery"]
drill_down_paths:
  []
duration: ""
verification_result: passed
completed_at: 2026-04-08T03:57:46.305Z
blocker_discovered: false
---

# S04: Reconciled Workspace Ref Truth and Safe Cleanup

**Workspace reference truth is now persisted in DB, the ARI registry and WorkspaceManager are rebuilt from DB after restart, and workspace cleanup gates on DB ref_count plus recovery phase — making cleanup safe across daemon restarts.**

## What Happened

This slice closed the gap between volatile in-memory workspace state and persisted DB truth, making workspace cleanup safe across daemon restarts.

**T01 — Wired session lifecycle to DB ref_count tracking.** Modified `handleWorkspacePrepare` to serialize the full `Source` spec (via `json.Marshal`) into the DB instead of defaulting to `"{}"`. Modified `handleSessionNew` to call both `store.AcquireWorkspace` (DB) and `registry.Acquire` (in-memory) after session creation, keeping both state stores consistent. AcquireWorkspace failures are logged but don't fail the RPC, matching the existing error-tolerance pattern. No explicit `ReleaseWorkspace` was added to `handleSessionRemove` because `meta.DeleteSession` already cascades `workspace_refs` rows via a DB trigger. Three integration tests prove: ref_count increments when sessions are created, decrements when sessions are removed, and Source spec is correctly persisted (not `{}`).

**T02 — Registry and WorkspaceManager rebuilt from DB after restart.** Added `Store.ListWorkspaceRefs` to query session IDs from the `workspace_refs` table. Added `Registry.RebuildFromDB` which loads all active workspaces from DB, deserializes Source JSON back into workspace specs, and populates RefCount + Refs. Added `WorkspaceManager.InitRefCounts` which sets in-memory refcounts from DB values (keyed by workspace path). Wired both into `cmd/agentd/main.go` after the recovery pass, before ARI server creation — both failures are non-fatal. Two new tests prove: registry correctly rebuilds workspace entries with RefCount from DB, and WorkspaceManager refcounts are correctly initialized.

**T03 — Cleanup gated on DB ref_count and recovery phase.** Replaced the volatile `meta.RefCount > 0` check in `handleWorkspaceCleanup` with a DB-based check via `store.GetWorkspace` — this survives daemon restarts. Added `recoveryGuard` at the top of the handler, blocking cleanup during active recovery phase (returns CodeRecoveryBlocked). Updated existing `TestARIWorkspaceCleanupWithRefs` to also set DB ref_count. Two new safety tests prove: cleanup is blocked when DB ref_count > 0, and cleanup is blocked during recovery phase.

The net result: after a daemon restart, `workspace/list` returns correct entries, `workspace/cleanup` correctly refuses to delete workspaces with active sessions (even though the in-memory registry was empty), and cleanup is blocked during the recovery window where session state is still being reconciled.

## Verification

All slice-level verification checks pass:

1. **Targeted S04 tests (7 new tests):** `go test ./pkg/ari/... -count=1 -run 'TestARISessionNewAcquiresWorkspaceRef|TestARISessionRemoveReleasesWorkspaceRef|TestARIWorkspacePrepareSourcePersisted|TestARIWorkspaceCleanupBlockedByDBRefCount|TestARIWorkspaceCleanupBlockedDuringRecovery|TestRegistryRebuildFromDB' -v` — all PASS (1.3s)
2. **Workspace manager test:** `go test ./pkg/workspace/... -count=1 -run TestWorkspaceManagerInitRefCounts -v` — PASS (1.8s)
3. **Full ARI regression:** `go test ./pkg/ari/... -count=1` — all PASS (6.4s)
4. **Full meta regression:** `go test ./pkg/meta/... -count=1` — all PASS (0.6s)
5. **Go vet:** `go vet ./pkg/ari/... ./pkg/workspace/... ./pkg/meta/... ./cmd/agentd/...` — clean
6. **Full build:** `go build ./cmd/agentd/... ./cmd/agent-shim/... ./pkg/...` — clean

## Requirements Advanced

- R044 — Delivered workspace cleanup hardening portion — DB-backed ref_count, restart rebuild, recovery guard

## Requirements Validated

- R037 — 7 integration tests prove workspace identity persistence, cleanup boundaries (DB ref_count gate), and safe cleanup across restarts

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

Updated existing TestARIWorkspaceCleanupWithRefs to set DB ref_count via store.AcquireWorkspace (required a fake session row). Necessary because T03's DB-first gate made the old registry-only ref invisible to the cleanup handler.

## Known Limitations

Registry rebuild does not handle the case where a workspace exists in DB but its on-disk path has been deleted (stale workspace). If the disk path is missing, the workspace is still registered in memory — a future enhancement could verify path existence during rebuild.

## Follow-ups

None.

## Files Created/Modified

- `pkg/ari/server.go` — handleWorkspacePrepare serializes Source spec to DB; handleSessionNew calls AcquireWorkspace + registry.Acquire; handleWorkspaceCleanup gates on DB ref_count + recovery guard
- `pkg/ari/server_test.go` — 5 new integration tests for ref_count tracking, source persistence, cleanup blocking
- `pkg/ari/registry.go` — RebuildFromDB method loads workspaces from DB into registry
- `pkg/ari/registry_test.go` — TestRegistryRebuildFromDB
- `pkg/workspace/manager.go` — InitRefCounts method sets in-memory refcounts from DB
- `pkg/workspace/manager_test.go` — TestWorkspaceManagerInitRefCounts
- `pkg/meta/workspace.go` — ListWorkspaceRefs helper for querying workspace_refs table
- `cmd/agentd/main.go` — Registry rebuild and InitRefCounts wired after recovery pass, before ARI server start
