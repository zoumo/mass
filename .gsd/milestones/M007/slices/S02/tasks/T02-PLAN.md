---
estimated_steps: 79
estimated_files: 5
skills_used: []
---

# T02: Add session/load to ShimClient; implement RestartPolicy tryReload/alwaysNew in RecoverSessions

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

## Inputs

- `pkg/agentd/process.go`
- `pkg/agentd/shim_client.go`
- `pkg/agentd/shim_client_test.go`
- `pkg/agentd/recovery.go`
- `pkg/agentd/recovery_test.go`
- `pkg/meta/models.go`
- `pkg/spec/state.go`
- `pkg/spec/state_types.go`

## Expected Output

- `pkg/agentd/shim_client.go`
- `pkg/agentd/shim_client_test.go`
- `pkg/agentd/recovery.go`
- `pkg/agentd/recovery_test.go`
- `pkg/meta/models.go`

## Verification

go test ./pkg/agentd/... -run 'TestRecovery_TryReload|TestRecovery_AlwaysNew|TestShimClient_Load' -count=1 -timeout 30s
go test ./pkg/agentd/... -count=1 -timeout 60s 2>&1 | grep -v 'TestProcessManagerStart'
go build ./...

## Observability Impact

Adds structured slog lines for tryReload outcomes (session/load success/failure/skipped) keyed by agent_key so recovery diagnostics are traceable in daemon logs.
