# S04: Agent Lifecycle — Async Create, Stop/Delete Separation, Restart

**Goal:** Replace synchronous agent/create with async semantics (returns creating immediately, bootstrap in background), implement the real agent/restart handler (was a MethodNotFound stub), add OAR_AGENT_ID/OAR_AGENT_NAME env vars to generateConfig, and wire the agentdctl restart subcommand.
**Demo:** After this: TBD

## Tasks
- [x] **T01: Made handleAgentCreate return state:"creating" immediately with background goroutine bootstrap, added creating-state guard to handleAgentPrompt, updated 20+ tests with pollAgentUntilReady helper, added TestARIAgentCreateAsync and TestARIAgentCreateAsyncErrorState — all pass** — Change handleAgentCreate to return state:"creating" immediately and launch a background goroutine that creates the session (in state:"created") and calls processes.Start. Update all existing tests that assert "created" as the initial create result. Add TestARIAgentCreateAsync and TestARIAgentCreateAsyncErrorState.

## Steps

1. In handleAgentCreate (pkg/ari/server.go): change agent initial state from AgentStateCreated to AgentStateCreating (line ~1008: `State: meta.AgentStateCreated`). Change the reply to return state:"creating".

2. Move session creation (meta.Session{...}) and AcquireWorkspace/registry.Acquire calls into a background goroutine launched AFTER the RPC reply. The goroutine must:
   - Use context.Background() (NOT the request ctx, which is dead after Reply)
   - Create the session with State: meta.SessionStateCreated (not creating — ProcessManager.Start requires SessionStateCreated)
   - Call h.srv.processes.Start(bgCtx, sessionId)
   - On success: h.srv.agents.UpdateState(bgCtx, agentId, meta.AgentStateCreated, "")
   - On failure: h.srv.agents.UpdateState(bgCtx, agentId, meta.AgentStateError, err.Error())
   - Log outcome with slog at Info (success) or Error (failure) including agentId and sessionId
   - Bound the goroutine with a 90-second timeout: `bgCtx, bgCancel := context.WithTimeout(context.Background(), 90*time.Second)`

3. Reply immediately after creating the agent record (before the goroutine): `conn.Reply(ctx, req.ID, AgentCreateResult{AgentId: agentId, State: "creating"})`

4. In pkg/ari/server.go handleAgentPrompt: add an early guard — if agent.State == meta.AgentStateCreating, reply with CodeInvalidParams "agent is still being provisioned; poll agent/status until state is 'created'" and return.

5. In pkg/ari/server_test.go: update all tests that assert result.State == "created" after agentCreate to accept "creating":
   - TestARIAgentCreateAndList: update both the create result assertion and the list state assertion to "creating" (agent not yet bootstrapped in test harness with no real runtime)
   - TestARIAgentStatus: update to expect "creating"
   - TestARIAgentDeleteRequiresStopped: the agent is now "creating" (not stopped), so delete still fails — assertion still valid but error message may differ ("stopped" must still appear if agents.Delete checks state). Verify this still works.
   - TestARIAgentDeleteAfterStop: after create, state is "creating"; the test calls agentStop which calls handleAgentStop — handleAgentStop calls processes.Stop (no-op for non-running session) then agents.UpdateState(stopped). Since agent is in "creating" state, check whether UpdateState allows creating→stopped. If not, add the transition in agent.go.

6. Add TestARIAgentCreateAsync in server_test.go (uses newSessionTestHarness — real mockagent shim):
   - Call agent/create → assert result.State == "creating"
   - Poll agent/status in a loop (max 30s, 200ms interval) until state != "creating"
   - Assert final state == "created"
   - Assert shimState is present (session is running)
   - Stop + delete cleanup

7. Add TestARIAgentCreateAsyncErrorState (uses newSessionTestHarness with invalid runtimeClass "nonexistent-class"):
   - Call agent/create with runtimeClass "nonexistent-class" → assert result.State == "creating"
   - Poll agent/status until state != "creating"
   - Assert final state == "error"
   - Assert agent.ErrorMessage non-empty

