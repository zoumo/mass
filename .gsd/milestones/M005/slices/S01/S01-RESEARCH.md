# S01 Research — Design Contract: Agent Model Convergence

## Summary

S01 is a **design-doc-only** slice. No Go code changes. The goal is to update 5 authority design documents (+ contract-convergence.md + README.md) to consistently describe **agent as the external object** and **session as internal runtime realization**, before any implementation begins in S02–S07. This follows the proven M002/S01 de-risking pattern (D032/K034): converge the contract first, then implement.

**Requirements owned:** None directly. S01 enables all downstream requirements (R047, R048, R049, R050, R051, R052) by establishing the design contract they implement against.

**Depth:** Targeted research. The pattern is established from M002/S01. The work is well-scoped document editing + verification script.

## Recommendation

Follow the exact M002/S01 pattern:
1. Update each design doc to agent-first language per the alignment plan (sections 11.1–11.5)
2. Update contract-convergence.md with new authority map entries and agent model invariants
3. Update README.md table (Container → Session mapping becomes Container → Agent externally)
4. Write a verification script that greps for contradictions
5. Run verification + `go test ./pkg/spec` to confirm bundle specs remain valid

## Implementation Landscape

### What Must Change — Per Document

#### 1. `docs/design/agentd/agentd.md` (180 lines — **heavy rewrite**)

**Current state:** The doc describes agentd's external model using `session` as the primary object. Key sections: Session Manager, Bootstrap Contract (`session/new` → `session/prompt`), `session/new`, `session/prompt`, Recovery posture — all session-centric.

**Required changes:**
- **Subsystem rename:** "Session Manager" → agentd now has two internal subsystems for the external object model:
  - "Agent Manager" — manages agent lifecycle (the external-facing durable identity, room+name key)
  - Session Manager becomes internal-only runtime instance tracker
- **Bootstrap Contract:** Rewrite from `workspace/prepare → session/new → session/prompt` to `workspace/prepare → agent/create (async) → agent/prompt`. Remove the "session/new is configuration-only" language; replace with "agent/create is async resource creation; agent/prompt is work entry".
- **New sections needed:** Agent identity (room+name), state machine (creating/created/running/stopped/error), async create semantics, stop/delete separation, restart, agent directory model (agentd merges bundle+state)
- **Recovery:** External recovery key changes from sessionId to agent identity (room+name)
- **Workspace refs:** Shift from session→agent level gating

#### 2. `docs/design/agentd/ari-spec.md` (288 lines — **heavy rewrite**)

**Current state:** All methods are `session/*`. The "Desired vs Realized" table maps to `session/new` and `session/prompt`. Room methods exist but expose `sessionId` in members.

**Required changes:**
- **Method surface:** Replace all `session/*` ARI methods with `agent/*`:
  - `session/new` → `agent/create` (async, returns `creating`)
  - `session/prompt` → `agent/prompt`
  - `session/cancel` → `agent/cancel`
  - `session/stop` → `agent/stop` (preserves state)
  - `session/remove` → `agent/delete` (requires stopped, cleans up)
  - NEW: `agent/restart` (re-bootstrap from existing state)
  - `session/list` → `agent/list`
  - `session/status` → `agent/status`
  - `session/attach` / `session/detach` → `agent/attach` / `agent/detach`
- **Params rewrite:** `agent/create` params: `room` (required), `name` (required), `description`, `runtimeClass`, `workspaceId`, `systemPrompt`, `env`, `mcpServers`, `permissions`, `labels`. No more `roomAgent` field — `name` IS the agent name in the room.
- **Room methods:** `room/status` response: members show `agentName`, `description`, `runtimeClass`, `agentState` — NOT `sessionId`/`state`
- **Events:** `session/update` → rename to `agent/update`? **Decision needed:** The plan doc doesn't specify this. The shim-facing surface stays `session/update`, but ARI-facing could become `agent/update`. This needs explicit resolution. Recommend: ARI events become `agent/update` and `agent/stateChange`; shim events stay `session/update` and `runtime/stateChange`.
- **Workspace methods:** Retained unchanged. workspace refs shift from session→agent.
- **Env precedence:** Update `session/new env overrides` → `agent/create env overrides`
- **Follow-on gaps:** R036/R044 references updated to reflect agent identity

#### 3. `docs/design/runtime/agent-shim.md` (179 lines — **light update**)

