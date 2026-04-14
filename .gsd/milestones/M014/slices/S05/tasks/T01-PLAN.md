---
estimated_steps: 31
estimated_files: 3
skills_used: []
---

# T01: Capture InitializeResponse into state.Session at bootstrap-complete and test

## Description

Currently Manager.Create() discards the InitializeResponse from conn.Initialize() (assigned to `_`). This task captures it, converts the ACP types to runtime-spec/api types, and writes Session to state.json in the bootstrap-complete writeState closure.

## Steps

1. In `internal/testutil/mockagent/main.go`, update the Initialize method to return a populated InitializeResponse with `AgentInfo: &acp.Implementation{Name: "mockagent", Version: "0.1.0"}` and `AgentCapabilities: acp.AgentCapabilities{LoadSession: true, McpCapabilities: acp.McpCapabilities{Sse: true}, PromptCapabilities: acp.PromptCapabilities{Image: true}}`.

2. In `pkg/shim/runtime/acp/runtime.go`, add a conversion function `convertInitializeToSession(resp acp.InitializeResponse) *apiruntime.SessionState` that maps:
   - `resp.AgentInfo` (*acp.Implementation) → `*apiruntime.AgentInfo` (handle nil — if AgentInfo is nil, leave session.AgentInfo nil)
   - `resp.AgentCapabilities` → `*apiruntime.AgentCapabilities` with all sub-fields: LoadSession, McpCapabilities{Http,Sse}, PromptCapabilities{Audio,EmbeddedContext,Image}, SessionCapabilities{Fork}
   - For SessionCapabilities.Fork: if resp.AgentCapabilities.SessionCapabilities.Fork != nil, set `&apiruntime.SessionForkCapabilities{}`

3. In Manager.Create(), change `_, handshakeErr = conn.Initialize(...)` to `initResp, handshakeErr := conn.Initialize(...)` (note: use `:=` not `=` since initResp is new). Declare `var initResp acp.InitializeResponse` before the defer block and use `initResp, handshakeErr = ...` with plain `=` to stay in the defer's scope.

4. In the bootstrap-complete writeState closure (the one that sets StatusIdle), add `s.Session = convertInitializeToSession(initResp)` so state.json has session data at bootstrap.

5. Add `TestCreate_PopulatesSession` to `pkg/shim/runtime/acp/runtime_test.go`:
   - Create manager, call Create(ctx)
   - ReadState via mgr.GetState()
   - Assert state.Session != nil
   - Assert state.Session.AgentInfo.Name == "mockagent"
   - Assert state.Session.AgentInfo.Version == "0.1.0"
   - Assert state.Session.Capabilities.LoadSession == true
   - Assert state.Session.Capabilities.McpCapabilities.Sse == true
   - Assert state.Session.Capabilities.PromptCapabilities.Image == true
   - Kill the process and verify Session survives Kill (leverages S03's closure pattern)

6. Run full test suite: `go test ./pkg/shim/runtime/acp/... -count=1` — all tests must pass.

## Must-Haves

- [ ] mockagent Initialize returns populated AgentInfo and AgentCapabilities
- [ ] convertInitializeToSession correctly maps all fields from ACP types to runtime-spec/api types
- [ ] Manager.Create() writes Session to state.json at bootstrap-complete
- [ ] TestCreate_PopulatesSession passes with correct assertions
- [ ] All existing runtime_test.go tests pass (zero regressions)

## Verification

- `go test ./pkg/shim/runtime/acp/... -count=1 -v -run TestCreate_PopulatesSession` passes
- `go test ./pkg/shim/runtime/acp/... -count=1` passes (full suite, zero regressions)
- `make build` succeeds

## Inputs

- ``pkg/shim/runtime/acp/runtime.go` — Manager.Create() with writeState closure pattern (S03)`
- ``pkg/runtime-spec/api/session.go` — SessionState, AgentInfo, AgentCapabilities types (S02)`
- ``internal/testutil/mockagent/main.go` — mock agent returning InitializeResponse`

## Expected Output

- ``pkg/shim/runtime/acp/runtime.go` — convertInitializeToSession function + Create() captures InitializeResponse and writes Session`
- ``pkg/shim/runtime/acp/runtime_test.go` — TestCreate_PopulatesSession test`
- ``internal/testutil/mockagent/main.go` — populated InitializeResponse with AgentInfo and capabilities`

## Verification

go test ./pkg/shim/runtime/acp/... -count=1 -v -run TestCreate_PopulatesSession && go test ./pkg/shim/runtime/acp/... -count=1 && make build
