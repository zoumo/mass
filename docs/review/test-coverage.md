# Test Coverage Review Report

Date: 2026-04-30

## Summary

| Metric | Value |
|--------|-------|
| Total statement coverage (excluding third_party) | ~49.9% |
| Test functions (excluding third_party) | 641 |
| Test files (excluding third_party) | 78 |
| Benchmark functions | 0 |
| Fuzz functions | 0 |
| Files using t.Parallel | 6 |
| Error path assertions | ~403 |
| Table-driven test files | ~20 |
| Pass/Fail | 1 FAIL (pkg/agentd — panic: send on closed channel), rest PASS |

**Known test failure**: `pkg/agentd` panics with `send on closed channel` in
`github.com/sourcegraph/jsonrpc2.(*Conn).readMessages` during recovery tests.
This is a race condition in the mock server teardown — it closes the listener
while jsonrpc2 goroutines still hold references to the connection channel.

## Coverage by Package

### Core packages (owned code)

| Package | Coverage | Assessment |
|---------|----------|------------|
| pkg/workspace | **93.6%** | Excellent |
| pkg/agentrun/api | **87.8%** | Very good |
| pkg/runtime-spec/api | **87.9%** | Very good |
| pkg/agentd/store | **78.8%** | Good |
| pkg/runtime-spec | **73.9%** | Good |
| pkg/agentrun/runtime/acp | **72.4%** | Good |
| pkg/agentrun/client | **70.6%** | Good |
| pkg/agentrun/server | **68.7%** | Acceptable |
| pkg/agentd | **68.3%** | Acceptable (but has test crash) |
| pkg/ari/server | **67.2%** | Acceptable |
| pkg/ari/api | **65.4%** | Acceptable |
| pkg/ari/client | **64.4%** | Acceptable |
| pkg/jsonrpc | **56.0%** | Below target |
| pkg/jsonrpc/ndjson | **100.0%** | Perfect |
| pkg/tui/component | **37.8%** | Low |
| pkg/watch | **32.5%** | Low |
| pkg/tui/chat | **28.4%** | Low |

### CLI packages

| Package | Coverage | Assessment |
|---------|----------|------------|
| cmd/massctl/commands/ext/pipeline | **87.7%** | Very good |
| cmd/massctl/commands/workspace | **87.3%** | Very good |
| cmd/massctl/commands/agent | **81.9%** | Good |
| cmd/massctl/commands/workspace/create | **59.3%** | Acceptable |
| cmd/massctl/commands/agentrun | **47.0%** | Below target |
| cmd/massctl/commands/cliutil | **38.8%** | Low |
| cmd/massctl/commands/compose | **15.8%** | Very low |
| cmd/mass/commands/run | **13.0%** | Very low |

### Completely untested packages (0% — no test files)

| Package | Description |
|---------|-------------|
| cmd/mass/commands/daemon | Daemon start/restart/status commands |
| cmd/mass/commands/version | Version display command |
| cmd/mass/commands/workspacemcp | Workspace MCP server command |
| cmd/massctl/commands/version | CLI version command |
| cmd/massctl/commands/ext (root) | Extension command root |
| internal/version | Build version info |
| internal/logging | Logging configuration |

## Untested Code

### Critical functions with 0% coverage (excluding CLI boilerplate)

**cmd/mass/commands/run/session_update.go** (0% — all 10 functions):
- `buildSessionUpdate`, `convertToStateCommands`, `convertToStateCommandInput`,
  `convertToStateConfigOptions`, `convertToStateConfigOptionSelect`,
  `convertToStateConfigSelectOptions`, `convertToStateConfigSelectOptionSlice`,
  `convertToStateSessionInfo`, `convertToStateCurrentMode`,
  `sortCommandsByName`, `sortConfigOptionsByID`
- These are pure data transformation functions that should be easy to test.

**cmd/mass/commands/workspacemcp/command.go** (0% — all 4 functions):
- `agentRunSendHandler`, `agentRunStatusHandler`, `NewCommand`, `run`

**cmd/massctl/commands/cliutil/workspace.go** (0% — all 4 functions):
- `CreateWorkspace`, `WaitWorkspaceReady`, `EnsureWorkspace`, `CreateAgentRun`
- Used by multiple CLI commands; testing would improve confidence in CLI workflows.

**cmd/massctl/commands/cliutil/source.go** (0% — `BuildSource`):
- Source building for workspace creation.

