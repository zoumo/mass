# S02: Metadata Store (SQLite) — UAT

**Milestone:** M001-tvc4z0
**Written:** 2026-04-03T02:12:09.861Z

# S02 UAT: Metadata Store (SQLite)

## Preconditions
1. Go toolchain installed (go 1.21+)
2. agentd binary built: `go build -o bin/agentd ./cmd/agentd`
3. Test environment: macOS or Linux with Unix socket support

## Test Cases

### TC01: Store Initialization and Schema Creation
**Purpose:** Verify Store creates database with correct schema

**Steps:**
1. Run: `go test ./pkg/meta/... -v -run TestNewStore`
2. Verify: Test passes, database file created in temp directory
3. Check: Schema version is 1, WAL journal mode enabled, foreign keys enabled

**Expected:** All 3 sub-tests pass (TestNewStore, TestNewStoreInvalidPath, TestNewStoreEmptyPath)

### TC02: Session CRUD Operations
**Purpose:** Verify Session create/read/update/delete works correctly

**Steps:**
1. Run: `go test ./pkg/meta/... -v -run TestSessionCRUD`
2. Run: `go test ./pkg/meta/... -v -run TestSessionFKConstraint`
3. Verify: CRUD round-trip succeeds, foreign key constraint prevents session without workspace

**Expected:** 7 session tests pass

### TC03: Workspace Reference Counting
**Purpose:** Verify Acquire/Release reference counting prevents premature cleanup

**Steps:**
1. Run: `go test ./pkg/meta/... -v -run TestWorkspaceRefCounting`
2. Run: `go test ./pkg/meta/... -v -run TestWorkspaceCannotDeleteWithRefs`
3. Verify: Acquire increments ref_count, Release decrements, delete fails with refs > 0

**Expected:** 10 workspace tests pass, ref counting behavior verified

### TC04: Room CRUD Operations
**Purpose:** Verify Room create/read/delete with session foreign key

**Steps:**
1. Run: `go test ./pkg/meta/... -v -run TestRoomCRUD`
2. Run: `go test ./pkg/meta/... -v -run TestRoomDeleteWithSessions`
3. Verify: CRUD round-trip succeeds, delete fails when sessions reference room

**Expected:** 9 room tests pass

### TC05: Transaction Rollback
**Purpose:** Verify BeginTx and rollback work correctly

**Steps:**
1. Run: `go test ./pkg/meta/... -v -run TestSessionTransactionRollback`
2. Run: `go test ./pkg/meta/... -v -run TestWorkspaceTransactionRollback`
3. Verify: Transaction rollback leaves database unchanged after intentional error

**Expected:** Transaction tests pass, rollback verified

### TC06: Daemon Lifecycle with Store
**Purpose:** Verify agentd initializes Store and closes on shutdown

**Steps:**
1. Run: `go test ./pkg/meta/... -v -run TestIntegration -tags integration`
2. Verify: TestIntegrationStoreInitWithAgentd passes
   - agentd starts with metaDB configured
   - database file created
   - SIGTERM triggers shutdown
   - Store.Close() called

**Expected:** Integration tests pass (2 tests)

### TC07: Daemon without Store (Ephemeral Mode)
**Purpose:** Verify daemon starts when metaDB not configured

**Steps:**
1. Run: `go test ./pkg/meta/... -v -run TestIntegrationStoreNotConfigured -tags integration`
2. Verify: Daemon starts, logs "metadata store not configured"
3. Verify: Shutdown completes without Store.Close()

**Expected:** Test passes, ephemeral mode works

### TC08: Full Test Suite
**Purpose:** Comprehensive verification of all metadata operations

**Steps:**
1. Run: `go test ./pkg/meta/... -v`
2. Verify: All 26+ tests pass without errors

**Expected:** PASS status, no test failures

## Edge Cases Covered
- Empty path creates in-memory database (TestNewStoreEmptyPath)
- Invalid path returns error (TestNewStoreInvalidPath)
- Non-existent entity Get/Delete returns error
- Duplicate workspace ID fails
- Non-active workspace Acquire fails (status != 'active')
- Empty labels JSON handled correctly

## Post-Test Verification
1. Build verification: `go build -o bin/agentd ./cmd/agentd` succeeds
2. No diagnostics errors: `go test ./pkg/meta/...` shows PASS
3. R003 (Metadata Store persistence) validated by test coverage
