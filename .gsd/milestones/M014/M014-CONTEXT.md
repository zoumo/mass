# M014: Enrich state.json + Session Metadata Pipeline

**Gathered:** 2026-04-14
**Status:** Ready for planning

## Project Description

Open Agent Runtime (OAR) is a layered runtime for headless coding agents. The `agentd shim` subcommand manages ACP agent process lifecycle, translates ACP session notifications into typed ShimEvents, and exposes a JSON-RPC surface over a Unix socket. state.json is the runtime's on-disk state record, read by agentd during recovery and by external observers.

## Why This Milestone

Currently, state.json only records process-level lifecycle (status, pid, bundle, exitCode). The ACP agent reports rich session metadata during bootstrap and runtime — capabilities, available commands, config options, session title, current mode — but this data is translated into the event stream and then discarded. There is no way for an external consumer to discover what an agent can do from state.json. Metadata changes also don't emit state_change events, so agentd and orchestrators can't react without polling.

Additionally, writeState() currently takes a full State literal, meaning Kill() and process-exit writes silently clobber already-persisted Session metadata and EventCounts. This is a correctness bug against goal 1.

The plan document at `docs/plan/enrich-state-and-usage-20260414.md` was authored and reviewed over 3 rounds (codex + claude-code). Status: **final-approved**. This milestone implements it exactly.

## User-Visible Outcome

### When this milestone is complete:

- `cat <state-dir>/state.json` shows agentInfo, capabilities, availableCommands, configOptions, sessionInfo, currentMode populated from ACP notifications
- `runtime/status` returns real-time eventCounts from Translator memory, not stale file snapshot
- Each metadata-only change (config_option, available_commands, session_info, current_mode) emits exactly one state_change event with sessionChanged field
- Kill() / process-exit: state.json still contains Session and EventCounts — not wiped
- `file_write`, `file_read`, `command` constants/types are gone from pkg/shim/api

### Entry point / environment

- Entry point: `agentd shim --bundle ... --id ...`
- Environment: local dev / integration tests
- Live dependencies: ACP agent process (mockagent in tests)

## Completion Class

- Contract complete means: `go test ./pkg/runtime-spec/... ./pkg/shim/...` all pass; round-trip tests cover full State schema; dead-code rg gates pass
- Integration complete means: state.json written by a running shim contains Session after bootstrap; metadata ACP events update state.json and emit state_change
- Operational complete means: Kill() preserves Session/EventCounts in state.json

## Final Integrated Acceptance

- A shim runs a prompt turn: state.json contains agentInfo + capabilities + eventCounts; runtime/status returns real-time counts
- An ACP config_option notification arrives: state.json.session.configOptions updated, one state_change event emitted with sessionChanged:["configOptions"]
- Kill() is called: state.json.status = "stopped", session metadata still present

## Architectural Decisions

### writeState as read-modify-write closure

**Decision:** Change `Manager.writeState(state State, reason string)` to `Manager.writeState(apply func(*apiruntime.State), reason string)`. Implementation reads current state.json, value-copies it, calls apply(), sets derived fields (UpdatedAt, EventCounts), writes atomically.

**Rationale:** All 6 current call sites pass full State literals that don't carry Session or EventCounts. Read-modify-write with closures is the only approach that preserves already-persisted fields across all write paths without changing each callsite's mental model of what they own.

**RISK-1:** `ReadState` error → only `errors.Is(err, os.ErrNotExist)` gets zero-value path. JSON corruption or permission errors must return immediately, no write.

### Metadata hook: Translator → Manager, not Manager watching channel

**Decision:** `Translator.sessionMetadataHook func(apishim.Event)` is called after `broadcastSessionEvent()` returns (lock released). Hook calls `Manager.UpdateSessionMetadata(changed []string, apply func(*State))`. Manager is NOT a second consumer of the ACP notification channel.

**Rationale:** Manager.Events() is a single-consumer channel used by Translator. Adding a second consumer would race. The hook-after-broadcast pattern preserves the single-consumer invariant and guarantees session events enter the event stream before metadata is persisted to state.json.

**Lock order (no deadlock):** `Translator.mu → release → Manager.mu → release → Translator.mu`. No nested lock acquisition.

### Manager.UpdateSessionMetadata exported, helpers package-local

**Decision:** `Manager.UpdateSessionMetadata()` is exported (capital U) because command.go's Service layer calls it from outside the acp package. Helper functions (`buildSessionUpdate`, `buildBootstrapSession`) live in the calling package (cmd/agentd/subcommands/shim/ or pkg/shim/server/). `convertAgentCapabilities()` stays unexported in pkg/shim/runtime/acp/.

