---
estimated_steps: 17
estimated_files: 13
skills_used: []
---

# T01: Add events wire types to pkg/shim/api/ and update all external consumers

This task is purely additive + consumer-rewrite for the wire-type layer. It does NOT delete pkg/events/ yet.

**Why this order**: pkg/shim/api/types.go already imports pkg/events for events.ShimEvent. The cleanest fix is to move ShimEvent (and all event-typed structs and constants) into pkg/shim/api itself — making types.go self-contained. External consumers of events.ShimEvent and events.EventType* then point to pkg/shim/api instead. translator.go and log.go are NOT moved here because they depend on pkg/events types being in the same package; once the types are in pkg/shim/api, T02 can move translator+log to pkg/shim/server and change their package declaration safely.

**Steps**:
1. Create `pkg/shim/api/shim_event.go` — copy content from `pkg/events/shim_event.go`, change `package events` → `package api`. All types (ShimEvent, PhaseForEvent) land in the api package.
2. Create `pkg/shim/api/event_types.go` — copy content from `pkg/events/types.go`, change `package events` → `package api`. All typed event structs (TextEvent, ToolCallEvent, StateChangeEvent, ContentBlock and helpers, etc.) land in api package.
3. Create `pkg/shim/api/event_constants.go` — copy content from `pkg/events/constants.go`, change `package events` → `package api`. EventType* and Category* constants land in api package.
4. Edit `pkg/shim/api/types.go` — remove the `"github.com/zoumo/oar/pkg/events"` import; all `events.ShimEvent` references become bare `ShimEvent` (same package).
5. Edit `pkg/shim/client/client.go` — change import from `"github.com/zoumo/oar/pkg/events"` to `apishim "github.com/zoumo/oar/pkg/shim/api"` (it already imports apishim); update `events.ShimEvent` → `apishim.ShimEvent`.
6. Edit `pkg/ari/server/server_test.go` — change `"github.com/zoumo/oar/pkg/events"` to `apishim "github.com/zoumo/oar/pkg/shim/api"` (already has shimapi import); replace `events.ShimEvent` → `apishim.ShimEvent`.
7. Edit `pkg/agentd/process.go` — change `"github.com/zoumo/oar/pkg/events"` import to `apishim "github.com/zoumo/oar/pkg/shim/api"`; replace all `events.ShimEvent`, `events.CategoryRuntime`, `events.EventTypeStateChange`, `events.StateChangeEvent` → `apishim.*`.
8. Edit `pkg/agentd/recovery.go` — same pattern: events import → apishim; replace events.ShimEvent usage.
9. Edit `pkg/agentd/mock_shim_server_test.go` — events import → apishim; replace events.ShimEvent and events.TextEvent.
10. Edit `pkg/agentd/shim_boundary_test.go` — events import → apishim; replace events.ShimEvent.
11. Edit `pkg/agentd/process_test.go` — events import → apishim; replace events.ShimEvent, events.TextEvent, events.TurnEndEvent.
12. Edit `cmd/agentdctl/subcommands/shim/command.go` — events import → apishim; replace all events.EventType* constants.
13. Edit `cmd/agentdctl/subcommands/shim/chat.go` — events import → apishim; replace all events.EventType* constants.
14. Run `go build ./pkg/shim/api/... ./pkg/agentd/... ./pkg/ari/server/... ./cmd/...` to verify zero errors. Then `make build`.

## Inputs

- ``pkg/events/shim_event.go` — source for ShimEvent struct and PhaseForEvent`
- ``pkg/events/types.go` — source for all typed event structs (TextEvent, ToolCallEvent, ContentBlock, etc.)`
- ``pkg/events/constants.go` — source for EventType* and Category* constants`
- ``pkg/shim/api/types.go` — target; drop pkg/events import, ShimEvent is now same-package`
- ``pkg/shim/client/client.go` — consumer; events.ShimEvent → apishim.ShimEvent`
- ``pkg/ari/server/server_test.go` — consumer; events.ShimEvent → apishim.ShimEvent`
- ``pkg/agentd/process.go` — consumer; events.ShimEvent + EventType* → apishim.*`
- ``pkg/agentd/recovery.go` — consumer; events.ShimEvent → apishim.ShimEvent`
- ``pkg/agentd/mock_shim_server_test.go` — consumer; events.ShimEvent → apishim.ShimEvent`
- ``pkg/agentd/shim_boundary_test.go` — consumer; events.ShimEvent → apishim.ShimEvent`
- ``pkg/agentd/process_test.go` — consumer; events.ShimEvent + TextEvent → apishim.*`
- ``cmd/agentdctl/subcommands/shim/command.go` — consumer; events.EventType* → apishim.EventType*`
- ``cmd/agentdctl/subcommands/shim/chat.go` — consumer; events.EventType* → apishim.EventType*`

## Expected Output

- ``pkg/shim/api/shim_event.go` — new file, package api`
- ``pkg/shim/api/event_types.go` — new file, package api`
- ``pkg/shim/api/event_constants.go` — new file, package api`
- ``pkg/shim/api/types.go` — updated; no pkg/events import`
- ``pkg/shim/client/client.go` — updated; no pkg/events import`
- ``pkg/ari/server/server_test.go` — updated; no pkg/events import`
- ``pkg/agentd/process.go` — updated; no pkg/events import`
- ``pkg/agentd/recovery.go` — updated; no pkg/events import`
- ``pkg/agentd/mock_shim_server_test.go` — updated; no pkg/events import`
- ``pkg/agentd/shim_boundary_test.go` — updated; no pkg/events import`
- ``pkg/agentd/process_test.go` — updated; no pkg/events import`
- ``cmd/agentdctl/subcommands/shim/command.go` — updated; no pkg/events import`
- ``cmd/agentdctl/subcommands/shim/chat.go` — updated; no pkg/events import`

## Verification

go build ./pkg/shim/api/... && go build ./pkg/agentd/... && go build ./pkg/ari/server/... && go build ./cmd/... && make build
