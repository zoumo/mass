# S01: Core types, translator, and envelope — UAT

**Milestone:** M011
**Written:** 2026-04-12T14:29:54.983Z

# S01 UAT

## Build
- `go build ./...` — passes

## Structural checks
- `api/events.go` has 5 new constants (EventTypeAvailableCommands through EventTypeUsage)
- `translate()` has no `return nil` branches
- `decodeEventPayload()` handles 17 event types
- All union types have MarshalJSON + UnmarshalJSON

## Docs
- `docs/design/runtime/runtime-spec.md` — 5 new event type rows + payload policy note
- `docs/design/runtime/shim-rpc-spec.md` — 5 new Typed Event rows + full tool_call/tool_result payloads
