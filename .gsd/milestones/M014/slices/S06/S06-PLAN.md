# S06: Session metadata hook chain

**Goal:** Session metadata ACP notifications (available_commands, config_option, session_info, current_mode) update state.json.session via a Translator→Manager hook chain, each emitting exactly one metadata-only state_change event; EventCounts flushed on every state write; Kill() preserves all session metadata.
**Demo:** After this: inject a ConfigOptionUpdate ACP notification into a running translator; state.json.session.configOptions updated; event log contains exactly one state_change with reason:config-updated and sessionChanged:[configOptions]; Kill() afterwards — state.json still has configOptions.

## Must-Haves

- 1. Inject ConfigOptionUpdate ACP notification → state.json.session.configOptions matches the notification payload
- 2. Event log contains exactly one state_change with reason:config-updated and sessionChanged:[configOptions] (metadata-only: previousStatus==status)
- 3. Kill() after metadata update → state.json.session.configOptions still present
- 4. EventCounts flushed to state.json on every writeState call (not just metadata updates)
- 5. All 4 metadata event types (available_commands, config_option, session_info, current_mode) dispatch correctly through buildSessionUpdate
- 6. `go test ./pkg/shim/runtime/acp/... ./pkg/shim/server/... -count=1` passes with zero regressions
- 7. `make build` succeeds

## Proof Level

- This slice proves: Integration — tests exercise the full ACP notification → Translator → Manager → state.json → state_change event chain with a real mock agent process

## Integration Closure

Upstream: S03 writeState closure pattern (all state writes are read-modify-write), S04 Translator.EventCounts() method, S05 convertInitializeToSession + SessionChanged on StateChange. New wiring: Translator.sessionMetadataHook → Manager.UpdateSessionMetadata → state.json + state_change emission; Manager.SetEventCountsFn → writeState EventCounts flush; buildSessionUpdate + convert helpers in shim command package. Remaining: S07 wires EventCounts into runtime/status overlay.

## Verification

- Manager.UpdateSessionMetadata failures are logged as structured errors (slog.Error with reason + changed fields). state_change events carry sessionChanged field identifying which metadata fields were updated. EventCounts in state.json provide cumulative event counts on every state write.

## Tasks

- [x] **T01: Manager.UpdateSessionMetadata + SetEventCountsFn + writeState EventCounts flush** `est:45m`
  Add the Manager-side infrastructure for session metadata updates and EventCounts flushing.

**Context:** Manager.writeState (from S03) is a read-modify-write closure that preserves Session across lifecycle writes. This task adds:
1. `eventCountsFn func() map[string]int` field on Manager + `SetEventCountsFn` setter — allows the Translator's EventCounts() to be injected.
2. Modify `writeState` to flush EventCounts on every write: after the closure runs and before `spec.WriteState`, call `m.eventCountsFn()` and set `state.EventCounts`.
3. `UpdateSessionMetadata(changed []string, reason string, apply func(*apiruntime.State)) error` — exported method that:
   - Acquires m.mu
   - Reads current state via spec.ReadState (error if not found — agent must exist)
   - Ensures state.Session is non-nil (create `&apiruntime.SessionState{}` if nil)
   - Calls `apply(&state)` to update specific session fields
   - Sets `state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)`
   - Sets `state.EventCounts` from eventCountsFn if available
   - Writes via spec.WriteState
   - Copies stateChangeHook reference + builds StateChange with SessionChanged
   - Releases m.mu
   - Calls hook outside lock (D120 lock order: Manager.mu released before Translator.mu acquired)

**Lock semantics:** UpdateSessionMetadata acquires m.mu for the read-modify-write-emit cycle. The stateChangeHook is called AFTER releasing m.mu, matching the existing emitStateChange pattern. The StateChange emitted has previousStatus==status (metadata-only) and SessionChanged populated.

**Differences from lifecycle writeState:** writeState only emits state_change on status transitions. UpdateSessionMetadata ALWAYS emits state_change (metadata-only).

## Steps

