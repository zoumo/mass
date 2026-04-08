# S04: Real CLI integration verification

**Goal:** Prove the converged runtime contract works end-to-end with real ACP CLI agents (gsd-pi and claude-code), not just mockagent, completing R039.
**Demo:** After this: TBD

## Tasks
- [x] **T01: Increased prompt timeout to 120s and created real CLI integration tests for gsd-pi and claude-code with graceful skip conditions** — ## Description

The ARI server's `handleSessionPrompt` auto-start uses a 10s context timeout, and `ProcessManager.waitForSocket` uses 5s in test mode (detected via `m.config.Socket != ""`). Real CLIs like gsd-pi (bunx pi-acp) need npm resolution + ACP handshake which can take 10-20s. This task increases those timeouts to safe values and writes the real CLI integration test file proving R039.

## Failure Modes

| Dependency | On error | On timeout | On malformed response |
|------------|----------|-----------|----------------------|
| gsd-pi (bunx pi-acp) | t.Skip with message | t.Skip — startup took too long | t.Fatal — unexpected response |
| claude-code (node adapter) | t.Skip with message | t.Skip — startup took too long | t.Fatal — unexpected response |
| ANTHROPIC_API_KEY | t.Skip when not set | N/A | N/A |
| LLM API (Anthropic/OpenAI) | t.Fatal with error context | Prompt timeout → t.Fatal | t.Fatal — stop reason check fails |

## Negative Tests

- **Missing prerequisites**: Each test skips gracefully when binary not found or API key not set, rather than failing
- **Timeout boundary**: The 30s start timeout and 30s prompt timeout are validated by real CLI startup time

## Steps

1. **Increase ARI server start timeout** — In `pkg/ari/server.go`, change the `handleSessionPrompt` auto-start context timeout from `10*time.Second` to `30*time.Second` (line ~478). Real CLI runtimes need npm resolution + ACP handshake which legitimately takes 10-20s.

2. **Increase ProcessManager.waitForSocket timeout** — In `pkg/agentd/process.go`, change the `waitForSocket` timeout to 20s for both test mode (currently 5s) and normal mode (currently 10s). The 5s test-mode timeout is based on mockagent's fast startup; real CLIs need more time. Remove the test-mode vs normal-mode distinction since 20s is a sensible default for all cases.

3. **Create `tests/integration/real_cli_test.go`** with:
   - Helper `setupAgentdTestWithRuntimeClass(t, runtimeClassName, runtimeClassConfig)` that creates a config YAML with the specified runtime class instead of hardcoded mockagent. Reuse the pattern from `setupAgentdTest` in session_test.go. Must use `/tmp/oar-*.sock` pattern for macOS socket path limits (K025). Must set `OAR_SHIM_BINARY` env var.
   - `TestRealCLI_GsdPi` — Skip conditions: `testing.Short()`, `bunx` not in PATH, `gsd` not in PATH. Runtime class config: `command: bunx`, `args: ["pi-acp"]`, `env: {PI_ACP_PI_COMMAND: gsd, PI_CODING_AGENT_DIR: /Users/jim/.gsd/agent}`. Full lifecycle: workspace/prepare → session/new(runtimeClass="gsd-pi") → session/prompt(text="respond with only the word hello") → assert stopReason is "end_turn" → session/status → assert state is "running" and shimState is non-nil → session/stop → session/remove → workspace/cleanup.
   - `TestRealCLI_ClaudeCode` — Skip conditions: `testing.Short()`, `ANTHROPIC_API_KEY` not set, claude-code adapter JS file doesn't exist at `/Users/jim/.bun/install/cache/@GH@agentclientprotocol-claude-agent-acp-7506223@@@1/dist/index.js`. Runtime class config: `command: node`, `args: ["/Users/jim/.bun/install/cache/@GH@agentclientprotocol-claude-agent-acp-7506223@@@1/dist/index.js"]`, `env: {ANTHROPIC_API_KEY: <from env>}`. Same lifecycle as gsd-pi.
   - Both tests use 180s test timeout and log each step for debugging.
   - Cleanup must kill leftover agent processes (bunx, pi-acp, node, claude-agent) on test teardown.

4. **Build binaries and run the real CLI tests:**
   ```
   go build -o bin/agentd ./cmd/agentd
   go build -o bin/agent-shim ./cmd/agent-shim
   go test ./tests/integration -run TestRealCLI -count=1 -v -timeout 180s
   ```

## Must-Haves

- [ ] ARI server start timeout increased from 10s to 30s
- [ ] ProcessManager.waitForSocket timeout increased to 20s
- [ ] TestRealCLI_GsdPi exercises full session lifecycle with real gsd-pi CLI
- [ ] TestRealCLI_ClaudeCode exercises full session lifecycle with real claude-code CLI
- [ ] Both tests skip gracefully when prerequisites are missing
- [ ] Both tests verify prompt response (stopReason=end_turn) and session state (running + shimState non-nil)
  - Estimate: 45m
  - Files: pkg/ari/server.go, pkg/agentd/process.go, tests/integration/real_cli_test.go
  - Verify: go build -o bin/agentd ./cmd/agentd && go build -o bin/agent-shim ./cmd/agent-shim && go test ./tests/integration -run TestRealCLI -count=1 -v -timeout 180s
- [x] **T02: All integration tests, unit tests, and real CLI tests pass — no regressions from timeout changes, R039 validated** — ## Description

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
  - Estimate: 20m
  - Files: tests/integration/real_cli_test.go, pkg/ari/server.go, pkg/agentd/process.go
  - Verify: go build -o bin/agentd ./cmd/agentd && go build -o bin/agent-shim ./cmd/agent-shim && go build -o bin/mockagent ./internal/testutil/mockagent && go test ./tests/integration -count=1 -v -timeout 180s && go test ./pkg/... -count=1 -timeout 120s
