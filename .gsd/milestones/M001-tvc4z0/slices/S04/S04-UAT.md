# S04: Session Manager — UAT

**Milestone:** M001-tvc4z0
**Written:** 2026-04-03T03:24:47.002Z

# S04 UAT: Session Manager CRUD + State Machine

## Preconditions
1. agentd daemon compiled successfully
2. Go test environment available
3. pkg/meta and pkg/agentd packages built

## Test Cases

### TC01: SessionState Constants Verify Design Doc Values
**Purpose:** Confirm SessionState constants match state machine lifecycle design

**Steps:**
1. Review pkg/meta/models.go SessionState constants
2. Verify constants exist: created, running, paused:warm, paused:cold, stopped
3. Verify old constants (running, stopped, paused, error from pre-S04) are replaced

**Expected Outcome:**
- Five SessionState constants defined with correct values
- Colon notation used for sub-states (paused:warm, paused:cold)
- Comments document each state's meaning

---

### TC02: SessionManager CRUD Round-Trip
**Purpose:** Verify Create/Get/List/Update/Delete operations work end-to-end

**Steps:**
1. Create SessionManager with in-memory Store
2. Create session with workspace_id and runtime_class
3. Get session by ID — verify state="created"
4. List sessions — verify created session appears
5. Update session state to "running"
6. Get session — verify state="running"
7. Update session state to "stopped"
8. Delete session
9. Get session — verify nil (deleted)

**Expected Outcome:**
- Create succeeds, returns session with state="created"
- Get returns correct session data
- List includes created session
- Update transitions state correctly
- Delete succeeds after session in "stopped" state
- Get returns nil after deletion

---

### TC03: State Machine Valid Transitions
**Purpose:** Verify all 9 valid state transitions work

**Steps:**
1. For each valid transition, create session and verify transition succeeds:
   - created → running (start)
   - created → stopped (cancel before start)
   - running → paused:warm (pause)
   - running → stopped (stop)
   - paused:warm → running (resume)
   - paused:warm → paused:cold (checkpoint)
   - paused:warm → stopped
   - paused:cold → running (restore)
   - paused:cold → stopped

**Expected Outcome:**
- All 9 transitions succeed without error
- Session state updates to target state after each transition

---

### TC04: State Machine Invalid Transitions Blocked
**Purpose:** Verify invalid transitions are rejected with meaningful errors

**Steps:**
1. For each invalid transition, attempt transition and verify rejection:
   - created → paused:warm (invalid, must start first)
   - running → created (invalid, can't go backwards)
   - stopped → running (invalid, terminal state)
   - paused:cold → paused:warm (invalid)
   - created → paused:cold (invalid)

**Expected Outcome:**
- All invalid transitions return ErrInvalidTransition error
- Error message includes session_id, from_state, to_state
- Error message includes valid_transitions list for debugging

---

### TC05: Delete Protection for Active Sessions
**Purpose:** Verify delete is blocked for running and paused:warm sessions

**Steps:**
1. Create session, transition to "running"
2. Attempt Delete — verify ErrDeleteProtected error
3. Transition to "paused:warm"
4. Attempt Delete — verify ErrDeleteProtected error
5. Transition to "paused:cold"
6. Attempt Delete — verify success (paused:cold is deletable)
7. Create new session, keep in "created" state
8. Delete — verify success (created is deletable)

**Expected Outcome:**
- Delete blocked for "running" state with ErrDeleteProtected
- Delete blocked for "paused:warm" state with ErrDeleteProtected
- Delete succeeds for "paused:cold" state
- Delete succeeds for "created" state
- Delete succeeds for "stopped" state

---

### TC06: Transition Method for Process Manager Integration
**Purpose:** Verify Transition method works as Process Manager entrypoint

**Steps:**
1. Create session
2. Call Transition(ctx, sessionID, SessionStateRunning)
3. Verify state="running"
4. Call Transition(ctx, sessionID, SessionStateStopped)
5. Verify state="stopped"

**Expected Outcome:**
- Transition method delegates to Update correctly
- State transitions succeed for valid targets
- Invalid transitions return ErrInvalidTransition

---

### TC07: List Filtering Works
**Purpose:** Verify SessionFilter parameter filters session list

**Steps:**
1. Create multiple sessions with different labels (e.g., env=prod, env=dev)
2. List with filter for env=prod
3. Verify only matching sessions returned
4. List with nil filter
5. Verify all sessions returned

**Expected Outcome:**
- Label filter returns only matching sessions
- Nil filter returns all sessions

---

### TC08: Structured Logging for Observability
**Purpose:** Verify session lifecycle events are logged with component=agentd.session

**Steps:**
1. Run tests with verbose logging enabled
2. Create session — verify INFO log with component=agentd.session, session_id, state
3. Transition state — verify INFO log with from_state, to_state
4. Attempt invalid transition — verify WARN log
5. Attempt delete on active session — verify WARN log with "delete blocked"

**Expected Outcome:**
- All session lifecycle events logged with component=agentd.session
- State transitions logged with from_state and to_state
- Protection events logged at WARN level with reason

---

## Edge Cases Covered

1. **Get/Update/Delete non-existent session** — Returns error "session does not exist"
2. **Create with invalid initial state** — Returns error "new session must start in 'created' state"
3. **Terminal state transitions** — "stopped" state has no valid transitions, empty ValidTransitions slice returned
4. **Empty labels** — Handled correctly, nil/empty labels stored and retrieved properly

## Verification Command

```bash
go test ./pkg/agentd/... ./pkg/meta/... -v
```

Expected: All 45 tests pass (27 meta + 18 agentd)
