---
id: S06
parent: M001-tvc4z0
milestone: M001-tvc4z0
provides:
  - ARI JSON-RPC session/* methods for session lifecycle management
  - Auto-start capability: session/prompt starts session if needed
  - Session state transitions: created → running → stopped
  - Error handling with appropriate JSON-RPC error codes
requires:
  - slice: S04
    provides: Session CRUD operations and state machine transitions
  - slice: S05
    provides: Process lifecycle management (Start/Stop/Connect)
affects:
  - S07
  - S08
key_files:
  - pkg/ari/types.go
  - pkg/ari/server.go
  - pkg/agentd/process.go
  - cmd/agentd/main.go
  - pkg/ari/server_test.go
key_decisions:
  - Used string type for createdAt/updatedAt in SessionInfo (RFC 3339 format) for explicit JSON-RPC representation
  - session/prompt auto-starts with 10s timeout for Start operation and 30s timeout for Prompt RPC
  - session/list does not filter by labels because meta.SessionFilter lacks Labels field (future enhancement)
  - main.go requires metadata store (metaDB) for session management - fails fast if not configured
  - Fixed forkShim to NOT use exec.CommandContext - shim must run independently of request context
  - handleSessionPrompt returns CodeInvalidParams when Connect fails due to not running
patterns_established:
  - Auto-start pattern: session/prompt automatically starts the session if state is created
  - Error code distinction: InvalidParams for client-provided invalid state, InternalError for system failures
  - Test harness pattern: prepareWorkspaceForSession helper for FK constraint before session tests
  - Process lifecycle: Shim process runs independently of request context, managed by Stop/watchProcess
observability_surfaces:
  - ari: session/new creating session
  - ari: session/prompt auto-starting session
  - ari: session/prompt completed
  - ari: session/stop stopping session
  - ari: session/remove removing session
drill_down_paths:
  - /Users/jim/code/zoumo/open-agent-runtime/.gsd/milestones/M001-tvc4z0/slices/S06/tasks/T01-SUMMARY.md
  - /Users/jim/code/zoumo/open-agent-runtime/.gsd/milestones/M001-tvc4z0/slices/S06/tasks/T02-SUMMARY.md
  - /Users/jim/code/zoumo/open-agent-runtime/.gsd/milestones/M001-tvc4z0/slices/S06/tasks/T03-SUMMARY.md
duration: ""
verification_result: passed
completed_at: 2026-04-06T15:52:42.720Z
blocker_discovered: false
---

# S06: ARI Service

**ARI JSON-RPC server now exposes all session/* methods (new/prompt/cancel/stop/remove/list/status/attach/detach), enabling CLI to create/prompt/stop sessions through the ARI interface; fixed critical shim process lifecycle bug.**

## What Happened

## Slice S06: ARI Service - Complete

This slice delivered the ARI JSON-RPC server session/* methods, enabling CLI and orchestrators to manage agent sessions through a standardized interface.

### What Was Built

**T01: Session Types (pkg/ari/types.go)**
- Added 16 structs covering all 9 session/* methods
- Each method has a Params struct (request) and Result struct (response)
- Followed existing workspace types pattern with camelCase JSON tags
- SessionInfo includes: id, workspaceId, runtimeClass, state, room, roomAgent, labels, createdAt, updatedAt
- ShimStateInfo maps spec.State fields for status endpoint

**T02: Session Handlers (pkg/ari/server.go)**
- Extended Server struct with: sessions, processes, runtimeClasses, config fields
- Implemented 9 handlers following unmarshal → call → reply pattern:
  - session/new: Creates session with UUID, initial state "created"
  - session/prompt: Auto-starts if state=="created", connects to shim, calls Prompt RPC
  - session/cancel: Connects to shim, calls Cancel RPC
  - session/stop: Calls ProcessManager.Stop for graceful shutdown
  - session/remove: Calls SessionManager.Delete, returns InvalidParams for ErrDeleteProtected
  - session/list: Lists all sessions (no label filtering)
  - session/status: Returns session info, populates shim state if running
  - session/attach: Returns shim RPC socket path
  - session/detach: Placeholder with nil result

**T03: Integration Tests (pkg/ari/server_test.go)**
- Extended test harness with mockagent runtime class
- Added prepareWorkspaceForSession helper for FK constraint
- 10 session tests + 17 workspace tests = 27 total, all passing

### Critical Bug Fix

**The exec.CommandContext Bug**

During verification, discovered a critical bug in ProcessManager.forkShim. The code was using:
```go
cmd := exec.CommandContext(ctx, shimBinary, args...)
```

This tied the shim process lifecycle to the request context. In handleSessionPrompt:
```go
startCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
_, err := h.srv.processes.Start(startCtx, p.SessionId)
cancel()  // <-- BUG: Cancels context, killing the shim!
```

When cancel() was called after Start returned, Go killed the shim process because it was tied to the context.

**Fix:** Changed forkShim to use `exec.Command` (not CommandContext). The shim process now runs independently of the request context. Lifecycle is managed by ProcessManager.Stop and watchProcess.

**Error Code Fix**

Also fixed handleSessionPrompt to return CodeInvalidParams (not CodeInternalError) when Connect fails due to "not running". This is semantically correct - the client provided a sessionId that is not in a valid state for the operation.

### Verification Results

All 27 ARI tests pass:
- 17 workspace tests (existing)
- 10 session tests (new)

Full test suite passes with no errors.

## Verification

All slice verification criteria met:
- go build ./pkg/ari/... passes with no compile errors ✓
- go test ./pkg/ari/... -v passes all tests (27 tests: 17 workspace + 10 session) ✓
- go test ./pkg/ari/... -run TestARISessionLifecycle -v passes ✓
- go test ./pkg/agentd/... -v passes all agentd tests ✓
- go test ./... passes all project tests ✓

Key bug fixes verified:
1. forkShim no longer uses exec.CommandContext - shim runs independently
2. handleSessionPrompt returns correct error code for "not running" case

## Requirements Advanced

- R006 — Implemented all 9 session/* method handlers with proper error handling and auto-start

## Requirements Validated

- R006 — All 27 ARI tests pass including 10 session tests covering lifecycle, error cases, auto-start, and state transitions

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

Fixed critical bug in forkShim that was causing shim processes to be killed immediately after start. Changed from exec.CommandContext to exec.Command so shim runs independently of request context. Also fixed handleSessionPrompt error code for "not running" case.

## Known Limitations

session/list does not support label filtering. The meta.SessionFilter struct only supports State, WorkspaceID, Room, and HasRoom filters.

## Follow-ups

None - slice complete and ready for S07 (agentdctl CLI)

## Files Created/Modified

- `pkg/ari/types.go` — Added 16 session method params/results structs following workspace types pattern
- `pkg/ari/server.go` — Extended Server struct with session deps, implemented 9 session/* handlers
- `pkg/agentd/process.go` — Fixed forkShim to use exec.Command (not CommandContext) so shim runs independently
- `cmd/agentd/main.go` — Added SessionManager, ProcessManager, RuntimeClassRegistry deps to ARI server
- `pkg/ari/server_test.go` — Added 10 session integration tests, updated test harness
