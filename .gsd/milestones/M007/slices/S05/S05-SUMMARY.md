---
id: S05
parent: M007
milestone: M007
provides:
  - ["Full integration test suite for M007 workspace/agent API", "Three infrastructure fixes in process.go (socket path, bootstrap state, stale socket cleanup)", "golangci-lint clean posture across all packages including tests/integration/", "bin/workspace-mcp-server binary built and verified"]
requires:
  - slice: S01
    provides: bbolt store, Agent+Workspace models, spec.StatusIdle — required for integration test store
  - slice: S02
    provides: ProcessManager shim write authority, tryReload/alwaysNew recovery — required for restart test behavior
  - slice: S03
    provides: Full ARI JSON-RPC surface (workspace/* + agent/* handlers) — the API surface all integration tests call
  - slice: S04
    provides: bin/workspace-mcp-server build and workspace-mcp-server command — verified by T01
affects:
  []
key_files:
  - ["tests/integration/session_test.go", "tests/integration/e2e_test.go", "tests/integration/restart_test.go", "tests/integration/concurrent_test.go", "tests/integration/real_cli_test.go", "pkg/agentd/process.go", "pkg/rpc/server_test.go", "bin/workspace-mcp-server", "pkg/meta/store.go", "pkg/workspace/manager.go", "cmd/agentdctl/helpers.go"]
key_decisions:
  - ["D101: Pass filepath.Base(stateDir) as --id to shim — fixes socket path mismatch between workspace-name and workspace/name formats", "D102: Bootstrap agent state from runtime/status after Subscribe — fixes missed creating→idle notification when SetStateChangeHook is nil during Create()", "Accept stopped or error for post-recovery state in TestAgentdRestartRecovery — recovery marks dead shims as stopped per D012/D029", "waitForAgentStateOneOf helper added to handle instant mockagent completing turns in <1ms before first poll fires"]
patterns_established:
  - ["Integration test helpers: createTestWorkspace/deleteTestWorkspace/createAgentAndWait/stopAndDeleteAgent with polling via waitForAgentState(client, workspace, name, state, timeout)", "Socket path pattern for integration tests: /tmp/oar-<pid>-<counter>.sock via atomic counter (K025/K062 compliance)", "waitForAgentStateOneOf for post-prompt polling — accepts multiple valid terminal states when async operations complete faster than poll interval", "os.Remove(socketPath) before fork as defensive cleanup in process.go forkShim"]
observability_surfaces:
  - ["agentd structured logs (component=agentd.process, component=ari.server, component=agentd.agent, component=meta.store) consumed directly by integration tests via stdout/stderr capture; state transitions logged at INFO level with prev/new state fields; recovery pass logged with count/recovered/failed totals"]
drill_down_paths:
  []
duration: ""
verification_result: passed
completed_at: 2026-04-09T23:08:30.674Z
blocker_discovered: false
---

# S05: Integration Tests + Final Verification

**All five integration test files rewritten for the new workspace/agent ARI, three infrastructure bugs fixed in pkg/agentd/process.go, and milestone-wide verification confirmed: `go test ./tests/integration/... -v -timeout 120s` passes, `golangci-lint run ./...` returns 0 issues.**

## What Happened

S05 delivered the final assembly proof for M007. Two tasks brought the codebase to full green.

**T01** fixed the two immediate blockers before the integration rewrite: replaced `spec.StatusCreated` with `spec.StatusIdle` at lines 230 and 277 of `pkg/rpc/server_test.go` (StatusCreated was deleted in M007), and built `bin/workspace-mcp-server` from `./cmd/workspace-mcp-server`. Additionally, 18 pre-existing lint issues in `./pkg/...` and `./cmd/...` were cleaned up: removed 6 unused helper functions from `cmd/agentdctl/helpers.go`, removed 1 unused `createTestWorkspace` from `pkg/agentd/agent_test.go`, fixed 3 British spellings (initialise→initialize) in `pkg/meta/store.go` and `pkg/workspace/manager.go`, and auto-fixed gci import ordering across 7 files. After T01, `golangci-lint run ./pkg/... ./cmd/...` → 0 issues; only `tests/integration/` typecheck errors remained (T02 scope).

**T02** rewrote all five `tests/integration/` files from scratch to use the new M007 ARI surface. The old files used deleted types (`ari.RoomCreateResult`, `ari.WorkspacePrepareResult`, `ari.SessionNewResult`, agentId UUID params) and did not compile. The new files implement the full lifecycle: `workspace/create` → poll `workspace/status` until `phase=="ready"` → `agent/create {workspace, name}` → poll `agent/status` until `state=="idle"` → `agent/prompt` (async) → `agent/stop` → `agent/delete` → `workspace/delete`.

During the test rewrite, three pre-existing bugs in `pkg/agentd/process.go` were discovered and fixed:
1. **Socket path mismatch (D101):** `forkShim` was passing the slash-separated agentKey (`workspace/name`) as `--id` to the shim. The shim constructs its socket as `filepath.Join(flagStateDir, flagID, "agent-shim.sock")`, so the slash created a nested subdirectory that agentd's `waitForSocket` never found. Fix: pass `filepath.Base(stateDir)` (hyphenated `workspace-name`) as `--id`.
2. **Missed idle notification (D102):** The shim's `SetStateChangeHook` is registered after `Create()` returns in `main.go`, so the creating→idle stateChange fires before the hook is set. The notification was silently dropped. Fix: after `Subscribe()`, call `runtime/status` on the shim and bootstrap the DB to `idle` directly if status reports a non-creating state.
3. **Stale socket files:** Previous test runs left socket files that caused `bind: address already in use` on subsequent runs. Fix: `os.Remove(socketPath)` in `forkShim` before fork (idempotent, ignores ENOENT).

A `waitForAgentStateOneOf` helper was added to `session_test.go` to handle the instant mockagent case: mockagent completes turns in <1ms, faster than the 200ms poll interval, so by the time the test polls, state may already be `idle` — the helper accepts any of the provided states.

**Final verification results:**
- `go test ./tests/integration/... -v -timeout 120s` → 9 tests: 7 PASS, 2 SKIP (TestRealCLI_GsdPi and TestRealCLI_ClaudeCode skip without ANTHROPIC_API_KEY — correct behavior)
- `golangci-lint run ./...` → **0 issues** (10.1s)
- `go build ./...` → clean
- `test -f bin/workspace-mcp-server` → 7.2 MB binary present
- `rg 'meta.AgentState|meta.SessionState|go-sqlite3' --type go` → zero matches

All M007 success criteria are met.

## Verification

**`go test ./tests/integration/... -v -timeout 120s`** — all 9 tests: TestMultipleConcurrentAgents PASS (1.12s), TestEndToEndPipeline PASS (0.66s), TestAgentdRestartRecovery PASS (3.54s), TestAgentLifecycle PASS (0.66s), TestAgentPromptAndStop PASS (0.64s), TestAgentPromptFromIdle PASS (0.65s), TestMultipleAgentPromptsSequential PASS (0.66s), TestRealCLI_GsdPi SKIP (no ANTHROPIC_API_KEY), TestRealCLI_ClaudeCode SKIP (no ANTHROPIC_API_KEY). Total: 9.071s.

**`golangci-lint run ./...`** → 0 issues in 10.1s (all packages including tests/integration/).

**`go build ./...`** → no output, exit 0.

**`test -f bin/workspace-mcp-server`** → 7.2 MB binary present.

**`rg 'meta.AgentState|meta.SessionState|go-sqlite3' --type go`** → zero matches (M007 deletions confirmed).

## Requirements Advanced

None.

## Requirements Validated

- R008 — Full pipeline agentd → agent-shim → mockagent exercised end-to-end: TestEndToEndPipeline proves workspace/create→agent/create→agent/prompt→agent/stop→agent/delete→workspace/delete with real binaries; TestAgentdRestartRecovery proves recovery behavior; TestMultipleConcurrentAgents proves concurrent agent handling.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

Three bugs in pkg/agentd/process.go were fixed as part of T02 (beyond the stated test-rewrite scope) because all three were blocking the slice verification goal. Restart test accepts stopped OR error (not just error) per D012/D029 recovery semantics. Added waitForAgentStateOneOf helper to handle instant mockagent timing edge case. T01 also cleaned 18 pre-existing lint issues in ./pkg/... and ./cmd/... beyond the two stated fixes, required to meet the T01 golangci-lint verification bar.

## Known Limitations

TestRealCLI_GsdPi and TestRealCLI_ClaudeCode skip when ANTHROPIC_API_KEY is not set — these test the full pipeline with a real LLM and are intentionally skipped in CI environments without credentials. The session/load shim-side handler (tryReload path) is not exercised by integration tests — it was deferred to a future milestone per D091; mockagent's session/load handler is a no-op. Recovery behavior documents dead shims as stopped (not error) — see D012/D029.

## Follow-ups

S05 completes M007. Future work: (1) add session/load handler to real agent-shim binary to fully exercise tryReload; (2) add integration test for workspace/send message delivery; (3) consider a CI toggle to run TestRealCLI_* with a mock LLM for lightweight functional verification.

## Files Created/Modified

- `tests/integration/session_test.go` — Full rewrite: createTestWorkspace/deleteTestWorkspace, waitForAgentState(workspace,name), createAgentAndWait(workspace,name,runtimeClass), stopAndDeleteAgent(workspace,name), setupAgentdTest with /tmp/oar-<pid>-<counter>.sock socket paths, waitForAgentStateOneOf helper
- `tests/integration/e2e_test.go` — Full rewrite: TestEndToEndPipeline — workspace/create→poll ready→agent/create→poll idle→agent/prompt→poll running/idle→agent/stop→poll stopped→agent/delete→workspace/delete
- `tests/integration/restart_test.go` — Full rewrite: TestAgentdRestartRecovery — 7-phase test using (workspace,name) identity, accepts stopped/error post-recovery state, verifies both agent identity preservation and agent/list
- `tests/integration/concurrent_test.go` — Full rewrite: TestMultipleConcurrentAgents — 3 agents with concurrent prompts tracked by name (not agentId), sync.Mutex for concurrent ARI client calls
- `tests/integration/real_cli_test.go` — Full rewrite: runRealCLILifecycle uses workspace/create+status, agent/create+status, agent/prompt+status, agent/stop, agent/delete, workspace/delete; TestRealCLI_GsdPi and TestRealCLI_ClaudeCode skip without ANTHROPIC_API_KEY
- `pkg/agentd/process.go` — Three bug fixes: (1) filepath.Base(stateDir) as --id to forkShim; (2) bootstrap DB state from shim runtime/status after Subscribe; (3) os.Remove(socketPath) before fork to clear stale sockets
- `pkg/rpc/server_test.go` — Replaced spec.StatusCreated with spec.StatusIdle at lines 230 and 277
- `bin/workspace-mcp-server` — Built 7.2 MB binary from ./cmd/workspace-mcp-server
- `pkg/meta/store.go` — Fixed British spelling: initialise→initialize
- `pkg/workspace/manager.go` — Fixed British spelling: initialise→initialize
- `cmd/agentdctl/helpers.go` — Removed 6 unused helper functions (parseLabels, splitComma, splitKeyValue, splitBy, trimSpace, isWhitespace)
