# S03 — Research: Atomic Event Resume and Damaged-Tail Tolerance

**Date:** 2026-04-08
**Depth:** Targeted — known technology (Go, JSONL, JSON-RPC), clear scope from M003 research, medium complexity.

## Summary

S03 has two deliverables: (1) make `events.jsonl` tolerant of damaged-tail writes, and (2) replace the current `History(fromSeq) → Subscribe(afterSeq)` two-step with an atomic catch-up-to-live mechanism that eliminates the event gap between the two calls. Both problems are well-scoped and the fix points are clear. The existing test infrastructure (mock shim server, `events` package unit tests, integration restart test) provides a solid foundation. No new libraries or unfamiliar technology is needed.

## Requirements Targeted

- **R035** (validated, but M003 strengthens) — "Runtime event recovery must offer a single resume path that closes the current gap between history replay and live subscription." R035 was validated at M002 proof level; this slice advances it beyond baseline to handle damaged logs and atomicity.
- **R044** (active) — "Additional restart, replay, cleanup, and cross-client hardening." S03 delivers the replay/event hardening component of R044.

## Recommendation

Two tasks, cleanly separable:

1. **Damaged-tail tolerance in `pkg/events/log.go`** — fix both `ReadEventLog` and `countLines`/`OpenEventLog` to skip corrupt tail lines instead of failing.
2. **Atomic subscribe-from-seq on the shim RPC surface** — add a `SubscribeFromSeq` method (or extend `session/subscribe`) that atomically reads history from the JSONL log starting at a given seq AND registers a live subscription, returning both the backfill entries and the subscription channel, all under a single lock hold in the Translator. Then update `handleSubscribe` in `pkg/rpc/server.go` and `recoverSession` in `pkg/agentd/recovery.go` to use the new atomic path.

## Implementation Landscape

### Problem 1: Damaged-Tail Tolerance

**Current behavior:**

- `countLines(path)` (used by `OpenEventLog`) scans all lines with `bufio.Scanner` and counts non-empty ones. It does NOT decode JSON, so a truncated JSONL line that is valid text but invalid JSON does not fail here. However, a partial write that leaves a truncated final line could cause `scanner.Err()` to return an error if the buffer overflows, though the 1MB buffer makes this unlikely in practice.
- `ReadEventLog(path, fromSeq)` decodes every line with `json.Decoder`. If ANY line fails to decode, the entire read fails with `"events: decode log entry"`. This means a single corrupt tail line (e.g., partial write after crash) makes all history unreadable.
- `OpenEventLog(path)` calls `countLines` to set `nextSeq`. If `countLines` fails, the log cannot be opened for append. In practice, `countLines` only fails on I/O errors, not JSON errors, so the main risk is `ReadEventLog`.
- The `Translator.broadcastEnvelope` writes to the log with `_ = log.Append(env)` — the error is silently ignored. So a damaged tail is most likely from a process crash mid-write, not from an explicit Append error.

**Fix approach:**

In `ReadEventLog`: when `json.Decoder.Decode` fails, check if there is more data after (i.e., try to read the next line). If we're at the end of the file (no more valid lines after the corrupt one), treat it as a damaged tail — log the skip and return the successfully decoded entries. If corrupt lines appear in the middle of the file (valid lines follow), that's a different and more serious problem; for this milestone, the scope is damaged-tail only.

