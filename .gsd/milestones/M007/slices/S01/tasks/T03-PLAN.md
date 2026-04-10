---
estimated_steps: 52
estimated_files: 8
skills_used: []
---

# T03: pkg/agentd compilation sweep — delete SessionManager, adapt to new meta.Agent model

Adapt pkg/agentd to compile with the new meta package (no Session, no Room, no AgentState, no SessionState). Delete SessionManager (it wrapped meta.Session which no longer exists). Adapt ProcessManager to use Agent identity (workspace, name) instead of session UUID. This is a mechanical adaptation — behavioral correctness (shim write authority, RestartPolicy) is left to S02.

Steps:
1. DELETE pkg/agentd/session.go and pkg/agentd/session_test.go entirely.

2. REWRITE pkg/agentd/agent.go:
   - Remove meta.AgentState; use spec.Status everywhere.
   - Change identity: Agent identified by (workspace, name) not UUID.
   - ErrDeleteNotStopped.State becomes spec.Status type.
   - ErrAgentAlreadyExists: Room → Workspace field.
   - AgentManager.Create(ctx, *meta.Agent): set default status.State=spec.StatusCreating.
   - AgentManager.Get(ctx, workspace, name string): call store.GetAgent(ctx, workspace, name).
   - AgentManager.GetByWorkspaceName → rename from GetByRoomName; params (workspace, name).
   - AgentManager.List(ctx, *meta.AgentFilter): call store.ListAgents.
   - AgentManager.UpdateStatus(ctx, workspace, name string, status meta.AgentStatus): call store.UpdateAgentStatus.
   - AgentManager.Delete(ctx, workspace, name string): check state is stopped/error then call store.DeleteAgent.
   - Remove all meta.AgentState references.

3. REWRITE pkg/agentd/process.go (mechanical — same logic, new types):
   - Remove `sessions *SessionManager` field from ProcessManager.
   - ProcessManager.NewProcessManager: remove sessions param.
   - ShimProcess: rename SessionID to AgentKey (type string, value = workspace+"/"+name).
   - ProcessManager.processes map key = workspace+"/"+name composite string.
   - Start(ctx, workspace, name string) instead of Start(ctx, sessionID string):
     * Fetch agent via m.agents.Get(ctx, workspace, name)
     * Check agent.Status.State == spec.StatusCreating (not SessionStateCreated)
     * generateConfig takes *meta.Agent
     * createBundle takes *meta.Agent
   - State transitions: replace `m.sessions.Transition(ctx, id, meta.SessionStateRunning)` with `m.agents.UpdateStatus(ctx, workspace, name, meta.AgentStatus{State: spec.StatusRunning})`
   - Stopped transition: `m.agents.UpdateStatus(ctx, workspace, name, meta.AgentStatus{State: spec.StatusStopped})`
   - generateConfig(*meta.Agent, *RuntimeClass): agent.Metadata.Name for config name; no Room/session fields; agent.Spec.SystemPrompt for systemPrompt
   - createBundle(*meta.Agent, spec.Config): use agent.Metadata.Workspace+agent.Metadata.Name as dir name fragment
   - forkShim: adapt similarly
   - After successful Start: call store.UpdateAgentStatus to set ShimSocketPath, ShimStateDir, ShimPID
   - Remove all meta.SessionStateXXX references; replace with spec.StatusXXX
   - Remove any room-related MCP injection logic (no more Room concept)

4. REWRITE pkg/agentd/recovery.go (mechanical — same logic, new types):
   - Replace `m.store.ListSessions(ctx, nil)` with `m.store.ListAgents(ctx, nil)` (nil filter = all agents)
   - Filter out terminal agents: skip spec.StatusStopped and spec.StatusError
   - Replace session.ShimSocketPath → agent.Status.ShimSocketPath
   - Replace session.ShimStateDir → agent.Status.ShimStateDir
   - Replace session.ShimPID → agent.Status.ShimPID
   - Replace m.sessions.Transition(ctx, id, stopped) → m.agents.UpdateStatus(ctx, ws, name, meta.AgentStatus{State: spec.StatusStopped})
   - Replace agent ID from UUID to (workspace, name) pair; ShimProcess.AgentKey = workspace+"/"+name
   - Update m.processes map to use workspace+"/"+name key
   - recoveredAgentIDs map: key = workspace+"/"+name

5. UPDATE pkg/agentd/agent_test.go, process_test.go, recovery_test.go, shim_client_test.go: just make them compile (fix type references; do not delete them). Replace meta.AgentState → spec.Status, meta.SessionState → spec.Status, SessionStateCreated → spec.StatusIdle, AgentStateCreated → spec.StatusIdle, AgentStateRunning → spec.StatusRunning, etc. Fix any GetByRoomName → GetByWorkspaceName, sessions.XXX removed.

6. UPDATE pkg/agentd/shim_client.go if it references spec.StatusCreated → spec.StatusIdle (grep first).

Constraints:
- The ShimProcess.AgentKey composite string format is workspace+"/"+name (matches bbolt key path convention).
- Do NOT implement shim write authority boundary (agentd still writes state directly — S02 fixes this).
- Do NOT implement RestartPolicy — that's S02.
- ProcessManager.NewProcessManager signature changes: remove sessions *SessionManager param.
- All meta.SessionState*, meta.AgentState*, meta.Session references must be eliminated.
- pkg/agentd/session.go and session_test.go are fully deleted.

## Inputs

- `pkg/meta/models.go`
- `pkg/meta/store.go`
- `pkg/meta/agent.go`
- `pkg/spec/state_types.go`
- `pkg/agentd/agent.go`
- `pkg/agentd/process.go`
- `pkg/agentd/recovery.go`
- `pkg/agentd/session.go`
- `pkg/agentd/shim_client.go`

## Expected Output

- `pkg/agentd/agent.go`
- `pkg/agentd/process.go`
- `pkg/agentd/recovery.go`
- `pkg/agentd/shim_client.go`

## Verification

cd /Users/jim/code/zoumo/open-agent-runtime && go build ./pkg/agentd/... && ! rg 'SessionManager|meta\.AgentState|meta\.SessionState|meta\.Session[^S]' --type go pkg/agentd/
