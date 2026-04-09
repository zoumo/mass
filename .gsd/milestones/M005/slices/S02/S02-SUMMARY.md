---
id: S02
parent: M005
milestone: M005
provides:
  - ["meta.Agent struct and meta.AgentState type with 5 constants (creating/created/running/stopped/error)", "Store.CreateAgent/GetAgent/GetAgentByRoomName/ListAgents/UpdateAgent/DeleteAgent CRUD methods", "agents table (schema v3) with room+name UNIQUE constraint and FK guards", "sessions.agent_id FK column (schema v4) linking sessions to agents", "meta.Session.AgentID field and SessionFilter.AgentID filter", "meta.SessionStateCreating and meta.SessionStateError constants", "Converged 5-state SessionManager validTransitions — paused:* fully removed and explicitly tested as rejected"]
requires:
  []
affects:
  - ["S03 — consumes meta.Agent types and CRUD for ARI handler migration", "S07 — builds agent recovery on top of room+name identity and agent_id FK"]
key_files:
  - ["pkg/meta/models.go", "pkg/meta/schema.sql", "pkg/meta/agent.go", "pkg/meta/agent_test.go", "pkg/meta/session.go", "pkg/meta/session_test.go", "pkg/agentd/session.go", "pkg/agentd/session_test.go", "pkg/agentd/recovery_test.go"]
key_decisions:
  - ["Paused:warm/paused:cold removed from state machine — replaced by creating/error at both meta.SessionState and meta.AgentState levels (D069, D070)", "SessionManager continues to use meta.SessionState (not meta.AgentState) — AgentManager using meta.AgentState deferred to S03 (D070)", "sessions.agent_id uses DEFAULT NULL for FK column — empty string would violate FK constraints on existing rows (K024)", "Two-task cross-package constant removal strategy: T01 adds TODO, T02 removes after fixing all consumers (K025, D069)", "Self-transitions are no-ops, not errors in SessionManager.Transition", "SessionManager.Create accepts both creating and created as valid initial states"]
patterns_established:
  - ["agent.go CRUD pattern mirrors session.go — new store entities can be scaffolded from this template", "DEFAULT NULL for nullable FK columns (not DEFAULT '')", "Two-task strategy for cross-package state constant removal", "5-state session machine mirrors agent model: creating→created/error, created→running/stopped, running→created/stopped/error, stopped→creating, error terminal"]
observability_surfaces:
  - none
drill_down_paths:
  []
duration: ""
verification_result: passed
completed_at: 2026-04-08T17:27:57.055Z
blocker_discovered: false
---

# S02: Schema & State Machine — agents Table and State Convergence

**Added agents table (schema v3, room+name UNIQUE FK), 5-state Agent model, full CRUD on Store, agent_id FK on sessions (schema v4), SessionManager converged to 5-state machine — paused:* fully removed — 102 tests pass.**

## What Happened

## S02: Schema & State Machine — agents Table and State Convergence

### What Was Built

S02 laid the storage and state-machine foundation for the agent-centric model introduced in M005/S01. The slice produced two main deliverables across two tasks.

**T01 — Agent Model, AgentState, and agents table CRUD (pkg/meta)**

Added `meta.AgentState` type with five constants: `AgentStateCreating`, `AgentStateCreated`, `AgentStateRunning`, `AgentStateStopped`, `AgentStateError`. Added `meta.Agent` struct with all required fields (`ID`, `Room`, `Name`, `RuntimeClass`, `WorkspaceID`, `Description`, `SystemPrompt`, `Labels`, `State`, `ErrorMessage`, `CreatedAt`, `UpdatedAt`).

Schema v3 was appended to `pkg/meta/schema.sql` adding the `agents` table with a `UNIQUE(room, name)` constraint, FK references to `rooms(name)` and `workspaces(id)` (both with `ON DELETE RESTRICT`), three indexes (`idx_agents_room`, `idx_agents_state`, `idx_agents_room_name`), and an `updated_at` trigger.