Concretely:
- Switch `ReadEventLog` from `json.Decoder` to line-by-line reading with `bufio.Scanner` + `json.Unmarshal` per line. This gives control over individual line failures.
- When a line fails to unmarshal, peek ahead. If no more valid lines follow, it's tail damage — break and return what we have. If valid lines follow, it's mid-file corruption — return an error (or skip with a warning, but damaged-tail scope says we don't need to handle this).
- Keep `countLines` as-is (it already doesn't decode JSON). But add a note/test that `OpenEventLog` initializes `nextSeq` from line count, so if the damaged tail has a valid-looking but un-decodable line, `nextSeq` will be higher than the last successfully decodable entry's seq. The `Append` method validates seq numbers, so the next append will use the line-count-based seq, which is correct — it just means the damaged line is conceptually a "slot" that was lost.

**Alternative considered:** Truncate the file to remove the damaged tail on `OpenEventLog`. This is destructive and not necessary — better to tolerate the damage in reads and let the append path skip over it naturally via line-count-based nextSeq.

### Problem 2: Atomic Subscribe-From-Seq (History + Subscribe Gap)

**Current gap analysis:**

The daemon's recovery flow (`recoverSession`) does:
1. `client.Status(ctx)` → gets `recovery.lastSeq` (the Translator's last assigned seq)
2. `client.History(ctx, &fromSeq)` → reads events.jsonl from disk via `ReadEventLog`
3. `client.Subscribe(ctx, &lastSeq)` → registers for live notifications with `afterSeq=lastSeq`

Events emitted by the Translator between steps 2 and 3 are missed:
- History reads from the **file** as of that moment.
- Subscribe registers with the **Translator** from that moment.
- Events emitted between those two moments are in neither result.
- The `afterSeq` parameter from step 1 is stale by the time step 3 runs.

Similarly, the RPC server's `handleSubscribe` and `handleHistory` are independent calls with no coordination.

**Fix approach — server-side atomic subscribe-from-seq:**

Add a new method to `events.Translator` that atomically:
1. Acquires the Translator's lock
2. Reads the JSONL log from a given `fromSeq` (using `ReadEventLog` or equivalent)
3. Registers a subscription channel
4. Records `nextSeq` at subscription time
5. Releases the lock
6. Returns (backfillEntries, subscriptionCh, subID, nextSeq)

Because the log write and the subscription registration both happen under the same lock (`Translator.mu`), no events can be emitted between the history read and the subscription start. The lock ordering is:
- `broadcastEnvelope` acquires `mu`, assigns seq, increments nextSeq, copies subs list, releases mu, then writes to log and sends to subs.
- The new `SubscribeFromSeq` acquires `mu`, reads log (file I/O under lock — acceptable because reads are fast and this is startup-only), registers sub, releases mu.

Since `broadcastEnvelope` holds the lock while incrementing seq and collecting subs, and `SubscribeFromSeq` holds the lock while reading + registering, there is no gap.

**Concern — file I/O under lock:** Reading the JSONL file under the Translator's mutex could block event broadcasting. For a recovery path that runs at startup, this is acceptable. The file is local, and history reads are fast (sub-millisecond for typical log sizes). The alternative (a more complex lock-free approach) is over-engineering for this use case.

**RPC surface changes:**

Option A: Extend `session/subscribe` to accept an optional `fromSeq` parameter that triggers the atomic path and returns backfill entries in the response.
Option B: Add a new RPC method `session/subscribeFromSeq` that combines history + subscribe.

Option A is cleaner — it extends the existing method and keeps the RPC surface minimal. The response shape changes to include an `entries` field when `fromSeq` is present.

```go
// Extended subscribe params
type SessionSubscribeParams struct {
    AfterSeq *int `json:"afterSeq,omitempty"` // existing: filter live events
    FromSeq  *int `json:"fromSeq,omitempty"`  // new: atomic backfill + subscribe
}

// Extended subscribe result
type SessionSubscribeResult struct {
    NextSeq int               `json:"nextSeq"`
    Entries []events.Envelope `json:"entries,omitempty"` // backfill when fromSeq present
}
```

When `fromSeq` is provided, the handler calls `Translator.SubscribeFromSeq(fromSeq)` and returns the backfill entries plus the nextSeq. The `afterSeq` parameter is ignored when `fromSeq` is present (or could be used as a compatibility fallback).

**Recovery path update:**

In `recoverSession`, replace the three-step `Status → History → Subscribe` with:
1. `client.Status(ctx)` → gets state (still needed for reconciliation)
2. `client.Subscribe(ctx, fromSeq=0)` → atomic backfill + subscribe

This eliminates the History call entirely. The subscribe response includes all events from seq 0 plus a live subscription.

### Key Files

| File | Role | Change Needed |
|------|------|---------------|
| `pkg/events/log.go` | Event log read/write | Fix `ReadEventLog` to tolerate damaged tail |
| `pkg/events/log_test.go` | Event log tests | Add damaged-tail tolerance tests |
| `pkg/events/translator.go` | Event fan-out + log writer | Add `SubscribeFromSeq(logPath, fromSeq)` method |
| `pkg/events/translator_test.go` | Translator tests | Test atomic subscribe-from-seq |
| `pkg/rpc/server.go` | Shim RPC server | Extend `handleSubscribe` to support `fromSeq` parameter |
| `pkg/rpc/server_test.go` | RPC server tests | Test extended subscribe with backfill |
| `pkg/agentd/shim_client.go` | Daemon-side shim client | Update `Subscribe` to pass `fromSeq` and receive entries |
| `pkg/agentd/recovery.go` | Recovery logic | Replace History+Subscribe with atomic Subscribe(fromSeq=0) |
| `pkg/agentd/recovery_test.go` | Recovery tests | Update mock shim + tests for new subscribe behavior |
| `pkg/agentd/shim_client_test.go` | Shim client + mock server tests | Update mock server for extended subscribe |

### Natural Seams

**Task 1: Damaged-tail tolerance** — Entirely within `pkg/events/log.go` and `pkg/events/log_test.go`. No external dependencies. Can be done independently and verified with unit tests.

**Task 2: Atomic subscribe-from-seq** — Spans the Translator, RPC server, ShimClient, recovery logic, and all their tests. This is the larger task. Build order within it:
1. Add `SubscribeFromSeq` to Translator (+ unit test)
2. Extend `handleSubscribe` in RPC server (+ test)
3. Update `ShimClient.Subscribe` and mock server (+ test)
4. Update `recoverSession` to use atomic subscribe (+ test)

### Verification

```bash
# Unit tests — damaged-tail + atomic subscribe
go test ./pkg/events/... -count=1 -v

# Unit tests — recovery with atomic subscribe
go test ./pkg/agentd/... -count=1 -v

# RPC server tests — extended subscribe
go test ./pkg/rpc/... -count=1 -v

# Full vet pass
go vet ./pkg/events/... ./pkg/agentd/... ./pkg/rpc/... ./cmd/agentd/...

# Build check
go build ./cmd/agentd/... ./cmd/agent-shim/... ./pkg/...
```

### Constraints

- `Translator.mu` is the critical coordination point. The atomic subscribe-from-seq approach relies on holding this lock during the file read. This is acceptable for startup/recovery but should not be used in hot paths.
- `ReadEventLog` uses `json.Decoder` today; switching to line-by-line scanning changes the error surface. Need to ensure the new behavior matches for all existing test cases.
- The `countLines` function counts non-empty lines regardless of JSON validity. After a damaged tail, `nextSeq` (from line count) may be higher than the last valid event's seq. This is correct — the damaged line is a lost slot, and the next append continues from the right position.
- The extended `session/subscribe` response shape adds `entries` — this is backward-compatible (existing callers just ignore the new field when it's absent).
- The `runtime/history` RPC method becomes redundant once `session/subscribe` supports `fromSeq`. It should be kept for backward compatibility but the recovery path should stop using it.

### Common Pitfalls

- **Fixing `ReadEventLog` but not testing `OpenEventLog` + `Append` after damage** — `OpenEventLog` uses `countLines` to set `nextSeq`. If a damaged tail has N valid lines + 1 corrupt line, `countLines` returns N+1 (it counts non-empty lines, not valid JSON). The next `Append` expects seq=N+1. This is actually correct, but it needs to be tested to prove the seq numbering stays consistent.
- **Holding the Translator lock during file I/O in production hot paths** — The atomic subscribe should only be used during recovery, not for regular subscribers. Regular `Subscribe()` (no fromSeq) should remain lock-free of file I/O.
- **Forgetting to update the mock shim server** — The mock server in `shim_client_test.go` needs to support the extended subscribe params/response for all existing recovery tests to keep working.

### Open Risks

- The Translator currently writes to the log with `_ = log.Append(env)` (best-effort). If the log file becomes unwritable mid-session, the Translator's `nextSeq` continues incrementing even though the log file is stale. `SubscribeFromSeq` would then read fewer entries than expected from disk. Mitigation: this is an existing issue, not introduced by S03, and is acceptable for now.

## Skills Discovered

| Technology | Skill | Status |
|------------|-------|--------|
| Go | none relevant found | not installed |
| JSON-RPC | none relevant found | not installed |

No external skills needed — this is core Go + existing codebase patterns.

## Sources

- `pkg/events/log.go` — current event log implementation showing the damaged-tail vulnerability
- `pkg/events/translator.go` — current Translator with `Subscribe()` and `broadcastEnvelope()` showing the gap
- `pkg/rpc/server.go` — shim RPC server with separate `handleHistory` and `handleSubscribe` handlers
- `pkg/agentd/recovery.go` — daemon recovery path with the three-step History→Subscribe flow
- `pkg/agentd/shim_client_test.go` — mock shim server infrastructure that needs updating
- M003-RESEARCH.md — milestone research identifying the History→Subscribe gap and build order
