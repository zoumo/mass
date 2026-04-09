# Code Review: Deep Design/Implementation Consistency Check

**Date**: 2026-04-09
**Status**: Follow-up fixes applied and re-verified
**Scope**: `agent` lifecycle/error semantics, recovery alignment, room teardown semantics, and current design-doc consistency

---

## Result

No significant open issues remain in the reviewed scope.

This follow-up pass re-checked the previously reported lifecycle and contract gaps, then verified the fixes in code, tests, and design docs. The `error` state is now treated as a retained failure state rather than a vague unhealthy marker.

## What Was Fixed

### 1. Prompt/send failure now lands in `error`

- `agent/prompt` now rejects `error` agents up front.
- `room/send` now rejects routing to `error` agents.
- When a turn fails after entering `running`, the agent is now reconciled to `error` instead of being flattened back to `created`.

Relevant code:

- `pkg/ari/server.go`

Relevant tests:

- `TestARIAgentPromptOnError`
- `TestARIAgentPromptStartFailureTransitionsError`
- `TestARIRoomSendErrors`
- `TestARIRoomSendStartFailureTransitionsError`

### 2. `agent/restart` contract now matches implementation intent

- Design docs now explicitly allow restarting agents from `stopped` or `error`.
- This matches the existing implementation and the intended operator recovery path.

Relevant code/docs:

- `docs/design/agentd/agentd.md`
- `docs/design/agentd/ari-spec.md`
- `pkg/ari/server.go`

Relevant tests:

- `TestARIAgentRestartFromError`

### 3. `agent/delete` now cleanly supports errored agents

- Errored agents are now treated as deletable without an extra stop step.
- `room/delete` also treats `error` members as non-active, so teardown no longer requires converting failure into `stopped` first.

Relevant code/docs:

- `pkg/agentd/agent.go`
- `pkg/ari/server.go`
- `docs/design/agentd/agentd.md`
- `docs/design/agentd/ari-spec.md`

Relevant tests:

- `TestAgentDelete_AllowsError`
- `TestARIAgentDeleteAllowsError`

### 4. Stale API comment fixed

- `AgentCreateResult.State` now documents the real async behavior: successful create returns `creating`, not `created`.

Relevant code:

- `pkg/ari/types.go`

## Verification

Targeted verification was run after the fixes:

```bash
go test ./pkg/agentd ./pkg/ari -run 'TestAgentDelete_|TestARIAgent(PromptOnError|PromptStartFailureTransitionsError|DeleteAllowsError|RestartFromError)|TestARIRoomSend(Errors|StartFailureTransitionsError)'
```

Result: passed.

Broader package-level regression was also run:

```bash
go test ./pkg/agentd ./pkg/ari
```

Result: passed.

## Residual Notes

- This review only speaks to the lifecycle/error-contract area covered above.
- It does not claim the entire repository is bug-free.
- If future work reintroduces richer per-agent bootstrap inputs or changes restart/delete policy again, design docs and tests should be updated in the same patch.
