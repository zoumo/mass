# S04 Research: Real CLI Integration Verification

**Depth:** Targeted — known technology (Go integration tests, ARI client, existing pipeline), moderate complexity from external CLI dependencies and API key requirements.

## Summary

S04 must prove that the converged runtime contract (S01 design, S02 clean-break protocol, S03 recovery persistence) works with real ACP CLI agents — `gsd-pi` (bunx pi-acp) and `claude-code` (node claude-agent-acp). The existing integration test infrastructure (tests/integration/) already proves the full agentd→agent-shim→mockagent pipeline including session lifecycle, workspace management, concurrent sessions, and restart recovery. S04 replaces mockagent with the real CLIs and verifies the same contract holds.

## Active Requirements This Slice Owns or Supports

| ID | Description | Role |
|----|-------------|------|
| R039 | Converged contract must be exercised with real `gsd-pi` and `claude-code` bundle surfaces | **Primary owner** — this is the core proof |
| R037 | Workspace identity, reuse, cleanup must be explicit | Supporting — real CLIs exercise workspace lifecycle |
| R044 | Follow-on hardening remains planned | Supporting — S04 may surface items for this backlog |

## Recommendation

Write a new integration test file (`tests/integration/real_cli_test.go`) that:

1. Defines runtime classes for `gsd-pi` and `claude-code` in the test config YAML
2. Starts agentd with those runtime classes
3. Runs the full session lifecycle (workspace prepare → session/new → session/prompt → session/status → session/stop → session/remove → workspace/cleanup) for each CLI
4. Skips gracefully when the external CLIs or API keys aren't available
5. Uses extended timeouts (120s+ for prompt) since real LLMs are slow
6. Verifies event delivery (session/update notifications arrive with valid envelopes)

## Implementation Landscape

### Existing Infrastructure (Reusable)

The test helpers in `tests/integration/session_test.go` provide the full scaffolding:
- `setupAgentdTest(t)` — starts agentd daemon with config, returns client + cleanup
- `prepareTestWorkspace(t, ctx, client)` — creates emptyDir workspace via ARI
- `createTestSession(t, client, workspaceId)` — creates session via `session/new`
- `waitForSocket(socketPath, timeout)` — polls for Unix socket readiness
- `waitForSessionState(t, client, sessionId, wantState, timeout)` — polls session/status

These need to be **parameterized** for the real CLI case — specifically the config YAML must define `gsd-pi` and `claude-code` runtime classes instead of `mockagent`.

### Runtime Class Configs for Real CLIs

**gsd-pi** runtime class:
```yaml
runtimeClasses:
  gsd-pi:
    command: bunx
    args: ["pi-acp"]
    env:
      PI_ACP_PI_COMMAND: gsd
      PI_CODING_AGENT_DIR: /Users/jim/.gsd/agent
      PATH: "${PATH}"
      HOME: "${HOME}"
```

**claude-code** runtime class:
```yaml
runtimeClasses:
  claude-code:
    command: node
    args: ["/Users/jim/.bun/install/cache/@GH@agentclientprotocol-claude-agent-acp-7506223@@@1/dist/index.js"]
    env:
      ANTHROPIC_API_KEY: "${ANTHROPIC_API_KEY}"
      PATH: "${PATH}"
      HOME: "${HOME}"
```

Note: `env` in `RuntimeClassConfig` uses `map[string]string` with `${VAR}` substitution via `os.Expand(value, os.Getenv)` in `NewRuntimeClassRegistry`. The runtime `mergeEnv` in `runtime.go` merges `os.Environ()` as the base, so PATH/HOME are inherited regardless. However, the ProcessManager generates the config with `spec.AcpProcess.Env` as a `[]string` of `KEY=VALUE` pairs from only the RuntimeClass env map — it does NOT inherit parent env. The agent-shim's runtime then calls `mergeEnv(os.Environ(), proc.Env)` which adds parent env back. So the `env` map only needs to contain explicit overrides. PATH/HOME are inherited automatically.

### Key Wiring Path

```
ARI client
  → session/new(workspaceId, runtimeClass="gsd-pi")
  → session/prompt(sessionId, text="say hello")
    → ProcessManager.Start(sessionID)
      → registry.Get("gsd-pi") → RuntimeClass{Command:"bunx", Args:["pi-acp"], Env:{PI_ACP_PI_COMMAND:gsd, ...}}
      → generateConfig(session, runtimeClass) → spec.Config (without systemPrompt)
      → createBundle(session, cfg) → bundlePath + workspace symlink + config.json
      → forkShim(ctx, session, rc, bundlePath, stateDir) → exec agent-shim --bundle <bundlePath>
      → agent-shim reads config.json → runtime.Create() → exec bunx pi-acp
        → ACP handshake: Initialize + NewSession
      → waitForSocket → DialWithHandler → Subscribe
      → client.Prompt("say hello") → shim forwards to ACP agent → LLM response
```

### Timeout Sensitivity

| Operation | Current Timeout | Risk with Real CLIs |
|-----------|----------------|---------------------|
| ProcessManager.Start auto-start | 10s (ARI server) | **High** — `bunx pi-acp` needs npm resolution + process start + ACP handshake. Could easily exceed 10s on first run. |
| waitForSocket | 5s (test mode) / 10s (production) | **Medium** — real CLI startup is slower than mockagent |
| session/prompt | 30s (ARI server) | **High** — real LLM API calls (Anthropic, gsd-pi→Anthropic/OpenAI) may take 10-60s |
| Test-level timeout | 60s (existing tests) | **Must increase** — entire real CLI lifecycle needs 120-180s |

