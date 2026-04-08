---
estimated_steps: 34
estimated_files: 3
skills_used: []
---

# T02: Full regression verification and R039 validation gate

## Description

Run the complete test suite to verify that the timeout changes from T01 don't break any existing tests, and that the real CLI tests pass (or skip gracefully). This is the slice's verification gate that proves R039.

## Steps

1. **Build all binaries** (agentd, agent-shim, mockagent):
   ```
   go build -o bin/agentd ./cmd/agentd
   go build -o bin/agent-shim ./cmd/agent-shim
   go build -o bin/mockagent ./internal/testutil/mockagent
   ```

2. **Run existing integration tests** — verify no regressions from timeout changes:
   ```
   go test ./tests/integration -run 'TestEndToEnd|TestSession|TestConcurrent|TestAgentdRestart' -count=1 -v -timeout 180s
   ```
   All existing tests must still pass.

3. **Run all unit tests** — verify no regressions in pkg/ari and pkg/agentd:
   ```
   go test ./pkg/... -count=1 -timeout 120s
   ```

4. **Run real CLI tests** and capture output:
   ```
   go test ./tests/integration -run TestRealCLI -count=1 -v -timeout 180s
   ```
   Tests should pass if external CLIs and API keys are available, or skip with clear messages.

5. **Verify the timeout values** are correct in source:
   ```
   rg '30 \* time.Second' pkg/ari/server.go
   rg '20 \* time.Second' pkg/agentd/process.go
   ```

6. **Document R039 validation** — summarize what was proven: which CLIs completed full lifecycle, which assertions passed, any skip conditions hit.

## Must-Haves

- [ ] All existing integration tests pass (TestEndToEndPipeline, TestSession*, TestConcurrent*, TestAgentdRestartRecovery)
- [ ] All unit tests in pkg/... pass
- [ ] Real CLI tests pass or skip with clear messages
- [ ] R039 proof documented: real CLI agents exercised the converged contract

## Inputs

- `tests/integration/real_cli_test.go`
- `pkg/ari/server.go`
- `pkg/agentd/process.go`

## Expected Output

- `tests/integration/real_cli_test.go`

## Verification

go build -o bin/agentd ./cmd/agentd && go build -o bin/agent-shim ./cmd/agent-shim && go build -o bin/mockagent ./internal/testutil/mockagent && go test ./tests/integration -count=1 -v -timeout 180s && go test ./pkg/... -count=1 -timeout 120s
