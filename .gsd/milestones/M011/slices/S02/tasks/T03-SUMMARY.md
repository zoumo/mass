---
id: T03
parent: S02
milestone: M011
key_files:
  - (none)
key_decisions:
  - (none)
duration: 
verification_result: passed
completed_at: 2026-04-12T14:42:03.438Z
blocker_discovered: false
---

# T03: Added 15 JSON shape alignment tests + union error cases; all 22 test matrix items covered

**Added 15 JSON shape alignment tests + union error cases; all 22 test matrix items covered**

## What Happened

Created pkg/events/wire_shape_test.go with 15 tests covering test matrix items 16-22: ContentBlock 5 variants shape alignment (text without Meta to account for ACP SDK's deliberate _meta strip in union MarshalJSON), ToolCallContent 3 variants, AvailableCommandInput unstructured flat shape (no type field), ConfigOption select ungrouped and grouped, EmbeddedResource text/blob (no type field, field-presence discriminated), union empty-variant marshal errors (5 unions), union unknown-type unmarshal errors. Added a code comment on the ContentBlock text test documenting the known ACP SDK behavior (selective nm map strips _meta) and why Meta-free inputs are used for structural key layout tests.

## Verification

go test ./pkg/events/... -run TestWireShape passes all 15 tests.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go test ./pkg/events/... -run TestWireShape` | 0 | ✅ pass (15 tests) | 1183ms |

## Deviations

None.

## Known Issues

None.

## Files Created/Modified

None.
