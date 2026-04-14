---
id: T02
parent: S04
milestone: M013
key_files:
  - pkg/shim/server/translator.go
  - pkg/shim/server/log.go
  - pkg/shim/server/service.go
  - pkg/shim/server/translator_test.go
  - pkg/shim/server/log_test.go
  - pkg/shim/server/wire_shape_test.go
  - pkg/shim/server/translate_rich_test.go
  - pkg/shim/runtime/acp/runtime.go
  - pkg/shim/runtime/acp/client.go
  - pkg/shim/runtime/acp/runtime_test.go
  - pkg/shim/runtime/acp/client_test.go
  - pkg/shim/api/event_types.go
  - cmd/agentd/subcommands/shim/command.go
key_decisions:
  - Added EventTypeOf(ev Event) string to pkg/shim/api/event_types.go as an exported accessor for the sealed Event interface's unexported eventType() method — necessary because pkg/shim/server cannot call unexported methods across package boundaries.
  - Removed legacyEventsToAPI JSON-round-trip bridge from service.go that was added in T01 as a temporary compatibility shim — now unnecessary since Translator produces apishim.ShimEvent natively.
  - go vet ./... fails on pre-existing third_party/ issue; scoped to ./pkg/... ./cmd/... for clean verification.
duration: 
verification_result: passed
completed_at: 2026-04-14T11:37:04.124Z
blocker_discovered: false
---

# T02: Move translator+log to pkg/shim/server/, runtime to pkg/shim/runtime/acp/, delete pkg/events/ and pkg/runtime/, all verification passes

**Move translator+log to pkg/shim/server/, runtime to pkg/shim/runtime/acp/, delete pkg/events/ and pkg/runtime/, all verification passes**

## What Happened

Created pkg/shim/server/translator.go and pkg/shim/server/log.go by copying from pkg/events/ with package changed to server and all ShimEvent/EventType*/Category* references updated to use apishim.* qualification. One non-obvious complication: the Event interface in pkg/shim/api uses an unexported eventType() method (sealed interface), which cannot be called cross-package. Resolved by adding a minimal EventTypeOf(ev Event) string exported helper to pkg/shim/api/event_types.go — this is the appropriate package-boundary solution. Moved all four test files (translator_test.go, log_test.go, wire_shape_test.go, translate_rich_test.go) to pkg/shim/server/ with package changed to server and all event types qualified via apishim.*. Created pkg/shim/runtime/acp/runtime.go and client.go with package changed from runtime to acp; moved runtime_test.go and client_test.go with package names updated to acp_test and acp respectively. Updated pkg/shim/server/service.go to drop pkg/events and pkg/runtime imports — Translator/EventLog/ReadEventLog are now same-package, runtime.Manager is now acpruntime.Manager; also removed the legacyEventsToAPI JSON-round-trip bridge added in T01 since translator now produces apishim.ShimEvent natively. Updated cmd/agentd/subcommands/shim/command.go to use acpruntime instead of pkg/runtime and shimserver instead of pkg/events. Deleted pkg/events/ and pkg/runtime/ directories. All verification passes: rg searches return exit 1 for both old import paths, make build exits 0, go test ./... all pass (pkg/shim/server 2.8s, pkg/shim/runtime/acp 4.9s, integration tests 105s), go vet ./pkg/... ./cmd/... exits 0 (pre-existing third_party/ vet failure is unrelated to this migration).

## Verification

rg 'zoumo/oar/pkg/events' --type go → exit 1 (zero matches). rg 'zoumo/oar/pkg/runtime"' --type go → exit 1 (zero matches). make build → exit 0 (agentd + agentdctl). go test ./... → all pass including pkg/shim/server and pkg/shim/runtime/acp. go vet ./pkg/... ./cmd/... → exit 0.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `rg 'zoumo/oar/pkg/events' --type go; echo exit:$?` | 1 | ✅ pass | 200ms |
| 2 | `rg 'zoumo/oar/pkg/runtime"' --type go; echo exit:$?` | 1 | ✅ pass | 200ms |
| 3 | `make build` | 0 | ✅ pass | 3200ms |
| 4 | `go test ./...` | 0 | ✅ pass | 107000ms |
| 5 | `go vet ./pkg/... ./cmd/...` | 0 | ✅ pass | 1100ms |

## Deviations

Added EventTypeOf() exported helper to pkg/shim/api/event_types.go — not in the task plan, but required to call the sealed Event interface's unexported eventType() method cross-package. This is a minimal, backward-compatible addition. Also removed the legacyEventsToAPI bridge from service.go that was not explicitly mentioned in the plan but is the correct cleanup now that Translator produces apishim.ShimEvent natively.

## Known Issues

go vet ./... reports a pre-existing lock-copy issue in third_party/charmbracelet/crush/csync/maps.go — unrelated to this migration, present before and after.

## Files Created/Modified

- `pkg/shim/server/translator.go`
- `pkg/shim/server/log.go`
- `pkg/shim/server/service.go`
- `pkg/shim/server/translator_test.go`
- `pkg/shim/server/log_test.go`
- `pkg/shim/server/wire_shape_test.go`
- `pkg/shim/server/translate_rich_test.go`
- `pkg/shim/runtime/acp/runtime.go`
- `pkg/shim/runtime/acp/client.go`
- `pkg/shim/runtime/acp/runtime_test.go`
- `pkg/shim/runtime/acp/client_test.go`
- `pkg/shim/api/event_types.go`
- `cmd/agentd/subcommands/shim/command.go`
