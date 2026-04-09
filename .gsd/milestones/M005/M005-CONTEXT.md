# M005 — agentd Agent Model Refactoring

## Source

This milestone implements the plan defined in `docs/plan/agent-runtime-alignment-plan.md`.

## Core Thesis

The refactoring has two axes with very different scopes:

1. **agent-shim (small)**: Retain existing RPC, bundle/state, single-session design. Only fix event ordering by adding `turnId`, `streamSeq`, `phase` to event envelopes.
2. **agentd (large)**: Complete external model rewrite from session → agent. New storage layer, converged state machine, async create, stop/delete separation, room alignment.

## What Changes

### External Object Model
- API primary: `agent` (not `session`)
- Identity: `room + name` unique key (all agents must belong to a room)
- Methods: `agent/create`, `agent/prompt`, `agent/cancel`, `agent/stop`, `agent/restart`, `agent/delete`, `agent/status`, `agent/list`, `agent/attach`, `agent/detach`
- Room methods retained: `room/create`, `room/status`, `room/send`, `room/delete`
- Workspace methods retained unchanged

### Storage
- New `agents` table: room, name, description, workspace_id, runtime_class, state, labels
- `sessions` table gains `agent_id` FK, remains as internal runtime instance
- `workspace_refs` references shift from session→agent level for cleanup gating

### State Machine
- States: `creating`, `created`, `running`, `stopped`, `error`
- `paused:warm` / `paused:cold` removed from active state machine
- Error transitions: `creating→error`, `created→error`, `running→error`, `error→created` (via restart), `error→stopped`, `error→deleting`

### Lifecycle
- `agent/create`: async — returns `creating`, background bootstrap, poll for `created`/`error`
- `agent/stop`: stops shim, preserves state
- `agent/delete`: requires stopped, cleans up agent directory, releases workspace
- `agent/restart`: re-bootstraps from existing state

### Events (agent-shim only change)
- Envelope gains: `turnId`, `streamSeq`, `phase`
- `seq` remains as global log sequence
- `turnId` correlates events to a prompt turn
- `streamSeq` orders events within a turn
- `runtime/stateChange` excluded from turn ordering

### Room Alignment
- `room/status` returns: agentName, description, runtimeClass, agentState
- `room/send` resolves by agent name
- `room-mcp-server`: rewritten with `modelcontextprotocol/go-sdk`
- Env vars: `OAR_AGENT_NAME`, `OAR_AGENT_ID`, `OAR_ROOM_NAME` (replace `OAR_SESSION_ID`)

## What Does NOT Change

- agent-shim RPC surface (`session/*`, `runtime/*` shim methods)
- agent-shim bundle/state directory model
- agent-shim single-session-per-shim design
- Workspace preparation/cleanup core logic
- ACP protocol handling

## Slice Execution Order (risk-ordered)

1. **S01 — Design Contract** (high risk): Update 5 authority docs before implementation. Proven de-risking pattern from M002/K034.
2. **S02 — Schema & State Machine** (high risk): agents table, FK, state convergence. Storage foundation.
3. **S03 — ARI Agent Surface** (high risk): agent/* methods, retire session/*. Core API change.
4. **S04 — Agent Lifecycle** (medium risk): Async create, stop/delete, restart. Depends on S03.
5. **S05 — Event Ordering** (medium risk): turnId/streamSeq/phase. Independent of agent API (depends only on S01 for design).
6. **S06 — Room & MCP Alignment** (medium risk): Room faces agent, SDK rewrite. Depends on S03.
7. **S07 — Recovery & Integration Proof** (low risk): Capstone — recovery + test migration + e2e proof.

## Design Docs to Update

| Doc | Key Changes |
|---|---|
| `docs/design/agentd/agentd.md` | External primary = agent, not session. Room+name identity. Async create. |
| `docs/design/agentd/ari-spec.md` | agent/* methods. workspace/* retained. session/* removed from external. |
| `docs/design/runtime/agent-shim.md` | Shim stability decision. Event ordering scope. |
| `docs/design/runtime/shim-rpc-spec.md` | turnId/streamSeq/phase event fields. Turn ordering semantics. |
| `docs/design/orchestrator/room-spec.md` | Members = agents. description/runtimeClass on members. |

## Requirements Coverage

| Requirement | Slice |
|---|---|
| R047 — agent/* ARI surface | S03 |
| R048 — async create | S04 |
| R049 — agent state machine | S02 |
| R050 — event ordering | S05 |
| R051 — room-mcp-server SDK | S06 |
| R052 — agent-identity recovery | S07 |

## Key Decisions

| ID | Decision |
|---|---|
| D060 | agent-shim stability posture |
| D061 | Agent replaces session as API primary |
| D062 | Agent state machine design |
| D063 | Async create semantics |
| D064 | Separate agents/sessions tables |
