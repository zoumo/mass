---
id: T01
parent: S04
milestone: M013
key_files:
  - pkg/shim/api/shim_event.go
  - pkg/shim/api/event_types.go
  - pkg/shim/api/event_constants.go
  - pkg/shim/api/types.go
  - pkg/shim/client/client.go
  - pkg/ari/server/server_test.go
  - pkg/agentd/process.go
  - pkg/agentd/recovery.go
  - pkg/agentd/mock_shim_server_test.go
  - pkg/agentd/shim_boundary_test.go
  - pkg/agentd/process_test.go
  - cmd/agentdctl/subcommands/shim/command.go
  - cmd/agentdctl/subcommands/shim/chat.go
  - pkg/shim/server/service.go
key_decisions:
  - Placed all event wire types (ShimEvent, Event interface, typed events, EventType*/Category* constants) into pkg/shim/api as three new files mirroring pkg/events/ — this makes pkg/shim/api self-contained and eliminates the cross-package events dependency from all ARI/agentd consumers.
  - Added legacyEventsToAPI JSON-round-trip bridge in service.go as a minimal T01→T02 compatibility shim rather than leaving a build-broken intermediate state.
duration: 
verification_result: passed
completed_at: 2026-04-14T11:12:04.543Z
blocker_discovered: false
---

# T01: Copied ShimEvent, typed event structs, and EventType*/Category* constants into pkg/shim/api; removed pkg/events import from types.go; updated all 10 consumer files to reference apishim instead of events

**Copied ShimEvent, typed event structs, and EventType*/Category* constants into pkg/shim/api; removed pkg/events import from types.go; updated all 10 consumer files to reference apishim instead of events**

## What Happened

Created three new files in pkg/shim/api/ by copying from pkg/events/ with only the package declaration changed (events → api): shim_event.go, event_types.go, event_constants.go. Removed the pkg/events import from pkg/shim/api/types.go and changed the two []events.ShimEvent fields to bare []ShimEvent. Updated all 10 external consumer files: pkg/shim/client/client.go (ParseShimEvent return type), pkg/ari/server/server_test.go (added apishim import, removed events import), pkg/agentd/process.go and recovery.go (events import dropped, all events.ShimEvent/CategoryRuntime/EventTypeStateChange/StateChangeEvent → apishim.*), and the four agentd test files (mock_shim_server_test.go, shim_boundary_test.go, process_test.go — events import removed, types updated to use their existing apishim/shim aliases), plus cmd/agentdctl/subcommands/shim/command.go and chat.go (events import dropped, EventType* constants → shimapi.*). One unplanned file was also fixed: pkg/shim/server/service.go, which the task plan omitted but is pulled in by make build via cmd/agentd. service.go still uses events.Translator and events.ReadEventLog (T02 concern), but those return []events.ShimEvent which is now incompatible with []apishim.ShimEvent. Added a legacyEventsToAPI JSON-round-trip bridge function and imported encoding/json — clearly commented as temporary until T02 moves translator+log into pkg/shim/server natively.

## Verification

Ran: go build ./pkg/shim/api/... (✅), go build ./pkg/agentd/... (✅), go build ./pkg/ari/server/... (✅), go build ./cmd/... (✅), make build (✅ both agentd and agentdctl binaries). go vet ./pkg/shim/api/... ./pkg/agentd/... ./pkg/ari/server/... ./cmd/... (✅ zero warnings). go test ./pkg/shim/api/... ./pkg/agentd/... ./pkg/ari/server/... ./pkg/shim/client/... (✅ pkg/agentd 7.0s, pkg/ari/server 3.7s).

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go build ./pkg/shim/api/... && go build ./pkg/agentd/... && go build ./pkg/ari/server/... && go build ./cmd/...` | 0 | ✅ pass | 4200ms |
| 2 | `make build` | 0 | ✅ pass | 3100ms |
| 3 | `go vet ./pkg/shim/api/... ./pkg/agentd/... ./pkg/ari/server/... ./cmd/...` | 0 | ✅ pass | 900ms |
| 4 | `go test ./pkg/shim/api/... ./pkg/agentd/... ./pkg/ari/server/... ./pkg/shim/client/...` | 0 | ✅ pass | 8200ms |

## Deviations

pkg/shim/server/service.go was not in the T01 task plan but required fixing to make `go build ./cmd/...` and `make build` pass. Added legacyEventsToAPI JSON-round-trip helper and encoding/json import. This is a temporary bridge explicitly commented for removal in T02.

## Known Issues

pkg/shim/server/service.go still imports pkg/events (for Translator + ReadEventLog) and uses events.ShimEvent internally — this is by design; T02 will move translator.go and log.go into pkg/shim/server, eliminating the bridge function.

## Files Created/Modified

- `pkg/shim/api/shim_event.go`
- `pkg/shim/api/event_types.go`
- `pkg/shim/api/event_constants.go`
- `pkg/shim/api/types.go`
- `pkg/shim/client/client.go`
- `pkg/ari/server/server_test.go`
- `pkg/agentd/process.go`
- `pkg/agentd/recovery.go`
- `pkg/agentd/mock_shim_server_test.go`
- `pkg/agentd/shim_boundary_test.go`
- `pkg/agentd/process_test.go`
- `cmd/agentdctl/subcommands/shim/command.go`
- `cmd/agentdctl/subcommands/shim/chat.go`
- `pkg/shim/server/service.go`
