---
estimated_steps: 30
estimated_files: 2
skills_used: []
---

# T01: Rewrite agentd.md and ari-spec.md to agent-first model

Rewrite the two heaviest authority documents — agentd.md and ari-spec.md — to consistently describe agent as the external object and session as internal runtime realization.

agentd.md changes:
- Rename 'Session Manager' subsystem description to show agentd has 'Agent Manager' (external lifecycle) and Session Manager (internal runtime instances)
- Rewrite Bootstrap Contract from session/new→session/prompt to workspace/prepare→agent/create (async)→agent/prompt
- Add agent identity section: room+name unique key, all agents belong to a room
- Add agent state machine: creating→created→running→stopped, error reachable from creating/created/running
- Add async create semantics: agent/create returns immediately, background bootstrap, poll agent/status
- Add stop/delete separation: stop preserves state, delete requires stopped + cleans up
- Add restart concept: re-bootstrap from existing state
- Update recovery section: external key is agent identity (room+name) not sessionId
- Session Manager section becomes internal-only: tracks shim processes, not exposed externally

ari-spec.md changes:
- Replace ALL session/* methods with agent/* equivalents:
  - session/new → agent/create (async, returns creating)
  - session/prompt → agent/prompt
  - session/cancel → agent/cancel
  - session/stop → agent/stop (preserves state)
  - session/remove → agent/delete (requires stopped)
  - NEW: agent/restart
  - session/list → agent/list
  - session/status → agent/status
  - session/attach / session/detach → agent/attach / agent/detach
- Rewrite agent/create params: room (required), name (required), description, runtimeClass, workspaceId, systemPrompt, env, mcpServers, permissions, labels
- Rewrite room/* response: members show agentName, description, runtimeClass, agentState (not sessionId/state)
- ARI events: agent/update and agent/stateChange at ARI level (shim→agentd boundary stays session/update)
- Update env precedence section from session/new to agent/create
- Remove paused:warm/paused:cold from state descriptions
- Update all JSON examples to use agent/* method names

Key constraint: shim-internal references to session/* are OK and expected. Only the EXTERNAL ARI surface changes to agent/*.

Decisions to apply: D061 (agent replaces session as API primary), D062 (agent state machine), D063 (async create), D064 (separate tables).

## Inputs

- ``docs/design/agentd/agentd.md` — current session-centric agentd design (180 lines)`
- ``docs/design/agentd/ari-spec.md` — current session/* ARI method surface (288 lines)`

## Expected Output

- ``docs/design/agentd/agentd.md` — rewritten with Agent Manager, agent identity, state machine, async create, stop/delete separation`
- ``docs/design/agentd/ari-spec.md` — rewritten with agent/* methods, agent/create params, agent events, no session/* in external surface`

## Verification

grep -c 'agent/create\|agent/prompt\|agent/stop\|agent/delete\|agent/status\|agent/list' docs/design/agentd/ari-spec.md | xargs test 6 -le && ! grep -E '"method":\s*"session/(new|prompt|cancel|stop|remove|list|status)"' docs/design/agentd/ari-spec.md && grep -q 'Agent Manager' docs/design/agentd/agentd.md && grep -q 'agent/create' docs/design/agentd/agentd.md && echo 'T01 verify pass'
