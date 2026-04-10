# S02: agentd Core Adaptation

**Goal:** Enforce the shim write authority boundary (D088) by wiring runtime/stateChange notifications into DB state updates and removing agentd's direct post-bootstrap state writes. Implement RestartPolicy tryReload/alwaysNew (D089) in RecoverSessions so tryReload attempts session/load on the connected shim and falls back gracefully, while alwaysNew skips it entirely.
**Demo:** After this: unit tests prove shim-only state writes post-bootstrap, tryReload/alwaysNew recovery semantics, and no Session concept anywhere in agentd.

## Must-Haves

- go test ./pkg/agentd/... passes (minus pre-existing TestProcessManagerStart which requires real shim binary)
- Unit tests prove: stateChange notifications drive DB transitions; Start() does not call UpdateStatus(StatusRunning) directly; tryReload calls session/load and falls back on failure; alwaysNew skips session/load
- meta.AgentSpec.RestartPolicy values updated to "tryReload" | "alwaysNew" (comment corrected)
- No Session concept anywhere in pkg/agentd (already true from S01, maintained)
- go build ./... remains green

## Proof Level

- This slice proves: contract

## Integration Closure

pkg/agentd is the sole modified package. All changes are unit-test-provable against the mockShimServer. pkg/rpc/server.go (shim-side session/load handler) is out of scope for S02 — full end-to-end tryReload requires S05 integration tests. S03 ARI handlers consume the updated Start() (no direct StatusRunning write) and must poll for StatusIdle via DB after agent/create.

## Verification

- New slog INFO/WARN lines: "stateChange: updating DB state" and "tryReload: session/load failed, continuing" — both keyed by agent_key for grep-ability.

## Tasks

- [x] **T01: Wire runtime/stateChange notifications to DB; remove direct post-bootstrap state writes from process.go** `est:3h`
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
  - Files: `pkg/agentd/process.go`, `pkg/agentd/shim_boundary_test.go`
  - Verify: go test ./pkg/agentd/... -run 'TestStateChange' -count=1 -timeout 30s
go build ./...

- [x] **T02: Add session/load to ShimClient; implement RestartPolicy tryReload/alwaysNew in RecoverSessions** `est:3h`
  Add a `Load(ctx, sessionID)` RPC method to ShimClient for the `session/load` clean-break call. Update `meta.AgentSpec.RestartPolicy` comment to reflect the new 'tryReload'/'alwaysNew' values. Implement RestartPolicy branching in `recoverAgent()`: tryReload reads the persisted ACP sessionId from the shim's state.json and calls `shimClient.Load()`, falling back silently on any failure; alwaysNew skips session/load entirely. Write unit tests proving both paths.

## Steps

1. In `pkg/agentd/shim_client.go`, add:
```go
// SessionLoadParams is the JSON body for the "session/load" RPC method.
type SessionLoadParams struct {
    SessionID string `json:"sessionId"`
}

// Load sends session/load to the shim with the given ACP sessionId.
// Returns nil on success; returns error if the shim rejects the call (e.g.
// runtime does not support session/load) so the caller can fall back.
func (c *ShimClient) Load(ctx context.Context, sessionID string) error {
    if err := c.call(ctx, "session/load", SessionLoadParams{SessionID: sessionID}, nil); err != nil {
        return fmt.Errorf("shim_client: session/load: session=%s: %w", c.socketPath, err)
    }
    return nil
}
```

2. In `pkg/meta/models.go`, update the RestartPolicy comment in AgentSpec from 'Values: never/on-failure/always' to:
```go
// RestartPolicy controls session continuation on agent restart.
// Values: "tryReload" — attempt ACP session/load to restore conversation history;
//         "alwaysNew" (default) — always start a fresh ACP session.
RestartPolicy string `json:"restartPolicy,omitempty"`
```
   Also add constants for the two values:
```go
const (
    RestartPolicyTryReload = "tryReload"
    RestartPolicyAlwaysNew = "alwaysNew"
)
```

