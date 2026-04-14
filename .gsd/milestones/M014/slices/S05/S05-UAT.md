# S05: ACP bootstrap capabilities capture — UAT

**Milestone:** M014
**Written:** 2026-04-14T16:24:47.976Z

## UAT: S05 — ACP bootstrap capabilities capture

### Preconditions
- Go toolchain available, `make build` succeeds
- mockagent binary builds (internal/testutil/mockagent)

### Test Case 1: state.json.session populated from InitializeResponse
**Steps:**
1. Run `go test ./pkg/shim/runtime/acp/... -count=1 -v -run TestRuntimeSuite/TestCreate_PopulatesSession`
2. Observe test creates a Manager with mockagent, calls Create(ctx), then reads state

**Expected:**
- state.Session is non-nil
- state.Session.AgentInfo.Name == "mockagent"
- state.Session.AgentInfo.Version == "0.1.0"
- state.Session.Capabilities.LoadSession == true
- state.Session.Capabilities.McpCapabilities.Sse == true
- state.Session.Capabilities.PromptCapabilities.Image == true
- After Kill(), state.Session still present (not clobbered)
- Test PASS

### Test Case 2: bootstrap-metadata state_change event in event log
**Steps:**
1. Run `go test ./pkg/shim/server/... -count=1 -v -run TestNotifyStateChange_WithSessionChanged`
2. Observe test creates Translator, emits NotifyStateChange with bootstrap-metadata reason and sessionChanged slice

**Expected:**
- Event log contains exactly 1 entry
- Entry type == "state_change", category == "runtime"
- Reason == "bootstrap-metadata"
- SessionChanged == ["agentInfo", "capabilities"]
- PreviousStatus == "idle", Status == "idle" (metadata-only, no status transition)
- Test PASS

### Test Case 3: Zero regressions in existing suites
**Steps:**
1. Run `go test ./pkg/shim/runtime/acp/... -count=1`
2. Run `go test ./pkg/shim/server/... -count=1`
3. Run `make build`

**Expected:**
- Both test suites pass with zero failures
- Binary builds without errors

### Edge Case: Nil AgentInfo in InitializeResponse
- `convertInitializeToSession` handles nil AgentInfo by leaving session.AgentInfo nil — no panic
- Covered by code-level nil check in the conversion function
