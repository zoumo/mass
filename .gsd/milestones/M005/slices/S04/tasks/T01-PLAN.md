---
estimated_steps: 32
estimated_files: 2
skills_used: []
---

# T01: Async agent/create: return creating immediately, bootstrap in background

Change handleAgentCreate to return state:"creating" immediately and launch a background goroutine that creates the session (in state:"created") and calls processes.Start. Update all existing tests that assert "created" as the initial create result. Add TestARIAgentCreateAsync and TestARIAgentCreateAsyncErrorState.

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

## Inputs

- ``pkg/ari/server.go` — handleAgentCreate current synchronous implementation; handleAgentPrompt for guard placement`
- ``pkg/ari/server_test.go` — TestARIAgentCreateAndList, TestARIAgentStatus, TestARIAgentDeleteRequiresStopped, TestARIAgentDeleteAfterStop to update; newSessionTestHarness for new async tests`
- ``pkg/agentd/process.go` — ProcessManager.Start signature and SessionStateCreated precondition`
- ``pkg/agentd/agent.go` — AgentManager.UpdateState and ErrDeleteNotStopped logic`

## Expected Output

- ``pkg/ari/server.go` — handleAgentCreate async with background goroutine; handleAgentPrompt with creating-state guard`
- ``pkg/ari/server_test.go` — updated existing tests + new TestARIAgentCreateAsync + TestARIAgentCreateAsyncErrorState`

## Verification

go test ./pkg/ari/... -count=1 -timeout 120s
go test ./pkg/ari/... -run TestARIAgentCreateAsync -v -timeout 60s
go test ./pkg/ari/... -run TestARIAgentCreateAsyncErrorState -v -timeout 60s
go build ./...

## Observability Impact

Background goroutine logs slog.Info/slog.Error with agentId and sessionId on bootstrap completion. Failure path writes error message to agent.ErrorMessage (visible via agent/status). State transitions (creating→created/error) are observable via agent/status polling.
