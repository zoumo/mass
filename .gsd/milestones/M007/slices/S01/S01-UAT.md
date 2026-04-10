# S01: Storage + Model Foundation — UAT

**Milestone:** M007
**Written:** 2026-04-09T20:20:30.757Z

# S01 UAT: Storage + Model Foundation

## Preconditions

- `go build ./...` passes (green codebase)
- bbolt is the only metadata dependency (`go-sqlite3` not in `go.mod`)
- `bin/agent-shim` binary present (for process_test subset)

---

## TC-01: bbolt Store Open/Close Lifecycle

**What:** Verify the store opens cleanly, creates bucket hierarchy, and closes without leaks.

**Steps:**
1. Run `go test ./pkg/meta/... -run TestNewStore -v -count=1`
2. Observe: TestNewStore_OpenClose, TestNewStore_ReopenExisting, TestNewStore_BucketsCreated, TestNewStore_PathAttribute, TestNewStore_InvalidPath all pass
3. Check that reopening an existing DB file succeeds without error (TestNewStore_ReopenExisting)
4. Check that opening an invalid path (non-existent directory) returns a non-nil error (TestNewStore_InvalidPath)

**Expected:** All 5 store tests pass. No goroutine or file-descriptor leaks.

---

## TC-02: Workspace CRUD via bbolt

**What:** Verify full CRUD lifecycle for Workspace objects.

**Steps:**
1. Run `go test ./pkg/meta/... -run 'TestCreateWorkspace|TestGetWorkspace|TestListWorkspaces|TestUpdateWorkspaceStatus|TestDeleteWorkspace' -v -count=1`
2. Verify:
   - TestCreateWorkspace: creates and retrieves a workspace by name
   - TestCreateWorkspace_Duplicate: second create with same name returns an error
   - TestGetWorkspace_NotFound: returns error for unknown name
   - TestListWorkspaces_FilterByPhase: only returns workspaces matching the given phase
   - TestUpdateWorkspaceStatus: phase transitions (pending→ready) reflected on get
   - TestDeleteWorkspace: workspace gone after delete
   - TestDeleteWorkspace_WithAgents: delete fails when agents exist in the workspace

**Expected:** All 11 workspace tests pass.

---

## TC-03: Agent CRUD with (workspace, name) Identity

**What:** Verify Agent CRUD using composite (workspace, name) identity — no UUID.

**Steps:**
1. Run `go test ./pkg/meta/... -run 'TestCreateAgent|TestGetAgent|TestListAgents|TestUpdateAgentStatus|TestDeleteAgent' -v -count=1`
2. Verify:
   - TestCreateAgent: create succeeds; agent retrievable by (workspace, name)
   - TestCreateAgent_DuplicateRejected: second create with same workspace+name returns error
   - TestGetAgent_NotFound: wrong name returns not-found error
   - TestGetAgent_NoWorkspaceBucket: wrong workspace returns not-found error (no bucket)
   - TestGetAgent_ByWorkspaceName: agent returned with correct workspace field
   - TestListAgents_AllWorkspaces: scanning across all workspaces returns all agents
   - TestListAgents_FilterByWorkspace: only agents in target workspace returned
   - TestListAgents_FilterByState: only agents with target spec.Status returned
   - TestDeleteAgent_SameName_DifferentWorkspace: agents in different workspaces are independent

**Expected:** All 18 agent tests pass.

---

## TC-04: spec.Status Unification — No StatusCreated, StatusIdle is canonical idle state

**What:** Verify StatusCreated is gone and StatusIdle="idle" is the post-handshake state.

**Steps:**
1. Run `rg 'StatusCreated' --type go pkg/spec/ pkg/runtime/`
2. Verify: zero matches
3. Run `grep 'StatusIdle\|StatusError' pkg/spec/state_types.go`
4. Verify: `StatusIdle Status = "idle"` and `StatusError Status = "error"` are present
5. Run `go test ./pkg/spec/... ./pkg/runtime/... -v -count=1 -run 'TestCreate_ReachesCreatedState|TestStatus'`
6. Verify: TestCreate_ReachesCreatedState passes with StatusIdle assertion (not StatusCreated)

**Expected:** Zero StatusCreated references; StatusIdle and StatusError present; runtime tests pass.

