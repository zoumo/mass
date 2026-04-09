---
id: S01
parent: M005
milestone: M005
provides:
  - ["Stable agent-first design contract for S02 schema design (agents table, sessions FK, state machine implementation)", "Stable agent/* method signatures for S03 ARI handler migration", "Turn-aware event ordering spec for S05 implementation (turnId/streamSeq/phase fields, replay semantics)", "scripts/verify-m005-s01-contract.sh as ongoing gate — S02-S07 implementors can run it to confirm no regressions", "agent-shim stability confirmation: S05 can add turnId/streamSeq fields without touching shim RPC methods"]
requires:
  []
affects:
  - ["S02 — agents table schema and state machine must match the 5-state model defined here", "S03 — ARI handler migration must match the agent/* method signatures and params defined in ari-spec.md", "S05 — turnId/streamSeq/phase implementation must match the ordering rules documented in shim-rpc-spec.md", "S06 — room/status member fields (agentName/agentState/description) match the spec defined in room-spec.md"]
key_files:
  - ["docs/design/agentd/agentd.md", "docs/design/agentd/ari-spec.md", "docs/design/runtime/shim-rpc-spec.md", "docs/design/runtime/agent-shim.md", "docs/design/orchestrator/room-spec.md", "docs/design/contract-convergence.md", "docs/design/README.md", "scripts/verify-m005-s01-contract.sh"]
key_decisions:
  - ["D060 applied — shim retains existing session/*+runtime/* RPC surface; only event ordering enhanced", "D061 applied — agent replaces session as external API primary across all 7 docs", "D062 applied — 5-state machine (creating/created/running/stopped/error); paused:warm/paused:cold removed", "D063 applied — agent/create is async, returns creating immediately; caller polls agent/status", "D064 applied — Session Manager documented as internal-only; Agent Manager documented as external lifecycle", "D065 applied — ARI events renamed agent/update and agent/stateChange at agentd→orchestrator boundary", "D066 (new) — turnId assigned at turn_start, cleared after turn_end; streamSeq resets to 0 per turn; runtime/stateChange excluded from turn ordering; seq is global dedup key", "D068 (new) — forbidden patterns in contract verifier scripts should target JSON method-string format, not prose, to avoid false-positives on legitimate shim-internal references"]
patterns_established:
  - ["Agent-first design contract: all external ARI surface uses agent/*; session/* is internal/shim-only", "Agent identity = room+name (stable, human-meaningful) replaces volatile sessionId as external key", "5-state agent state machine: creating→created→running→stopped with error reachable from creating/created/running; paused:* retired", "Async create pattern: agent/create returns immediately with creating state; caller polls agent/status", "Turn-aware event ordering: turnId/streamSeq/phase on session/update; runtime/stateChange excluded; (turnId,streamSeq) for within-turn, seq for cross-turn replay", "Contract verifier forbidden patterns: scope to JSON method-string format to avoid false-positives on prose", "Boundary translation: rename events at the agentd→orchestrator perimeter, not inside the shim"]
observability_surfaces:
  - ["scripts/verify-m005-s01-contract.sh — runnable at any time to confirm all 7 authority documents remain contradiction-free. Exit code 0 = clean; non-zero = specific failure message naming the document and check."]
drill_down_paths:
  - [".gsd/milestones/M005/slices/S01/tasks/T01-SUMMARY.md", ".gsd/milestones/M005/slices/S01/tasks/T02-SUMMARY.md", ".gsd/milestones/M005/slices/S01/tasks/T03-SUMMARY.md", ".gsd/milestones/M005/slices/S01/tasks/T04-SUMMARY.md"]
duration: ""
verification_result: passed
completed_at: 2026-04-08T15:50:58.138Z
blocker_discovered: false
---

# S01: Design Contract — Agent Model Convergence

**Rewrote all 7 authority documents to consistently describe agent as the external object and session as internal runtime realization; verification script confirms zero contradictions; bundle spec smoke test passes.**

## What Happened

S01 completed a design-contract convergence pass across all 7 authority documents in the M005 scope, establishing agent as the stable external identity and session as internal implementation detail.

**T01 — agentd.md and ari-spec.md (heaviest documents)**

