---
estimated_steps: 44
estimated_files: 5
skills_used: []
---

# T02: Translator hook + buildSessionUpdate + command.go wiring + integration test

Wire the session metadata hook chain end-to-end: Translator calls hook after broadcasting metadata events, buildSessionUpdate converts shim API event types to runtime-spec state mutations, command.go connects them.

**Context:** T01 added Manager.UpdateSessionMetadata. This task completes the chain:
1. Translator gets `sessionMetadataHook func(apishim.Event)` field + `SetSessionMetadataHook` setter (set once before Start(), never changed — no lock needed)
2. `maybeNotifyMetadata(ev apishim.Event)` — called from `run()` after `broadcastSessionEvent(ev)` returns (Translator.mu released). Fires hook only for metadata event types: AvailableCommandsEvent, ConfigOptionEvent, SessionInfoEvent, CurrentModeEvent. All other event types (text, thinking, tool_call, etc.) are ignored.
3. New file `cmd/agentd/subcommands/shim/session_update.go` — contains:
   - `buildSessionUpdate(ev apishim.Event) (changed []string, reason string, apply func(*apiruntime.State))` — switch on event type:
     - AvailableCommandsEvent → ["availableCommands"], "commands-updated", closure setting st.Session.AvailableCommands
     - ConfigOptionEvent → ["configOptions"], "config-updated", closure setting st.Session.ConfigOptions
     - SessionInfoEvent → ["sessionInfo"], "session-info-updated", closure setting st.Session.SessionInfo
     - CurrentModeEvent → ["currentMode"], "mode-updated", closure setting st.Session.CurrentMode
   - Convert helpers: `convertToStateCommands`, `convertToStateConfigOptions`, `convertToStateSessionInfo`, `convertToStateCurrentMode` — field-by-field conversion from apishim types to apiruntime types (types are parallel mirrors)
   - Sort helpers: `sortCommandsByName` sorts apiruntime.AvailableCommand by Name; `sortConfigOptionsByID` sorts apiruntime.ConfigOption by Select.ID
4. In `command.go`, after creating Translator and before `trans.Start()`:
   - `trans.SetSessionMetadataHook(func(ev apishim.Event) { changed, reason, apply := buildSessionUpdate(ev); if changed == nil { return }; if err := mgr.UpdateSessionMetadata(changed, reason, apply); err != nil { logger.Error("session metadata update failed", "changed", changed, "error", err) } })`
   - `mgr.SetEventCountsFn(trans.EventCounts)`
5. Integration test in `pkg/shim/server/translator_test.go`:
   - **TestSessionMetadataHook_ConfigOption**: Create Translator with mock channel → set sessionMetadataHook → inject ConfigOptionUpdate notification → verify hook called with ConfigOptionEvent
   - **TestSessionMetadataHook_IgnoresNonMetadata**: Inject text notification → verify hook NOT called
6. Integration test in `pkg/shim/runtime/acp/runtime_test.go`:
   - **TestMetadataHookChain_ConfigOption**: Full chain: Create manager → Create() → set stateChangeHook → call UpdateSessionMetadata with config options → verify state.json has configOptions + verify state_change emitted with sessionChanged:[configOptions] + Kill → verify configOptions survives

**Lock order (D120):** Translator.mu (broadcastSessionEvent) → release → maybeNotifyMetadata (no lock) → Manager.mu (UpdateSessionMetadata) → release → Translator.mu (NotifyStateChange via hook). No nesting, no deadlock.

**Import constraint (D123):** session_update.go imports apiruntime and apishim but NOT pkg/runtime-spec. Convert helpers do field-by-field mapping.

## Steps

1. Add `sessionMetadataHook func(apishim.Event)` field to Translator struct.
2. Add `SetSessionMetadataHook(hook func(apishim.Event))` method.
3. Add `maybeNotifyMetadata(ev apishim.Event)` method — type-switch on ev, call hook for the 4 metadata types.
4. In `run()`, after `t.broadcastSessionEvent(ev)`, add `t.maybeNotifyMetadata(ev)` call.
5. Create `cmd/agentd/subcommands/shim/session_update.go` with buildSessionUpdate + convert helpers + sort helpers.
6. In `command.go`, wire SetSessionMetadataHook + SetEventCountsFn before trans.Start().
7. Add Translator hook tests in translator_test.go.
8. Add full-chain integration test in runtime_test.go (or a new integration test file).

## Must-Haves

- [ ] sessionMetadataHook field + SetSessionMetadataHook on Translator
- [ ] maybeNotifyMetadata called after broadcastSessionEvent in run()
- [ ] maybeNotifyMetadata only fires for 4 metadata event types
- [ ] buildSessionUpdate dispatches all 4 event types with correct changed/reason/apply
- [ ] Convert helpers correctly map apishim → apiruntime types (commands, configOptions, sessionInfo, currentMode)
- [ ] command.go wires SetSessionMetadataHook + SetEventCountsFn
- [ ] Tests prove: hook called for config_option, hook NOT called for text, state_change emitted with sessionChanged, Kill preserves metadata

## Verification

- `go test ./pkg/shim/server/... -count=1 -v -run 'TestSessionMetadataHook'` — PASS
- `go test ./pkg/shim/runtime/acp/... -count=1 -v -run 'TestRuntimeSuite/TestMetadataHookChain'` — PASS
- `go test ./pkg/shim/runtime/acp/... ./pkg/shim/server/... -count=1` — full suites PASS
- `make build` — clean

## Inputs

- ``pkg/shim/runtime/acp/runtime.go` — Manager with UpdateSessionMetadata, SetEventCountsFn (from T01)`
- ``pkg/shim/runtime/acp/runtime_test.go` — existing test helpers + T01 test patterns`
- ``pkg/shim/server/translator.go` — Translator with broadcast(), broadcastSessionEvent(), run() loop, EventCounts()`
- ``pkg/shim/server/translator_test.go` — existing translator test patterns (makeNotif, sendAndDrainShimEvent)`
- ``cmd/agentd/subcommands/shim/command.go` — existing wiring (SetStateChangeHook, synthetic bootstrap-metadata)`
- ``pkg/shim/api/event_types.go` — AvailableCommandsEvent, ConfigOptionEvent, SessionInfoEvent, CurrentModeEvent types`
- ``pkg/runtime-spec/api/session.go` — SessionState, AvailableCommand, ConfigOption, SessionInfo types`

## Expected Output

- ``pkg/shim/server/translator.go` — sessionMetadataHook field, SetSessionMetadataHook, maybeNotifyMetadata, run() hook call`
- ``pkg/shim/server/translator_test.go` — TestSessionMetadataHook_ConfigOption, TestSessionMetadataHook_IgnoresNonMetadata`
- ``cmd/agentd/subcommands/shim/session_update.go` — buildSessionUpdate, convert helpers, sort helpers`
- ``cmd/agentd/subcommands/shim/command.go` — SetSessionMetadataHook + SetEventCountsFn wiring`
- ``pkg/shim/runtime/acp/runtime_test.go` — TestMetadataHookChain_ConfigOption`

## Verification

go test ./pkg/shim/server/... -count=1 -v -run 'TestSessionMetadataHook' && go test ./pkg/shim/runtime/acp/... -count=1 -v -run 'TestRuntimeSuite/TestMetadataHookChain' && go test ./pkg/shim/runtime/acp/... ./pkg/shim/server/... -count=1 && make build
