---
estimated_steps: 48
estimated_files: 2
skills_used: []
---

# T02: Rewrite design docs: ari-spec.md + agentd.md

Rewrite both design documents to reflect the M007 terminal-state model. No Go code changes. The docs must match the actual implemented API surface (workspace/* + agent/* methods as in pkg/ari/types.go and pkg/ari/server.go).

**docs/design/agentd/ari-spec.md — full rewrite:**
Remove all Room/room/* sections, agentId references, session/* references in the ARI contract. Replace with the current implemented surface:

```
Workspace Methods:
  workspace/create   {name, source}             → {name, phase}
  workspace/status   {name}                     → {name, phase, path?, members[]}
  workspace/list     {}                         → {workspaces[]}
  workspace/delete   {name}                     → {} (blocked if agents exist)
  workspace/send     {workspace, from, to, message} → {delivered}

Agent Methods:
  agent/create    {workspace, name, runtimeClass, restartPolicy?, systemPrompt?} → {workspace, name, state:"creating"}
  agent/prompt    {workspace, name, prompt}      → {accepted}
  agent/cancel    {workspace, name}              → {}
  agent/stop      {workspace, name}              → {}
  agent/delete    {workspace, name}              → {} (requires stopped/error)
  agent/restart   {workspace, name}              → {} (requires stopped/error)
  agent/list      {workspace?, state?}           → {agents[]}
  agent/status    {workspace, name}              → AgentInfo
  agent/attach    {workspace, name}              → {} (returns shim socket path)

Events: agent/update, agent/stateChange
```

Key points to express:
- Transport: JSON-RPC 2.0 over Unix domain socket, default path `/run/agentd/agentd.sock`
- Identity: (workspace, name) pair — no agentId UUID
- workspace/create is async (returns pending, poll workspace/status until ready)
- agent/create is async (returns creating, poll agent/status until idle)
- agent/prompt rejected when state is creating/stopped/error
- workspace/delete blocked when agents exist (JSON-RPC error CodeRecoveryBlocked -32001)
- State values: creating, idle, running, stopped, error (no 'created')
- workspace-mcp-server uses workspace_send and workspace_status tools
- No Room/* methods
- No session/* references in the ARI contract
- Error codes: -32001 (CodeRecoveryBlocked for blocked ops), -32602 (invalid params for not-found)

Include a concrete JSON-RPC example for workspace/create + workspace/status and agent/create + agent/status to show the async polling pattern.

**docs/design/agentd/agentd.md — targeted update:**
Update the Agent Manager section: replace `room + name` identity with `workspace + name`. Remove references to Session Manager as an internal subsystem (agentd no longer has a Session concept — it has AgentManager + ProcessManager). Update state machine values to match spec.Status: creating/idle/running/stopped/error (remove 'created'). Remove room/* method references. Keep the Workspace Manager section intact (it's already correct). Remove any mention of Room projection or realized Room.

**Verification:**
```bash
# No Room methods in ARI contract
! grep -n 'room/create\|room/delete\|room/status\|room/send' docs/design/agentd/ari-spec.md
# No agentId in ARI contract  
! grep -n 'agentId' docs/design/agentd/ari-spec.md
# No Session Manager subsystem
! grep -n 'Session Manager' docs/design/agentd/agentd.md
# workspace+name identity present
grep -n 'workspace.*name\|name.*workspace' docs/design/agentd/agentd.md
```

## Inputs

- ``docs/design/agentd/ari-spec.md` — current doc with stale Room/agentId content`
- ``docs/design/agentd/agentd.md` — current doc with stale Session Manager/Room content`
- ``pkg/ari/types.go` — authoritative type definitions to match`
- ``pkg/ari/server.go` — authoritative handler list to match`

## Expected Output

- ``docs/design/agentd/ari-spec.md` — rewritten to workspace/agent model`
- ``docs/design/agentd/agentd.md` — updated to remove Session Manager, Room references`

## Verification

! grep -n 'room/create\|room/delete\|room/status\|room/send\|agentId\|Session Manager' docs/design/agentd/ari-spec.md docs/design/agentd/agentd.md | grep -v '# ' && grep -q 'workspace/create' docs/design/agentd/ari-spec.md && grep -q 'workspace.*name' docs/design/agentd/agentd.md
