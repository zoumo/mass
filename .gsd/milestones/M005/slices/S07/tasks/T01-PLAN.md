---
estimated_steps: 22
estimated_files: 6
skills_used: []
---

# T01: Inject AgentManager into ProcessManager and reconcile agent states in RecoverSessions

ProcessManager currently has no AgentManager field. After daemon restart, when RecoverSessions succeeds or fails to reconnect a shim, the parent agent's state in the agents table is never updated — an agent may remain in 'running' state even though its session was just marked stopped. This task adds AgentManager to ProcessManager and implements agent state reconciliation in RecoverSessions.

**Steps:**

1. **Add `agents *AgentManager` field to `ProcessManager` struct** in `pkg/agentd/process.go`. Add it after the `sessions` field.

2. **Update `NewProcessManager` signature** to `NewProcessManager(registry *RuntimeClassRegistry, sessions *SessionManager, agents *AgentManager, store *meta.Store, cfg Config) *ProcessManager`. Set `m.agents = agents` in the body.

3. **Update `RecoverSessions` in `pkg/agentd/recovery.go`** to reconcile agent states:
   - In the failure branch (after `sessions.Transition(stopped)`): if `session.AgentID != ""`, call `m.agents.UpdateState(ctx, session.AgentID, meta.AgentStateError, "session lost: shim not recovered after daemon restart")`.
   - In the success branch (after `SetSessionRecoveryInfo`): if `session.AgentID != ""` and the recovered shim reported `spec.StatusRunning`, call `m.agents.UpdateState(ctx, session.AgentID, meta.AgentStateRunning, "")`.
   - After the main recovery loop, add a 'creating cleanup' pass: call `m.store.ListAgents(ctx, &meta.AgentFilter{State: meta.AgentStateCreating})`, and for each agent that does NOT have a successfully recovered session, call `m.agents.UpdateState(ctx, agent.ID, meta.AgentStateError, "agent bootstrap lost: daemon restarted during creating phase")`.
   - To implement the creating cleanup, collect recovered session AgentIDs into a `map[string]bool` during the main loop, then use it for the cleanup pass.
   - Note: `recoverSession` currently does NOT return the shim status — you'll need to either return it from `recoverSession` or re-read it from the processes map. The cleaner approach: change `recoverSession` to return `(spec.Status, error)` (the status from `runtime/status`), so the caller in `RecoverSessions` can use it directly. Handle backward compat by having the failure case return `(spec.StatusStopped, err)` (or just check err != nil).

4. **Update all 5 `NewProcessManager` call sites** (pass the newly-added `agents` argument):
   - `cmd/agentd/main.go` line ~83: `agentd.NewProcessManager(runtimeClasses, sessions, agents, store, cfg)` — `agents` is already created just above.
   - `pkg/ari/server_test.go` line ~88: `agentd.NewProcessManager(runtimeClasses, sessions, agents, store, cfg)` — `agents` is already created in both harnesses.
   - `pkg/ari/server_test.go` line ~205: `agentd.NewProcessManager(runtimeRegistry, sessions, agents, store, cfg)` — same.
   - `pkg/agentd/process_test.go` line ~95: add a `agentMgr := NewAgentManager(store)` and pass it.
   - `pkg/agentd/recovery_test.go` line ~37 in `setupRecoveryTest`: add `agents := NewAgentManager(store)` and pass it.

5. **Add unit tests in `pkg/agentd/recovery_test.go`** covering agent state reconciliation (add after existing tests):
   - `TestRecoverSessions_AgentStateErrorOnDeadShim` — creates a session+agent linked via AgentID; shim dead → session stopped + agent transitions to error. Assert `agents.Get(ctx, agentID).State == meta.AgentStateError`.
   - `TestRecoverSessions_AgentStateRunningOnLiveShim` — creates a session+agent; shim alive+running → session recovered + agent transitions to running. Assert `agents.Get(ctx, agentID).State == meta.AgentStateRunning`.
   - `TestRecoverSessions_CreatingAgentMarkedError` — creates an agent in AgentStateCreating with no live session (no session row); after RecoverSessions, agent should be in AgentStateError.
   - Use the existing `setupRecoveryTest` helper (which will be updated to pass agents). Create agents via `store.CreateAgent` in the test directly (no need to go through AgentManager for setup). Link session to agent by setting `session.AgentID` — but note `createRecoveryTestSession` doesn't set AgentID. You'll need to either extend the helper or add a follow-up `store.UpdateAgent` call... actually, the cleanest approach is to add a separate helper or inline the session creation with AgentID set.
   - For the creating cleanup test: call `store.CreateAgent(ctx, &meta.Agent{ID: agentID, Room: "r", Name: "n", RuntimeClass: "default", State: meta.AgentStateCreating})` directly, run RecoverSessions (no sessions in DB), verify agent.State == AgentStateError.

## Inputs

- ``pkg/agentd/process.go``
- ``pkg/agentd/recovery.go``
- ``pkg/agentd/recovery_test.go``
- ``cmd/agentd/main.go``
- ``pkg/ari/server_test.go``
- ``pkg/agentd/process_test.go``
- ``pkg/agentd/agent.go``
- ``pkg/meta/models.go``
- ``pkg/meta/agent.go``

## Expected Output

- ``pkg/agentd/process.go``
- ``pkg/agentd/recovery.go``
- ``pkg/agentd/recovery_test.go``
- ``cmd/agentd/main.go``
- ``pkg/ari/server_test.go``
- ``pkg/agentd/process_test.go``

## Verification

go test ./pkg/agentd/... -count=1 -timeout 120s && go test ./pkg/ari/... -count=1 -timeout 120s && go build ./...

## Observability Impact

RecoverSessions will emit slog.Info('recovery: reconciled agent state to running', agentId) on success and slog.Warn('recovery: agent stuck in creating, marking error', agentId) for creating-cleanup. Agent state transitions are visible via agent/status and queryable in the agents table.
