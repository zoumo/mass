---
estimated_steps: 48
estimated_files: 5
skills_used: []
---

# T02: Extend StateChangeEvent with SessionChanged, emit synthetic bootstrap-metadata event, and test

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

## Inputs

- ``pkg/shim/runtime/acp/runtime.go` — StateChange struct and emitStateChange (T01 already delivered Session capture)`
- ``pkg/shim/api/event_types.go` — StateChangeEvent struct`
- ``pkg/shim/server/translator.go` — NotifyStateChange and broadcast`
- ``cmd/agentd/subcommands/shim/command.go` — bootstrap wiring with trans.Start() and stateChangeHook`

## Expected Output

- ``pkg/shim/api/event_types.go` — StateChangeEvent with SessionChanged field`
- ``pkg/shim/runtime/acp/runtime.go` — StateChange struct with SessionChanged field`
- ``pkg/shim/server/translator.go` — NotifyStateChange with sessionChanged parameter`
- ``pkg/shim/server/translator_test.go` — TestNotifyStateChange_WithSessionChanged test`
- ``cmd/agentd/subcommands/shim/command.go` — synthetic bootstrap-metadata event + updated stateChangeHook`

## Verification

go test ./pkg/shim/server/... -count=1 -v -run TestNotifyStateChange_WithSessionChanged && go test ./pkg/shim/server/... -count=1 && go test ./pkg/shim/runtime/acp/... -count=1 && make build