**Current state:** Already describes shim as "one shim per session" with session/* RPC. The doc is descriptive (not normative). Authority delegation to runtime-spec and shim-rpc-spec is clean.

**Required changes (per plan section 11.2):**
- Add explicit stability conclusion: "agent-shim's existing RPC boundary is preserved in M005. The shim's only enhancement is event ordering."
- Add M005 scope statement: "The refactoring primary is agentd. agent-shim retains session/* + runtime/* RPC, bundle/state separation, and single-session-per-shim design."
- Add event ordering scope: "M005/S05 adds turnId, streamSeq, phase to event envelopes for turn-aware ordering. No shim protocol or directory restructuring."
- **Important:** Do NOT rename shim's internal `session/*` to `agent/*`. The shim continues to serve one "session" (the internal runtime instance). The agent abstraction exists only at agentd level.

#### 4. `docs/design/runtime/shim-rpc-spec.md` (408 lines — **moderate update**)

**Current state:** Clean-break surface is `session/*` + `runtime/*`. Has SequenceMeta with `seq` only. Turn events (`turn_start`, `turn_end`) exist as typed events but have no turn-aware ordering fields.

**Required changes (per plan section 11.3):**
- **New event ordering fields:** Add to SequenceMeta (or alongside it):
  - `turnId` (string) — correlates events to a specific prompt turn
  - `streamSeq` (int) — orders events within a turn (monotonic per turn)
  - `phase` (string) — categorizes event timing (e.g. "thinking", "acting", "tool_call")
- **Ordering rules:**
  - `seq` continues as global log sequence (for recovery/dedup)
  - `turnId` assigned on `turn_start`, cleared on `turn_end`
  - `streamSeq` resets to 0 on each `turn_start`, increments within turn
  - `runtime/stateChange` excluded from turn ordering (uses `seq` only, no `turnId`)
- **Replay semantics:** Chat/replay orders by `(turnId, streamSeq)` within a turn, falls back to `seq` across turns
- **Shim RPC methods stay as-is:** `session/prompt`, `session/cancel`, `session/subscribe`, `runtime/status`, `runtime/history`, `runtime/stop`. No renaming.

#### 5. `docs/design/orchestrator/room-spec.md` (242 lines — **moderate update**)

**Current state:** Desired-state spec. `spec.agents` array describes desired members. But "Projection to Runtime" section still maps to `session/new` and `session/prompt`. `room/status` example shows `sessionId` in members.

**Required changes (per plan section 11.5):**
- **Members = agents:** Room members map to agents, not sessions. The projection flow: `spec.agents[i]` → `agent/create` (not `session/new`)
- **Projection to Runtime section:** Rewrite steps:
  1. `workspace/prepare` for `spec.workspace`
  2. `room/create` to register runtime projection
  3. For each member: `agent/create` with `room`, `name`, `runtimeClass`, `workspaceId`, etc.
  4. Poll `agent/status` until `created` or `error`
  5. Deliver work through `agent/prompt`
  6. Inspect via `room/status` or `agent/list`
  7. `agent/stop` → `agent/delete` → `room/delete` → `workspace/cleanup`
- **room/status response:** Members show `agentName`, `description`, `runtimeClass`, `agentState` — no `sessionId`
- **spec.agents fields:** Add `description` field to agent spec (currently only name, runtimeClass, systemPrompt)
- **Bootstrap contract reference:** Update `session/new` → `agent/create`, `session/prompt` → `agent/prompt`

#### 6. `docs/design/contract-convergence.md` (104 lines — **moderate update**)

**Required changes:**
- **Authority Map table:** Update entries to reflect agent as external primary. Session bootstrap row → Agent creation. Work execution row → `agent/prompt`.
- **Bootstrap Contract:** Rewrite from session-centric to agent-centric. `agent/create` (async) replaces `session/new`.
- **State Mapping table:** Add agent layer between Orchestrator and agentd/ARI. agentd now has two identity layers: Agent (external, room+name) and Session (internal, runtime instance).
- **New section:** "Agent Model Convergence" — document the M005 convergence invariants:
  - Agent is the external API object; session is internal realization
  - Agent identity = room + name (unique key)
  - All agents belong to a room
  - State machine: creating → created → running → stopped; error reachable from creating/created/running
  - `paused:warm` / `paused:cold` removed from active state machine
- **Shim Target Contract:** Add note that shim surface is UNCHANGED — session/*/runtime/* stays
- **Security Boundaries:** Workspace refs shift from session→agent level

#### 7. `docs/design/README.md` (139 lines — **light update**)

**Required changes:**
- Architecture table: `Container → Session` should note "Agent (external) / Session (internal)" or similar
- Architecture diagram: agentd box should show "Agent Manager" alongside Session/Workspace/Room managers
- Document index: agentd section should mention agent as external object

### What Does NOT Change

- `docs/design/runtime/runtime-spec.md` — The runtime spec uses generic "agent" language already for state/lifecycle. Its `id` field is the OAR runtime object ID. No changes needed UNLESS we want to add the `error` state to the runtime spec's status enum. **Decision point:** The alignment plan (section 5.3) adds `error` as a state but the runtime-spec currently only defines `creating`, `created`, `running`, `stopped`. The `error` state should be added to runtime-spec as well.
- `docs/design/runtime/config-spec.md` — Bundle config unchanged
- `docs/design/runtime/design.md` — Design rationale, no normative content
- `docs/design/workspace/workspace-spec.md` — Workspace spec unchanged

### Verification Script Design

Model after `scripts/verify-m002-s01-contract.sh`. The new script should:

**Positive checks (require_heading):**
- contract-convergence.md has "Agent Model Convergence" section
- agentd.md has agent identity / state machine sections
- ari-spec.md has `agent/create`, `agent/prompt` method sections
- shim-rpc-spec.md has turn ordering / turnId section

**Negative checks (forbid_pattern):**
- `docs/design/agentd/ari-spec.md`: No `"method": "session/new"`, `"method": "session/prompt"`, `"method": "session/remove"` etc. in normative examples
- `docs/design/agentd/agentd.md`: No `session/new` or `session/prompt` as external API language (shim-internal references are OK)
- `docs/design/orchestrator/room-spec.md`: No `sessionId` in room/status examples, no `session/new` in projection steps
- `docs/design/agentd/ari-spec.md`: No `paused:warm` or `paused:cold` in state descriptions
- All 5 docs: No contradiction where session is presented as external API object

**Bundle spec test:** `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1` (ensure bundle specs remain valid — no code touched, but smoke test)

### Key Design Decisions to Resolve/Document in This Slice

1. **ARI event naming:** Should ARI events become `agent/update` + `agent/stateChange` (agentd translates from shim's `session/update`)? Or keep `session/update` at ARI level too? **Recommendation:** Rename to `agent/update` at ARI level for consistency. The shim→agentd boundary uses session/* events; agentd→orchestrator boundary uses agent/* events.

2. **runtime-spec `error` state:** Should `error` be added to runtime-spec's state enum? The alignment plan defines it for the agent state machine, but the runtime-spec currently only has creating/created/running/stopped. **Recommendation:** Add `error` to runtime-spec. The shim can report process-level errors via this state.

3. **`runtime-spec.md` updates:** The plan section 11.4 says to update runtime-spec to describe "agentd's directory aggregation strategy." This means documenting that agentd MAY merge bundle+state into a single agent directory while the shim continues to see them as separate. **Recommendation:** Add a short "agentd Directory Model" note to runtime-spec or keep it in agentd.md only. Prefer agentd.md — runtime-spec should stay implementation-agnostic.

### Natural Task Decomposition

The work divides into 4 natural tasks:

1. **T01: Update agentd authority docs** (`agentd.md` + `ari-spec.md`) — Heaviest work. These two docs are tightly coupled and should be updated together. ~60% of the effort.

2. **T02: Update shim-rpc-spec with event ordering design** (`shim-rpc-spec.md` + `agent-shim.md`) — Add turnId/streamSeq/phase design to shim-rpc-spec. Add stability statement to agent-shim.md. Independent of T01.

3. **T03: Update room-spec and convergence docs** (`room-spec.md` + `contract-convergence.md` + `README.md`) — Update room projection to agent-first. Update authority map and convergence invariants. Depends on T01 (needs to reference the same agent/* method names).

4. **T04: Verification script + smoke test** — Write `scripts/verify-m005-s01-contract.sh`. Run it. Run `go test ./pkg/spec`. Depends on T01–T03.

### Risk Assessment

- **Low risk overall.** This is document editing, not code. The pattern is proven from M002/S01.
- **Primary risk:** Inconsistency between docs. The verification script mitigates this.
- **Secondary risk:** Over-specifying implementation details in design docs. The docs should define the contract, not dictate implementation. Keep agent directory model and session-to-agent mapping light.

### Existing Patterns & Prior Art

- M002/S01 established the design-convergence pattern with `scripts/verify-m002-s01-contract.sh`
- The existing verification script checks headings (positive), forbids stale patterns (negative), and runs bundle spec tests
- contract-convergence.md already has the authority map, bootstrap contract, state mapping, and security boundaries sections

### Forward Intelligence for S02–S07

Things downstream slices should know from this research:

- **S02 (Schema):** The `agents` table needs: `room TEXT NOT NULL`, `name TEXT NOT NULL`, `UNIQUE(room, name)`, `description TEXT`, `workspace_id TEXT`, `runtime_class TEXT`, `state TEXT`, `labels TEXT`. Sessions table gets `agent_id TEXT` FK. `workspace_refs` shifts from `session_id` to `agent_id`. Schema migration from v2 → v3.
- **S03 (ARI):** `pkg/ari/server.go` dispatch switch has 9 `session/*` cases + 4 `room/*` + 3 `workspace/*`. All `session/*` cases become `agent/*`. The SessionManager is already in `pkg/agentd/session.go` — needs an AgentManager alongside it.
- **S04 (Lifecycle):** Current state machine in `pkg/agentd/session.go` has `paused:warm`/`paused:cold` transitions. These get removed. `error` state and its transitions get added.
- **S05 (Events):** `pkg/events/envelope.go` SequenceMeta has `Seq` only. Must add `TurnId`, `StreamSeq`, `Phase`. `pkg/events/translator.go` already has `NotifyTurnStart()`/`NotifyTurnEnd()` — these are the attachment points.
- **S06 (Room MCP):** `cmd/room-mcp-server/main.go` uses `OAR_SESSION_ID` env var. Changes to `OAR_AGENT_NAME`/`OAR_ROOM_NAME`.
- **S07 (Recovery):** Recovery in `pkg/agentd/recovery.go` currently works by session. Must shift to agent identity as external key.

## Skills Discovered

No specialized skills needed — this is pure design document editing in markdown.
