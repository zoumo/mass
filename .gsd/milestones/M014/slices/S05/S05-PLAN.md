# S05: ACP bootstrap capabilities capture

**Goal:** ACP Initialize() response captured and written to state.Session at bootstrap-complete; synthetic state_change(bootstrap-metadata) emitted after Translator.Start() so subscribers get it via history backfill.
**Demo:** After this: test runs Manager.Create() with a mock ACP server that returns a populated InitializeResponse; ReadState() shows state.json.session.agentInfo.name matches the mock response; state.json.session.capabilities.loadSession matches; bootstrap-metadata state_change event appears in event log.

## Must-Haves

- Manager.Create() populates state.Session.AgentInfo and state.Session.Capabilities from the ACP InitializeResponse
- mockagent returns populated AgentInfo (name="mockagent", version="0.1.0") and AgentCapabilities (loadSession=true) in InitializeResponse
- StateChangeEvent has SessionChanged []string field (omitempty)
- NotifyStateChange accepts sessionChanged []string parameter
- command.go emits synthetic trans.NotifyStateChange("idle","idle",pid,"bootstrap-metadata",["agentInfo","capabilities"]) after trans.Start()
- TestCreate_PopulatesSession passes — proves state.json session content matches mock
- TestNotifyStateChange_WithSessionChanged passes — proves bootstrap-metadata event with SessionChanged field appears in event log
- All existing tests in pkg/shim/runtime/acp and pkg/shim/server pass (zero regressions)

## Proof Level

- This slice proves: integration — tests exercise real Manager.Create() with mockagent binary and Translator with EventLog

## Integration Closure

- Upstream surfaces consumed: `pkg/runtime-spec/api/session.go` (SessionState, AgentInfo, AgentCapabilities types from S02), `pkg/shim/runtime/acp/runtime.go` (writeState closure pattern from S03), `github.com/coder/acp-go-sdk` (InitializeResponse, Implementation, AgentCapabilities)
- New wiring introduced: Manager.Create() captures InitializeResponse and writes Session to state.json; command.go emits synthetic state_change after trans.Start(); StateChangeEvent carries SessionChanged field
- What remains: S06 (session metadata hook chain) wires runtime ACP notifications into state.json updates with state_change events; S07 (runtime/status overlay) adds EventCounts overlay

## Verification

- Runtime signals: bootstrap-metadata state_change event in event log with sessionChanged:["agentInfo","capabilities"]; state.json.session populated at bootstrap-complete
- Inspection surfaces: cat state.json shows session.agentInfo and session.capabilities after bootstrap; event log JSONL contains bootstrap-metadata event
- Failure visibility: if InitializeResponse capture fails, state.Session remains nil — detectable by checking state.json.session == null after bootstrap-complete

## Tasks

- [x] **T01: Capture InitializeResponse into state.Session at bootstrap-complete and test** `est:45m`
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
  - Files: `pkg/shim/runtime/acp/runtime.go`, `pkg/shim/runtime/acp/runtime_test.go`, `internal/testutil/mockagent/main.go`
  - Verify: go test ./pkg/shim/runtime/acp/... -count=1 -v -run TestCreate_PopulatesSession && go test ./pkg/shim/runtime/acp/... -count=1 && make build

- [x] **T02: Extend StateChangeEvent with SessionChanged, emit synthetic bootstrap-metadata event, and test** `est:45m`
  ## Description

Per D124, after Translator.Start() in command.go, a synthetic state_change event with reason `bootstrap-metadata` and sessionChanged `["agentInfo","capabilities"]` must be emitted so subscribers discover the agent's identity and capabilities via history backfill (fromSeq=0). This requires extending StateChangeEvent, StateChange, and NotifyStateChange with a SessionChanged field.

## Steps

1. In `pkg/shim/api/event_types.go`, add `SessionChanged []string \`json:"sessionChanged,omitempty"\`` to `StateChangeEvent` struct (after Reason field).

2. In `pkg/shim/runtime/acp/runtime.go`, add `SessionChanged []string` to the `StateChange` struct (after Reason field).

