---
estimated_steps: 1
estimated_files: 2
skills_used: []
---

# T02: Create SessionManager with state machine

Create pkg/agentd/session.go with SessionManager struct wrapping pkg/meta Store. Implement Create/Get/List/Update/Delete methods. Add state machine validation: define valid transitions table, validate on UpdateSession, return error for invalid transitions. Implement Delete protection: check state before deletion, return error if session is running or paused:warm. Add Transition method for Process Manager integration. Create comprehensive tests in pkg/agentd/session_test.go covering CRUD round-trips, valid transitions, invalid transitions, Delete protection.

## Inputs

- `pkg/meta/store.go`
- `pkg/meta/session.go`
- `pkg/meta/models.go`

## Expected Output

- `pkg/agentd/session.go`
- `pkg/agentd/session_test.go`

## Verification

go test ./pkg/agentd/... -v

## Observability Impact

SessionManager logs state transitions with component=agentd.session, session_id, from_state, to_state. Delete protection errors logged with session state info.
