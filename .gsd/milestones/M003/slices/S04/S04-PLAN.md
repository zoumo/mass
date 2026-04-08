# S04: Reconciled Workspace Ref Truth and Safe Cleanup

**Goal:** Workspace reference truth is persisted to DB via session lifecycle hooks, the ARI registry is rebuilt from DB after restart, and workspace/cleanup gates on DB ref_count + recovery phase — making cleanup safe across daemon restarts.
**Demo:** After this: TBD

## Tasks
- [x] **T01: Wired handleSessionNew to call store.AcquireWorkspace + registry.Acquire and handleWorkspacePrepare to persist full Source JSON to DB, with 3 integration tests proving ref_count tracking and source persistence.** — ## Description

The DB has `AcquireWorkspace`/`ReleaseWorkspace` methods (implemented and tested in `pkg/meta/workspace.go`) but the live session path never calls them. This task wires the acquire side into `handleSessionNew` and fixes `handleWorkspacePrepare` to persist the full Source spec.

## Steps

1. In `pkg/ari/server.go` `handleWorkspacePrepare`, serialize `p.Spec.Source` as JSON into the `meta.Workspace.Source` field before calling `store.CreateWorkspace`. Currently the Source field is omitted (defaults to `json.RawMessage("{}")` in CreateWorkspace). Use `json.Marshal(p.Spec.Source)` and set `workspace.Source = sourceJSON`.

2. In `pkg/ari/server.go` `handleSessionNew`, after successful `sessions.Create`, call `h.srv.store.AcquireWorkspace(ctx, p.WorkspaceId, sessionId)` to record the session→workspace ref in DB. Also call `h.srv.registry.Acquire(p.WorkspaceId, sessionId)` to keep the in-memory registry consistent. If AcquireWorkspace fails, log the error but don't fail the RPC (mirrors the pattern used in handleWorkspacePrepare for CreateWorkspace). The release side is already handled: `meta.DeleteSession` already deletes `workspace_refs` rows, and the trigger decrements `ref_count`. Do NOT add an explicit `ReleaseWorkspace` call in `handleSessionRemove` — that would double-release.

3. Add test `TestARISessionNewAcquiresWorkspaceRef` in `pkg/ari/server_test.go`: prepare a workspace, create a session via `session/new`, then query DB `store.GetWorkspace` and assert `RefCount == 1`. Create a second session on same workspace, assert `RefCount == 2`.

4. Add test `TestARISessionRemoveReleasesWorkspaceRef` in `pkg/ari/server_test.go`: prepare workspace, create session, assert `RefCount == 1`, then stop + remove the session, assert `RefCount == 0`.

5. Add test `TestARIWorkspacePrepareSourcePersisted` in `pkg/ari/server_test.go`: prepare a workspace with a git source spec, then query DB `store.GetWorkspace` and assert Source is not `{}` — it should contain the serialized Source with `type: "git"`.

## Must-Haves

- [ ] `handleWorkspacePrepare` persists Source spec (not `{}`) to DB
- [ ] `handleSessionNew` calls `store.AcquireWorkspace` after session creation
- [ ] `handleSessionNew` calls `registry.Acquire` to keep in-memory state consistent
- [ ] No explicit `ReleaseWorkspace` in `handleSessionRemove` (avoid double-release)
- [ ] 3 new tests pass proving ref acquire, ref release, and source persistence

## Verification

- `go test ./pkg/ari/... -count=1 -run 'TestARISessionNewAcquiresWorkspaceRef|TestARISessionRemoveReleasesWorkspaceRef|TestARIWorkspacePrepareSourcePersisted' -v` — all 3 PASS
- `go test ./pkg/ari/... -count=1` — all existing tests still pass
- `go vet ./pkg/ari/...` — clean

## Inputs

- `pkg/ari/server.go` — handleWorkspacePrepare and handleSessionNew need modification
- `pkg/ari/server_test.go` — existing test harness to extend
- `pkg/meta/workspace.go` — AcquireWorkspace/ReleaseWorkspace already implemented
- `pkg/meta/session.go` — DeleteSession already cleans up workspace_refs

## Expected Output

- `pkg/ari/server.go` — modified: Source serialization in handleWorkspacePrepare, AcquireWorkspace call in handleSessionNew
- `pkg/ari/server_test.go` — modified: 3 new test functions added
  - Estimate: 45m
  - Files: pkg/ari/server.go, pkg/ari/server_test.go
  - Verify: go test ./pkg/ari/... -count=1 -run 'TestARISessionNewAcquiresWorkspaceRef|TestARISessionRemoveReleasesWorkspaceRef|TestARIWorkspacePrepareSourcePersisted' -v && go test ./pkg/ari/... -count=1 && go vet ./pkg/ari/...
- [x] **T02: Added Registry.RebuildFromDB and WorkspaceManager.InitRefCounts methods that load workspace state from DB after daemon restart, plus wired both into cmd/agentd/main.go after the recovery pass.** — ## Description

After a daemon restart, the ARI registry is empty — `workspace/list` returns nothing and `workspace/cleanup` returns "not found". This task adds a rebuild function that loads workspaces from DB and repopulates both the `ari.Registry` and `workspace.WorkspaceManager` refcounts.

## Steps