---

## TC-05: pkg/runtime Writes "idle" to state.json

**What:** Verify state.json emits "idle" (not "created") after ACP handshake and after each prompt.

**Steps:**
1. Run `go test ./pkg/runtime/... -v -count=1 -run 'TestRuntimeSuite' -timeout 60s`
2. Observe test output for state transitions
3. Run `grep -n 'StatusIdle\|StatusCreated' pkg/runtime/runtime.go`
4. Verify: two occurrences of StatusIdle (bootstrap-complete, prompt-completed); zero StatusCreated

**Expected:** TestRuntimeSuite passes (48 tests); only StatusIdle references in runtime.go.

---

## TC-06: No SQLite / No Legacy References Anywhere

**What:** Verify the banned reference set is globally zero.

**Steps:**
1. `rg 'meta\.AgentState|meta\.SessionState|go-sqlite3' --type go` → must exit 1 (zero matches)
2. `rg 'meta\.Session[^S]' --type go` → must exit 1 (zero matches)
3. `rg 'SessionManager' --type go` → must exit 1 (zero matches)
4. `grep 'mattn/go-sqlite3' go.mod` → must return empty
5. `ls pkg/meta/schema.sql pkg/meta/session.go pkg/meta/room.go 2>&1` → all "No such file"

**Expected:** All five checks confirm deletion is complete.

---

## TC-07: Full Codebase Compilation

**What:** Verify go build ./... is green with the new type system.

**Steps:**
1. Run `go build ./...`
2. Verify: exit code 0, no output

**Expected:** Clean build across all packages.

---

## TC-08: pkg/agentd Agent Identity and Recovery Tests

**What:** Verify AgentManager and RecoverSessions work with (workspace,name) identity.

**Steps:**
1. Run `go test ./pkg/agentd/... -run '^Test(Agent|RecoverSessions|RecoveryPhase|IsRecovering|GenerateConfig)' -v -count=1 -timeout 30s`
2. Verify:
   - TestAgentManagerCreate: agent created with StatusCreating, (workspace,name) stored
   - TestAgentManagerGet: retrieves by (workspace,name)
   - TestAgentManagerDelete: blocks on non-stopped agents
   - TestRecoverSessions: creating-phase agents marked StatusError, not StatusStopped
   - TestRecoveryPhaseTransition: phase transitions idle→recovering→complete

**Expected:** All tests in those patterns pass.

---

## TC-09: pkg/ari Types and Registry with New Model

**What:** Verify the new ARI types compile and registry uses name-keyed workspaces.

**Steps:**
1. Run `go test ./pkg/ari/... -v -count=1 -timeout 30s`
2. Verify registry tests: Add/Get/Remove by workspace name; Acquire/Release by agentKey
3. Verify types compilation: WorkspaceCreateParams, AgentCreateParams, AgentPromptParams all use Workspace+Name (no UUID)

**Expected:** All 10 pkg/ari tests pass.

---

## TC-10: Edge Case — DeleteWorkspace With Agents Returns Error

**What:** Verify workspace deletion is blocked while agents exist.

**Steps:**
1. Run `go test ./pkg/meta/... -run TestDeleteWorkspace_WithAgents -v -count=1`
2. Observe: test creates a workspace, creates an agent in it, then calls DeleteWorkspace
3. Verify: DeleteWorkspace returns a non-nil error containing workspace name

**Expected:** Test passes; deletion blocked; no data corruption.

---

## Edge Cases Covered by Unit Tests

| Scenario | Test | Expected |
|----------|------|----------|
| Duplicate workspace create | TestCreateWorkspace_Duplicate | Error returned |
| Duplicate agent create | TestCreateAgent_DuplicateRejected | Error returned |
| Get agent from non-existent workspace | TestGetAgent_NoWorkspaceBucket | Not-found error |
| List agents across all workspaces | TestListAgents_AllWorkspaces | All agents returned |
| Agent in workspace A vs B with same name | TestDeleteAgent_SameName_DifferentWorkspace | Independent |
| Update non-existent agent status | TestUpdateAgentStatus_NotFound | Error returned |
| Delete non-existent workspace | TestDeleteWorkspace_NotFound | Error returned |
| Invalid store path | TestNewStore_InvalidPath | Error returned |

