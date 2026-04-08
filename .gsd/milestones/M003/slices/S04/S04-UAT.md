# S04: Reconciled Workspace Ref Truth and Safe Cleanup — UAT

**Milestone:** M003
**Written:** 2026-04-08T03:57:46.305Z

## UAT: Reconciled Workspace Ref Truth and Safe Cleanup

### Preconditions
- Go toolchain installed (go 1.22+)
- Working directory: `/Users/jim/code/zoumo/open-agent-runtime`
- All dependencies available (`go mod tidy` has been run)

---

### Test Case 1: Session creation tracks workspace refs in DB

**Steps:**
1. Run `go test ./pkg/ari/... -count=1 -run TestARISessionNewAcquiresWorkspaceRef -v`
2. Observe test output

**Expected:**
- Test prepares a workspace, creates session 1, queries DB — `RefCount == 1`
- Creates session 2 on same workspace, queries DB — `RefCount == 2`
- PASS

---

### Test Case 2: Session removal decrements workspace refs in DB

**Steps:**
1. Run `go test ./pkg/ari/... -count=1 -run TestARISessionRemoveReleasesWorkspaceRef -v`
2. Observe test output

**Expected:**
- Test prepares workspace, creates session, asserts `RefCount == 1`
- Removes session, asserts `RefCount == 0`
- PASS

---

### Test Case 3: Workspace Source spec persisted to DB

**Steps:**
1. Run `go test ./pkg/ari/... -count=1 -run TestARIWorkspacePrepareSourcePersisted -v`
2. Observe test output

**Expected:**
- Test prepares workspace with git source spec
- Queries DB and asserts Source is not `{}` — contains serialized source with type field
- PASS

---

### Test Case 4: Cleanup blocked when DB ref_count > 0

**Steps:**
1. Run `go test ./pkg/ari/... -count=1 -run TestARIWorkspaceCleanupBlockedByDBRefCount -v`
2. Observe test output

**Expected:**
- Test prepares workspace, creates session (DB ref_count=1)
- Calls `workspace/cleanup` — returns error about active references
- Removes session (DB ref_count=0), calls cleanup again — succeeds
- PASS

---

### Test Case 5: Cleanup blocked during recovery phase

**Steps:**
1. Run `go test ./pkg/ari/... -count=1 -run TestARIWorkspaceCleanupBlockedDuringRecovery -v`
2. Observe test output

**Expected:**
- Test sets ProcessManager to recovering state
- Calls `workspace/cleanup` — returns `CodeRecoveryBlocked` (-32001)
- PASS

---

### Test Case 6: Registry rebuilt from DB after restart

**Steps:**
1. Run `go test ./pkg/ari/... -count=1 -run TestRegistryRebuildFromDB -v`
2. Observe test output

**Expected:**
- Test creates workspaces + refs in DB directly (simulating pre-restart state)
- Calls `Registry.RebuildFromDB(store)` on an empty registry
- Asserts `registry.List()` returns correct workspaces with correct RefCount values
- PASS

---

### Test Case 7: WorkspaceManager refcounts initialized from DB

**Steps:**
1. Run `go test ./pkg/workspace/... -count=1 -run TestWorkspaceManagerInitRefCounts -v`
2. Observe test output

**Expected:**
- Test creates store with workspace at ref_count=2
- Calls `manager.InitRefCounts(store)`
- Calls `manager.Release(path)` — returns 1 (proving refcount was initialized to 2)
- PASS

---

### Test Case 8: Full regression — no existing tests broken

**Steps:**
1. Run `go test ./pkg/ari/... -count=1`
2. Run `go test ./pkg/meta/... -count=1`
3. Run `go test ./pkg/workspace/... -count=1`
4. Run `go vet ./pkg/ari/... ./pkg/workspace/... ./pkg/meta/... ./cmd/agentd/...`
5. Run `go build ./cmd/agentd/... ./cmd/agent-shim/... ./pkg/...`

**Expected:**
- All test suites pass with zero failures
- go vet produces no warnings
- Full build succeeds with no errors

---

### Edge Cases

**EC1: Daemon restart with active workspace refs**
- Before restart: workspace has ref_count=2 in DB (2 active sessions)
- After restart: `Registry.RebuildFromDB` populates registry with RefCount=2
- `workspace/cleanup` correctly blocked by DB ref_count check
- Validated by: TestRegistryRebuildFromDB + TestARIWorkspaceCleanupBlockedByDBRefCount

**EC2: Cleanup attempt during recovery window**
- Recovery phase is active (shims being reconnected)
- `workspace/cleanup` returns CodeRecoveryBlocked before even parsing params
- Validated by: TestARIWorkspaceCleanupBlockedDuringRecovery

**EC3: Double-release prevention**
- `handleSessionRemove` does NOT call `ReleaseWorkspace` explicitly
- `meta.DeleteSession` cascade handles workspace_ref deletion via DB trigger
- Prevents ref_count going negative from double-release
- Validated by: TestARISessionRemoveReleasesWorkspaceRef (ref_count goes to 0, not -1)
