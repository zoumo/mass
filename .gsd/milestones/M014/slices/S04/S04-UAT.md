# S04: Translator eventCounts — UAT

**Milestone:** M014
**Written:** 2026-04-14T15:42:47.198Z

## UAT: Translator eventCounts

### Preconditions
- Go toolchain available
- Working directory: `/Users/jim/code/zoumo/open-agent-runtime`

### Test 1: Per-type counts after a prompt turn
1. Run `go test ./pkg/shim/server/... -run TestEventCounts_PromptTurn -v`
2. **Expected:** Test PASS. EventCounts() returns turn_start:1, user_message:1, text:2, tool_call:1, turn_end:1, state_change:1.

### Test 2: Fail-closed — dropped events not counted
1. Run `go test ./pkg/shim/server/... -run TestEventCounts_FailClosedOnAppendFailure -v`
2. **Expected:** Test PASS. After closing the EventLog file, a subsequent AgentMessageChunk is dropped. EventCounts()["text"] stays at 1 (not incremented to 2). An slog.Error line appears for the failed append.

### Test 3: Clean build
1. Run `go build ./pkg/shim/...`
2. **Expected:** Exit 0, no output.

### Test 4: Full shim/server test suite regression
1. Run `go test ./pkg/shim/server/... -count=1`
2. **Expected:** All tests PASS, no failures.

### Edge Cases Verified by Tests
- **Thread safety:** EventCounts() acquires the mutex and returns a map copy — concurrent callers cannot observe partial writes.
- **Zero baseline:** Before any events, EventCounts() returns an empty map (no pre-populated keys).
- **Single counting site:** Only broadcast() increments eventCounts — no translate(), broadcastSessionEvent(), or Notify* method counts independently.
