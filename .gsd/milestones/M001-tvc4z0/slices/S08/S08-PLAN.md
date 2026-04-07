# S08: Integration Tests

**Goal:** Integration tests: full pipeline, session lifecycle, agentd restart recovery
**Demo:** After this: Full pipeline works: agentd → agent-shim → mockagent end-to-end; restart recovery verified

## Tasks
- [x] **T01: End-to-End Pipeline Test** — End-to-end test proving agentd → agent-shim → mockagent full lifecycle works. Create integration test that starts agentd daemon, creates workspace, creates session, prompts mockagent, verifies response, stops session, cleans up. Proves all components work together.
  - Estimate: 1h
  - Files: `tests/integration/e2e_test.go`
  - Verify: go test ./tests/integration/... -run TestEndToEndPipeline -v passes
- [x] **T02: Session Lifecycle Tests** — Test all session state transitions and error handling. Create tests covering session state machine: created → running → stopped, error cases like prompt on stopped session, remove on running session.
  - Estimate: 45m
  - Files: `tests/integration/session_test.go`
  - Verify: go test ./tests/integration/... -run TestSession -v passes all tests
- [x] **T03: agentd Restart Recovery Test** — Test agentd restart reconnects to existing shim sockets. Create test that starts session, kills agentd (keeps shim running), restarts agentd, verifies reconnect to existing shim.
  - Estimate: 45m
  - Files: `tests/integration/restart_test.go`
  - Verify: go test ./tests/integration/... -run TestAgentdRestart -v passes
- [x] **T04: Multiple Concurrent Sessions Test** — Test multiple sessions running concurrently. Create test with multiple sessions (2-3) running simultaneously, verify each responds independently, no interference.
  - Estimate: 30m
  - Files: `tests/integration/concurrent_test.go`
  - Verify: go test ./tests/integration/... -run TestMultipleConcurrent -v passes
