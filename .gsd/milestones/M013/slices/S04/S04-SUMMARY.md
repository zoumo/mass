---
id: S04
parent: M013
milestone: M013
provides:
  - ["pkg/shim/api — complete self-contained event wire types (ShimEvent, typed events, EventType*/Category* constants, EventTypeOf accessor)", "pkg/shim/server — translator.go (Translator, NewTranslator, OpenEventLog), log.go (ReadEventLog, EventLog) with full test coverage", "pkg/shim/runtime/acp — ACP runtime (Manager, New, StateChange) and client with full test coverage", "M013 package restructure complete: api/ subdirectories contain only pure types; all implementation in typed server/ or client/ packages; pkg/events and pkg/runtime no longer exist"]
requires:
  - slice: S01
    provides: Removed api/runtime and api/types.go; all consumers migrated
  - slice: S02
    provides: pkg/ari/{api,server,client} subdirectory structure established
  - slice: S03
    provides: pkg/shim/api, pkg/shim/server, pkg/shim/client structure; api/ directory removed
affects:
  []
key_files:
  - ["pkg/shim/api/shim_event.go", "pkg/shim/api/event_types.go", "pkg/shim/api/event_constants.go", "pkg/shim/api/types.go", "pkg/shim/server/translator.go", "pkg/shim/server/log.go", "pkg/shim/server/service.go", "pkg/shim/server/translator_test.go", "pkg/shim/server/log_test.go", "pkg/shim/server/wire_shape_test.go", "pkg/shim/server/translate_rich_test.go", "pkg/shim/runtime/acp/runtime.go", "pkg/shim/runtime/acp/client.go", "pkg/shim/runtime/acp/runtime_test.go", "pkg/shim/runtime/acp/client_test.go", "cmd/agentd/subcommands/shim/command.go", "pkg/agentd/process.go", "pkg/agentd/recovery.go"]
key_decisions:
  - ["D117 — Move event wire types to pkg/shim/api first (T01), then move translator+log to pkg/shim/server (T02): two-task split ensures types are in their final home before implementation consumers are moved", "D118 — EventTypeOf(ev Event) string added to pkg/shim/api/event_types.go as exported cross-package accessor for the sealed Event interface's unexported eventType() method"]
patterns_established:
  - ["Two-task migration pattern for packages with sealed event models: (1) copy types to api/ package and update all consumers; (2) move implementation to server/ package and add qualifier prefix — described in K087", "Sealed interface cross-package bridge: add EventTypeOf() exported accessor in the interface-owning package (pkg/shim/api) rather than duplicating or exposing the unexported method — described in K086", "Temporary JSON-round-trip bridge in T01 as explicit compatibility shim during multi-task migration; removed cleanly in T02 once implementation moves to same package"]
observability_surfaces:
  - None — this is a pure structural migration. Translator, EventLog, and ShimEvent retain all existing behavior; only their package paths changed.
drill_down_paths:
  []
duration: ""
verification_result: passed
completed_at: 2026-04-14T11:43:29.693Z
blocker_discovered: false
---

# S04: Events impl + ACP runtime migration + final verification

**Migrated pkg/events/ wire types into pkg/shim/api/, moved translator+log to pkg/shim/server/, relocated pkg/runtime/ to pkg/shim/runtime/acp/, deleted both source packages; make build + go test ./... + go vet (first-party) all pass.**

## What Happened

S04 completed the final phase of the M013 package restructure by eliminating pkg/events/ and pkg/runtime/ and distributing their contents into the new canonical locations.

