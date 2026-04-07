---
estimated_steps: 4
estimated_files: 8
skills_used: []
---

# T01: Land replayable notification envelopes and the clean-break shim server surface

**Slice:** S02 — shim-rpc clean break
**Milestone:** M002

## Description

Start at the irreversible seam: make live notifications, replay history, and runtime status share one truthful protocol surface before any downstream caller is migrated. Keep the existing bootstrap boundary intact — `mgr.Create()` still happens before the externally visible event stream starts — while adding the clean-break method names and recovery metadata the later slices depend on.

## Failure Modes

| Dependency | On error | On timeout | On malformed response |
|------------|----------|-----------|----------------------|
| `pkg/runtime.Manager` state plus prompt lifecycle | Reply with JSON-RPC internal error and keep the socket alive for later inspection. | Abort the call, leave state inspection available through `runtime/status`, and avoid partial envelopes. | Convert to typed error handling or a failing test; never panic or emit a half-shaped notification. |
| `pkg/events.EventLog` history reads and writes | Surface an internal error for replay and status calls and keep the live subscription path isolated. | Do not block the live event path on slow history access. | Reject or fail the decode path explicitly instead of silently replaying corrupted rows. |

## Load Profile

- **Shared resources**: subscriber channels, the JSONL event log, and the persisted runtime state file.
- **Per-operation cost**: one envelope translation and log append per live event; replay is proportional to history size from `fromSeq` onward.
- **10x breakpoint**: slow subscribers or unbounded history reads would back up or over-read the log first, so the task must preserve non-blocking fan-out and explicit `afterSeq` and `fromSeq` semantics.

## Negative Tests

- **Malformed inputs**: missing or negative `afterSeq` and `fromSeq`, missing prompt text, and unknown legacy method names.
- **Error paths**: unreadable or corrupt event-log rows, runtime manager failures during prompt, cancel, or stop, and stop while a client disconnects mid-stream.
- **Boundary conditions**: empty history, subscribe from the latest sequence, replay from sequence zero, and runtime-state changes that must not include bootstrap traffic.

## Steps

1. Introduce a canonical replayable notification envelope in `pkg/events` and `pkg/rpc` so the live subscription path and `runtime/history` use the same method-plus-params shape with monotonic sequence metadata.
2. Rename server dispatch to `session/prompt`, `session/cancel`, `session/subscribe`, `runtime/status`, `runtime/history`, and `runtime/stop`, add `afterSeq` replay semantics, and make legacy PascalCase and `$/event` calls fail fast.
3. Add `runtime/status.recovery.lastSeq` plus a real `runtime/stateChange` emission path tied to runtime lifecycle transitions, while preserving the current post-`Create()` bootstrap visibility boundary.
4. Extend `pkg/rpc/server_test.go`, `pkg/events/log_test.go`, and `pkg/events/translator_test.go` with assertions for renamed methods, legacy rejection, replay envelope shape, `afterSeq` filtering, state-change delivery, and stop reply-before-disconnect behavior.

## Must-Haves

- [ ] `runtime/history` replays the same envelope shape that live subscribers receive.
- [ ] `session/subscribe(afterSeq)` only emits events with `seq > afterSeq`.
- [ ] `runtime/status` returns nested state plus `recovery.lastSeq`.
- [ ] No bootstrap ACP chatter becomes externally visible just because the event plumbing changed.

## Verification

- `go test ./pkg/events ./pkg/rpc -count=1`
- `go test ./pkg/rpc -run 'TestRPCServer' -count=1` or equivalent focused server assertions cover legacy rejection, replay, status, and stop semantics.

## Observability Impact

- Signals added and changed: canonical replayable notification envelopes with sequence and timestamp metadata, runtime/stateChange, and runtime/status.recovery.lastSeq
- How a future agent inspects this: runtime/history, runtime/status, and go test ./pkg/events ./pkg/rpc -count=1
- Failure state exposed: malformed params and unreadable history return typed JSON-RPC errors instead of silent coercion

## Inputs

- `docs/design/runtime/shim-rpc-spec.md` — authoritative clean-break protocol contract
- `docs/design/runtime/agent-shim.md` — shim ownership and bootstrap boundary notes
- `pkg/events/log.go` — current raw JSONL history shape
- `pkg/events/translator.go` — current typed-event fan-out and old turn helpers
- `pkg/rpc/server.go` — legacy PascalCase methods and `$/event` notifications
- `pkg/runtime/runtime.go` — runtime state transitions that need `runtime/stateChange` coverage
- `cmd/agent-shim/main.go` — bootstrap ordering that must remain non-external

## Expected Output

- `pkg/events/log.go` — canonical replayable history-envelope storage and read path
- `pkg/events/translator.go` — state-change-capable translation and broadcast path
- `pkg/events/log_test.go` — history-envelope assertions
- `pkg/events/translator_test.go` — state-change and replay helper assertions
- `pkg/rpc/server.go` — renamed clean-break shim RPC surface
- `pkg/rpc/server_test.go` — protocol and legacy-rejection tests
- `pkg/runtime/runtime.go` — runtime lifecycle hooks needed for truthful state-change emission
- `cmd/agent-shim/main.go` — preserved bootstrap ordering around the new event and state plumbing
