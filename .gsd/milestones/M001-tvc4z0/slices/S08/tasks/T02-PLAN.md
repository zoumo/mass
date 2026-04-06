---
estimated_steps: 10
estimated_files: 1
skills_used: []
---

# T02: Session Lifecycle Tests

Test all session state transitions and error handling. Create tests covering session state machine: created → running → stopped, error cases like prompt on stopped session, remove on running session.

## Steps

1. Create tests/integration/session_test.go file
2. Write TestSessionLifecycle function:
   - Start agentd
   - session/new → verify state=created
   - session/prompt → auto-start → verify state=running
   - session/status → returns shim state
   - session/stop → verify state=stopped
   - session/remove → session deleted
3. Write TestSessionPromptStoppedSession function:
   - Start agentd, create session
   - Prompt once (state=running)
   - Stop session (state=stopped)
   - Prompt again → expect InvalidParams error
4. Write TestSessionRemoveRunningSession function:
   - Start agentd, create session
   - Prompt (state=running)
   - Remove session → expect InvalidParams error (protected while running)
5. Write TestSessionList function:
   - Create 3 sessions
   - session/list → verify count=3
   - Remove all sessions
   - session/list → verify count=0
6. Add helper functions for setup/teardown
7. Add timeout contexts for all operations
8. Run tests: go test ./tests/integration/... -run TestSession -v

## Must-Haves

- [ ] TestSessionLifecycle passes with all transitions
- [ ] TestSessionPromptStoppedSession returns InvalidParams error
- [ ] TestSessionRemoveRunningSession returns InvalidParams error (protected)
- [ ] TestSessionList shows correct session count

## Failure Modes

| State | Invalid transition | Error code |
|-------|-------------------|------------|
| created | session/prompt on stopped | InvalidParams |
| running | session/remove | InvalidParams |
| stopped | session/prompt | InvalidParams |

## Negative Tests

- Prompt on stopped session: InvalidParams error
- Remove running session: InvalidParams error
- Remove non-existent session: InvalidParams error
- List with invalid filters: no error, empty result

## Inputs

- `pkg/agentd/session_manager.go` — Session Manager with state machine
- `pkg/ari/client.go` — ARI client for JSON-RPC calls
- `tests/integration/e2e_test.go` — Test setup patterns

## Expected Output

- `tests/integration/session_test.go` — Session lifecycle tests

## Verification

go test ./tests/integration/... -run TestSession -v passes all tests

## Observability Impact

None — test code only