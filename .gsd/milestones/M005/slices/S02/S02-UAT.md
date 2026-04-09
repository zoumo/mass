# S02: Schema & State Machine — agents Table and State Convergence — UAT

**Milestone:** M005
**Written:** 2026-04-08T17:27:57.057Z

# S02 UAT: Schema & State Machine — agents Table and State Convergence

## Preconditions

- Go toolchain available (`go version` succeeds)
- Working directory: `/Users/jim/code/zoumo/open-agent-runtime`
- Packages: `pkg/meta` and `pkg/agentd` are buildable (`go build ./pkg/meta/... ./pkg/agentd/...`)

---

## Test Group 1: Agent CRUD Operations (pkg/meta)

### TC-01: Agent CRUD round-trip

**Command:**
```
go test ./pkg/meta/... -run TestAgentCRUDRoundTrip -count=1 -v
```

**Expected:** `--- PASS: TestAgentCRUDRoundTrip` — Create agent, GetAgent returns same fields, UpdateAgent changes state, DeleteAgent returns nil, second GetAgent returns nil (not found).

---

### TC-02: Agent lookup by room+name

**Command:**
```
go test ./pkg/meta/... -run TestAgentGetByRoomName -count=1 -v
```

**Expected:** `--- PASS: TestAgentGetByRoomName` — `GetAgentByRoomName(ctx, room, name)` returns the agent with matching fields.

---

### TC-03: Unique room+name constraint enforced

**Command:**
```
go test ./pkg/meta/... -run TestAgentUniqueRoomName -count=1 -v
```

**Expected:** `--- PASS: TestAgentUniqueRoomName` — Second `CreateAgent` with same room+name returns an error containing "UNIQUE constraint failed" or equivalent.

---

### TC-04: FK constraint — non-existent room

**Command:**
```
go test ./pkg/meta/... -run TestAgentFKConstraintRoom -count=1 -v
```

**Expected:** `--- PASS: TestAgentFKConstraintRoom` — `CreateAgent` with non-existent room fails with a constraint error.

---

### TC-05: FK constraint — non-existent workspace

**Command:**
```
go test ./pkg/meta/... -run TestAgentFKConstraintWorkspace -count=1 -v
```

**Expected:** `--- PASS: TestAgentFKConstraintWorkspace` — `CreateAgent` with non-existent workspace_id fails with a constraint error.

---

### TC-06: List agents with state and room filters

**Command:**
```
go test ./pkg/meta/... -run TestListAgentsFiltering -count=1 -v
```

**Expected:** `--- PASS: TestListAgentsFiltering` — Filter by state returns only matching agents; filter by room returns only agents in that room; empty filter returns all agents.

---

### TC-07: Update and delete non-existent agent return error

**Command:**
```
go test ./pkg/meta/... -run "TestAgentUpdateNonExistent|TestAgentDeleteNonExistent" -count=1 -v
```

**Expected:** Both `--- PASS` — operations on non-existent IDs return non-nil errors.

---

### TC-08: Schema v3 agents table, indexes, and trigger exist

**Command:**
```
go test ./pkg/meta/... -run TestSchemav3AgentsTableExists -count=1 -v
```

**Expected:** `--- PASS: TestSchemav3AgentsTableExists` — All of these are present after `NewStore`: `agents` table, `idx_agents_room`, `idx_agents_state`, `idx_agents_room_name` indexes, and `trg_agents_updated` trigger.

---

## Test Group 2: Session AgentID FK (pkg/meta)

### TC-09: session.agent_id column populated and retrieved

**Command:**
```
go test ./pkg/meta/... -run TestSessionCRUD -count=1 -v
```

**Expected:** `--- PASS: TestSessionCRUD` — Sessions can be created and read back without error; agent_id field is present (may be empty string for sessions without agents).

---

### TC-10: List sessions filtered by AgentID

**Command:**
```
go test ./pkg/meta/... -run TestListSessionsFiltering -count=1 -v
```

**Expected:** `--- PASS: TestListSessionsFiltering` — `SessionFilter{AgentID: "some-id"}` returns only sessions linked to that agent.

---

## Test Group 3: SessionManager 5-State Machine (pkg/agentd)

### TC-11: All valid transitions accepted

**Command:**
```
go test ./pkg/agentd/... -run TestSessionManagerValidTransitions -count=1 -v
```

**Expected:** `--- PASS: TestSessionManagerValidTransitions` with all subtests passing:
- `creating_to_created`
- `creating_to_error`
- `created_to_running`
- `created_to_stopped`
- `running_to_created`
- `running_to_stopped`
- `running_to_error`
- `stopped_to_creating`

---

### TC-12: All invalid transitions rejected

**Command:**
```
go test ./pkg/agentd/... -run TestSessionManagerInvalidTransitions -count=1 -v
```

**Expected:** `--- PASS: TestSessionManagerInvalidTransitions` with all subtests passing, explicitly including:
- `creating_to_paused_warm` — rejected
- `creating_to_paused_cold` — rejected
- `created_to_paused_warm` — rejected
- `running_to_paused_warm` — rejected
- `error_to_creating` — rejected (error is terminal)
- `error_to_created` — rejected
- `error_to_running` — rejected
- `error_to_stopped` — rejected

---

### TC-13: Delete protection on creating and running states

**Command:**
```
go test ./pkg/agentd/... -run TestSessionManagerDeleteProtection -count=1 -v
```

**Expected:** `--- PASS: TestSessionManagerDeleteProtection` — `Delete` on a session in `creating` or `running` state returns `ErrDeleteProtected`. Delete on `created`, `stopped`, or `error` state succeeds.

---

### TC-14: IsValidTransition utility function covers new states

**Command:**
```
go test ./pkg/agentd/... -run TestIsValidTransition -count=1 -v
```

**Expected:** `--- PASS: TestIsValidTransition` — All 5 new states appear in at least one valid transition; paused:warm and paused:cold appear as invalid targets in at least one subtest.

---

## Test Group 4: Full Package Regression

### TC-15: All pkg/meta and pkg/agentd tests pass

**Command:**
```
go test ./pkg/meta/... ./pkg/agentd/... -count=1
```

**Expected:** `ok github.com/open-agent-d/open-agent-d/pkg/meta` and `ok github.com/open-agent-d/open-agent-d/pkg/agentd` — exit code 0, no FAIL lines. Minimum 102 tests passing.

---

### TC-16: Zero paused:* references in production Go code

**Command:**
```
rg 'PausedWarm|PausedCold|paused:warm|paused:cold' --type go
```

**Expected:** Command exits with code 1 (no matches). Any output indicates a regression — paused:* constants must not exist in any .go file.

---

## Edge Cases

### EC-01: Self-transition is a no-op

Create a session in `created` state, call `Transition(created → created)` — should return nil (not `ErrInvalidTransition`). The session remains in `created` state.

### EC-02: Schema migration idempotency

```
go test ./pkg/meta/... -run TestSchemaMigrationIdempotency -count=1 -v
```

Running `NewStore` twice against the same database file should succeed — `CREATE TABLE IF NOT EXISTS` and `INSERT OR IGNORE INTO schema_version` are idempotent.

### EC-03: agent_id NULL FK is valid

A session without an associated agent (`AgentID = ""`) can be created and retrieved without FK constraint errors. The NULL default means the FK is optional — not all sessions must have an agent (backward compatibility).

