---
estimated_steps: 1
estimated_files: 2
skills_used: []
---

# T03: Implemented Stop/State/Connect methods on ProcessManager; TestProcessManagerStart failing due to shim handshake issue

Implement ProcessManager.Stop(ctx, sessionID): call ShimClient.Shutdown RPC, wait for process exit, kill if timeout, remove bundle directory, transition session to "stopped". Implement State(ctx, sessionID): call ShimClient.GetState, return shim status. Implement Connect(ctx, sessionID): return ShimClient for direct RPC access. Add shimProcesses map with sync.RWMutex for concurrent access. Comprehensive integration tests: TestProcessManagerPrompt, TestProcessManagerStop, TestProcessManagerFullLifecycle, TestProcessManagerMultipleSessions, TestProcessManagerProcessCrash, TestProcessManagerBadRuntimeClass.

## Inputs

- `pkg/agentd/process.go`
- `pkg/agentd/shim_client.go`
- `pkg/agentd/session.go`

## Expected Output

- `pkg/agentd/process.go`
- `pkg/agentd/process_test.go`

## Verification

go test ./pkg/agentd/... -run ProcessManager -v passes all 8+ integration tests, showing full lifecycle from created → running → stopped
