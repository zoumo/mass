---
id: T02
parent: S07
milestone: M014
key_files:
  - docs/design/runtime/shim-rpc-spec.md
  - docs/design/runtime/runtime-spec.md
key_decisions:
  - Per K029, agent-shim.md was intentionally not updated — it is descriptive only and defers to shim-rpc-spec.md and runtime-spec.md for protocol details
duration: 
verification_result: passed
completed_at: 2026-04-14T17:14:10.979Z
blocker_discovered: false
---

# T02: Update design docs to reflect M014 enriched state schema (updatedAt, session, eventCounts, sessionChanged)

**Update design docs to reflect M014 enriched state schema (updatedAt, session, eventCounts, sessionChanged)**

## What Happened

Updated both normative design docs to reflect the M014-enriched state schema:

**shim-rpc-spec.md:**
- Added `updatedAt`, `session` (with `agentInfo` and `capabilities` example), and `eventCounts` to the `runtime/status` response JSON example.
- Added a prose note (in Chinese, matching surrounding text) explaining that `eventCounts` in the `runtime/status` response is overlaid from Translator memory for real-time accuracy and may differ from the value persisted in state.json.
- Added a second `state_change` example showing a metadata-only change with `sessionChanged: ["configOptions"]` where `previousStatus == status`.
- Added a prose note explaining when `sessionChanged` appears and listing all possible values: agentInfo, capabilities, availableCommands, configOptions, sessionInfo, currentMode.

**runtime-spec.md:**
- Added `updatedAt`, `session` (with `agentInfo` example), and `eventCounts` to the State Example JSON.
- Added field descriptions for all three new fields after the "The state MAY include additional properties" line, following the existing bullet-point style.

**Not touched:** `docs/design/runtime/agent-shim.md` — per K029, it is descriptive only and defers to the spec docs.

## Verification

All four grep gates pass; make build succeeds; go test ./... passes all packages.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `grep -q 'eventCounts' docs/design/runtime/shim-rpc-spec.md && grep -q 'eventCounts' docs/design/runtime/runtime-spec.md && grep -q 'sessionChanged' docs/design/runtime/shim-rpc-spec.md && grep -q 'updatedAt' docs/design/runtime/runtime-spec.md` | 0 | ✅ pass | 50ms |
| 2 | `make build` | 0 | ✅ pass | 3000ms |
| 3 | `go test ./...` | 0 | ✅ pass | 110100ms |

## Deviations

None.

## Known Issues

None.

## Files Created/Modified

- `docs/design/runtime/shim-rpc-spec.md`
- `docs/design/runtime/runtime-spec.md`
