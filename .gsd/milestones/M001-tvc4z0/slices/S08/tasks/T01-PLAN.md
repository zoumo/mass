---
estimated_steps: 1
estimated_files: 1
skills_used: []
---

# T01: End-to-End Pipeline Test

End-to-end test proving agentd → agent-shim → mockagent full lifecycle works. Create integration test that starts agentd daemon, creates workspace, creates session, prompts mockagent, verifies response, stops session, cleans up. Proves all components work together.

## Inputs

- ``pkg/ari/client.go``
- ``cmd/agentd/main.go``
- ``cmd/mockagent/``
- ``pkg/spec/types.go``

## Expected Output

- ``tests/integration/e2e_test.go``

## Verification

go test ./tests/integration/... -run TestEndToEndPipeline -v passes

## Observability Impact

None — test code only
