# S04: Session Manager

**Goal:** Session Manager CRUD works with state machine validation for session lifecycle transitions (Created → Running → Paused:Warm → Paused:Cold → Stopped), preventing invalid transitions and Delete on active sessions.
**Demo:** After this: Session Manager CRUD works, state machine transitions verified

## Tasks
- [x] **T01: Updated SessionState constants to match state machine lifecycle design** — Align SessionState constants in pkg/meta/models.go with design doc specification. Change from running/stopped/paused/error to created/running/paused:warm/paused:cold/stopped. Update all tests in pkg/meta/session_test.go to use new constants. Re-run tests to verify persistence layer works with new state values.
  - Estimate: 30m
  - Files: pkg/meta/models.go, pkg/meta/session_test.go
  - Verify: go test ./pkg/meta/... -v
- [x] **T02: Created SessionManager with CRUD operations and state machine validation for session lifecycle transitions** — Create pkg/agentd/session.go with SessionManager struct wrapping pkg/meta Store. Implement Create/Get/List/Update/Delete methods. Add state machine validation: define valid transitions table, validate on UpdateSession, return error for invalid transitions. Implement Delete protection: check state before deletion, return error if session is running or paused:warm. Add Transition method for Process Manager integration. Create comprehensive tests in pkg/agentd/session_test.go covering CRUD round-trips, valid transitions, invalid transitions, Delete protection.
  - Estimate: 1h
  - Files: pkg/agentd/session.go, pkg/agentd/session_test.go
  - Verify: go test ./pkg/agentd/... -v