**T01 — Event wire types into pkg/shim/api/**

Three new files were added to pkg/shim/api/: shim_event.go (ShimEvent, PhaseForEvent), event_types.go (TextEvent, ToolCallEvent, StateChangeEvent, ContentBlock helpers, Event interface), and event_constants.go (EventType* and Category* constants). These were sourced from pkg/events/ with only the package declaration changed (events → api). pkg/shim/api/types.go had its pkg/events import removed and all `events.ShimEvent` references changed to bare `ShimEvent` (now same-package). Ten external consumer files were updated — pkg/shim/client/client.go, pkg/ari/server/server_test.go, pkg/agentd/process.go, pkg/agentd/recovery.go, and four agentd test files, plus cmd/agentdctl/subcommands/shim/command.go and chat.go — all pointing events.* references to apishim.* from their existing alias. One unplanned fix was required: pkg/shim/server/service.go needed a temporary legacyEventsToAPI JSON-round-trip bridge to handle the type incompatibility between pkg/events.ShimEvent (still used by Translator) and the new apishim.ShimEvent. This bridge was explicitly documented as T02-temporary.

**T02 — Implementation files move, directory deletion, final verification**

translator.go and log.go were created in pkg/shim/server/ from their pkg/events/ counterparts, with package changed to server and all unqualified event-type refs updated to apishim.* qualification. One non-obvious obstacle: the sealed Event interface in pkg/shim/api uses an unexported eventType() method, which cannot be called from pkg/shim/server across the package boundary. This was resolved by adding EventTypeOf(ev Event) string as an exported accessor to pkg/shim/api/event_types.go — a minimal, backward-compatible addition that preserves the sealed interface for implementors. All four test files (translator_test.go, log_test.go, wire_shape_test.go, translate_rich_test.go) were moved to pkg/shim/server/ with package names updated. pkg/shim/runtime/acp/runtime.go and client.go were created from pkg/runtime/ counterparts with package changed from runtime to acp; runtime_test.go and client_test.go moved with package names updated. pkg/shim/server/service.go was fully cleaned: dropped pkg/events and pkg/runtime imports (Translator/EventLog/ReadEventLog are now same-package; runtime.Manager became acpruntime.Manager), and the T01 legacyEventsToAPI bridge was removed. cmd/agentd/subcommands/shim/command.go updated to use acpruntime and shimserver instead of pkg/runtime and pkg/events. pkg/events/ and pkg/runtime/ directories deleted.

**Verification outcome**

- `rg 'zoumo/oar/pkg/events' --type go` → exit 1 (zero matches)
- `rg 'zoumo/oar/pkg/runtime"' --type go` → exit 1 (zero matches)
- `make build` → exit 0 (agentd + agentdctl)
- `go test ./...` → all packages pass including pkg/shim/server, pkg/shim/runtime/acp, and 103s integration tests
- `go vet ./pkg/... ./cmd/...` → exit 0 (full `go vet ./...` reports a pre-existing lock-copy issue in third_party/charmbracelet/crush/csync/maps.go — present before M013, unrelated to this migration)

The M013 package restructure is now complete: api/ contains only pure types, all implementation lives in typed server/ or client/ packages, and the two legacy implementation packages (pkg/events, pkg/runtime) no longer exist.

## Verification

All slice-level verification gates passed:
1. `rg 'zoumo/oar/pkg/events' --type go` → exit 1 ✅ (zero matches — pkg/events fully eliminated)
2. `rg 'zoumo/oar/pkg/runtime"' --type go` → exit 1 ✅ (zero matches — pkg/runtime fully eliminated, trailing quote excludes pkg/runtime-spec)
3. `make build` → exit 0 ✅ (agentd and agentdctl binaries built cleanly)
4. `go test ./...` → exit 0 ✅ (all packages pass: pkg/shim/server 2.8s cached, pkg/shim/runtime/acp 4.9s cached, pkg/agentd 6.4s, integration tests 103.5s)
5. `go vet ./pkg/... ./cmd/...` → exit 0 ✅ (all first-party packages clean; pre-existing third_party/ vet issue in csync/maps.go is out-of-scope)
6. `ls pkg/events` → no such directory ✅
7. `ls pkg/runtime` → no such directory ✅
8. `ls pkg/shim/server/` → translator.go, log.go, service.go + 4 test files ✅
9. `ls pkg/shim/runtime/acp/` → runtime.go, client.go, runtime_test.go, client_test.go ✅

## Requirements Advanced

None.

## Requirements Validated

- R020 — pkg/shim/runtime/acp/runtime.go preserves full terminal manager implementation; go test ./pkg/shim/runtime/acp/... passes including TestTerminalManager_Create_* tests
- R026 — TerminalOutput implementation preserved in pkg/shim/runtime/acp; TestTerminalManager_Output_* tests pass
- R027 — KillTerminalCommand implementation preserved; TestTerminalManager_Kill_* tests pass
- R028 — ReleaseTerminal implementation preserved; TestTerminalManager_Release_* tests pass
- R029 — WaitForTerminalExit implementation preserved; TestTerminalManager_WaitForExit_* tests pass

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

T01 fixed pkg/shim/server/service.go (not in the T01 plan) by adding a temporary legacyEventsToAPI JSON-round-trip bridge to keep make build passing at the T01 boundary. T02 removed the bridge. T02 also added EventTypeOf() to pkg/shim/api/event_types.go (not in plan) to resolve the sealed-interface cross-package constraint.

## Known Limitations

go vet ./... reports a pre-existing lock-copy issue in third_party/charmbracelet/crush/csync/maps.go — unrelated to S04, present before M013. First-party packages (./pkg/... ./cmd/...) are fully vet-clean.

## Follow-ups

None. The M013 package restructure is complete. All four slices delivered their goals and all three verification commands (make build, go test ./..., go vet first-party) pass.

## Files Created/Modified

- `pkg/shim/api/shim_event.go` — New — ShimEvent struct and PhaseForEvent function, moved from pkg/events
- `pkg/shim/api/event_types.go` — New — typed event structs (TextEvent, ToolCallEvent, StateChangeEvent, ContentBlock, etc.), Event interface, EventTypeOf accessor
- `pkg/shim/api/event_constants.go` — New — EventType* and Category* constants, moved from pkg/events
- `pkg/shim/api/types.go` — Removed pkg/events import; ShimEvent fields now use same-package bare type
- `pkg/shim/server/translator.go` — New — Translator implementation, moved from pkg/events; package changed to server; all event refs qualified via apishim.*
- `pkg/shim/server/log.go` — New — EventLog/ReadEventLog/OpenEventLog, moved from pkg/events; package changed to server
- `pkg/shim/server/service.go` — Dropped pkg/events and pkg/runtime imports; Translator/EventLog same-package; runtime.Manager → acpruntime.Manager; T01 bridge removed
- `pkg/shim/server/translator_test.go` — New — moved from pkg/events; package server; apishim.* qualifiers
- `pkg/shim/server/log_test.go` — New — moved from pkg/events; package server
- `pkg/shim/server/wire_shape_test.go` — New — moved from pkg/events; package server; apishim.* qualifiers
- `pkg/shim/server/translate_rich_test.go` — New — moved from pkg/events; package server; apishim.* qualifiers
- `pkg/shim/runtime/acp/runtime.go` — New — ACP runtime Manager, moved from pkg/runtime; package acp
- `pkg/shim/runtime/acp/client.go` — New — ACP client, moved from pkg/runtime; package acp
- `pkg/shim/runtime/acp/runtime_test.go` — New — moved from pkg/runtime; package acp_test
- `pkg/shim/runtime/acp/client_test.go` — New — moved from pkg/runtime; package acp
- `cmd/agentd/subcommands/shim/command.go` — Updated pkg/runtime → acpruntime, pkg/events → shimserver
- `pkg/shim/client/client.go` — Updated events.ShimEvent → apishim.ShimEvent
- `pkg/ari/server/server_test.go` — Updated events import → apishim
- `pkg/agentd/process.go` — Updated events.* → apishim.*
- `pkg/agentd/recovery.go` — Updated events.ShimEvent → apishim.ShimEvent
- `pkg/agentd/mock_shim_server_test.go` — Updated events.* → apishim.*
- `pkg/agentd/shim_boundary_test.go` — Updated events.ShimEvent → apishim.ShimEvent
- `pkg/agentd/process_test.go` — Updated events.* → apishim.*
- `cmd/agentdctl/subcommands/shim/command.go` — Updated events.EventType* → shimapi.*
- `cmd/agentdctl/subcommands/shim/chat.go` — Updated events.EventType* → shimapi.*