The ARI server's hardcoded 30s prompt timeout is a real risk. For S04 tests, the test should use the ARI client directly (bypassing the ARI server timeout) or the test config should be prepared so the prompt is trivially short ("respond with just the word 'hello'").

Actually — re-checking: the ARI server is part of the pipeline. The 30s timeout in `handleSessionPrompt` caps the entire prompt round-trip. For real CLIs this is likely insufficient. However, for a minimal smoke test ("respond with just one word"), most LLM APIs respond in 2-5s, so 30s should be enough. The 10s auto-start timeout is the bigger risk.

### Skip Conditions

Tests should skip when:
1. `testing.Short()` — standard integration test skip
2. `bunx` not found in PATH (gsd-pi unavailable)
3. `ANTHROPIC_API_KEY` not set (claude-code cannot authenticate)
4. `gsd` not found in PATH (gsd-pi needs the gsd binary)
5. claude-code adapter JS file doesn't exist

### SystemPrompt Gap

`ProcessManager.generateConfig()` does not include `systemPrompt` in the generated `spec.Config`. The real bundle configs have it, but agentd doesn't pass it through. This is by design (D016/D018: session/new is configuration-only), and both CLIs work without a system prompt — they just won't have role instructions. For S04 proof purposes, this is acceptable. If systemPrompt wiring through agentd is needed, it's a follow-up enhancement.

### What S04 Must Actually Prove (R039)

Per the requirement and milestone context:
1. `gsd-pi` can be launched through agentd → agent-shim → bunx pi-acp, complete ACP handshake, respond to a prompt, and transition through session states correctly
2. `claude-code` can be launched through agentd → agent-shim → node claude-agent-acp, same lifecycle
3. Session status, event delivery, and cleanup work the same as with mockagent
4. The clean-break protocol surface (session/*, runtime/*) works end-to-end with real agents

### Natural Task Decomposition

1. **T01 — Build real CLI integration test for gsd-pi**: Write `TestRealCLI_GsdPi` in `tests/integration/real_cli_test.go`. Configure the runtime class, run full lifecycle, verify prompt response and session states. Skip when prerequisites missing.

2. **T02 — Build real CLI integration test for claude-code**: Write `TestRealCLI_ClaudeCode` in same file. Same lifecycle. Skip when ANTHROPIC_API_KEY missing or adapter JS not found.

3. **T03 — Verify event continuity with real CLIs**: After prompt, verify that session/status returns shimState with valid PID, and that events were delivered (event log has entries). This can be part of T01/T02 or a focused follow-up assertion.

4. **T04 — Slice verification gate**: Run all integration tests (existing + new), verify no regressions, document any follow-ups discovered.

### Risks

1. **Startup timeout (10s auto-start)**: Real CLIs may exceed the 10s auto-start timeout in ARI's handleSessionPrompt. Mitigation: use a warm start (pre-resolve npm packages) or increase timeout. Alternatively, call `session/prompt` with a longer client-side timeout and accept the 10s start timeout since the processManager.Start itself doesn't have a timeout — only the ARI handler does. The test can call `processManager.Start` directly... but no, the test uses the ARI client. The 10s is the ARI handler's timeout for Start, which calls processManager.Start. The actual Start may take longer. We may need to increase this in the ARI server or work around it.

   Actually, looking more carefully: the ARI handler uses `context.WithTimeout(ctx, 10*time.Second)` for Start. The processManager.Start passes this context to forkShim (which uses exec.Command, not CommandContext — so the 10s doesn't kill the shim). But waitForSocket has its own 5s/10s timeout. The 10s context timeout may cancel before waitForSocket completes. This is a real risk.

   Practical mitigation: pre-run `bunx pi-acp --help` or similar to warm up the npm cache before the test. Or increase the ARI handler timeout to 30s for start operations.

2. **API costs**: Each test prompt incurs a real API call. Keep prompts minimal ("respond with only the word hello").

3. **Flakiness**: Network issues, API rate limits, npm registry issues could cause test failures. These tests should be tagged as "external" or only run explicitly.

### Files to Create/Modify

| File | Action | Purpose |
|------|--------|---------|
| `tests/integration/real_cli_test.go` | Create | Real CLI integration tests |
| `pkg/ari/server.go` | Possibly modify | Increase auto-start timeout if needed |

### Verification Commands

```bash
# Build binaries
go build -o bin/agentd ./cmd/agentd && go build -o bin/agent-shim ./cmd/agent-shim

# Run real CLI tests (requires external CLIs + API keys)
go test ./tests/integration -run TestRealCLI -count=1 -v -timeout 180s

# Run all existing integration tests (regression check)
go test ./tests/integration -count=1 -v -timeout 180s

# Verify no regressions in unit tests
go test ./pkg/... -count=1 -timeout 120s
```

## Skills Discovered

No new skills needed — this is Go integration testing using established patterns already in the codebase.

## Sources

- `tests/integration/e2e_test.go` — existing full pipeline integration test pattern
- `tests/integration/session_test.go` — helper functions (setupAgentdTest, etc.)
- `tests/integration/restart_test.go` — restart recovery test pattern
- `pkg/agentd/process.go` — ProcessManager.Start() and generateConfig()
- `pkg/agentd/runtimeclass.go` — RuntimeClassRegistry
- `pkg/runtime/runtime.go` — runtime.Manager.Create() ACP handshake
- `bin/bundles/gsd-pi/config.json` — real gsd-pi bundle config
- `bin/bundles/claude-code/config.json` — real claude-code bundle config
- `bin/bundles/README.md` — bundle setup instructions
- `internal/testutil/mockagent/main.go` — mockagent ACP implementation reference
