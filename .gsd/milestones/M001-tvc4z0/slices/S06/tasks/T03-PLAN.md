---
estimated_steps: 8
estimated_files: 1
skills_used: []
---

# T03: Add integration tests for session methods

Extend testHarness to set up SessionManager, ProcessManager, RuntimeClassRegistry with mockagent. Create workspace first (workspace/prepare). Test session lifecycle: session/new → verify state=created → session/prompt → verify state=running, stopReason received → session/stop → verify state=stopped → session/remove. Test error cases: prompt on stopped session, remove on running session, invalid transitions. Follow S05 TestProcessManagerStart pattern for mockagent setup.

## Negative Tests

- **Malformed inputs**: nil params for session/new, missing sessionId for prompt/stop/remove
- **Error paths**: prompt on stopped session (InvalidParams "not running"), remove on running session (InvalidParams "delete protected"), session not found for all methods
- **Boundary conditions**: session/list on empty DB, session/status on stopped session (no shimState)

## Steps

1. Open `pkg/ari/server_test.go` and extend testHarness struct: add store (*meta.Store), sessions (*agentd.SessionManager), processes (*agentd.ProcessManager), runtimeClasses (*agentd.RuntimeClassRegistry), config (agentd.Config)
2. Update newTestHarness(): create temp SQLite DB file, create meta.Store, create SessionManager, create RuntimeClassRegistry with "mockagent" class pointing to OAR_SHIM_BINARY env, create ProcessManager, create Config with WorkspaceRoot=temp dir
3. Update newTestHarness(): update ari.New() call to pass new dependencies
4. Update newTestHarness() cleanup: close store, remove SQLite DB file, kill any remaining shim processes
5. Write TestARISessionLifecycle: create workspace via workspace/prepare, call session/new with workspaceId/runtimeClass="mockagent", verify result.sessionId and state="created", call session/prompt with sessionId/text="hello", verify stopReason="end_turn", call session/status verify state="running", call session/stop, verify session/status shows state="stopped", call session/remove, verify session/list empty
6. Write TestARISessionPromptAutoStart: create workspace, create session, verify state="created", call session/prompt WITHOUT prior session/start, verify state transitions to "running" after prompt (auto-start worked)
7. Write TestARISessionPromptOnStopped: create workspace, create session, prompt to start, stop session, call session/prompt on stopped session, verify InvalidParams error with "not running" message
8. Write TestARISessionRemoveProtected: create workspace, create session, prompt to start (state=running), call session/remove, verify InvalidParams error with ErrDeleteProtected message, stop session, verify session/remove succeeds
9. Write TestARISessionList: create workspace, create 2 sessions with different labels, call session/list, verify 2 sessions returned, call session/list with labels filter, verify 1 session returned
10. Write TestARISessionNotFound: call session/status with nonexistent sessionId, verify InvalidParams "not found"
11. Run `go test ./pkg/ari/... -v` to verify all tests pass

## Must-Haves

- [ ] testHarness sets up SessionManager, ProcessManager, RuntimeClassRegistry, meta.Store with mockagent
- [ ] TestARISessionLifecycle tests full round-trip: workspace → session → prompt → stop → remove
- [ ] TestARISessionPromptAutoStart verifies auto-start on prompt when state="created"
- [ ] TestARISessionPromptOnStopped verifies error for prompt on stopped session
- [ ] TestARISessionRemoveProtected verifies ErrDeleteProtected blocks remove
- [ ] TestARISessionNotFound verifies "not found" errors
- [ ] All tests pass: go test ./pkg/ari/... -v

## Verification

```bash
go test ./pkg/ari/... -v
go test ./pkg/ari/... -run TestARISessionLifecycle -v
```

## Observability Impact

Integration tests verify observability surfaces: session/status returns correct state, events received via ShimProcess.Events channel, logs show auto-start decision and shutdown sequence.

## Inputs

- `pkg/ari/server_test.go` — existing testHarness and workspace tests
- `pkg/ari/server.go` — session handlers from T02
- `pkg/ari/types.go` — session types from T01
- `pkg/agentd/session.go` — SessionManager API
- `pkg/agentd/process.go` — ProcessManager API, ShimProcess struct
- `pkg/agentd/shim_client.go` — ShimClient API
- `pkg/agentd/runtime.go` — RuntimeClassRegistry API
- `pkg/meta/models.go` — Session struct
- `pkg/meta/store.go` — meta.Store API for SQLite
- `pkg/workspace/manager.go` — WorkspaceManager for workspace creation

## Expected Output

- `pkg/ari/server_test.go` — extended testHarness and session tests