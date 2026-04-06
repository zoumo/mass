---
estimated_steps: 12
estimated_files: 1
skills_used: []
---

# T01: End-to-End Pipeline Test

End-to-end test proving agentd → agent-shim → mockagent full lifecycle works. Create integration test that starts agentd daemon, creates workspace, creates session, prompts mockagent, verifies response, stops session, cleans up. Proves all components work together.

## Steps

1. Create tests/integration/ directory structure
2. Create tests/integration/e2e_test.go with build tag for integration tests
3. Create TestEndToEndPipeline function with t.TempDir() for workspace root
4. Write setupAgentd helper: starts agentd daemon with test config (mockagent runtime class)
5. Write test config YAML with mockagent runtime class pointing to cmd/mockagent
6. Write test body:
   - Start agentd daemon with test config
   - Call workspace/prepare via ARI client
   - Call session/new via ARI client
   - Call session/prompt via ARI client
   - Verify mockagent response (stopReason=end_turn)
   - Call session/stop
   - Call session/remove
   - Call workspace/cleanup
   - Stop agentd daemon
7. Use existing pkg/ari/client.go for ARI calls
8. Handle cleanup with t.Cleanup() for graceful shutdown
9. Add timeout context for each operation
10. Run test: go test -run TestEndToEndPipeline

## Must-Haves

- [ ] TestEndToEndPipeline passes with full lifecycle
- [ ] agentd starts and listens on socket
- [ ] workspace/prepare creates workspace
- [ ] session/new creates session with state=created
- [ ] session/prompt auto-starts shim, returns stopReason=end_turn
- [ ] session/stop stops shim gracefully
- [ ] session/remove deletes session
- [ ] workspace/cleanup removes workspace
- [ ] agentd shuts down cleanly

## Failure Modes

| Component | On error | On timeout | On unexpected state |
|-----------|----------|-----------|---------------------|
| agentd start | t.Fatalf with error | Context timeout | N/A |
| workspace/prepare | Return RPC error | Context timeout | N/A |
| session/new | Return RPC error | Context timeout | state != created |
| session/prompt | Return RPC error | Context timeout | stopReason != end_turn |

## Negative Tests

- agentd socket missing: test fails with clear error
- mockagent binary missing: shim fails to start, prompt returns error
- Invalid workspace spec: workspace/prepare returns error
- Invalid runtimeClass: session/new returns error

## Inputs

- `pkg/ari/client.go` — ARI client for JSON-RPC calls
- `cmd/agentd/main.go` — agentd entry point
- `cmd/mockagent/` — mock agent for testing
- `pkg/spec/types.go` — Session/Workspace types

## Expected Output

- `tests/integration/e2e_test.go` — End-to-end integration test

## Verification

go test ./tests/integration/... -run TestEndToEndPipeline -v passes

## Observability Impact

None — test code only