---
estimated_steps: 10
estimated_files: 1
skills_used: []
---

# T04: Multiple Concurrent Sessions Test

Test multiple sessions running concurrently. Create test with multiple sessions (2-3) running simultaneously, verify each responds independently, no interference.

## Steps

1. Create tests/integration/concurrent_test.go file
2. Write TestMultipleConcurrentSessions function:
   - Start agentd
   - Prepare 2-3 workspaces (different paths)
   - Create 2-3 sessions with different workspaces
   - Record session IDs for verification
3. Prompt sessions concurrently:
   - Use goroutines with sync.WaitGroup
   - Each goroutine: session/prompt → wait for response
   - Collect results in channel or mutex-protected slice
4. Verify responses:
   - Each session responded successfully
   - Each response matches its session (no cross-talk)
   - All stopReasons are end_turn
5. Stop and cleanup:
   - Stop all sessions
   - Remove all sessions
   - Cleanup all workspaces
   - Stop agentd
6. Add timeout contexts with generous limits for concurrent ops
7. Add error collection and reporting
8. Run test: go test ./tests/integration/... -run TestMultipleConcurrent -v

## Must-Haves

- [ ] TestMultipleConcurrentSessions passes
- [ ] Both/all sessions created successfully
- [ ] Both/all prompts return correct responses
- [ ] No interference between sessions
- [ ] Both/all sessions stopped cleanly

## Failure Modes

| Phase | On error | On timeout | On interference |
|-------|----------|-----------|-----------------|
| Create sessions | RPC error | Context timeout | N/A |
| Concurrent prompts | Goroutine panic | Context timeout | Response mismatch |
| Cleanup | RPC error | Context timeout | N/A |

## Negative Tests

- Large number of concurrent sessions (stress test): verify system handles gracefully
- One session fails: other sessions continue unaffected
- Session creation limit: verify error handling when limit reached

## Inputs

- `pkg/agentd/session_manager.go` — Session Manager
- `pkg/agentd/process_manager.go` — Process Manager
- `pkg/ari/client.go` — ARI client for JSON-RPC calls
- `tests/integration/e2e_test.go` — Test setup patterns

## Expected Output

- `tests/integration/concurrent_test.go` — Concurrent sessions test

## Verification

go test ./tests/integration/... -run TestMultipleConcurrent -v passes

## Observability Impact

None — test code only