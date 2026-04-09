---
estimated_steps: 20
estimated_files: 3
skills_used: []
---

# T01: Implement AgentManager in pkg/agentd

Create `pkg/agentd/agent.go` with an `AgentManager` type that wraps `meta.Store` and provides Create/Get/GetByRoomName/List/UpdateState/Delete with domain error types. Add `pkg/agentd/agent_test.go` with unit tests covering the full lifecycle and all error paths. AgentManager mirrors the SessionManager pattern established in session.go.

Design constraints:
- AgentManager uses `meta.AgentState` directly (not meta.SessionState) per D070
- Delete enforces `stopped` precondition — returns `ErrDeleteNotStopped` if agent state is not stopped
- Error types: `ErrAgentNotFound`, `ErrDeleteNotStopped`, `ErrAgentAlreadyExists`
- `ErrAgentAlreadyExists` wraps the unique-violation message from `meta.Store.CreateAgent` (checks for `already exists` substring)
- Uses `slog` with `component=agentd.agent` for structured logging (same pattern as session.go)
- `UpdateState` calls `store.UpdateAgent(ctx, id, state, errorMessage, nil)` — no label mutation here
- `Delete` validates stopped state before calling `store.DeleteAgent`
- `Create` sets default state to `AgentStateCreated` if empty (S03 uses synchronous create — state=created immediately, not creating; async creating state is S04)

Unit tests in `pkg/agentd/agent_test.go` must cover:
1. Create round-trip: create → get, verify fields
2. GetByRoomName: create → get by room+name
3. List with state filter
4. List with room filter
5. UpdateState: create → updateState → get, verify state
6. Delete requires stopped: create → updateState(stopped) → delete succeeds
7. Delete protected: create (state=created) → delete returns ErrDeleteNotStopped
8. Get not found: returns nil, nil
9. Delete not found: returns ErrAgentNotFound

## Inputs

- ``pkg/meta/models.go` — meta.Agent struct, meta.AgentState constants, meta.AgentFilter`
- ``pkg/meta/agent.go` — Store.CreateAgent/GetAgent/GetAgentByRoomName/ListAgents/UpdateAgent/DeleteAgent`
- ``pkg/agentd/session.go` — SessionManager pattern to mirror (struct, constructor, method signatures, error types, slog usage)`

## Expected Output

- ``pkg/agentd/agent.go` — new file: AgentManager struct, NewAgentManager constructor, Create/Get/GetByRoomName/List/UpdateState/Delete methods, ErrAgentNotFound/ErrDeleteNotStopped/ErrAgentAlreadyExists error types`
- ``pkg/agentd/agent_test.go` — new file: 9 unit tests covering full CRUD lifecycle and error paths`

## Verification

go test ./pkg/agentd/... -count=1 -timeout 60s -run TestAgent