3. In `pkg/shim/server/translator.go`, extend `NotifyStateChange` signature from `(previousStatus, status string, pid int, reason string)` to `(previousStatus, status string, pid int, reason string, sessionChanged []string)`. Inside the function, add `SessionChanged: sessionChanged` to the `StateChangeEvent` literal.

4. In `cmd/agentd/subcommands/shim/command.go`, update the stateChangeHook closure to pass `change.SessionChanged` as the 5th argument to `trans.NotifyStateChange(...)`. For lifecycle transitions, Manager.emitStateChange constructs StateChange with SessionChanged=nil, so this transparently passes nil for existing lifecycle events.

5. In `cmd/agentd/subcommands/shim/command.go`, after `trans.Start()` and before building the Service, add the synthetic event:
   ```go
   // Emit synthetic bootstrap-metadata so subscribers discover agent identity
   // and capabilities via history backfill (D124).
   {
       st, _ := mgr.GetState()
       trans.NotifyStateChange("idle", "idle", st.PID, "bootstrap-metadata", []string{"agentInfo", "capabilities"})
   }
   ```

6. Update all existing NotifyStateChange call sites in test files to pass `nil` (or `[]string(nil)`) as the 5th argument:
   - `pkg/shim/server/translator_test.go` — ~6 call sites
   - Verify no other callers exist via `rg 'NotifyStateChange' pkg/ cmd/ -g '*.go'`

7. Add `TestNotifyStateChange_WithSessionChanged` to `pkg/shim/server/translator_test.go`:
   - Create a temp dir, open an EventLog
   - Create a Translator with the EventLog (use a closed notification channel)
   - Call tr.Start()
   - Call tr.NotifyStateChange("idle", "idle", 42, "bootstrap-metadata", []string{"agentInfo", "capabilities"})
   - Read the event log via ReadEventLog(logPath, 0)
   - Assert len(entries) == 1
   - Assert entries[0].Type == "state_change"
   - Assert entries[0].Category == "runtime"
   - Type-assert Content to StateChangeEvent
   - Assert Reason == "bootstrap-metadata"
   - Assert SessionChanged == []string{"agentInfo", "capabilities"}
   - Assert PreviousStatus == "idle" and Status == "idle" (metadata-only, no status change)
   - Call tr.Stop()

8. Run full test suites to verify zero regressions.

## Must-Haves

- [ ] StateChangeEvent has SessionChanged []string field with omitempty JSON tag
- [ ] StateChange struct has SessionChanged []string field
- [ ] NotifyStateChange accepts sessionChanged []string parameter
- [ ] command.go emits synthetic bootstrap-metadata event after trans.Start()
- [ ] command.go stateChangeHook relays SessionChanged
- [ ] All existing NotifyStateChange callers updated (nil for lifecycle events)
- [ ] TestNotifyStateChange_WithSessionChanged passes
- [ ] All existing translator tests pass (zero regressions)

## Verification

- `go test ./pkg/shim/server/... -count=1 -v -run TestNotifyStateChange_WithSessionChanged` passes
- `go test ./pkg/shim/server/... -count=1` passes (full suite, zero regressions)
- `go test ./pkg/shim/runtime/acp/... -count=1` passes (runtime tests still pass)
- `make build` succeeds
  - Files: `pkg/shim/api/event_types.go`, `pkg/shim/runtime/acp/runtime.go`, `pkg/shim/server/translator.go`, `pkg/shim/server/translator_test.go`, `cmd/agentd/subcommands/shim/command.go`
  - Verify: go test ./pkg/shim/server/... -count=1 -v -run TestNotifyStateChange_WithSessionChanged && go test ./pkg/shim/server/... -count=1 && go test ./pkg/shim/runtime/acp/... -count=1 && make build

## Files Likely Touched

- pkg/shim/runtime/acp/runtime.go
- pkg/shim/runtime/acp/runtime_test.go
- internal/testutil/mockagent/main.go
- pkg/shim/api/event_types.go
- pkg/shim/server/translator.go
- pkg/shim/server/translator_test.go
- cmd/agentd/subcommands/shim/command.go