Both documents were fully rewritten to the agent-first model. agentd.md gained: Agent Manager subsystem (external lifecycle), Session Manager marked internal-only, agent identity section (room+name unique key), 5-state machine (creating→created→running→stopped with error reachable from creating/created/running), async agent/create semantics with status polling, stop/delete separation, restart concept, and recovery posture keyed on room+name instead of sessionId. ari-spec.md replaced all session/* methods with agent/* equivalents (agent/create, agent/prompt, agent/cancel, agent/stop, agent/delete, agent/restart, agent/list, agent/status, agent/attach/detach), rewrote agent/create params with room+name as required identity fields, updated room/status members to show agentName/agentState without sessionId, renamed ARI events to agent/update and agent/stateChange, and updated env precedence. Shim-internal session/* references were intentionally preserved per D060.

**T02 — shim-rpc-spec.md and agent-shim.md (shim layer)**

shim-rpc-spec.md received a new "Turn-Aware Event Ordering" section documenting three new optional fields on session/update envelopes (turnId, streamSeq, phase), four ordering rules (turnId assigned at turn_start, cleared after turn_end; streamSeq resets to 0 per turn; runtime/stateChange excluded from turn ordering; seq retained as global dedup key), and replay semantics ((turnId, streamSeq) within a turn, seq across turns). All six RPC methods were left completely unchanged. agent-shim.md received an explicit M005 stability statement confirming the shim retains its existing session/* + runtime/* surface and its only M005 enhancement is event ordering.

**T03 — room-spec.md, contract-convergence.md, README.md (remaining 3)**

room-spec.md Projection to Runtime flow rewritten to use agent/create (async with status polling) instead of session/new; spec.agents gained a description field; the 'session/new vs session/prompt' section renamed to 'agent/create vs agent/prompt'; sessionId removed from room/status members (now shows agentName/description/runtimeClass/agentState). contract-convergence.md's Authority Map revised to reference agent/create and agent/prompt, Bootstrap Contract rewritten around async semantics, new 'Agent Model Convergence' section added documenting all M005 invariants (agent as external object, room+name identity, state machine, shim surface unchanged), State Mapping table expanded with Agent Manager (external) and Session Manager (internal) rows, and Security Boundaries updated. README.md OCI-to-OAR mapping table and architecture diagram updated to distinguish Agent (external) / Session (internal), with agentd section noting agent as external object. One deviation: the README grep check `Agent.*external` initially failed because 'Agent' and '(external)' were on separate diagram lines — resolved by redrawing the diagram box as 'Agent Manager (external API object: agent/*)' on one line.

**T04 — Verification script**

scripts/verify-m005-s01-contract.sh written modeled on verify-m002-s01-contract.sh, reusing the require_heading/forbid_pattern helper pattern. 5 positive heading checks across all 7 authority docs; 5 negative pattern checks using JSON method-string format to avoid false-positives on legitimate shim-internal prose references. Script exits 0, bundle spec smoke test (go test ./pkg/spec -run TestExampleBundlesAreValid) passes in 0.518s.

**Decisions applied:** D060 (shim stability), D061 (agent-first external model), D062 (5-state machine), D063 (async create), D064 (separate tables), D065 (agent/update event naming). New decisions recorded: D066 (turnId/streamSeq turn ordering semantics), D067 (forbidden-pattern scoping to JSON method strings).

## Verification

All S01 verification gates passed:

1. `bash scripts/verify-m005-s01-contract.sh` → exit 0, "M005/S01 contract verification passed"
2. `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1` → ok 1.199s
3. T01: grep count ≥ 6 for agent/* methods in ari-spec.md (actual: 25); no session/(new|prompt|cancel|stop|remove|list|status) in JSON examples; 'Agent Manager' in agentd.md; 'agent/create' in agentd.md — all pass
4. T02: turnId, streamSeq, phase in shim-rpc-spec.md; M005 in agent-shim.md — all pass
5. T03: agent/create in room-spec.md; no sessionId in room-spec.md; 'Agent Model Convergence' in contract-convergence.md; 'Agent.*external' in README.md — all pass

## Requirements Advanced

- R047 — Design contract fully specifies the agent/* external surface; code-level implementation (S03) advances to final validation
- R050 — Design contract fully specifies turn-aware ordering semantics; unit test proof (S05) advances to final validation

## Requirements Validated

- R047 — ari-spec.md now uses agent/* exclusively for all external ARI methods; no session/* appears in normative JSON examples; grep count=25 for agent methods; agent identity documented as room+name throughout
- R050 — shim-rpc-spec.md Turn-Aware Event Ordering section documents turnId/streamSeq/phase fields, ordering rules, and replay semantics; grep checks pass for all three fields

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

README.md grep check `Agent.*external` failed on the first pass because 'Agent' and '(external)' were on separate lines in the ASCII diagram. Resolved by redrawing the diagram box as 'Agent Manager (external API object: agent/*)' on one line — no plan-level deviation, only an implementation detail adjustment.

## Known Limitations

R047 code-level validation (grep gate: no session/* in ARI dispatch) is deferred to S03 when the actual Go code migration happens. R050 unit test proof (turnId assigned on turn_start, replay ordering) is deferred to S05 when the event ordering is implemented in the shim. S01 provides the design authority for both; implementation proof comes in later slices.

## Follow-ups

None required for downstream slices. S02 can begin immediately using the agents table schema and state machine from agentd.md. S05 can begin immediately using the turn-ordering spec from shim-rpc-spec.md (depends only on S01 per the roadmap).

## Files Created/Modified

- `docs/design/agentd/agentd.md` — Rewrote to agent-first model: Agent Manager (external), Session Manager (internal), room+name identity, 5-state machine, async create semantics, stop/delete separation, restart, recovery keyed on room+name
- `docs/design/agentd/ari-spec.md` — Replaced all session/* methods with agent/* equivalents; rewrote agent/create params; updated room/status members (agentName/agentState); renamed events to agent/update and agent/stateChange; removed paused:* states
- `docs/design/runtime/shim-rpc-spec.md` — Added Turn-Aware Event Ordering section with turnId/streamSeq/phase fields, ordering rules, and replay semantics; all 6 RPC methods unchanged
- `docs/design/runtime/agent-shim.md` — Added M005 stability statement and scope note: shim retains session/*+runtime/* surface; only event ordering enhanced
- `docs/design/orchestrator/room-spec.md` — Rewritten Projection to Runtime to use agent/create async flow; sessionId removed from room/status; spec.agents gains description field
- `docs/design/contract-convergence.md` — Authority Map, Bootstrap Contract, State Mapping, and Security Boundaries updated to agent model; new Agent Model Convergence section added with all M005 invariants
- `docs/design/README.md` — OCI mapping table and architecture diagram updated to Agent (external) / Session (internal); agentd section notes agent as external object
- `scripts/verify-m005-s01-contract.sh` — New verification script: 5 positive heading checks and 5 negative JSON-method-string pattern checks across all 7 authority docs; exits 0 on full compliance
