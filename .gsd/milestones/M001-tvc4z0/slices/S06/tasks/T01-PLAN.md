---
estimated_steps: 10
estimated_files: 1
skills_used: []
---

# T01: Add session params/results types to pkg/ari/types.go

Define all session/* method params and results structs following existing workspace types pattern. Each method needs a Params struct (request) and a Result struct (response). Types are pure data structures with JSON tags, no business logic.

## Steps

1. Open `pkg/ari/types.go` and add comment block for session types section
2. Define SessionNewParams struct: workspaceId (required), runtimeClass (required), labels (optional map), room (optional string), roomAgent (optional string)
3. Define SessionNewResult struct: sessionId (string), state (string, always "created")
4. Define SessionPromptParams struct: sessionId (required), text (required)
5. Define SessionPromptResult struct: stopReason (string: "end_turn", "cancelled", "tool_use")
6. Define SessionCancelParams struct: sessionId (required)
7. Define SessionStopParams struct: sessionId (required)
8. Define SessionRemoveParams struct: sessionId (required)
9. Define SessionListParams struct: labels (optional map for filtering)
10. Define SessionListResult struct: sessions array of SessionInfo structs
11. Define SessionInfo struct: id, workspaceId, runtimeClass, state, room, roomAgent, labels, createdAt, updatedAt (matches meta.Session fields)
12. Define SessionStatusParams struct: sessionId (required)
13. Define SessionStatusResult struct: session (SessionInfo), shimState (optional, only if running)
14. Define ShimStateInfo struct: status, pid, bundle, exitCode (matches spec.State fields)
15. Define SessionAttachParams struct: sessionId (required)
16. Define SessionAttachResult struct: socketPath (string, shim RPC socket path)
17. Define SessionDetachParams struct: sessionId (required)
18. Run `go build ./pkg/ari/...` to verify no compile errors

## Must-Haves

- [ ] All 9 session methods have corresponding Params and Result structs
- [ ] SessionInfo struct matches meta.Session fields exactly (id, workspaceId, runtimeClass, state, room, roomAgent, labels, createdAt, updatedAt)
- [ ] All structs have proper JSON tags matching JSON-RPC naming convention (camelCase: sessionId, workspaceId, runtimeClass, stopReason)
- [ ] Optional fields use `omitempty` JSON tag
- [ ] go build passes with no errors

## Verification

```bash
go build ./pkg/ari/...
```

## Inputs

- `pkg/ari/types.go` — existing workspace types, follow same pattern

## Expected Output

- `pkg/ari/types.go` — updated with session types section