`pkg/meta/agent.go` was created with `AgentFilter` struct and six CRUD methods on `Store`: `CreateAgent`, `GetAgent`, `GetAgentByRoomName`, `ListAgents`, `UpdateAgent`, `DeleteAgent`. Pattern mirrors the established `session.go` pattern. 9 unit tests in `pkg/meta/agent_test.go` cover: CRUD round-trip, get-by-room-name, unique room+name constraint, FK constraint on non-existent room, FK constraint on non-existent workspace, list filtering (state + room), update non-existent, delete non-existent, and v3 table/index/trigger schema verification.

T01 retained `SessionStatePausedWarm` and `SessionStatePausedCold` with `TODO(T02)` comments to keep the build green while T02 updated all callers across package boundaries (D069).

**T02 — agent_id FK, state machine convergence, paused:* removal**

Schema v4 added `sessions.agent_id TEXT DEFAULT NULL REFERENCES agents(id) ON DELETE SET NULL` plus `idx_sessions_agent_id`. The NULL default was chosen over empty string to avoid FK constraint violations on pre-existing rows (K024).

`meta.Session` gained an `AgentID string` field. `SessionFilter` gained an optional `AgentID string` field. `CreateSession`, `GetSession`, and `ListSessions` were updated to handle the new column using the same NULL pattern as `room`.

`SessionStatePausedWarm` and `SessionStatePausedCold` were removed from `models.go` and replaced with `SessionStateCreating SessionState = "creating"` and `SessionStateError SessionState = "error"`. This mirrors the 5-state agent model at the session (internal) level per D070.

`pkg/agentd/session.go` `validTransitions` was converged to the 5-state model:
- `creating → {created, error}`
- `created → {running, stopped}`
- `running → {created, stopped, error}`
- `stopped → {creating}` (restart)
- `error → {}` (terminal)

`deleteProtectedStates` was updated to cover `creating` and `running`. `SessionManager.Create` was updated to accept both `creating` and `created` as valid initial states (the existing code defaulted to `creating` but callers can provide `created` for recovery).

`pkg/agentd/session_test.go` was rewritten with 8 valid-transition tests, 14 invalid-transition tests (including explicit paused:warm and paused:cold rejection as literals), and delete-protection tests for all 5 states. `pkg/meta/session_test.go` was updated to remove paused:* filter tests and add `agent_id` round-trip coverage. `pkg/agentd/recovery_test.go` and `pkg/agentd/store_test.go` were fixed to accommodate removed constants and new schema version.

### Verification Summary

| Check | Result |
|-------|--------|
| `go test ./pkg/meta/... -count=1` | ✅ pass — 42 tests |
| `go test ./pkg/agentd/... -count=1` | ✅ pass — 60 tests |
| `go test ./pkg/meta/... ./pkg/agentd/... -count=1` | ✅ pass — 102 tests |
| `rg 'PausedWarm\|PausedCold\|paused:warm\|paused:cold' --type go` | ✅ exit=1 (zero matches in production Go) |
| `pkg/ari/types.go` and `pkg/ari/server.go` have residual prose comments | ⚠️ comment-only, no code logic — acceptable; removed in S03 |

### Known Issues / Follow-on Work

1. `pkg/ari/types.go` and `pkg/ari/server.go` contain comment-only references to `paused:warm` / `paused:cold` (prose description, not code logic). These will be cleaned up in S03 when the ARI handler surface is migrated.
2. `pkg/agentd/recovery.go` currently only filters out `stopped` as terminal during recovery — `error` should also be treated as terminal. This is a correctness gap to address in S07 (Recovery & Integration Proof).
3. `meta.AgentState` and `meta.SessionState` are parallel types with the same 5 state values. The `AgentManager` that uses `meta.AgentState` directly is deferred to S03.

### Patterns Established

