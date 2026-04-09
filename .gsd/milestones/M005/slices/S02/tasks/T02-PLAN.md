---
estimated_steps: 38
estimated_files: 6
skills_used: []
---

# T02: Add agent_id FK to sessions, converge SessionManager to 5-state agent model, remove paused:*

Add `sessions.agent_id` FK column pointing at agents.id. Add `Session.AgentID` field to the Go model. Update `CreateSession` / `GetSession` / `ListSessions` to include the new column. Converge `SessionManager.validTransitions` (pkg/agentd/session.go) to the 5-state agent model: creating→created/error, created→running/stopped, running→created/stopped/error, stopped→creating (restart), error is terminal. Remove `paused:warm` and `paused:cold` from both `models.go` constants and `validTransitions`. Update `deleteProtectedStates` to cover `running` and `creating`. Update all tests in `pkg/agentd/session_test.go` and `pkg/meta/session_test.go` that referenced paused:* states.

## Steps

1. Open `pkg/meta/schema.sql`. Append schema v4:
   ```sql
   -- Schema v4: sessions.agent_id FK
   ALTER TABLE sessions ADD COLUMN agent_id TEXT DEFAULT '' REFERENCES agents(id) ON DELETE SET NULL;
   CREATE INDEX IF NOT EXISTS idx_sessions_agent_id ON sessions(agent_id);
   INSERT OR IGNORE INTO schema_version (version) VALUES (4);
   ```

2. Open `pkg/meta/models.go`. Remove `SessionStatePausedWarm` and `SessionStatePausedCold` constants (the TODO left by T01). Add `AgentID string` field to `Session` struct.

3. Open `pkg/meta/session.go`. Update `CreateSession` to include `agent_id` in the INSERT. Update `GetSession` and `ListSessions` row scans to read `agent_id`. Update `SessionFilter` struct to include optional `AgentID string` field. Add `agent_id = ?` filtering in `ListSessions`.

4. Open `pkg/agentd/session.go`. Replace `validTransitions` map to match the 5-state agent model:
   ```go
   // creating -> created (bootstrap ok) or error (bootstrap fail)
   meta.AgentStateCreating: {meta.AgentStateCreated, meta.AgentStateError},
   // created -> running (prompt) or stopped (agent/stop while idle)
   meta.AgentStateCreated: {meta.AgentStateRunning, meta.AgentStateStopped},
   // running -> created (turn done), stopped (agent/stop), error (runtime failure)
   meta.AgentStateRunning: {meta.AgentStateCreated, meta.AgentStateStopped, meta.AgentStateError},
   // stopped -> creating (agent/restart)
   meta.AgentStateStopped: {meta.AgentStateCreating},
   // error is terminal
   meta.AgentStateError: {},
   ```
   **Important**: the SessionManager currently operates on `meta.SessionState`. Since `meta.Agent` is now a separate type with `meta.AgentState`, the SessionManager should continue to use `meta.SessionState` for sessions. The agent state machine will live in a new `AgentManager` struct (introduced in S03). For this slice, the task is:
   a. Update `validTransitions` to remove paused:warm/paused:cold and add the analogous new states that sessions can be in now. The sessions table is an internal implementation — its states mirror the agent's states but use `meta.SessionState`. After removing paused:*, the session states that remain are: `created`, `running`, `stopped` (SessionStateCreated/Running/Stopped already exist). Add two new SessionState constants: `SessionStateCreating` and `SessionStateError`.
   b. Update `validTransitions` for the `SessionManager` to mirror the agent state machine: creating→created/error, created→running/stopped, running→created/stopped/error, stopped→creating, error is terminal.
   c. Update `deleteProtectedStates` to include `running` and `creating` (remove paused:warm).

5. Open `pkg/meta/models.go`. Add `SessionStateCreating SessionState = "creating"` and `SessionStateError SessionState = "error"` constants.

6. Update `pkg/agentd/session_test.go`:
   - Remove all test cases that reference `meta.SessionStatePausedWarm` or `meta.SessionStatePausedCold`.
   - Add test cases for the new transitions: `creating→created`, `creating→error`, `running→created`, `running→error`, `stopped→creating`, `error is terminal`.
   - Update `TestSessionManagerInvalidTransitions` to verify paused:* are rejected.
   - Update `TestIsValidTransition` to cover new states.

7. Update `pkg/meta/session_test.go`:
   - Remove test cases that create sessions with `SessionStatePausedWarm` / `SessionStatePausedCold`.
   - Fix `TestListSessionsFiltering` to not use paused:warm as a filter state.

8. Run `go test ./pkg/meta/... ./pkg/agentd/... -count=1` and confirm all tests pass.

## Inputs

- ``pkg/meta/schema.sql` — v3 agents table from T01 output`
- ``pkg/meta/models.go` — Agent/AgentState from T01; paused:* TODO comment`
- ``pkg/meta/agent.go` — Agent CRUD from T01`
- ``pkg/meta/session.go` — existing CRUD to extend with agent_id`
- ``pkg/meta/session_test.go` — existing tests to update`
- ``pkg/agentd/session.go` — existing SessionManager and validTransitions`
- ``pkg/agentd/session_test.go` — existing tests to update for new state machine`

## Expected Output

- ``pkg/meta/schema.sql` — sessions.agent_id column + index + schema v4 record appended`
- ``pkg/meta/models.go` — paused:* removed; SessionStateCreating and SessionStateError added; Session.AgentID field added`
- ``pkg/meta/session.go` — CreateSession/GetSession/ListSessions handle agent_id column; SessionFilter gains AgentID field`
- ``pkg/meta/session_test.go` — updated to remove paused:* usage; no compilation errors`
- ``pkg/agentd/session.go` — validTransitions updated to 5-state model; deleteProtectedStates updated`
- ``pkg/agentd/session_test.go` — updated to cover new states; paused:* test cases replaced with creating/error variants`

## Verification

go test ./pkg/meta/... ./pkg/agentd/... -count=1 2>&1; echo exit=$?
