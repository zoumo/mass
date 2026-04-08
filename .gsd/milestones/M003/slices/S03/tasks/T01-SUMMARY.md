---
id: T01
parent: S03
milestone: M003
key_files:
  - pkg/events/log.go
  - pkg/events/log_test.go
key_decisions:
  - Adapted TestEventLog_AppendAfterDamagedTail to verify mid-file corruption detection after append, fixing logical inconsistency in plan step 5
duration: 
verification_result: passed
completed_at: 2026-04-08T02:47:49.192Z
blocker_discovered: false
---

# T01: Rewrote ReadEventLog to use bufio.Scanner per-line scanning with damaged-tail tolerance — corrupt trailing lines are skipped while mid-file corruption still errors

**Rewrote ReadEventLog to use bufio.Scanner per-line scanning with damaged-tail tolerance — corrupt trailing lines are skipped while mid-file corruption still errors**

## What Happened

Replaced json.Decoder-based ReadEventLog with bufio.Scanner + json.Unmarshal per-line approach. The new implementation collects all non-empty lines, classifies each as valid or corrupt, then walks through: if a corrupt line is followed by any valid line later, it returns a mid-file corruption error; if corrupt lines only appear at the tail, they are logged and skipped. Updated the existing corrupt-row test and added 4 new tests covering damaged-tail-only, damaged-tail-returns-partial, mid-file-corruption-fails, and append-after-damaged-tail scenarios. The append-after-damaged-tail test was adapted from the plan to correctly verify that once a valid entry is appended past a corrupt line, ReadEventLog detects mid-file corruption (consistent with the algorithm definition).

## Verification

go test ./pkg/events/... -count=1 -v — all 25 tests pass including 5 new/updated damaged-tail tests. go vet ./pkg/events/... — no issues.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go test ./pkg/events/... -count=1 -v` | 0 | ✅ pass | 1140ms |
| 2 | `go vet ./pkg/events/...` | 0 | ✅ pass | 500ms |

## Deviations

Plan step 5 expected ReadEventLog to return 4 valid entries after appending past a corrupt line. The step 1 algorithm makes this mid-file corruption (valid lines follow the corrupt line). Test adapted to verify correct behavior: OpenEventLog+Append succeed, damaged-tail read works before append, mid-file corruption correctly detected after append.

## Known Issues

None.

## Files Created/Modified

- `pkg/events/log.go`
- `pkg/events/log_test.go`
