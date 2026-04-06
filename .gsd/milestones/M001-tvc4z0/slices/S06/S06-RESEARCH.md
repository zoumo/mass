# M001-tvc4z0/S06 — Research

**Date:** 2026-04-06

## Summary

S06 adds session/* methods to the existing ARI JSON-RPC server. The current server (pkg/ari/server.go) only handles workspace/* methods. Session management is provided by SessionManager (pkg/agentd/session.go) with CRUD + state machine validation, and shim process control by ProcessManager (pkg/agentd/process.go) with Start/Stop/State/Connect methods. The work is straightforward wiring following established patterns — add SessionManager/ProcessManager dependencies to Server, implement 9 session method handlers, define corresponding types, and write integration tests.

**Primary Recommendation:** Extend the existing ARI Server with session/* method handlers. The Server struct needs SessionManager, ProcessManager, RuntimeClassRegistry, and Config as additional dependencies. Implement session/new to create sessions in DB, session/prompt to auto-start if needed then send prompt via ShimClient, session/stop to gracefully stop shim, etc. Follow the existing workspace handler patterns exactly.

## Recommendation

**Approach:** Extend existing ARI Server with session dependencies and method handlers. This is straightforward pattern application — no new architecture needed. The workspace handlers (handleWorkspacePrepare, handleWorkspaceList, handleWorkspaceCleanup) already demonstrate the exact pattern: unmarshal params, call manager method, marshal result, reply via jsonrpc2.Conn.

**Why:** The SessionManager and ProcessManager already implement all needed functionality. SessionManager provides Create/Get/List/Update/Delete/Transition. ProcessManager provides Start/Stop/State/Connect. ShimClient provides Prompt/Cancel/Subscribe. The ARI handlers just need to wire these together with JSON-RPC request/response handling.

**Key design decision:** session/prompt should auto-start if session is in "created" state. R006 doesn list session/start as a method, so the natural workflow is session/new → session/prompt → session/stop. Auto-start on first prompt simplifies CLI UX.

## Implementation Landscape

### Key Files

- `pkg/ari/server.go` — Existing ARI Server with workspace/* handlers. **Needs modification:** add SessionManager, ProcessManager, RuntimeClassRegistry, Config fields; extend New() signature; add session/* method handlers (sessionNew, sessionPrompt, sessionCancel, sessionStop, sessionRemove, sessionList, sessionStatus, sessionAttach, sessionDetach)
- `pkg/ari/types.go` — Existing types for workspace params/results. **Needs modification:** add 9+ new types for session params/results (SessionNewParams, SessionNewResult, SessionPromptParams, SessionPromptResult, etc.)
- `pkg/ari/server_test.go` — Existing integration tests for workspace methods. **Needs modification:** extend testHarness to set up SessionManager/ProcessManager/mockagent; add session method tests
- `pkg/agentd/session.go` — SessionManager with Create/Get/List/Update/Delete/Transition methods. **Read-only:** provides session CRUD with state machine validation
- `pkg/agentd/process.go` — ProcessManager with Start/Stop/State/Connect methods. **Read-only:** provides shim process lifecycle management
- `pkg/agentd/shim_client.go` — ShimClient with Prompt/Cancel/Subscribe methods. **Read-only:** provides RPC to shim
- `pkg/meta/models.go` — Session struct definition. **Read-only:** defines Session{ID, RuntimeClass, WorkspaceID, Room, RoomAgent, Labels, State, CreatedAt, UpdatedAt}

### Build Order

1. **Types first** (pkg/ari/types.go) — Define all session params/results structs. Zero dependencies, easiest to verify.
2. **Server struct extension** (pkg/ari/server.go) — Add SessionManager/ProcessManager/RuntimeClassRegistry/Config fields to Server struct, extend New() signature. This breaks the existing API but is necessary.
3. **Method handlers** (pkg/ari/server.go) — Add session/* method handlers in Handle() switch, implement 9 handler functions. Each handler follows existing pattern: unmarshal params → call manager → marshal result → reply.
4. **Integration tests** (pkg/ari/server_test.go) — Extend testHarness with SessionManager/ProcessManager setup using mockagent (following S05 TestProcessManagerStart pattern). Test full session lifecycle: new → prompt → stop → remove.

### Method Handler Implementation Details

| Method | Params | Manager Call | Result | Notes |
|--------|--------|--------------|--------|-------|
| session/new | workspaceId, runtimeClass, labels?, room? | SessionManager.Create | sessionId, state="created" | Creates session in DB |
| session/prompt | sessionId, text | ProcessManager.Connect → ShimClient.Prompt (auto-start if "created") | stopReason | Auto-start: ProcessManager.Start if state="created" |
| session/cancel | sessionId | ProcessManager.Connect → ShimClient.Cancel | nil | Only for running sessions |
| session/stop | sessionId | ProcessManager.Stop | nil | Graceful shutdown via Shutdown RPC |
| session/remove | sessionId | SessionManager.Delete | nil | Blocked if running/paused:warm (ErrDeleteProtected) |
| session/list | labels? | SessionManager.List | sessions[] | Optional label filter |
| session/status | sessionId | SessionManager.Get + ProcessManager.State (if running) | session info + shim state | Combines DB + shim state |
| session/attach | sessionId | ProcessManager.Connect | shim socket path? | Returns ShimClient or connection info |
| session/detach | sessionId | (no-op?) | nil | Placeholder — no clear semantics |

### Auto-Start Pattern for session/prompt

```go
func (h *connHandler) handleSessionPrompt(ctx, conn, req) {
    // 1. Unmarshal params
    // 2. Get session from SessionManager
    // 3. If session.State == "created", call ProcessManager.Start(sessionID)
    // 4. Call ProcessManager.Connect(sessionID) to get ShimClient
    // 5. Call client.Prompt(ctx, params.Text)
    // 6. Reply with result
}
```

### Server Struct Extension

```go
type Server struct {
    manager       *workspace.WorkspaceManager   // existing
    registry      *Registry                      // existing
    baseDir       string                         // existing
    path          string                         // existing
    mu            sync.Mutex                     // existing
    listener      net.Listener                   // existing
    done          chan struct{}                  // existing
    once          sync.Once                      // existing
    
    // NEW for S06:
    sessions      *SessionManager
    processes     *ProcessManager
    runtimeClasses *RuntimeClassRegistry
    config        Config
}
```

### New() Signature Extension

```go
// Existing:
func New(manager *workspace.WorkspaceManager, registry *Registry, socketPath, baseDir string) *Server

// New:
func New(manager *workspace.WorkspaceManager, registry *Registry, sessions *SessionManager, processes *ProcessManager, runtimeClasses *RuntimeClassRegistry, config Config, socketPath, baseDir string) *Server
```

### Error Handling Patterns

| Error | Code | Message |
|-------|------|---------|
| Session not found | InvalidParams | "session %q not found" |
| Session not running (for prompt/cancel) | InvalidParams | "session %q is not running (state: %s)" |
| Invalid state transition | InvalidParams | ErrInvalidTransition.Error() |
| Delete protected | InvalidParams | ErrDeleteProtected.Error() |
| Process start failure | InternalError | "start session %s: %v" |
| Prompt RPC failure | InternalError | "prompt failed: %v" |

### Test Strategy

Follow existing testHarness pattern in server_test.go:
1. Create testHarness with SessionManager, ProcessManager, mockagent (use OAR_SHIM_BINARY env to point to mockagent)
2. Create workspace first (workspace/prepare) — session needs workspaceId
3. Test session lifecycle: session/new → verify state="created" → session/prompt → verify state="running" → session/stop → verify state="stopped" → session/remove
4. Test error cases: prompt on stopped session, remove on running session, invalid state transitions

## Skills Discovered

No relevant skills installed. The work uses established patterns from the codebase (JSON-RPC handlers, manager wrappers).

## Don't Hand-Roll

None. All components exist in codebase: jsonrpc2 (S05), SessionManager (S04), ProcessManager (S05), ShimClient (S05), workspace patterns (M001-tlbeko).

## Sources

- Existing codebase: pkg/ari/server.go (workspace handler pattern)
- S05 ShimClient/ProcessManager implementation (RPC patterns)
- S04 SessionManager implementation (session CRUD patterns)
- R006 requirement: session/* methods (new/prompt/cancel/stop/remove/list/status/attach/detach)

## Risks

1. **New() signature change breaks existing callers** — cmd/agentd/main.go will need updates to pass new dependencies. Low risk: straightforward addition.

2. **Auto-start on prompt adds complexity** — session/prompt handler needs to check state and call ProcessManager.Start if "created". Medium risk: need to handle start failures gracefully (cleanup session? leave in "created" state?).

3. **session/detach semantics unclear** — R006 mentions it but no clear semantics. Low risk: implement as no-op or placeholder, defer to future slice.

4. **Test setup complexity** — Need workspace (workspace/prepare), session (session/new), and mockagent running for full session/prompt test. Medium risk: follow S05 TestProcessManagerStart pattern carefully.

## Forward Intelligence

- **S05 ShimProcess.Events channel** — Events are delivered async via jsonrpc2.AsyncHandler. session/prompt tests should verify events received (like TestProcessManagerStart does).
- **S05 socket readiness** — Use net.Dial("unix", socketPath) to verify socket is accepting connections (os.OpenFile doesn't work on sockets).
- **S04 state machine** — Terminal state "stopped" has no valid transitions. Session in "stopped" state can only be deleted.
- **S04 delete protection** — Running and paused:warm sessions cannot be deleted. session/remove must handle ErrDeleteProtected.
- **ProcessManager.Start workflow** — Requires session in "created" state, workspaceId must exist in DB, runtimeClass must resolve.
- **ShimClient.Prompt returns stopReason** — "end_turn", "cancelled", "tool_use". Tests should verify stopReason in result.
- **Session model fields** — RuntimeClass and WorkspaceID are required. Room, RoomAgent, Labels are optional.