**cmd/massctl/commands/compose/** (effectively 0% on runtime code):
- `newApplyCmd`, `NewCommand`, `createAgentRun`, `printSocketInfo`, `newRunCmd` all at 0%.
- Only `parseConfig` and `validateConfig` are tested (via config_test.go).

**pkg/agentd/agent.go — `UpdatePhase`** (42.9%):
- Important state machine function with incomplete coverage.

**pkg/watch/retry.go** (covered only through server_test.go indirectly, low coverage):
- RetryWatcher is critical for event delivery reliability.

### Low-coverage functions in core packages

| Function | Coverage | Risk |
|----------|----------|------|
| `pkg/agentd/agent.go:UpdatePhase` | 42.9% | State transitions may have untested edge cases |
| `pkg/jsonrpc/client.go` (multiple) | ~56% overall | RPC error handling, reconnection |
| `pkg/watch/server.go` | 32.5% | Event broadcast to watchers |
| `pkg/tui/chat/chat.go` | ~28% | Core chat model logic |
| `pkg/tui/component/chat.go` | ~38% | Chat UI rendering |

## Test Quality Issues

### 1. Panic in test suite (HIGH)

`pkg/agentd` test suite crashes with `panic: send on closed channel` in
`jsonrpc2.(*Conn).readMessages`. This occurs in `TestRecoverSessions_MixedLiveAndDead`
where the mock server teardown races with active jsonrpc2 connections. The panic
causes the entire package test to report FAIL, masking whether individual tests pass.

**Fix**: Add synchronization in `mockRunServer.close()` to ensure all connections
are fully drained before closing the listener, or use `t.Cleanup` ordering to
disconnect clients before stopping the server.

### 2. Duplicated mock implementations (MEDIUM)

Three separate `mockClient` implementations exist across CLI test packages:
- `cmd/massctl/commands/agent/mock_test.go`
- `cmd/massctl/commands/agentrun/mock_test.go`
- `cmd/massctl/commands/workspace/mock_test.go`

These are nearly identical (all implement `ariclient.Client` with function hooks)
but are duplicated in each package. They should be consolidated into a shared
`internal/testutil` or `cmd/massctl/commands/testutil` package.

### 3. Timing-dependent tests (MEDIUM)

19 test files contain `time.Sleep`, `time.After`, or `Eventually` patterns.
While most use `require.Eventually` (good), several use raw `time.Sleep` for
synchronization (e.g., `run_boundary_test.go:302` sleeps 100ms, `process_test.go`
polls with deadlines). These can be flaky under CI load.

Notable examples:
- `pkg/agentd/run_boundary_test.go:302`: `time.Sleep(100 * time.Millisecond)` to wait for notifications
- `pkg/agentd/run_boundary_test.go:368`: `time.Sleep(150 * time.Millisecond)` for malformed notification delivery
- `pkg/agentd/process_test.go:136`: Polling loop with `time.Sleep(100ms)` for state transitions

### 4. Integration tests require pre-built binaries (LOW)

Integration tests (`tests/integration/mass/`) and process tests (`pkg/agentd/process_test.go`)
require `bin/mass` and `bin/mockagent` to be pre-built. The process tests build
them on-the-fly (good), but integration tests fail with "binary not found" if
`make build` hasn't been run. This creates a hidden dependency.

### 5. Limited use of t.Parallel (LOW)

Only 6 of 78 test files use `t.Parallel()`. The `pkg/agentd/agent_test.go` and
`pkg/agentd/options_test.go` correctly use parallel execution, but most other
packages do not. Adding `t.Parallel()` to pure-logic tests would improve test
suite execution time.

## Missing Test Scenarios

### No fuzz testing

Zero fuzz tests exist. Key candidates for fuzzing:
- **JSON unmarshalling**: `pkg/agentrun/api/event.go` — `AgentRunEvent.UnmarshalJSON` handles
  discriminated unions; malformed JSON could cause panics.
- **NDJSON reader**: `pkg/jsonrpc/ndjson/reader.go` — already has edge case tests, but
  fuzz testing would catch unexpected inputs.
- **YAML parsing**: `cmd/massctl/commands/compose/config.go` — parses user-provided YAML.
- **Pipeline validation**: `cmd/massctl/commands/ext/pipeline/validate.go` — validates
  user-provided pipeline YAML.

### No benchmark testing

Zero benchmarks exist. Key candidates:
- **Event translation**: `pkg/agentrun/server/translator.go` — hot path for streaming events.
- **NDJSON decoding**: `pkg/jsonrpc/ndjson/reader.go` — hot path for event stream.
- **Watch server fanout**: `pkg/watch/server.go` — broadcasts to multiple watchers.
- **bbolt store operations**: `pkg/agentd/store/` — disk I/O bound operations.

### No explicit race condition testing

While 9 test files mention "race" in comments, no tests are specifically designed
to detect races (e.g., running concurrent operations and checking invariants).
The `-race` flag is not referenced in any Makefile target.

Key race-prone areas:
- `pkg/agentd/process.go` — `processes` map accessed by multiple goroutines
- `pkg/agentrun/server/translator.go` — subscriber management with Start/Stop
- `pkg/watch/server.go` — concurrent Publish/Accept/disconnect

### Untested error scenarios

| Scenario | Package | Why it matters |
|----------|---------|----------------|
| Store corruption / bbolt read errors | pkg/agentd/store | Data integrity |
| Socket path too long (> 104 chars) | pkg/agentd/process | macOS-specific failure |
| Agent-run binary not found | pkg/agentd/process | Startup failure |
| Concurrent agent creates for same name | pkg/agentd/agent | Race condition |
| Client disconnect during prompt | pkg/agentrun/client | Graceful degradation |
| Malformed RPC response from agent-run | pkg/agentrun/client | Error resilience |
| Workspace hook timeout | pkg/workspace/hook | Hanging hooks |
| Git clone failure (network error) | pkg/workspace/git | Network resilience |
| MCP server connection failure | cmd/mass/commands/workspacemcp | Startup failure |

### Under-tested areas

1. **TUI chat model** (28.4%): The core interactive chat model (`pkg/tui/chat/chat.go`)
   is barely tested. The `waitNotif` chain, `handleNotif`, and keyboard routing
   (documented extensively in CLAUDE.md) have no unit tests. Only slash commands,
   completions, history, and streaming helpers are tested.

2. **Watch package** (32.5%): `RetryWatcher` reconnection logic, backoff behavior,
   and cursor-based replay are critical for reliability but under-tested.

3. **Compose commands** (15.8%): Only config parsing is tested; the actual
   `apply` and `run` commands (which orchestrate workspace+agent creation) have no tests.

4. **Session update conversion** (0%): The `session_update.go` file contains 10
   pure functions that convert between API types — ideal for table-driven tests
   but completely untested.

## Recommendations

### Priority 1 — Fix test crash and critical gaps

1. **Fix `pkg/agentd` test panic**: The `send on closed channel` crash in
   `mockRunServer` must be fixed to get reliable CI results. The root cause is
   a race between `srv.close()` and active jsonrpc2 connections.

2. **Test `session_update.go` conversion functions**: These 10 pure functions
   are the lowest-hanging fruit — no mocking needed, pure input/output.
   Estimated effort: 1-2 hours, high coverage gain.

3. **Test `cliutil/workspace.go` helpers**: `CreateWorkspace`, `WaitWorkspaceReady`,
   `EnsureWorkspace`, and `CreateAgentRun` are used by many CLI commands. Test with
   the existing mock client pattern.

### Priority 2 — Improve core package coverage

4. **Increase `pkg/jsonrpc` coverage to 70%+**: Focus on error paths in
   `client.go` (reconnection, timeout, malformed response).

5. **Increase `pkg/watch` coverage to 60%+**: Test `RetryWatcher` reconnection,
   cursor replay, and backoff behavior. The existing `recordConn` helper is well
   designed — extend it.

6. **Test `pkg/agentd/agent.go:UpdatePhase`** edge cases: The 42.9% coverage
   suggests the "phase mismatch" and "not found" paths need more tests.

### Priority 3 — Add missing test categories

7. **Add fuzz tests** for JSON unmarshalling in `pkg/agentrun/api/event.go` and
   `pkg/jsonrpc/ndjson/reader.go`. These parse untrusted input and are high-value
   fuzz targets.

8. **Add benchmarks** for `pkg/agentrun/server/translator.go` and
   `pkg/jsonrpc/ndjson/reader.go` to establish performance baselines.

9. **Add `-race` to CI**: Run `go test -race ./...` in CI to catch data races
   in the concurrent code paths (process map, translator, watch server).

### Priority 4 — Reduce duplication and improve patterns

10. **Consolidate mock clients**: Move the three duplicated `mockClient`
    implementations into a shared `cmd/massctl/commands/testutil/` package.

11. **Add `t.Parallel()` to pure-logic tests**: Especially in `pkg/agentrun/api`,
    `pkg/runtime-spec`, `pkg/agentd/store`, and CLI command tests.

12. **Replace `time.Sleep` with channel-based synchronization** in
    `pkg/agentd/run_boundary_test.go` where raw sleeps are used for
    notification delivery timing.

### Priority 5 — TUI and compose testing

13. **Add TUI chat model tests**: At minimum, test `handleNotif` dispatch and
    the `waitNotif` chain behavior documented in CLAUDE.md. The chat package
    has extensive design documentation but only 28.4% test coverage.

14. **Test compose `apply` and `run` commands**: These orchestrate the full
    workspace+agent creation workflow and are completely untested.
