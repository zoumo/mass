# S02: ⬜

**Goal:** Add agents table with room+name unique key and AgentState (creating/created/running/stopped/error), link sessions via agent_id FK, and converge the SessionManager state machine to reject paused:* transitions.
**Demo:** After this: TBD

## Tasks
- [x] **T01: Add Agent model, AgentState, and agents table CRUD to pkg/meta** — 
- [x] **T02: Add agent_id FK to sessions, converge SessionManager to 5-state agent model, remove paused:** — 
  - Files: pkg/meta/schema.sql, pkg/meta/models.go, pkg/meta/session.go, pkg/meta/session_test.go, pkg/agentd/session.go, pkg/agentd/session_test.go
  - Verify: go test ./pkg/meta/... ./pkg/agentd/... -count=1 2>&1; echo exit=$?
