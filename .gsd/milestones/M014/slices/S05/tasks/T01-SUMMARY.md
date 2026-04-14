---
id: T01
parent: S05
milestone: M014
key_files:
  - pkg/shim/runtime/acp/runtime.go
  - pkg/shim/runtime/acp/runtime_test.go
  - internal/testutil/mockagent/main.go
key_decisions:
  - Declared initResp before the defer block and used plain = assignment to keep it in scope for both the defer cleanup and the bootstrap-complete closure
duration: 
verification_result: passed
completed_at: 2026-04-14T16:16:35.916Z
blocker_discovered: false
---

# T01: Capture ACP InitializeResponse into state.Session at bootstrap-complete with conversion function and test

**Capture ACP InitializeResponse into state.Session at bootstrap-complete with conversion function and test**

## What Happened

Manager.Create() previously discarded the InitializeResponse from conn.Initialize() (assigned to `_`). This task captures it, converts ACP types to runtime-spec/api types, and writes Session to state.json in the bootstrap-complete writeState closure.

**Changes made:**

1. **internal/testutil/mockagent/main.go** — Updated Initialize() to return a populated InitializeResponse with `AgentInfo: &acp.Implementation{Name: "mockagent", Version: "0.1.0"}` and capabilities `LoadSession: true, Sse: true, Image: true`.

2. **pkg/shim/runtime/acp/runtime.go** — Three changes:
   - Added `convertInitializeToSession(resp acp.InitializeResponse) *apiruntime.SessionState` that maps all ACP types (Implementation → AgentInfo, AgentCapabilities → AgentCapabilities with McpCapabilities, PromptCapabilities, SessionCapabilities including Fork nil-handling).
   - Changed `_, handshakeErr = conn.Initialize(...)` to `initResp, handshakeErr = conn.Initialize(...)` using a `var initResp` declared before the defer block to keep it in scope.
   - Added `s.Session = convertInitializeToSession(initResp)` in the bootstrap-complete writeState closure.

3. **pkg/shim/runtime/acp/runtime_test.go** — Added `TestCreate_PopulatesSession` that creates a manager, calls Create(ctx), reads state, asserts Session != nil, verifies AgentInfo.Name == "mockagent", AgentInfo.Version == "0.1.0", Capabilities.LoadSession == true, McpCapabilities.Sse == true, PromptCapabilities.Image == true, then kills and verifies Session survives Kill().

## Verification

All three verification commands pass:
1. `go test ./pkg/shim/runtime/acp/... -count=1 -v -run TestRuntimeSuite/TestCreate_PopulatesSession` — PASS
2. `go test ./pkg/shim/runtime/acp/... -count=1` — all 19 tests PASS, zero regressions
3. `make build` — succeeds, builds agentd and agentdctl binaries

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go test ./pkg/shim/runtime/acp/... -count=1 -v -run TestRuntimeSuite/TestCreate_PopulatesSession` | 0 | ✅ pass | 1586ms |
| 2 | `go test ./pkg/shim/runtime/acp/... -count=1 -v` | 0 | ✅ pass | 1876ms |
| 3 | `make build` | 0 | ✅ pass | 5000ms |

## Deviations

None.

## Known Issues

None.

## Files Created/Modified

- `pkg/shim/runtime/acp/runtime.go`
- `pkg/shim/runtime/acp/runtime_test.go`
- `internal/testutil/mockagent/main.go`
