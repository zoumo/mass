# S02: EmptyDir + Local Handlers — UAT

**Milestone:** M001-tlbeko
**Written:** 2026-04-02T18:03:56.786Z

# S02 UAT: EmptyDir + Local Handlers

**Prerequisites:**
- Go toolchain installed
- Working directory: `/Users/jim/code/zoumo/open-agent-runtime`
- Workspace package tests passing

## Test Cases

### TC1: EmptyDirHandler creates empty directory
**Steps:**
1. Create test source with `Type: SourceTypeEmptyDir`
2. Call `handler.Prepare(ctx, source, targetDir)` with a fresh targetDir
3. Verify `os.Stat(targetDir)` shows directory exists
4. Verify returned path equals targetDir

**Expected:** Directory created at targetDir, returned path matches targetDir

### TC2: EmptyDirHandler rejects non-emptyDir sources
**Steps:**
1. Create test source with `Type: SourceTypeGit`
2. Call `handler.Prepare(ctx, source, targetDir)`
3. Verify error contains "EmptyDirHandler cannot handle source type"

**Expected:** Error returned with correct message format

### TC3: EmptyDirHandler handles nested paths
**Steps:**
1. Call `handler.Prepare(ctx, source, "/tmp/nested/deep/path")`
2. Verify all intermediate directories created

**Expected:** Nested directory structure created successfully

### TC4: EmptyDirHandler handles existing directory
**Steps:**
1. Pre-create directory with `os.MkdirAll(targetDir, 0755)`
2. Call `handler.Prepare(ctx, source, targetDir)`
3. Verify no error, directory still exists

**Expected:** No error, existing directory preserved

### TC5: LocalHandler validates existing directory
**Steps:**
1. Create test source with `Type: SourceTypeLocal`, valid path
2. Create directory with `os.MkdirAll(source.Local.Path, 0755)`
3. Call `handler.Prepare(ctx, source, anyTargetDir)`
4. Verify returned path equals source.Local.Path (NOT targetDir)

**Expected:** Path validated, returns source.Local.Path

### TC6: LocalHandler rejects non-local sources
**Steps:**
1. Create test source with `Type: SourceTypeGit`
2. Call `handler.Prepare(ctx, source, targetDir)`
3. Verify error contains "LocalHandler cannot handle source type"

**Expected:** Error returned with correct message format

### TC7: LocalHandler rejects non-existent paths
**Steps:**
1. Create test source with `Type: SourceTypeLocal`, non-existent path
2. Call `handler.Prepare(ctx, source, targetDir)`
3. Verify error contains "does not exist"

**Expected:** Error returned indicating path doesn't exist

### TC8: LocalHandler rejects file paths
**Steps:**
1. Create test file with `os.Create(tempPath)`
2. Create test source with `Type: SourceTypeLocal`, path = tempPath
3. Call `handler.Prepare(ctx, source, targetDir)`
4. Verify error contains "is not a directory"

**Expected:** Error returned indicating path is not a directory

### TC9: Verify unmanaged semantics
**Steps:**
1. Create local source with existing directory path `/tmp/existing`
2. Call `handler.Prepare(ctx, source, "/tmp/target")`
3. Verify returned path is `/tmp/existing` (NOT `/tmp/target`)
4. Verify `/tmp/target` was NOT created

**Expected:** Returns source.Local.Path, targetDir ignored

### TC10: Full test suite verification
**Steps:**
1. Run: `go test ./pkg/workspace/... -v -count=1`
2. Verify all 60 tests pass
3. Verify EmptyDir tests: TestEmptyDirHandlerRejectsNonEmptyDirSource (3 subtests), TestEmptyDirHandlerIntegration (4 subtests)
4. Verify Local tests: TestLocalHandlerRejectsNonLocalSource (3 subtests), TestLocalHandlerPathDoesNotExist, TestLocalHandlerPathIsFile, TestLocalHandlerIntegration (5 subtests)

**Expected:** All tests pass, correct test counts

## Execution Record

| TC | Status | Notes |
|----|--------|-------|
| TC1 | ✅ PASS | Verified via TestEmptyDirHandlerIntegration/creates_empty_directory |
| TC2 | ✅ PASS | Verified via TestEmptyDirHandlerRejectsNonEmptyDirSource (3 subtests) |
| TC3 | ✅ PASS | Verified via TestEmptyDirHandlerIntegration/creates_nested_directories |
| TC4 | ✅ PASS | Verified via TestEmptyDirHandlerIntegration/handles_existing_directory |
| TC5 | ✅ PASS | Verified via TestLocalHandlerIntegration/returns_source_path_not_targetDir |
| TC6 | ✅ PASS | Verified via TestLocalHandlerRejectsNonLocalSource (3 subtests) |
| TC7 | ✅ PASS | Verified via TestLocalHandlerPathDoesNotExist |
| TC8 | ✅ PASS | Verified via TestLocalHandlerPathIsFile |
| TC9 | ✅ PASS | Verified via TestLocalHandlerIntegration/returns_source_path_not_targetDir |
| TC10 | ✅ PASS | Full suite: 60 tests pass, correct counts |

**UAT Status: PASS**
