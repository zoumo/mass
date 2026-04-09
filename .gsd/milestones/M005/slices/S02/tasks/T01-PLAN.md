---
estimated_steps: 56
estimated_files: 4
skills_used: []
---

# T01: Add Agent model, AgentState, and agents table CRUD to pkg/meta

Add the `meta.Agent` struct, `meta.AgentState` type with the five legal states (creating/created/running/stopped/error), a schema v3 migration adding the `agents` table with room+name UNIQUE constraint, and full CRUD methods on `meta.Store`. Write unit tests for the new CRUD operations covering the normal path, unique constraint, FK guard on room, and list filtering.

## Steps

1. Open `pkg/meta/models.go`. Add:
   - `AgentState` type string and constants: `AgentStateCreating`, `AgentStateCreated`, `AgentStateRunning`, `AgentStateStopped`, `AgentStateError`.
   - `Agent` struct with fields: `ID string`, `Room string`, `Name string`, `RuntimeClass string`, `WorkspaceID string`, `Description string`, `SystemPrompt string`, `Labels map[string]string`, `State AgentState`, `ErrorMessage string`, `CreatedAt time.Time`, `UpdatedAt time.Time`.
   - Remove `SessionStatePausedWarm` and `SessionStatePausedCold` constants from `SessionState`. **IMPORTANT**: these constants are still referenced in `pkg/agentd/session.go` and its tests — leave the constants in place in this task and leave a TODO comment; T02 will remove them after updating all callers.

2. Open `pkg/meta/schema.sql`. Append a schema v3 block:
   ```sql
   -- Schema v3: agents table
   CREATE TABLE IF NOT EXISTS agents (
       id TEXT PRIMARY KEY,
       room TEXT NOT NULL,
       name TEXT NOT NULL,
       runtime_class TEXT NOT NULL,
       workspace_id TEXT NOT NULL,
       description TEXT DEFAULT '',
       system_prompt TEXT DEFAULT '',
       labels TEXT DEFAULT '{}',
       state TEXT NOT NULL DEFAULT 'creating',
       error_message TEXT DEFAULT '',
       created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
       updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
       UNIQUE(room, name),
       FOREIGN KEY (room) REFERENCES rooms(name) ON DELETE RESTRICT,
       FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE RESTRICT
   );
   CREATE INDEX IF NOT EXISTS idx_agents_room ON agents(room);
   CREATE INDEX IF NOT EXISTS idx_agents_state ON agents(state);
   CREATE INDEX IF NOT EXISTS idx_agents_room_name ON agents(room, name);
   CREATE TRIGGER IF NOT EXISTS trg_agents_updated
       AFTER UPDATE ON agents
       FOR EACH ROW WHEN OLD.updated_at = NEW.updated_at
       BEGIN
           UPDATE agents SET updated_at = CURRENT_TIMESTAMP WHERE id = NEW.id;
       END;
   INSERT OR IGNORE INTO schema_version (version) VALUES (3);
   ```

3. Create `pkg/meta/agent.go`. Implement:
   - `AgentFilter` struct (State, Room fields).
   - `CreateAgent(ctx, *Agent) error` — validates ID, Room, Name, RuntimeClass, WorkspaceID; sets default state to creating.
   - `GetAgent(ctx, id string) (*Agent, error)` — returns nil if not found.
   - `GetAgentByRoomName(ctx, room, name string) (*Agent, error)` — looks up by UNIQUE(room,name).
   - `ListAgents(ctx, *AgentFilter) ([]*Agent, error)` — filters by State and/or Room; ordered by created_at DESC.
   - `UpdateAgent(ctx, id string, state AgentState, errorMessage string, labels map[string]string) error` — partial update; returns error if not found.
   - `DeleteAgent(ctx, id string) error` — returns error if not found.

4. Create `pkg/meta/agent_test.go`. Write tests:
   - `TestAgentCRUDRoundTrip` — create, get, update state, delete.
   - `TestAgentGetByRoomName` — create agent, retrieve by room+name.
   - `TestAgentUniqueRoomName` — two agents with same room+name should fail.
   - `TestAgentFKConstraintRoom` — agent with non-existent room should fail.
   - `TestAgentFKConstraintWorkspace` — agent with non-existent workspace should fail.
   - `TestListAgentsFiltering` — filter by state, filter by room.
   - `TestAgentUpdateNonExistent` — update non-existent ID returns error.
   - `TestAgentDeleteNonExistent` — delete non-existent ID returns error.
   - `TestSchemav3AgentsTableExists` — verify agents table, indexes, and triggers exist after NewStore.

5. Run `go test ./pkg/meta/... -count=1` and confirm all tests pass (including the existing 26+).

## Inputs

- ``pkg/meta/models.go` — existing SessionState/Session/Workspace/Room types`
- ``pkg/meta/schema.sql` — existing schema v1+v2; append v3 block here`
- ``pkg/meta/store.go` — Store type to hang new CRUD methods on`
- ``pkg/meta/store_test.go` — newTestStore helper to reuse in agent_test.go`
- ``pkg/meta/session.go` — CRUD pattern to follow for agent.go`

## Expected Output

- ``pkg/meta/models.go` — AgentState constants + Agent struct added; paused:* constants get TODO but are not removed yet`
- ``pkg/meta/schema.sql` — agents table + indexes + trigger + schema v3 version record appended`
- ``pkg/meta/agent.go` — AgentFilter + all 6 CRUD methods on Store`
- ``pkg/meta/agent_test.go` — ≥9 tests covering normal path, constraint violations, filtering`

## Verification

go test ./pkg/meta/... -count=1 -v 2>&1 | tail -30; echo exit=$?
