---
estimated_steps: 31
estimated_files: 3
skills_used: []
---

# T03: Update room-spec, contract-convergence, and README to agent-first model

Update the remaining 3 docs to complete the contract convergence. Depends on T01 because the agent/* method names and state machine must be established first.

room-spec.md changes (242 lines, moderate update):
- Rewrite 'Projection to Runtime' section steps:
  1. workspace/prepare for spec.workspace
  2. room/create to register runtime projection
  3. For each member: agent/create with room, name, runtimeClass, workspaceId, etc.
  4. Poll agent/status until created or error
  5. Deliver work through agent/prompt
  6. Inspect via room/status or agent/list
  7. agent/stop → agent/delete → room/delete → workspace/cleanup
- room/status response: members show agentName, description, runtimeClass, agentState — remove sessionId
- Update spec.agents fields: add description field
- Replace session/new → agent/create references, session/prompt → agent/prompt
- Remove or rewrite 'session/new vs session/prompt' section to 'agent/create vs agent/prompt'
- Update runtimeClass table reference from session/new to agent/create

contract-convergence.md changes (104 lines, moderate update):
- Update Authority Map table: session bootstrap → agent creation, work execution → agent/prompt
- Rewrite Bootstrap Contract section: agent/create (async) replaces session/new, agent/prompt replaces session/prompt
- Update State Mapping table: add agent layer between orchestrator and agentd/ARI
- Add new section '## Agent Model Convergence' documenting M005 invariants:
  - Agent is external API object; session is internal realization
  - Agent identity = room + name (unique key)
  - All agents belong to a room
  - State machine: creating→created→running→stopped; error reachable from creating/created/running
  - paused:warm/paused:cold removed from active state machine
- Add note that shim surface (session/*/runtime/*) is UNCHANGED
- Update security boundaries: workspace refs shift from session→agent level

README.md changes (139 lines, light update):
- Architecture mapping table: Container → Session becomes Container → Agent (external) / Session (internal)
- Architecture diagram text: agentd box should mention Agent Manager
- Document index: agentd section notes agent as external object

## Inputs

- ``docs/design/orchestrator/room-spec.md` — current room spec with session/new projection (242 lines)`
- ``docs/design/contract-convergence.md` — current authority map and bootstrap contract (104 lines)`
- ``docs/design/README.md` — current architecture overview (139 lines)`
- ``docs/design/agentd/ari-spec.md` — T01 output needed for consistent agent/* method names`

## Expected Output

- ``docs/design/orchestrator/room-spec.md` — rewritten projection to agent/create, no sessionId in room/status`
- ``docs/design/contract-convergence.md` — updated authority map, bootstrap contract, new Agent Model Convergence section`
- ``docs/design/README.md` — updated architecture table and diagram to reflect agent as external object`

## Verification

grep -q 'agent/create' docs/design/orchestrator/room-spec.md && ! grep -q 'sessionId' docs/design/orchestrator/room-spec.md && grep -q 'Agent Model Convergence' docs/design/contract-convergence.md && grep -qi 'Agent.*external\|external.*Agent' docs/design/README.md && echo 'T03 verify pass'