- `agent.go` / `agent_test.go` follow the exact session/room CRUD pattern — new store methods can be scaffolded mechanically.
- The `DEFAULT NULL` FK pattern (vs `DEFAULT ''`) for nullable FK columns is codified in K024.
- Two-task constant removal strategy for cross-package state machine convergence codified in K025.
- `SessionManager.Create` accepts both `creating` and `created` as valid initial states — callers that recover a session into `created` state don't need to synthesize a transition.
- Self-transitions are no-ops in `SessionManager.Transition`, not errors — simplifies callers that may re-apply the current state.


## Verification

All slice-level verification checks passed:

1. `go test ./pkg/meta/... ./pkg/agentd/... -count=1` → exit=0, 102 tests pass (42 pkg/meta + 60 pkg/agentd)
2. `rg 'PausedWarm|PausedCold|paused:warm|paused:cold' --type go` → exit=1 (no matches in any .go file)
3. `agents` table verified present in schema.sql at v3 with UNIQUE(room,name), FK constraints, 3 indexes, and updated_at trigger
4. `sessions.agent_id` FK column verified present at schema v4 with DEFAULT NULL and ON DELETE SET NULL
5. `meta.AgentState` and 5 constants present in models.go; `meta.Agent` struct present with all required fields
6. `SessionStateCreating` and `SessionStateError` added; `SessionStatePausedWarm`/`SessionStatePausedCold` removed
7. `validTransitions` in pkg/agentd/session.go shows 5-state model; paused:* explicitly tested and rejected in session_test.go
8. `deleteProtectedStates` covers `creating` and `running`

## Requirements Advanced

- R052 — agents table with room+name UNIQUE key and agent_id FK on sessions provides the storage foundation for identity-based agent recovery — S07 will add the recovery logic on top

## Requirements Validated

- R049 — State transition unit tests in pkg/agentd/session_test.go cover all 5 states (creating/created/running/stopped/error) and explicitly reject paused:warm/paused:cold as invalid transitions. rg confirms zero production Go references to PausedWarm/PausedCold/paused:warm/paused:cold. 102 tests pass.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

T02 also updated pkg/agentd/recovery_test.go, pkg/agentd/store_test.go outside the original task plan scope to fix references to removed constants and schema version assertions. agent_id column uses DEFAULT NULL instead of DEFAULT '' to avoid FK constraint violations on pre-existing rows.

## Known Limitations

1. pkg/ari/types.go and pkg/ari/server.go retain comment-only references to paused:warm/paused:cold — cleaned up in S03. 2. pkg/agentd/recovery.go only filters stopped as terminal; error should also be filtered — addressed in S07. 3. AgentManager using meta.AgentState directly is deferred to S03.

## Follow-ups

S03 (ARI handler migration) can now consume meta.Agent types and Store CRUD. S07 should add error state to recovery terminal-state filter in recovery.go.

## Files Created/Modified

- `pkg/meta/models.go` — Added AgentState type + 5 constants, Agent struct; added SessionStateCreating/SessionStateError; removed SessionStatePausedWarm/SessionStatePausedCold; added Session.AgentID field
- `pkg/meta/schema.sql` — Appended schema v3 (agents table + indexes + trigger) and schema v4 (sessions.agent_id FK column)
- `pkg/meta/agent.go` — New file — AgentFilter struct and 6 CRUD methods: CreateAgent, GetAgent, GetAgentByRoomName, ListAgents, UpdateAgent, DeleteAgent
- `pkg/meta/agent_test.go` — New file — 9 unit tests covering CRUD, constraints, filtering, and schema verification
- `pkg/meta/session.go` — Updated CreateSession/GetSession/ListSessions for agent_id column; added AgentID filter to SessionFilter
- `pkg/meta/session_test.go` — Removed paused:* test cases; fixed schema version assertions; added agent_id round-trip coverage
- `pkg/agentd/session.go` — Converged validTransitions to 5-state model; updated deleteProtectedStates; updated Create to accept creating/created
- `pkg/agentd/session_test.go` — Rewrote with 8 valid-transition + 14 invalid-transition tests (explicit paused:* rejection) + delete-protection tests
- `pkg/agentd/recovery_test.go` — Fixed references to removed paused:* constants and updated schema version assertions
