---
estimated_steps: 1
estimated_files: 1
skills_used: []
---

# T02: Session Lifecycle Tests

Test all session state transitions and error handling. Create tests covering session state machine: created → running → stopped, error cases like prompt on stopped session, remove on running session.

## Inputs

- ``pkg/agentd/session_manager.go``
- ``pkg/ari/client.go``
- ``tests/integration/e2e_test.go``

## Expected Output

- ``tests/integration/session_test.go``

## Verification

go test ./tests/integration/... -run TestSession -v passes all tests

## Observability Impact

None — test code only
