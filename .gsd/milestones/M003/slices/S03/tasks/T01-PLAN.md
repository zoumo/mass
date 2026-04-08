---
estimated_steps: 24
estimated_files: 2
skills_used: []
---

# T01: Add damaged-tail tolerance to ReadEventLog

## Description

Switch `ReadEventLog` from `json.Decoder` (which fails the entire read on any corrupt line) to `bufio.Scanner` + `json.Unmarshal` per line. When a line fails to unmarshal AND no valid lines follow it, treat it as a damaged tail (partial write from a crash) — log the skip and return the successfully decoded entries. If corrupt lines appear in the middle of the file (valid lines follow), return an error as before.

Also verify that `OpenEventLog` + `Append` works correctly after a damaged tail: `countLines` counts the corrupt line as a non-empty line, so `nextSeq` will be one higher than the last valid entry's seq. The next `Append` uses the line-count-based seq, which is correct — the damaged line is a lost slot.

## Steps

1. In `pkg/events/log.go`, rewrite `ReadEventLog` to use `bufio.Scanner` + `json.Unmarshal` per line instead of `json.Decoder`. Use a 1MB buffer (matching `countLines`). For each non-empty line that fails `json.Unmarshal`, peek ahead (continue scanning). If any subsequent line is non-empty AND valid JSON, we have mid-file corruption — return an error. If no valid lines follow, it's tail damage — break and return what we have. Use `log.Printf` (matching existing logging style in the package) to log skipped damaged tail lines.

2. In `pkg/events/log_test.go`, update `TestReadEventLog_CorruptRowFails` — this test writes a single corrupt line. Since that's the only line and no valid lines follow, the new behavior should return an empty slice (not an error). Rename it to `TestReadEventLog_DamagedTailReturnsPartial` and write content with valid entries followed by a corrupt tail.

3. Add `TestReadEventLog_DamagedTailTolerated` — write 3 valid JSONL entries + 1 truncated JSON line at the end. Assert `ReadEventLog` returns the 3 valid entries without error.

4. Add `TestReadEventLog_MidFileCorruptionFails` — write 2 valid entries + 1 corrupt line + 2 more valid entries. Assert `ReadEventLog` returns an error (mid-file corruption is not tolerated).

5. Add `TestEventLog_AppendAfterDamagedTail` — write 3 valid JSONL entries + 1 corrupt tail line to a file. Call `OpenEventLog` (which uses `countLines` — should return 4, setting nextSeq=4). Append a new entry with seq=4. Close and read back all entries. Assert: 3 original + 1 new = 4 valid entries, corrupt line skipped in the read.

## Must-Haves

- [ ] `ReadEventLog` uses line-by-line scanning, not `json.Decoder`
- [ ] Damaged tail (corrupt line(s) at end of file with no valid lines after) returns partial results, not error
- [ ] Mid-file corruption (valid lines after corrupt lines) still returns an error
- [ ] `OpenEventLog` + `Append` works correctly after damaged tail — seq numbering is consistent
- [ ] All existing `log_test.go` tests pass (updated as needed for new behavior)

## Verification

- `go test ./pkg/events/... -count=1 -v` — all tests pass including new damaged-tail tests
- `go vet ./pkg/events/...` — no issues

## Inputs

- `pkg/events/log.go` — current ReadEventLog implementation to modify
- `pkg/events/log_test.go` — existing tests to update and extend

## Expected Output

- `pkg/events/log.go` — ReadEventLog rewritten with line-by-line scanning and damaged-tail tolerance
- `pkg/events/log_test.go` — 3+ new tests for damaged-tail scenarios, updated existing corrupt-row test

## Inputs

- `pkg/events/log.go`
- `pkg/events/log_test.go`

## Expected Output

- `pkg/events/log.go`
- `pkg/events/log_test.go`

## Verification

go test ./pkg/events/... -count=1 -v && go vet ./pkg/events/...
