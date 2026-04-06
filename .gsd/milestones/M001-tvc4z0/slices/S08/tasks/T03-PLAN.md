---
estimated_steps: 12
estimated_files: 1
skills_used: []
---

# T03: agentd Restart Recovery Test

Test agentd restart reconnects to existing shim sockets. Create test that starts session, kills agentd (keeps shim running), restarts agentd, verifies reconnect to existing shim.

## Steps

1. Create tests/integration/restart_test.go file
2. Write TestAgentdRestartRecovery function:
   - Start agentd with test config
   - Prepare workspace
   - Create session
   - Prompt session (shim starts, state=running)
   - Verify shim is running (check process or socket)
3. Kill agentd process (SIGTERM, keeps shim running):
   - Find agentd PID
   - Send SIGTERM
   - Wait for process to exit
   - Verify shim still running
4. Restart agentd with same socket/config:
   - Start new agentd instance
   - Wait for socket to be ready
5. Verify reconnection:
   - Call session/status → verify shim reconnected, state=running
   - Call session/prompt → verify shim responds
6. Clean up:
   - Stop session
   - Remove session
   - Cleanup workspace
   - Stop agentd
7. Add helper functions for process management
8. Add timeout contexts
9. Run test: go test ./tests/integration/... -run TestAgentdRestart -v

## Must-Haves

- [ ] TestAgentdRestartRecovery passes
- [ ] agentd reconnects to existing shim on startup
- [ ] session/status shows running after restart
- [ ] session/prompt works after restart

## Failure Modes

| Phase | On error | On timeout | On unexpected state |
|-------|----------|-----------|---------------------|
| Kill agentd | Process not found | Process still running | N/A |
| Restart agentd | Socket bind error | Not ready in time | N/A |
| Reconnect | Shim socket gone | No response | state != running |

## Negative Tests

- Shim killed before restart: session/status shows disconnected state
- Socket file deleted: agentd fails to start or reconnect fails
- Multiple restarts: each restart reconnects successfully

## Inputs

- `pkg/agentd/process_manager.go` — Process Manager with shim tracking
- `pkg/ari/client.go` — ARI client for JSON-RPC calls
- `tests/integration/e2e_test.go` — Test setup patterns

## Expected Output

- `tests/integration/restart_test.go` — Restart recovery test

## Verification

go test ./tests/integration/... -run TestAgentdRestart -v passes

## Observability Impact

None — test code only