---
estimated_steps: 48
estimated_files: 5
skills_used: []
---

# T02: Rebuild ARI registry and WorkspaceManager refcounts from DB on startup

## Description

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

## Inputs

- ``pkg/ari/registry.go` — Registry struct to extend`
- ``pkg/workspace/manager.go` — WorkspaceManager struct to extend`
- ``cmd/agentd/main.go` — startup sequence after recovery pass`
- ``pkg/ari/server.go` — T01 wired Source persistence`
- ``pkg/meta/workspace.go` — ListWorkspaces and AcquireWorkspace APIs`

## Expected Output

- ``pkg/ari/registry.go` — RebuildFromDB method added`
- ``pkg/ari/registry_test.go` — new file with TestRegistryRebuildFromDB`
- ``pkg/workspace/manager.go` — InitRefCounts method added`
- ``pkg/workspace/manager_test.go` — TestWorkspaceManagerInitRefCounts added`
- ``cmd/agentd/main.go` — rebuild calls after recovery, before ARI server creation`

## Verification

go test ./pkg/ari/... -count=1 -run TestRegistryRebuildFromDB -v && go test ./pkg/workspace/... -count=1 -run TestWorkspaceManagerInitRefCounts -v && go build ./cmd/agentd/... && go vet ./pkg/ari/... ./pkg/workspace/... ./cmd/agentd/...