**RISK-4:** Inconsistent casing causes compile errors — use UpdateSessionMetadata everywhere in the external call chain.

### EventCounts counted in broadcast(), not translate()

**Decision:** `Translator.eventCounts[ev.Type]++` runs in `broadcast()` after `t.nextSeq++`, before fan-out. This is the only counting point.

**Rationale:** translate() only handles ACP-translated events. broadcast() is called by all event origins including manual NotifyTurnStart/NotifyTurnEnd/NotifyUserPrompt/NotifyStateChange. Counting at broadcast() guarantees 100% coverage with a single counting site. Log-append failures skip the increment (fail-closed semantics preserved).

### Bootstrap synthetic state_change

**Decision:** After `trans.Start()` and `mgr.SetStateChangeHook()` in command.go, emit `trans.NotifyStateChange("idle","idle",pid,"bootstrap-metadata",["agentInfo","capabilities"])`. This is a metadata-only event with previousStatus==status.

**RISK-2:** This event enters the event log before the RPC listener starts. Fresh live subscribers won't receive it in real-time — they must use `session/subscribe(fromSeq=0)` backfill. Tests and docs must not claim all live subscribers receive it.

### Derived fields (updatedAt, eventCounts) do not independently trigger state_change

**Decision:** `updatedAt` and `eventCounts` are set on every state write but never appear in `sessionChanged` and never cause an independent `state_change` emission.

**Rationale:** If eventCounts triggered state_change, state_change would increment eventCounts, which would trigger another state_change — infinite recursion. These are diagnostic annotations, not state machine events.

**RISK-3:** The state.json eventCounts.state_change will be off by 1 for the most recent state_change (counted in broadcast() after writeState() was already called). runtime/status overlay provides the accurate real-time value.

### runtime-spec/api types are self-contained, no import of pkg/shim/api

**Decision:** All new types in `pkg/runtime-spec/api/state.go` (SessionState, AgentInfo, AgentCapabilities, AvailableCommand, ConfigOption, etc.) are defined inline with their own custom MarshalJSON/UnmarshalJSON. They do not import `pkg/shim/api`.

**Rationale:** pkg/runtime-spec is an independent spec package. Importing pkg/shim/api would create a cross-dependency between the spec layer and the implementation layer. JSON wire shapes are identical but the types are separate. If shapes diverge, both must be updated.

### State machine: derived field trigger rules

| Field | Triggers state_change | In sessionChanged | Notes |
|---|---|---|---|
| status | ✅ | ❌ | lifecycle |
| agentInfo | synthetic only | ✅ | bootstrap-metadata |
| capabilities | synthetic only | ✅ | bootstrap-metadata |
| availableCommands | ✅ via UpdateSessionMetadata | ✅ | metadata hook |
| configOptions | ✅ via UpdateSessionMetadata | ✅ | metadata hook |
| sessionInfo | ✅ via UpdateSessionMetadata | ✅ | metadata hook |
| currentMode | ✅ via UpdateSessionMetadata | ✅ | metadata hook |
| updatedAt | ❌ | ❌ | derived |
| eventCounts | ❌ | ❌ | derived |

## Error Handling Strategy

