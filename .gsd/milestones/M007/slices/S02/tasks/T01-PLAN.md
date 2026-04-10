---
estimated_steps: 21
estimated_files: 2
skills_used: []
---

# T01: Wire runtime/stateChange notifications to DB; remove direct post-bootstrap state writes from process.go

Remove the direct UpdateStatus(StatusRunning) call from Start() step 9. Add a runtime/stateChange branch in the DialWithHandler notification closures inside Start() and recoverAgent() so incoming stateChange notifications drive DB state transitions. Write unit tests that prove the boundary without requiring a real shim binary.

## Steps

1. In `pkg/agentd/process.go`, locate the `DialWithHandler` notification closure in `Start()` (the one that handles `events.MethodSessionUpdate`). Add a `case events.MethodRuntimeStateChange:` branch that:
   - Calls `ParseRuntimeStateChange(params)` to decode the notification
   - On error: logs Warn and returns (drop malformed notification)
   - On success: calls `m.agents.UpdateStatus(updateCtx, workspace, name, meta.AgentStatus{State: spec.Status(p.Status), ShimSocketPath: shimProc.SocketPath, ShimStateDir: shimProc.StateDir, ShimPID: shimProc.PID})` with a 5-second background context (not the request context — the request may have ended)
   - Logs Info with agent_key and new state

2. Delete step 9 from `Start()` — the `m.agents.UpdateStatus(ctx, workspace, name, meta.AgentStatus{State: spec.StatusRunning, ...})` call after Subscribe. The shim will emit creating→idle stateChange when its ACP handshake completes; the notification handler will update the DB.

3. In the same file, locate the `DialWithHandler` notification closure in `recoverAgent()`. Apply the same runtime/stateChange handling (same pattern, but the workspace/name come from the agent parameter — already in scope).

4. Create `pkg/agentd/shim_boundary_test.go`. Define a helper `invokeStateChangeHandler(t, pm, store, workspace, name, prevStatus, newStatus string)` that:
   - Looks up the agent's live ShimProcess in pm.processes
   - Directly calls the stateChange handler by doing a DialWithHandler to the mock server and having the mock server emit a stateChange notification
   - OR (simpler): extracts the handler via a test-visible accessor

   Actually the cleanest approach: write a `processStateChangeForTest(pm, workspace, name, newStatus)` helper that crafts a `events.RuntimeStateChangeParams` JSON, then constructs a full stateChange notification and delivers it by having the mock shim server emit it after subscribe. Use the existing `mockShimServer` + `newMockShimServer` from shim_client_test.go.

   Write these tests:
   - `TestStateChange_CreatingToIdle_UpdatesDB`: Set up ProcessManager + Store. Create agent at StatusCreating. Create mockShimServer that queues a runtime/stateChange creating→idle notification to emit after Subscribe. Create a fake ShimProcess with a connected ShimClient (use DialWithHandler with the stateChange-aware handler — call the helper that builds the closure). Register ShimProcess in pm.processes. Call Subscribe. Wait for stateChange. Assert DB state == StatusIdle.
   - `TestStateChange_RunningToIdle_UpdatesDB`: Same pattern, idle→running→idle cycle via two successive stateChange notifications.
   - `TestStart_DoesNotWriteStatusRunning`: After Start() returns (using mock shim that emits stateChange before return), verify the ONLY UpdateStatus calls were (a) the bootstrap config write (creating) and (b) the stateChange-driven update. This requires a spy AgentManager wrapper or checking that DB never transiently shows StatusRunning before the stateChange. Simplest: stub the mock to NOT emit stateChange, call Start() up to the Subscribe step (can't easily do this without a real shim socket). Instead, test via a lower-level approach: after mocking Subscribe success, verify DB is NOT StatusRunning by reading store directly.

   **Practical note**: TestProcessManagerStart is the only test that calls the full Start() pipeline and it requires a real shim binary (pre-existing failure). For the boundary test, test the notification handler in isolation using the `DialWithHandler` pattern directly — no need to call Start().

5. Run `go test ./pkg/agentd/... -run 'TestStateChange' -v` to verify new tests pass.
6. Run `go build ./...` to confirm no compilation errors.

## Inputs

- `pkg/agentd/process.go`
- `pkg/agentd/shim_client.go`
- `pkg/agentd/shim_client_test.go`
- `pkg/agentd/recovery.go`
- `pkg/agentd/agent.go`
- `pkg/events/envelope.go`
- `pkg/meta/models.go`
- `pkg/spec/state_types.go`

## Expected Output

- `pkg/agentd/process.go`
- `pkg/agentd/shim_boundary_test.go`

## Verification

go test ./pkg/agentd/... -run 'TestStateChange' -count=1 -timeout 30s
go build ./...

## Observability Impact

Adds slog.Info('stateChange: updating agent DB state', 'agent_key', key, 'prev', prevStatus, 'new', newStatus) in the notification handler so future agents can diagnose state transition gaps.