1. Add a new exported method `RebuildFromDB(store *meta.Store)` on `*ari.Registry` in `pkg/ari/registry.go`. This method calls `store.ListWorkspaces(ctx, nil)` to get all active workspaces, then for each workspace:
   - Reconstruct a `workspace.WorkspaceSpec` from the DB record: unmarshal `ws.Source` (json.RawMessage) into `workspace.Source`, set `Metadata.Name = ws.Name`.
   - Call `r.Add(ws.ID, ws.Name, ws.Path, spec)` to register the workspace.
   - For each workspace, query workspace_refs: we need the session IDs. Since `store.GetWorkspace` returns `RefCount` but not the session IDs, use DB query via a new helper or just set the registry RefCount from the DB value. The simplest approach: after `r.Add`, directly set `meta.RefCount = ws.RefCount` on the registry entry. For the Refs list (session IDs), either query `workspace_refs` table or leave Refs empty (acceptable — they're for debugging only). Prefer: add a `ListWorkspaceRefs(ctx, workspaceID)` method to `meta.Store` that returns session IDs from `workspace_refs`, then populate `registry.Refs`.
   - Alternative simpler approach: Add a method `AddFromDB(id, name, path string, source json.RawMessage, refCount int)` to the registry that handles Source deserialization internally. This avoids exposing reconstruction logic to the caller.

2. Add a new exported method `InitRefCounts(store *meta.Store)` on `*workspace.WorkspaceManager` in `pkg/workspace/manager.go` (or accept a map). This loads all workspaces from DB and sets `m.refCount[ws.Path] = ws.RefCount` for each. Note: the WorkspaceManager uses workspace path (targetDir) as the refCount key, not workspace ID.

3. In `cmd/agentd/main.go`, after the recovery pass block (after `recoverCancel()`), add the registry rebuild:
   ```go
   // Rebuild registry from DB after recovery.
   if err := registry.RebuildFromDB(store); err != nil {
       log.Printf("agentd: registry rebuild failed (non-fatal): %v", err)
   } else {
       log.Printf("agentd: registry rebuilt from database")
   }
   // Initialize workspace manager refcounts from DB.
   if err := manager.InitRefCounts(store); err != nil {
       log.Printf("agentd: workspace refcount init failed (non-fatal): %v", err)
   }
   ```
   Place this AFTER recovery but BEFORE the ARI server is created (so the server starts with a populated registry).

4. Add test `TestRegistryRebuildFromDB` in `pkg/ari/registry_test.go` (new file): create a `meta.Store`, insert workspaces with refs via `store.CreateWorkspace` + `store.AcquireWorkspace`, then call `registry.RebuildFromDB(store)` and verify `registry.List()` returns the correct workspaces with correct RefCount values.

5. Add test `TestWorkspaceManagerInitRefCounts` in `pkg/workspace/manager_test.go`: create a store, insert a workspace with refCount=2, call `manager.InitRefCounts(store)`, then verify that `manager.Release(path)` returns 1 (proving the refcount was initialized to 2).

## Must-Haves

- [ ] `Registry.RebuildFromDB` loads all active workspaces from DB into the registry with correct RefCount
- [ ] `WorkspaceManager.InitRefCounts` initializes in-memory refcounts from DB values
- [ ] `cmd/agentd/main.go` calls both rebuild functions after recovery, before ARI server start
- [ ] Source spec from DB is correctly deserialized back into `workspace.Source` in the registry
- [ ] 2 new tests pass

## Verification

- `go test ./pkg/ari/... -count=1 -run TestRegistryRebuildFromDB -v` — PASS
- `go test ./pkg/workspace/... -count=1 -run TestWorkspaceManagerInitRefCounts -v` — PASS
- `go build ./cmd/agentd/...` — builds clean
- `go vet ./pkg/ari/... ./pkg/workspace/... ./cmd/agentd/...` — clean

## Inputs

- `pkg/ari/registry.go` — Registry struct to extend with RebuildFromDB
- `pkg/workspace/manager.go` — WorkspaceManager struct to extend with InitRefCounts
- `cmd/agentd/main.go` — startup sequence to modify
- `pkg/ari/server.go` — T01 output: Source spec now persisted in DB
- `pkg/meta/workspace.go` — ListWorkspaces, GetWorkspace already available

## Expected Output

- `pkg/ari/registry.go` — modified: RebuildFromDB method added
- `pkg/ari/registry_test.go` — new file: TestRegistryRebuildFromDB
- `pkg/workspace/manager.go` — modified: InitRefCounts method added
- `pkg/workspace/manager_test.go` — modified: TestWorkspaceManagerInitRefCounts added
- `cmd/agentd/main.go` — modified: rebuild calls after recovery pass
  - Estimate: 45m
  - Files: pkg/ari/registry.go, pkg/ari/registry_test.go, pkg/workspace/manager.go, pkg/workspace/manager_test.go, cmd/agentd/main.go
  - Verify: go test ./pkg/ari/... -count=1 -run TestRegistryRebuildFromDB -v && go test ./pkg/workspace/... -count=1 -run TestWorkspaceManagerInitRefCounts -v && go build ./cmd/agentd/... && go vet ./pkg/ari/... ./pkg/workspace/... ./cmd/agentd/...
- [x] **T03: Changed handleWorkspaceCleanup to gate on persisted DB ref_count instead of volatile in-memory RefCount, added recovery-phase guard, and wrote 2 safety tests proving cleanup is blocked by active DB refs and during recovery.** — ## Description

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
  - Estimate: 30m
  - Files: pkg/ari/server.go, pkg/ari/server_test.go
  - Verify: go test ./pkg/ari/... -count=1 -run 'TestARIWorkspaceCleanupBlockedByDBRefCount|TestARIWorkspaceCleanupBlockedDuringRecovery' -v && go test ./pkg/ari/... -count=1 && go test ./pkg/meta/... -count=1 && go vet ./pkg/ari/... ./pkg/meta/... && go build ./cmd/agentd/... ./cmd/agent-shim/... ./pkg/...
