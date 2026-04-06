# S05: ARI Workspace Methods — UAT

**Milestone:** M001-tlbeko
**Written:** 2026-04-02T19:54:04.315Z

# S05 UAT: ARI Workspace Methods

## Preconditions
1. agentd binary built and available
2. WorkspaceManager configured with all source handlers (Git, EmptyDir, Local)
3. ARI Unix socket available at configured path
4. JSON-RPC client capable of calling workspace/* methods

## Test Cases

### TC01: EmptyDir Workspace Prepare/List/Cleanup Lifecycle
**Steps:**
1. Call `workspace/prepare` with EmptyDir spec:
   ```json
   {
     "spec": {
       "oarVersion": "0.1.0",
       "metadata": {"name": "test-emptydir"},
       "source": {"type": "emptyDir"}
     }
   }
   ```
2. Verify response contains `workspaceId` (UUID format), `path` (existing directory), `status` = "ready"
3. Call `workspace/list` with empty params `{}`
4. Verify response contains 1 workspace with matching workspaceId
5. Call `workspace/cleanup` with `{"workspaceId": "<id from step 1>"}`
6. Verify response is success (no error)
7. Call `workspace/list` again
8. Verify response contains 0 workspaces

**Expected:** Full lifecycle works — prepare creates workspace, list tracks it, cleanup removes it.

### TC02: Git Workspace Prepare with Ref/Depth
**Steps:**
1. Call `workspace/prepare` with Git spec:
   ```json
   {
     "spec": {
       "oarVersion": "0.1.0",
       "metadata": {"name": "test-git"},
       "source": {
         "type": "git",
         "git": {
           "url": "https://github.com/example/repo.git",
           "ref": "main",
           "depth": 1
         }
       }
     }
   }
   ```
2. Verify response contains `workspaceId`, `path` with `.git` directory, `status` = "ready"
3. Verify cloned repository has correct branch checked out
4. Call `workspace/cleanup`
5. Verify workspace directory is deleted

**Expected:** Git clone works with ref/depth support, cleanup deletes managed directory.

### TC03: Local Workspace Prepare (Unmanaged)
**Steps:**
1. Create a test directory `/tmp/test-local-workspace`
2. Call `workspace/prepare` with Local spec:
   ```json
   {
     "spec": {
       "oarVersion": "0.1.0",
       "metadata": {"name": "test-local"},
       "source": {
         "type": "local",
         "local": {"path": "/tmp/test-local-workspace"}
       }
     }
   }
   ```
3. Verify response contains `workspaceId`, `path` = `/tmp/test-local-workspace` (original path)
4. Call `workspace/cleanup`
5. Verify response is success
6. Verify `/tmp/test-local-workspace` still exists (not deleted)

**Expected:** Local workspace is validated but not deleted on cleanup (unmanaged semantics).

### TC04: Cleanup Failure with Active References
**Steps:**
1. Call `workspace/prepare` with EmptyDir spec
2. Simulate session reference: call Registry.Acquire(workspaceId, "session-123")
3. Call `workspace/cleanup` with the workspaceId
4. Verify response contains JSON-RPC InternalError with message indicating refs > 0
5. Verify workspace still exists (cleanup blocked)
6. Release reference: call Registry.Release(workspaceId, "session-123")
7. Call `workspace/cleanup` again
8. Verify cleanup succeeds

**Expected:** Cleanup fails when RefCount > 0, succeeds after refs released.

### TC05: Invalid WorkspaceSpec Validation
**Steps:**
1. Call `workspace/prepare` with missing oarVersion:
   ```json
   {"spec": {"metadata": {"name": "test"}, "source": {"type": "emptyDir"}}}
   ```
2. Verify response contains JSON-RPC InvalidParams error

3. Call `workspace/prepare` with missing metadata.name:
   ```json
   {"spec": {"oarVersion": "0.1.0", "source": {"type": "emptyDir"}}}
   ```
4. Verify response contains InvalidParams error

5. Call `workspace/prepare` with unsupported major version:
   ```json
   {"spec": {"oarVersion": "1.0.0", "metadata": {"name": "test"}, "source": {"type": "emptyDir"}}}
   ```
6. Verify response contains InvalidParams error

**Expected:** Invalid specs return JSON-RPC InvalidParams with descriptive error messages.

### TC06: Cleanup Nonexistent Workspace
**Steps:**
1. Call `workspace/cleanup` with random UUID that doesn't exist:
   ```json
   {"workspaceId": "00000000-0000-0000-0000-000000000000"}
   ```
2. Verify response contains JSON-RPC error (InternalError or InvalidParams)

**Expected:** Cleanup nonexistent workspace returns appropriate error.

### TC07: Unknown Method Handling
**Steps:**
1. Call `workspace/unknownMethod` with any params
2. Verify response contains JSON-RPC MethodNotFound error (-32601)

**Expected:** Unknown methods return MethodNotFound error.

### TC08: Hook Failure During Prepare
**Steps:**
1. Call `workspace/prepare` with spec containing failing setup hook:
   ```json
   {
     "spec": {
       "oarVersion": "0.1.0",
       "metadata": {"name": "test-hook-fail"},
       "source": {"type": "emptyDir"},
       "hooks": {
         "setup": [{"command": "exit 1", "timeout": "5s"}]
       }
     }
   }
   ```
2. Verify response contains error with Phase = "prepare-hooks"
3. Verify workspace directory does not exist (partial state cleaned up)

**Expected:** Hook failures abort preparation and clean up partial state.

## Pass Criteria
- All 8 test cases pass
- No unexpected errors
- Workspace directories created/deleted correctly based on source type
- Reference counting prevents premature cleanup
