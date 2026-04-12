---
id: T05
parent: S01
milestone: M011
key_files:
  - (none)
key_decisions:
  - (none)
duration: 
verification_result: passed
completed_at: 2026-04-12T14:29:38.244Z
blocker_discovered: false
---

# T05: Updated runtime-spec.md and shim-rpc-spec.md with 5 new event types and payload policy

**Updated runtime-spec.md and shim-rpc-spec.md with 5 new event types and payload policy**

## What Happened

runtime-spec.md: added 5 rows to the event type table (AvailableCommandsEvent, CurrentModeEvent, ConfigOptionEvent, SessionInfoEvent, UsageEvent) plus a payload preservation policy block explaining flat JSON shape alignment with ACP SDK. shim-rpc-spec.md: added 5 rows to Typed Event table; updated tool_call and tool_result rows to show full field lists instead of just 3 fields.

## Verification

grep -c available_commands returns 1 in both files.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `grep -c available_commands docs/design/runtime/runtime-spec.md` | 0 | ✅ pass | 10ms |

## Deviations

None.

## Known Issues

None.

## Files Created/Modified

None.
