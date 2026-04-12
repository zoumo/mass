---
id: T01
parent: S02
milestone: M011
key_files:
  - (none)
key_decisions:
  - (none)
duration: 
verification_result: passed
completed_at: 2026-04-12T14:33:04.868Z
blocker_discovered: false
---

# T01: Fixed 6 broken existing tests — Content field assertions updated, IgnoredVariants replaced with PreviouslyIgnoredVariants

**Fixed 6 broken existing tests — Content field assertions updated, IgnoredVariants replaced with PreviouslyIgnoredVariants**

## What Happened

Fixed TestTranslate_AgentMessageChunk, TestTranslate_AgentThoughtChunk, TestTranslate_UserMessageChunk, TestFanOut_ThreeSubscribers to use field-by-field assertions instead of full struct equality — Content *ContentBlock is now non-nil after translation. Fixed TestEventLog_TranslatorWritesCanonicalEnvelope similarly. Renamed TestTranslate_IgnoredVariants to TestTranslate_PreviouslyIgnoredVariants and updated it to assert all 3 events are produced (AvailableCommandsEvent + CurrentModeEvent + TextEvent) since these branches no longer return nil.

## Verification

go test ./pkg/events/... passes with no failures.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go test ./pkg/events/...` | 0 | ✅ pass | 2315ms |

## Deviations

None.

## Known Issues

None.

## Files Created/Modified

None.
