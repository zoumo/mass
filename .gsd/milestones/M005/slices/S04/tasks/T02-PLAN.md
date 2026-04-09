---
estimated_steps: 53
estimated_files: 4
skills_used: []
---

# T02: Implement handleAgentRestart + TestARIAgentRestartAsync + agentdctl restart subcommand

Replace the MethodNotFound stub with a real handleAgentRestart implementation. Add AgentRestartResult type to types.go. Replace TestARIAgentRestartStub with TestARIAgentRestartAsync. Add the agentdctl restart subcommand.

## Steps

1. In pkg/ari/types.go: add AgentRestartResult struct after AgentRestartParams:
   ```go
   // AgentRestartResult is the response for agent/restart.
   type AgentRestartResult struct {
       AgentId string `json:"agentId"`
       State   string `json:"state"`
   }
   ```

2. In pkg/ari/server.go: replace handleAgentRestart stub with real implementation:
   ```
   handleAgentRestart:
   1. Unmarshal AgentRestartParams
   2. Get agent — validate exists (404 on nil)
   3. Validate state is stopped or error — return CodeInvalidParams if not
   4. Find linked session via linkedSessionForAgent (may be nil)
   5. Generate new sessionId = uuid.New().String()
   6. Update agent state to creating: agents.UpdateState(ctx, agentId, AgentStateCreating, "")
   7. Reply immediately: AgentRestartResult{AgentId: agentId, State: "creating"}
   8. Launch background goroutine (context.Background(), 90s timeout):
      a. If old session exists: sessions.Delete(bgCtx, oldSession.ID) + registry.Release(oldSession.WorkspaceID, oldSession.ID)
      b. Get agent again (for WorkspaceID, Room, Name, RuntimeClass, Labels): agents.Get(bgCtx, agentId)
      c. Create new session: sessions.Create(bgCtx, &meta.Session{ID: newSessionId, AgentID: agentId, State: meta.SessionStateCreated, WorkspaceID: agent.WorkspaceID, RuntimeClass: agent.RuntimeClass, Room: agent.Room, RoomAgent: agent.Name, Labels: agent.Labels, CreatedAt: time.Now(), UpdatedAt: time.Now()})
      d. AcquireWorkspace(bgCtx, agent.WorkspaceID, newSessionId) + registry.Acquire(agent.WorkspaceID, newSessionId)
      e. processes.Start(bgCtx, newSessionId)
      f. On success: agents.UpdateState(bgCtx, agentId, AgentStateCreated, "")
      g. On failure: agents.UpdateState(bgCtx, agentId, AgentStateError, err.Error()) + cleanup new session
      h. Log outcome with slog including agentId, oldSessionId (if any), newSessionId
   ```
   Note: The old session deletion happens inside the goroutine (not before Reply) to keep the critical path minimal. Between Reply and goroutine start, the agent is in "creating" state — any prompt attempts will hit the guard added in T01.

3. In pkg/ari/server_test.go: replace TestARIAgentRestartStub with TestARIAgentRestartAsync (uses newSessionTestHarness):
   - create agent → poll until state == "created" (max 30s)
   - send a prompt to verify agent responds
   - agent/stop → assert state == "stopped"
   - agent/restart → assert result.State == "creating"
   - poll agent/status until state != "creating" (max 30s)
   - assert final state == "created"
   - send a second prompt to verify restart completed
   - agent/stop + agent/delete cleanup

4. In cmd/agentdctl/agent.go: add agentRestartCmd:
   ```go
   var agentRestartCmd = &cobra.Command{
       Use:   "restart <agent-id>",
       Short: "Restart a stopped agent",
       Args:  cobra.ExactArgs(1),
       RunE:  runAgentRestart,
   }
   ```
   Add runAgentRestart function and register agentRestartCmd in init(). Use AgentRestartParams + AgentRestartResult.

## Key constraint
The old session is deleted inside the goroutine, not before Reply. This avoids a race where Reply has not yet been sent when the goroutine tries to delete. The agent state transitions to "creating" synchronously before Reply so any concurrent prompt calls see the guard.

Agents.UpdateState must allow stopped→creating and error→creating. Check pkg/agentd/agent.go UpdateState — if it just calls store.UpdateAgent without validating transitions, it works as-is. If there is a validTransitions check in the agent manager (unlike session manager), we may need to add those transitions.

## Inputs

- ``pkg/ari/server.go` — handleAgentRestart stub (to replace); handleAgentDelete pattern (pre-fetch session, delete agent, delete session, release registry) as restart analog; T01 output: handleAgentCreate goroutine pattern to copy`
- ``pkg/ari/types.go` — AgentRestartParams exists, AgentRestartResult missing`
- ``pkg/ari/server_test.go` — TestARIAgentRestartStub (to replace); TestARIAgentPrompt (newSessionTestHarness pattern to copy)`
- ``pkg/agentd/agent.go` — UpdateState method signature`
- ``cmd/agentdctl/agent.go` — existing subcommand pattern (agentStopCmd) to copy for restart`

## Expected Output

- ``pkg/ari/types.go` — AgentRestartResult added`
- ``pkg/ari/server.go` — handleAgentRestart real implementation`
- ``pkg/ari/server_test.go` — TestARIAgentRestartAsync replacing TestARIAgentRestartStub`
- ``cmd/agentdctl/agent.go` — agentRestartCmd subcommand`

## Verification

go test ./pkg/ari/... -run TestARIAgentRestartAsync -v -timeout 90s
go test ./pkg/ari/... -count=1 -timeout 120s
go build ./...
go build -o /tmp/agentdctl ./cmd/agentdctl && /tmp/agentdctl agent restart --help

## Observability Impact

Restart goroutine logs slog.Info/slog.Error with agentId, oldSessionId, newSessionId on completion. State transitions observable via agent/status polling.
