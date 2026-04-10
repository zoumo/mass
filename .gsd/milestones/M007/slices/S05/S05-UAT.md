# S05: Integration Tests + Final Verification — UAT

**Milestone:** M007
**Written:** 2026-04-09T23:08:30.674Z

## UAT: S05 Integration Tests + Final Verification

### Prerequisites
- Go toolchain installed (`go version` ≥ 1.21)
- `bin/agentd`, `bin/agent-shim`, `bin/mockagent` built (`go build -o bin/agentd ./cmd/agentd && go build -o bin/agent-shim ./cmd/agent-shim`)
- `bin/workspace-mcp-server` present (`test -f bin/workspace-mcp-server`)
- `golangci-lint` v2 installed
- No `ANTHROPIC_API_KEY` required for the core test suite

---

### TC-01: Full Build Passes

**Steps:**
1. `go build ./...`

**Expected:** Exit 0, no output.

---

### TC-02: golangci-lint Returns 0 Issues

**Steps:**
1. `golangci-lint run ./...`

**Expected:** Output `0 issues.`, exit 0. Must include `tests/integration/` packages.

---

### TC-03: workspace-mcp-server Binary Present

**Steps:**
1. `test -f bin/workspace-mcp-server && ls -lh bin/workspace-mcp-server`

**Expected:** Exit 0, file ≥7 MB, executable bit set.

---

### TC-04: No Deleted M007 Types in Codebase

**Steps:**
1. `rg 'meta\.AgentState|meta\.SessionState|go-sqlite3' --type go`

**Expected:** Zero matches (exit 1 from rg — no lines found).

---

### TC-05: End-to-End Pipeline Test

**Steps:**
1. `go test ./tests/integration/... -v -timeout 120s -run TestEndToEndPipeline`

**Expected:**
- `workspace/create` returns `phase=pending`; polling `workspace/status` yields `phase=ready`
- `agent/create` returns `state=creating`; polling `agent/status` yields `state=idle`
- `agent/prompt` returns `accepted=true` (async dispatch)
- `agent/status` returns `state=idle` or `state=running` after prompt
- `agent/stop` triggers transition to `state=stopped`
- `agent/delete` succeeds (no error)
- `workspace/delete` succeeds (no error)
- Test passes: `--- PASS: TestEndToEndPipeline`

---

### TC-06: Agent Lifecycle State Machine

**Steps:**
1. `go test ./tests/integration/... -v -timeout 120s -run TestAgentLifecycle`

**Expected:**
- `idle → (prompt) → running/idle → (stop) → stopped → (delete) → not found`
- After delete: `agent/status` returns JSON-RPC error -32602 ("agent not found")
- Test passes: `--- PASS: TestAgentLifecycle`

---

### TC-07: Restart Recovery — Dead Shim Fail-Closed

**Steps:**
1. `go test ./tests/integration/... -v -timeout 120s -run TestAgentdRestartRecovery`

**Expected:**
- Phase 1: 2 agents created (agent-a, agent-b) in workspace test-ws; both reach `state=idle`
- Phase 2: agentd stopped; all shim processes killed
- Phase 3: agentd restarted; recovery log shows `recovered=0 failed=2 total=2`
- Phase 4: `agent/status {workspace:test-ws, name:agent-a}` returns `state=stopped` (or `state=error`)
- Phase 5: `agent/status {workspace:test-ws, name:agent-b}` returns `state=stopped` (or `state=error`)
- Phase 6: `agent/list {workspace:test-ws}` returns 2 agents
- Phase 7: cleanup via `agent/stop` + `agent/delete` + `workspace/delete` succeeds
- Test passes: `--- PASS: TestAgentdRestartRecovery`

---

### TC-08: Multiple Concurrent Agents

**Steps:**
1. `go test ./tests/integration/... -v -timeout 120s -run TestMultipleConcurrentAgents`

**Expected:**
- 3 agents created in workspace concurrent-ws
- All 3 prompts dispatched concurrently (async); all return `accepted=true`
- Each agent reaches `state=running` or `state=idle` after prompt
- Sequential stop+delete succeeds for all 3 agents
- Test passes: `--- PASS: TestMultipleConcurrentAgents`

---

### TC-09: Sequential Prompts — Turn Completion Between Prompts

**Steps:**
1. `go test ./tests/integration/... -v -timeout 120s -run TestMultipleAgentPromptsSequential`

**Expected:**
- 3 prompts sent sequentially; between each prompt, `agent/status` returns `state=idle` (turn completed)
- All 3 prompts accepted and completed
- Test passes: `--- PASS: TestMultipleAgentPromptsSequential`

---

### TC-10: Short-Mode Skips Gracefully

**Steps:**
1. `go test ./tests/integration/... -v -timeout 120s -short`

**Expected:** All tests skip (`--- SKIP`), exit 0. No binary invocations.

---

### TC-11: Real CLI Tests Skip Without API Key

**Steps:**
1. `go test ./tests/integration/... -v -timeout 120s -run 'TestRealCLI'`

**Expected:** Both TestRealCLI_GsdPi and TestRealCLI_ClaudeCode skip with message "skipping: ANTHROPIC_API_KEY not set", exit 0.

---

### Edge Cases

**EC-01: Socket reuse across test runs** — Run the full integration suite twice back-to-back without cleanup. Second run must pass. (Validates `os.Remove(socketPath)` pre-fork fix; stale socket files are cleared.)

**EC-02: agent/delete requires stopped state** — Calling `agent/delete` before `agent/stop` should return JSON-RPC error (agent not in stopped/error state). Confirmed by lifecycle tests calling stop before delete.

**EC-03: workspace/delete requires no active agents** — Calling `workspace/delete` before deleting all agents should return JSON-RPC error (`-32001 CodeRecoveryBlocked` or invalid state). Confirmed by teardown order in all tests (agents deleted before workspace).

**EC-04: agent/status after delete returns not-found** — After `agent/delete`, `agent/status` must return error `-32602` (not found). Confirmed by TestAgentLifecycle step 5.