## Key constraint
agent.State == AgentStateCreating means "creating" is NOT "stopped", so agents.Delete should still fail for creating agents. Verify ErrDeleteNotStopped handles creating state (it already does: the check is `current.State != meta.AgentStateStopped`).

The session row is created inside the goroutine, not before Reply. So between Reply and goroutine start, a brief window has no session row. agent/status during this window returns nil session and no shimState — this is correct behavior.
  - Estimate: 3-4 hours
  - Files: pkg/ari/server.go, pkg/ari/server_test.go
  - Verify: go test ./pkg/ari/... -count=1 -timeout 120s
go test ./pkg/ari/... -run TestARIAgentCreateAsync -v -timeout 60s
go test ./pkg/ari/... -run TestARIAgentCreateAsyncErrorState -v -timeout 60s
go build ./...
- [x] **T02: Replaced MethodNotFound stub with real async handleAgentRestart, added AgentRestartResult to types.go, replaced TestARIAgentRestartStub with TestARIAgentRestartAsync, and wired agentdctl restart subcommand — all tests pass** — Replace the MethodNotFound stub with a real handleAgentRestart implementation. Add AgentRestartResult type to types.go. Replace TestARIAgentRestartStub with TestARIAgentRestartAsync. Add the agentdctl restart subcommand.

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
  - Estimate: 2-3 hours
  - Files: pkg/ari/types.go, pkg/ari/server.go, pkg/ari/server_test.go, cmd/agentdctl/agent.go
  - Verify: go test ./pkg/ari/... -run TestARIAgentRestartAsync -v -timeout 90s
go test ./pkg/ari/... -count=1 -timeout 120s
go build ./...
go build -o /tmp/agentdctl ./cmd/agentdctl && /tmp/agentdctl agent restart --help
- [x] **T03: Added OAR_AGENT_ID and OAR_AGENT_NAME to generateConfig mcpServers env block, keeping OAR_SESSION_ID/OAR_ROOM_AGENT as deprecated aliases** — In pkg/agentd/process.go generateConfig, add OAR_AGENT_ID (= session.AgentID) and OAR_AGENT_NAME (= session.RoomAgent) to the MCP server env vars, keeping OAR_SESSION_ID as a deprecated alias for backward compat until S06 removes it.

## Steps

1. In pkg/agentd/process.go, locate the mcpServers env var block (~line 280):
   ```go
   {Name: "OAR_AGENTD_SOCKET", Value: m.config.Socket},
   {Name: "OAR_ROOM_NAME",     Value: session.Room},
   {Name: "OAR_SESSION_ID",    Value: session.ID},    // deprecated: remove in S06
   {Name: "OAR_ROOM_AGENT",    Value: session.RoomAgent},
   ```
   Add two new entries and annotate the deprecated one:
   ```go
   {Name: "OAR_AGENTD_SOCKET", Value: m.config.Socket},
   {Name: "OAR_ROOM_NAME",     Value: session.Room},
   {Name: "OAR_AGENT_ID",      Value: session.AgentID},   // agent-level identity (M005)
   {Name: "OAR_AGENT_NAME",    Value: session.RoomAgent}, // agent name within room (M005)
   {Name: "OAR_SESSION_ID",    Value: session.ID},        // deprecated: alias for OAR_AGENT_ID; remove in S06
   {Name: "OAR_ROOM_AGENT",    Value: session.RoomAgent}, // deprecated: alias for OAR_AGENT_NAME; remove in S06
   ```
   (OAR_ROOM_AGENT is also a deprecated alias for OAR_AGENT_NAME — keep both for now)

2. Run go build ./... to confirm clean build.

3. Run go test ./pkg/agentd/... to confirm existing tests pass.

## Notes
- session.AgentID may be empty string for sessions not linked to an agent (edge case in test harness). This is fine — the env var is just empty, which is the same as before.
- This change is additive: no existing behavior changes, existing tests need no updates.
- OAR_SESSION_ID and OAR_ROOM_AGENT remain present as deprecated aliases; S06 removes them when room-mcp-server is rewritten to use the new names.
  - Estimate: 30 minutes
  - Files: pkg/agentd/process.go
  - Verify: go build ./...
go test ./pkg/agentd/... -count=1 -timeout 60s
