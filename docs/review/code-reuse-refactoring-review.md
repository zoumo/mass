# Review: code-reuse-refactoring.md

Reviewed proposal: `docs/proposal/code-reuse-refactoring.md`

Last updated: 2026-04-26

## Summary

The proposal now incorporates the first review pass for the RPC helpers and
fan-out abstraction. Two previous findings are resolved. Two issues remain for
the current version:

| Severity | Count |
|----------|-------|
| P1 | 0 |
| P2 | 2 |

## Current Findings

### [P2] reserveAndConnect still changes connect-failure state semantics

**File:** `docs/proposal/code-reuse-refactoring.md` lines 107-130

**Issue:** The revised helper says connect failure will "automatically roll back
to idle", while also saying callers remain responsible for
`recordPromptDeliveryFailure` behavior. In the current implementation, connect
failure is handled by `recordPromptDeliveryFailure(..., true)` for
`workspace/send`, `agentrun/prompt`, and task dispatch. When runtime status is
unavailable, that path marks the agent `error`, not `idle`.

**Why it matters:** If the refactor rolls back to `idle` on connect failure,
it can make a non-running or broken reserved agent immediately look available
again. That differs from the current failure path and can cause repeated
dispatch attempts instead of surfacing the runtime failure state.

**Suggestion:** Do not bake "rollback to idle on connect failure" into
`reserveAndConnect` unless that behavior is intentionally changing. Prefer one
of these contracts:

- `reserveIdleAgent` only performs recovery/get/idle/CAS and returns the
  reserved agent; callers keep the existing `Connect` and
  `recordPromptDeliveryFailure` logic.
- `reserveAndConnect` accepts a typed failure policy/callback that preserves
  each caller's current `recordPromptDeliveryFailure` behavior and RPC message.

### [P2] updateAgentRun example references undefined ErrInvalidInput

**File:** `docs/proposal/code-reuse-refactoring.md` lines 150-160

**Issue:** The proposed `updateAgentRun` helper returns `ErrInvalidInput`, but
`pkg/agentd/store` does not currently define that symbol. Existing store methods
return contextual errors such as `store: workspace is required` and
`store: agent name is required`.

**Why it matters:** Implementing the proposal literally will not compile, and
adding a new generic error would also weaken existing error messages unless the
callers wrap it carefully.

**Suggestion:** Keep the current validation messages in the helper, for example:

```go
if workspace == "" {
    return fmt.Errorf("store: workspace is required")
}
if name == "" {
    return fmt.Errorf("store: agent name is required")
}
```

## Resolved Findings

### [Resolved] Helper signatures did not cover error-only RPCs

The proposal now adds `UnaryCommand` and `NullaryCommand`, covering handlers
that return only `error`.

### [Resolved] Generic Fanout erased incompatible delivery semantics

The proposal now downgrades fan-out unification to a design-first task and
explicitly documents the different delivery policies for `WatchServer`,
`Translator`, and `jsonrpc.Client`.
