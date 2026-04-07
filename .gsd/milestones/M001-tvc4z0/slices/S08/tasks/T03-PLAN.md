---
estimated_steps: 1
estimated_files: 1
skills_used: []
---

# T03: agentd Restart Recovery Test

Test agentd restart reconnects to existing shim sockets. Create test that starts session, kills agentd (keeps shim running), restarts agentd, verifies reconnect to existing shim.

## Inputs

- ``pkg/agentd/process_manager.go``
- ``pkg/ari/client.go``
- ``tests/integration/e2e_test.go``

## Expected Output

- ``tests/integration/restart_test.go``

## Verification

go test ./tests/integration/... -run TestAgentdRestart -v passes

## Observability Impact

None — test code only
