# S04: Workspace Lifecycle — UAT

**Milestone:** M001-tlbeko
**Written:** 2026-04-02T19:13:21.039Z

# S04 UAT: Workspace Lifecycle

## Test Suite Overview
This UAT validates the WorkspaceManager lifecycle orchestration: Prepare workflow (source routing + setup hooks), Cleanup workflow (teardown hooks + managed directory deletion), and reference counting semantics.

## Prerequisites
- Go test environment configured
- Workspace package built: `pkg/workspace/`
- Git available for Git source tests
- Test fixtures: mock hooks, temporary directories

---

## TC-01: Prepare workflow with Git source

**Purpose:** Validate Git source preparation with setup hooks

**Steps:**
1. Create WorkspaceSpec with Git source (valid repo URL, branch ref)
2. Call WorkspaceManager.Prepare(ctx, spec, targetDir)
3. Verify workspace directory exists at targetDir
4. Verify setup hooks executed (check hook output)
5. Call WorkspaceManager.Cleanup(ctx, targetDir, spec)
6. Verify workspace directory deleted

**Expected Results:**
- Prepare returns workspacePath == targetDir
- Workspace directory contains cloned repo content
- Setup hooks executed in order
- Cleanup deletes managed directory

**Actual:** ✅ PASS - TestWorkspaceManagerLifecycleGit passes (1.71s)

---

## TC-02: Prepare workflow with EmptyDir source

**Purpose:** Validate EmptyDir source preparation

**Steps:**
1. Create WorkspaceSpec with EmptyDir source
2. Call WorkspaceManager.Prepare(ctx, spec, targetDir)
3. Verify empty directory created at targetDir
4. Call WorkspaceManager.Cleanup(ctx, targetDir, spec)
5. Verify workspace directory deleted

**Expected Results:**
- Prepare returns workspacePath == targetDir
- Empty directory created
- Cleanup deletes managed directory

**Actual:** ✅ PASS - TestWorkspaceManagerLifecycleEmptyDir passes

---

## TC-03: Prepare workflow with Local source

**Purpose:** Validate Local source preparation (unmanaged)

**Steps:**
1. Create pre-existing test directory
2. Create WorkspaceSpec with Local source pointing to test directory
3. Call WorkspaceManager.Prepare(ctx, spec, targetDir)
4. Verify returns existing path (NOT targetDir)
5. Call WorkspaceManager.Cleanup(ctx, existingPath, spec)
6. Verify directory NOT deleted (unmanaged)

**Expected Results:**
- Prepare returns source.Local.Path (existing path)
- Cleanup does NOT delete directory

**Actual:** ✅ PASS - TestWorkspaceManagerLifecycleLocal passes

---

## TC-04: Reference counting prevents premature cleanup

**Purpose:** Validate Acquire/Release reference counting

**Steps:**
1. Prepare workspace (refCount becomes 1)
2. Call Acquire(workspaceID) → refCount = 2 (second session)
3. Call Release(workspaceID) → returns count = 1
4. Call Cleanup(ctx, workspaceID, spec) → count > 0, returns nil, no deletion
5. Call Release(workspaceID) → returns count = 0
6. Call Cleanup(ctx, workspaceID, spec) → count == 0, cleanup proceeds

**Expected Results:**
- First Cleanup (count=1) does NOT delete workspace
- Second Cleanup (count=0) deletes workspace

**Actual:** ✅ PASS - TestWorkspaceManagerReferenceCounting passes

---

## TC-05: Setup hook failure triggers cleanup for managed workspaces

**Purpose:** Validate managed cleanup on setup hook failure

**Steps:**
1. Create WorkspaceSpec with Git source and failing setup hook
2. Call WorkspaceManager.Prepare(ctx, spec, targetDir)
3. Expect WorkspaceError with Phase="prepare-hooks"
4. Verify targetDir NOT left behind (cleaned up)

**Expected Results:**
- Prepare returns WorkspaceError
- WorkspaceError.Phase == "prepare-hooks"
- Managed workspace directory cleaned up (no partial state)