- `UpdateSessionMetadata`: state.json write failure → log structured error, do NOT roll back already-broadcast session event, do NOT emit state_change (state didn't change successfully)
- `writeState` (lifecycle): ReadState error that is NOT os.ErrNotExist → return error immediately, no write
- `broadcast()`: log.Append failure → drop event (fail-closed), do NOT increment nextSeq, do NOT increment eventCounts

## Risks and Unknowns

- RISK-1: ReadState non-ErrNotExist errors silently treated as first write — must guard with errors.Is(err, os.ErrNotExist)
- RISK-2: Bootstrap synthetic event not receivable by fresh live subscribers — test must verify via history, not live channel
- RISK-3: eventCounts.state_change in state.json is off by 1 for the most recent state_change — acceptable; runtime/status overlay corrects it
- RISK-4: UpdateSessionMetadata export boundary — must be capital-U everywhere it's called from outside the acp package

## Existing Codebase / Prior Art

- `pkg/shim/runtime/acp/runtime.go` — Manager with current `writeState(State, reason)` signature (all 6 call sites need migration)
- `pkg/shim/server/translator.go` — Translator with broadcast() (counting point goes here); no sessionMetadataHook yet
- `pkg/runtime-spec/api/state.go` — State struct (add Session, EventCounts, UpdatedAt)
- `pkg/shim/api/event_constants.go` — has dead EventTypeFileWrite/Read/Command constants
- `pkg/shim/api/event_types.go` — has dead FileWriteEvent/ReadEvent/CommandEvent types; has union marshal patterns to copy for new types
- `cmd/agentd/subcommands/shim/command.go` — bootstrap wiring (synthetic event goes after trans.Start())
- `pkg/shim/server/service.go` — Status() (add EventCounts overlay)
- `docs/plan/enrich-state-and-usage-20260414.md` — authoritative full spec with Go type definitions, conversion functions, test matrix

## Relevant Requirements

- R053 — Session metadata in state.json (primary)
- R054 — Metadata changes emit state_change events
- R055 — EventCounts in state.json and runtime/status
- R056 — Bootstrap capabilities captured and signaled
- R057 — writeState preserves Session across lifecycle transitions
- R058 — Dead placeholder event types removed
- R059 — updatedAt on all state writes

## Scope

### In Scope

- New types in pkg/runtime-spec/api: SessionState, AgentInfo, AgentCapabilities (+ sub-types), AvailableCommand (+ input types), ConfigOption (+ select/options types), SessionInfo — with full custom MarshalJSON matching pkg/shim/api wire shape
- UpdatedAt, Session, EventCounts fields on State
- writeState refactored to closure read-modify-write
- UpdateSessionMetadata on Manager
- sessionMetadataHook + maybeNotifyMetadata on Translator
- eventCounts map + EventCounts() snapshot + SetEventCountsFn on Manager
- NotifyStateChange signature extended with sessionChanged []string
- StateChange struct extended with SessionChanged []string
- StateChangeEvent extended with SessionChanged []string
- ACP Initialize() response captured → buildBootstrapSession
- Bootstrap synthetic state_change in command.go
- runtime/status EventCounts overlay in service.go
- Dead code removal: EventTypeFileWrite/Read/Command, FileWriteEvent/ReadEvent/CommandEvent, decode cases
- Design doc updates: shim-rpc-spec.md, agent-shim.md

### Out of Scope / Non-Goals

- Usage data in state.json (explicitly excluded)
- Any ARI layer changes
- Any agentd DB changes
- Any breaking change to existing ShimEvent wire shape (only additions)

## Technical Constraints

- pkg/runtime-spec/api must NOT import pkg/shim/api
- Lock order: Translator.mu → release → Manager.mu → release → Translator.mu. No nesting.
- All Manager state reads/writes under Manager.mu. stateChangeHook called OUTSIDE Manager.mu.
- errors.Is(err, os.ErrNotExist) is the only error that gets zero-value first-write treatment in writeState
- Socket path ≤104 bytes on macOS — use os.MkdirTemp("/tmp","oar-*") for tests that exercise writeState paths

## Testing Requirements

Full test matrix from plan doc (改动 8 section). Key tests:
- State round-trip: WriteState → ReadState covers all new fields including union marshal
- Translator eventCounts: ACP events counted; manual events counted; log-append failure not counted; EventCounts() returns copy not reference
- Manager.UpdateSessionMeta session field merge preserves untouched fields; state_change emitted with correct reason/sessionChanged
- writeState closure: Kill() preserves Session/EventCounts; process-exit preserves EventCounts
- Bootstrap capabilities: state.json at bootstrap-complete has agentInfo+capabilities from InitializeResponse
- Derived fields: one metadata event → exactly one state_change; sessionChanged does not contain "eventCounts" or "updatedAt"
- runtime/status overlay: returns Translator memory counts not file counts

## Acceptance Criteria

1. `make build` passes
2. `go test ./pkg/runtime-spec/... ./pkg/shim/...` all pass
3. `! rg "EventTypeFileWrite|EventTypeFileRead|EventTypeCommand|FileWriteEvent|FileReadEvent|CommandEvent" --type go --glob '!docs/plan/*'` — no output
4. `! rg "file_write|file_read" --type go --glob '!docs/plan/*' --glob '!docs/design/*'` — no output
5. State round-trip test covers Session + EventCounts + UpdatedAt
6. Kill() test proves Session survives in state.json
7. Bootstrap-metadata synthetic event appears in history
8. One config_option ACP event → exactly one state_change in event log

## Open Questions

- None. All design decisions resolved in 3-round review.
