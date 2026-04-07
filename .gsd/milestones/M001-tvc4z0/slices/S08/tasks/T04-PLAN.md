---
estimated_steps: 1
estimated_files: 1
skills_used: []
---

# T04: Multiple Concurrent Sessions Test

Test multiple sessions running concurrently. Create test with multiple sessions (2-3) running simultaneously, verify each responds independently, no interference.

## Inputs

- ``pkg/agentd/session_manager.go``
- ``pkg/agentd/process_manager.go``
- ``pkg/ari/client.go``
- ``tests/integration/e2e_test.go``

## Expected Output

- ``tests/integration/concurrent_test.go``

## Verification

go test ./tests/integration/... -run TestMultipleConcurrent -v passes

## Observability Impact

None — test code only
