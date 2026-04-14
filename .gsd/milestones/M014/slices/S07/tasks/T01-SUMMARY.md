---
id: T01
parent: S07
milestone: M014
key_files:
  - pkg/shim/server/service.go
  - pkg/shim/server/service_test.go
key_decisions:
  - Overlay EventCounts via direct assignment rather than merging — full replacement is correct because Translator holds the authoritative cumulative counts
duration: 
verification_result: passed
completed_at: 2026-04-14T17:09:28.856Z
blocker_discovered: false
---

# T01: Status() now overlays Translator's real-time in-memory EventCounts onto the state.json snapshot, with test proving stale file counts are replaced

**Status() now overlays Translator's real-time in-memory EventCounts onto the state.json snapshot, with test proving stale file counts are replaced**

## What Happened

The `Service.Status()` method in `pkg/shim/server/service.go` previously returned the `EventCounts` exactly as read from `state.json` via `mgr.GetState()`. Because EventCounts is only flushed to disk piggy-backed on lifecycle transitions and metadata updates, the file value was stale between writes.

Added a single-line overlay after the `GetState()` call: `st.EventCounts = s.trans.EventCounts()`. This replaces the disk-read EventCounts with the authoritative real-time counts held in Translator memory.

Created `pkg/shim/server/service_test.go` with `TestStatus_EventCountsOverlay` that:
1. Writes a `state.json` with stale EventCounts (`{"stale_event": 99}`).
2. Creates a Manager pointing at that temp state dir.
3. Creates a Translator and broadcasts 2 `state_change` events to build different in-memory counts.
4. Calls `svc.Status()` and asserts the returned EventCounts matches Translator memory (2 state_change events), NOT the stale file value.
5. Also asserts the stale key (`stale_event`) is absent from the response.

## Verification

1. `go test ./pkg/shim/server/... -run TestStatus -v -count=1` — TestStatus_EventCountsOverlay PASS, confirms overlay replaces stale file counts with Translator memory.
2. `make build` — agentd and agentdctl compile cleanly.
3. `go test ./pkg/shim/server/... -count=1` — all package tests pass, no regressions.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go test ./pkg/shim/server/... -run TestStatus -v -count=1` | 0 | ✅ pass | 1218ms |
| 2 | `make build` | 0 | ✅ pass | 5000ms |
| 3 | `go test ./pkg/shim/server/... -count=1` | 0 | ✅ pass | 1188ms |

## Deviations

None.

## Known Issues

None.

## Files Created/Modified

- `pkg/shim/server/service.go`
- `pkg/shim/server/service_test.go`