**Actual:** ✅ PASS - TestWorkspaceManagerPrepareHookFailureCleanupManaged passes

---

## TC-06: Setup hook failure does NOT cleanup unmanaged (Local) workspaces

**Purpose:** Validate Local workspace NOT cleaned up on hook failure

**Steps:**
1. Create pre-existing test directory
2. Create WorkspaceSpec with Local source and failing setup hook
3. Call WorkspaceManager.Prepare(ctx, spec, targetDir)
4. Expect WorkspaceError with Phase="prepare-hooks"
5. Verify existing directory still exists (NOT deleted)

**Expected Results:**
- Prepare returns WorkspaceError
- Local workspace directory NOT deleted (unmanaged)

**Actual:** ✅ PASS - TestWorkspaceManagerPrepareHookFailureCleanup (unmanaged subtest) passes

---

## TC-07: Teardown hook failure does NOT prevent cleanup

**Purpose:** Validate best-effort teardown cleanup semantics

**Steps:**
1. Create WorkspaceSpec with Git source and failing teardown hook
2. Call Prepare → succeeds
3. Call Cleanup(ctx, workspaceID, spec)
4. Verify teardown hook failure logged
5. Verify managed directory still deleted

**Expected Results:**
- Cleanup returns nil (success)
- Teardown hook error logged
- Managed workspace directory deleted despite hook failure

**Actual:** ✅ PASS - TestWorkspaceManagerCleanupHookFailure passes

---

## TC-08: Multiple sessions sharing workspace

**Purpose:** Validate multi-session reference counting scenario

**Steps:**
1. Prepare workspace (count=1)
2. Acquire twice → count=3 (three sessions)
3. Release → count=2
4. Cleanup → count > 0, no deletion
5. Release → count=1
6. Cleanup → count > 0, no deletion
7. Release → count=0
8. Cleanup → count == 0, deletion proceeds

**Expected Results:**
- Workspace only deleted when count reaches zero
- Intermediate Cleanups return nil without deletion

**Actual:** ✅ PASS - TestWorkspaceManagerMultipleSessions passes

---

## TC-09: Invalid spec validation failure

**Purpose:** Validate spec validation before preparation

**Steps:**
1. Create WorkspaceSpec with missing oarVersion
2. Call WorkspaceManager.Prepare(ctx, spec, targetDir)
3. Expect WorkspaceError

**Expected Results:**
- Prepare returns WorkspaceError immediately
- No handler called

**Actual:** ✅ PASS - TestWorkspaceManagerPrepareInvalidSpec passes (4 subtests)

---

## TC-10: WorkspaceError structure and diagnostics

**Purpose:** Validate WorkspaceError provides structured diagnostics

**Steps:**
1. Trigger Prepare failure at various phases
2. Verify WorkspaceError.Phase identifies failure point
3. Verify WorkspaceError.Unwrap() enables errors.Is/errors.As

**Expected Results:**
- Phase field correctly identifies: prepare-source, prepare-hooks, cleanup-delete
- Error() method produces formatted string
- Unwrap() returns underlying error

**Actual:** ✅ PASS - TestWorkspaceErrorStructure, TestWorkspaceErrorErrorMethod, TestWorkspaceErrorUnwrap pass

---

## Summary

| TC | Name | Result |
|----|------|--------|
| TC-01 | Prepare Git source | ✅ PASS |
| TC-02 | Prepare EmptyDir source | ✅ PASS |
| TC-03 | Prepare Local source | ✅ PASS |
| TC-04 | Reference counting | ✅ PASS |
| TC-05 | Setup hook failure cleanup (managed) | ✅ PASS |
| TC-06 | Setup hook failure (unmanaged) | ✅ PASS |
| TC-07 | Teardown hook best-effort | ✅ PASS |
| TC-08 | Multiple sessions | ✅ PASS |
| TC-09 | Invalid spec validation | ✅ PASS |
| TC-10 | WorkspaceError diagnostics | ✅ PASS |

**Overall:** ✅ All 10 UAT test cases pass. Slice S04 delivers complete Workspace lifecycle orchestration with reference counting.
