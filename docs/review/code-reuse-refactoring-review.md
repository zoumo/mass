# Review: code-reuse-refactoring.md

Reviewed proposal: `docs/proposal/code-reuse-refactoring.md`

## Summary

Found 3 substantive issues. The highest-risk item is the proposed generic
`Fanout[T]`, because the current implementations have different delivery and
ordering semantics that the proposed API does not model.

| Severity | Count |
|----------|-------|
| P1 | 1 |
| P2 | 2 |

## Findings

### [P1] Generic Fanout erases incompatible delivery semantics

**File:** `docs/proposal/code-reuse-refactoring.md` lines 153-169

**Issue:** The three implementations are not equivalent enough for the proposed
`Fanout[T]` API. `WatchServer` uses unbuffered blocking sends plus `publishMu`
to preserve global order; `Translator` logs and assigns seq under the same lock
before nonblocking eviction; `jsonrpc.Client` filters by method and drops slow
subscriber messages without eviction so fallback routing remains correct.

**Why it matters:** A shared fanout abstraction that does not make these
semantics explicit can silently change watch ordering, recovery behavior, or
notification delivery.

**Suggestion:** Treat Wave 2.1 as a design task before implementation. Either
split the abstractions by delivery policy or make ordering, filtering, drop vs
evict, and log-before-fanout behavior explicit in the API.

### [P2] Helper signatures do not cover error-only RPCs

**File:** `docs/proposal/code-reuse-refactoring.md` lines 42-56

**Issue:** The proposed `UnaryMethod` and `NullaryMethod` only accept handlers
returning `(*Res, error)`, but several methods called out in the section return
only `error`, including `Load`, `Cancel`, `Stop`, and ARI delete/cancel/stop
methods.

**Why it matters:** As written, the advertised replacement cannot compile for a
meaningful part of the duplication the proposal intends to remove.

**Suggestion:** Add command-style helpers such as `UnaryCommand` and
`NullaryCommand`, or define the helper layer around explicit `any` results so
error-only handlers are supported intentionally.

### [P2] reserveAndConnect contract omits failure semantics

**File:** `docs/proposal/code-reuse-refactoring.md` lines 85-97

**Issue:** The extraction bundles `Connect` into the reservation helper but does
not specify how operation-specific failure handling is preserved. Current
callers record prompt delivery failures with different messages and targets:
`workspace/send` uses `To`, prompt uses `Name`, and task dispatch includes the
operation.

**Why it matters:** These calls determine whether the agent is marked error
after a reserved `idle -> running` transition. A vague helper contract could
leave agents stuck, make race behavior harder to diagnose, or normalize the
wrong RPC errors.

**Suggestion:** Define the returned failure contract or callback hooks before
implementation. Keep operation-specific logging, error messages, target names,
and `recordPromptDeliveryFailure` behavior visible at the call site or encoded
in a typed option struct.