3. In `pkg/agentd/recovery.go`, update `recoverAgent()`. After the `status, err := client.Status(ctx)` call and the switch reconciliation block (roughly after the atomic Subscribe call), add a tryReload block:

```go
// Apply RestartPolicy: tryReload attempts ACP session/load to restore
// conversation history. alwaysNew (default) starts fresh.
if agent.Spec.RestartPolicy == meta.RestartPolicyTryReload {
    sessionID, readErr := m.readStateSessionID(agent.Status.ShimStateDir)
    if readErr != nil {
        logger.Info("tryReload: could not read sessionId from state file, skipping",
            "error", readErr)
    } else if sessionID != "" {
        if loadErr := client.Load(ctx, sessionID); loadErr != nil {
            logger.Info("tryReload: session/load failed, falling back",
                "session_id", sessionID, "error", loadErr)
        } else {
            logger.Info("tryReload: session/load succeeded", "session_id", sessionID)
        }
    } else {
        logger.Info("tryReload: no sessionId in state file, skipping")
    }
}
```

   Add a private helper method `readStateSessionID(stateDir string) (string, error)` to ProcessManager:
```go
func (m *ProcessManager) readStateSessionID(stateDir string) (string, error) {
    if stateDir == "" {
        return "", fmt.Errorf("no state dir")
    }
    state, err := spec.ReadState(stateDir)
    if err != nil {
        return "", err
    }
    return state.ID, nil
}
```

4. Extend `mockShimServer` in `pkg/agentd/shim_client_test.go`:
   - Add `loadCalled bool` and `loadCalledWith string` fields (protected by mu)
   - Add `loadSessionErr error` field (default nil = success)
   - Handle `session/load` in the server's dispatch switch, record the call

5. Add tests in `pkg/agentd/recovery_test.go`:
   - `TestRecovery_TryReload_AttemptsSessionLoad`: create agent with RestartPolicy=tryReload, create mock state.json with known sessionId in ShimStateDir, configure mock shim to handle session/load successfully, run recoverAgent, assert mockShimServer.loadCalled==true and loadCalledWith==sessionId
   - `TestRecovery_TryReload_FallsBackOnLoadFailure`: mock shim returns error for session/load, verify recoverAgent still succeeds (returns no error, agent is in processes map)
   - `TestRecovery_TryReload_FallsBackOnMissingStateFile`: ShimStateDir is a non-existent path, verify recoverAgent still succeeds without panicking
   - `TestRecovery_AlwaysNew_SkipsSessionLoad`: agent with RestartPolicy=alwaysNew (or empty), run recoverAgent, assert mockShimServer.loadCalled==false

6. Add `TestShimClient_Load_Success` and `TestShimClient_Load_RpcError` to `pkg/agentd/shim_client_test.go`.

7. Run `go test ./pkg/agentd/... -run 'TestRecovery_TryReload|TestRecovery_AlwaysNew|TestShimClient_Load' -count=1 -timeout 30s`.
8. Run `go test ./pkg/agentd/... -count=1 -timeout 60s` to confirm full suite (minus pre-existing TestProcessManagerStart).
9. Run `go build ./...` to confirm green build.
  - Files: `pkg/agentd/shim_client.go`, `pkg/agentd/shim_client_test.go`, `pkg/agentd/recovery.go`, `pkg/agentd/recovery_test.go`, `pkg/meta/models.go`
  - Verify: go test ./pkg/agentd/... -run 'TestRecovery_TryReload|TestRecovery_AlwaysNew|TestShimClient_Load' -count=1 -timeout 30s
go test ./pkg/agentd/... -count=1 -timeout 60s 2>&1 | grep -v 'TestProcessManagerStart'
go build ./...

## Files Likely Touched

- pkg/agentd/process.go
- pkg/agentd/shim_boundary_test.go
- pkg/agentd/shim_client.go
- pkg/agentd/shim_client_test.go
- pkg/agentd/recovery.go
- pkg/agentd/recovery_test.go
- pkg/meta/models.go
