---
estimated_steps: 1
estimated_files: 2
skills_used: []
---

# T02: Implement ProcessManager Start workflow

Create ProcessManager struct with RuntimeClassRegistry, SessionManager, meta.Store, config fields. Implement Start(ctx, sessionID) method executing full workflow: get Session → resolve RuntimeClass → generate config.json → create bundle directory with workspace symlink → fork agent-shim process → wait for socket → connect ShimClient → subscribe events → transition session state to "running". ShimProcess struct tracks running shim. Integration test with mockagent shows Start creates running session.

## Inputs

- `pkg/agentd/shim_client.go`
- `pkg/agentd/session.go`
- `pkg/agentd/runtimeclass.go`
- `pkg/agentd/config.go`
- `pkg/meta/models.go`
- `pkg/meta/store.go`
- `pkg/spec/types.go`
- `pkg/spec/state.go`
- `cmd/agent-shim/main.go`

## Expected Output

- `pkg/agentd/process.go`
- `pkg/agentd/process_test.go`

## Verification

go test ./pkg/agentd/... -run TestProcessManagerStart -v passes, showing session state=running, PID>0, events received