1. Add `eventCountsFn func() map[string]int` field to Manager struct and `SetEventCountsFn(fn func() map[string]int)` method.
2. In `writeState`, after `apply(&state)` and `state.UpdatedAt = ...`, add: `if m.eventCountsFn != nil { state.EventCounts = m.eventCountsFn() }`.
3. Add `UpdateSessionMetadata(changed []string, reason string, apply func(*apiruntime.State)) error` method:
   - Lock m.mu
   - Read state via spec.ReadState; return error if fails (no ErrNotExist guard — state must exist)
   - If state.Session == nil, set state.Session = &apiruntime.SessionState{}
   - apply(&state)
   - state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
   - if m.eventCountsFn != nil { state.EventCounts = m.eventCountsFn() }
   - spec.WriteState(m.stateDir, state); return on error (do NOT emit state_change)
   - Copy hook := m.stateChangeHook
   - Build change := StateChange{SessionID: state.ID, PreviousStatus: state.Status, Status: state.Status, PID: state.PID, Reason: reason, SessionChanged: changed}
   - Unlock m.mu
   - If hook != nil, call hook(change)
4. Add tests in runtime_test.go:
   - **TestUpdateSessionMetadata_UpdatesStateJSON**: Create manager → Create() → call UpdateSessionMetadata with configOptions apply → ReadState → verify session.configOptions populated + UpdatedAt set
   - **TestUpdateSessionMetadata_EmitsStateChange**: Create → register stateChangeHook → UpdateSessionMetadata → verify hook called with correct PreviousStatus==Status, Reason, SessionChanged
   - **TestUpdateSessionMetadata_PreservedByKill**: Create → UpdateSessionMetadata → Kill → ReadState → verify configOptions still present
   - **TestWriteState_FlushesEventCounts**: Create → SetEventCountsFn(mock returning counts) → trigger writeState via Kill → ReadState → verify EventCounts populated

## Must-Haves

- [ ] eventCountsFn field + SetEventCountsFn on Manager
- [ ] writeState flushes EventCounts on every write
- [ ] UpdateSessionMetadata exported method with correct lock/emit semantics
- [ ] UpdateSessionMetadata emits state_change with previousStatus==status and sessionChanged populated
- [ ] Hook called OUTSIDE m.mu (no nested lock)
- [ ] All new tests pass; existing tests pass (zero regressions)

## Verification

- `go test ./pkg/shim/runtime/acp/... -count=1 -v -run 'TestRuntimeSuite/(TestUpdateSessionMetadata|TestWriteState_FlushesEventCounts)'` — all PASS
- `go test ./pkg/shim/runtime/acp/... -count=1` — full suite PASS
- `make build` — clean
  - Files: `pkg/shim/runtime/acp/runtime.go`, `pkg/shim/runtime/acp/runtime_test.go`
  - Verify: go test ./pkg/shim/runtime/acp/... -count=1 -v -run 'TestRuntimeSuite/(TestUpdateSessionMetadata|TestWriteState_FlushesEventCounts)' && go test ./pkg/shim/runtime/acp/... -count=1 && make build

- [ ] **T02: Translator hook + buildSessionUpdate + command.go wiring + integration test** `est:60m`
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
  - Files: `pkg/shim/server/translator.go`, `pkg/shim/server/translator_test.go`, `cmd/agentd/subcommands/shim/session_update.go`, `cmd/agentd/subcommands/shim/command.go`, `pkg/shim/runtime/acp/runtime_test.go`
  - Verify: go test ./pkg/shim/server/... -count=1 -v -run 'TestSessionMetadataHook' && go test ./pkg/shim/runtime/acp/... -count=1 -v -run 'TestRuntimeSuite/TestMetadataHookChain' && go test ./pkg/shim/runtime/acp/... ./pkg/shim/server/... -count=1 && make build

## Files Likely Touched

- pkg/shim/runtime/acp/runtime.go
- pkg/shim/runtime/acp/runtime_test.go
- pkg/shim/server/translator.go
- pkg/shim/server/translator_test.go
- cmd/agentd/subcommands/shim/session_update.go
- cmd/agentd/subcommands/shim/command.go
