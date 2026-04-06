---
estimated_steps: 12
estimated_files: 2
skills_used: []
---

# T02: Extend Server struct and implement session method handlers

Add SessionManager, ProcessManager, RuntimeClassRegistry, Config fields to Server struct. Extend New() signature with these dependencies. Add 9 session/* cases to Handle() switch. Implement each handler following existing workspace handler pattern: unmarshal params → call manager method → marshal result → reply. Key behavior: session/prompt auto-starts if session.State == 'created'.

## Failure Modes

| Dependency | On error | On timeout | On malformed response |
|------------|----------|-----------|----------------------|
| SessionManager.Get | Reply InvalidParams "session not found" | Reply InvalidParams | N/A |
| ProcessManager.Start | Reply InternalError "start session failed" | Reply InternalError after 10s | N/A |
| ShimClient.Prompt | Reply InternalError "prompt failed" | Reply InternalError after 30s | N/A |
| SessionManager.Delete (protected) | Reply InvalidParams ErrDeleteProtected.Error() | N/A | N/A |

## Steps

1. Open `pkg/ari/server.go` and add new fields to Server struct: sessions (*agentd.SessionManager), processes (*agentd.ProcessManager), runtimeClasses (*agentd.RuntimeClassRegistry), config (agentd.Config)
2. Update New() function signature to accept these new dependencies: New(manager, registry, sessions, processes, runtimeClasses, config, socketPath, baseDir)
3. Update New() to initialize new Server fields with passed dependencies
4. Import agentd package in server.go (add import statement)
5. In Handle() switch, add case "session/new" → call handleSessionNew
6. Implement handleSessionNew: unmarshal SessionNewParams, call sessions.Create with new meta.Session (generate UUID for ID, set State to "created"), reply with SessionNewResult
7. In Handle() switch, add case "session/prompt" → call handleSessionPrompt
8. Implement handleSessionPrompt: unmarshal SessionPromptParams, get session from sessions.Get, if state=="created" call processes.Start (auto-start), call processes.Connect to get ShimClient, call client.Prompt, reply with SessionPromptResult
9. In Handle() switch, add case "session/cancel" → call handleSessionCancel
10. Implement handleSessionCancel: unmarshal SessionCancelParams, call processes.Connect, call client.Cancel, reply with nil result
11. In Handle() switch, add case "session/stop" → call handleSessionStop
12. Implement handleSessionStop: unmarshal SessionStopParams, call processes.Stop, reply with nil result
13. In Handle() switch, add case "session/remove" → call handleSessionRemove
14. Implement handleSessionRemove: unmarshal SessionRemoveParams, call sessions.Delete, handle ErrDeleteProtected by replying InvalidParams, reply with nil result
15. In Handle() switch, add case "session/list" → call handleSessionList
16. Implement handleSessionList: unmarshal SessionListParams (optional), call sessions.List with label filter, convert to SessionInfo array, reply with SessionListResult
17. In Handle() switch, add case "session/status" → call handleSessionStatus
18. Implement handleSessionStatus: unmarshal SessionStatusParams, call sessions.Get, if running call processes.State for shim state, reply with SessionStatusResult
19. In Handle() switch, add case "session/attach" → call handleSessionAttach
20. Implement handleSessionAttach: unmarshal SessionAttachParams, call processes.GetProcess to get ShimProcess, reply with SessionAttachResult containing shimProc.SocketPath
21. In Handle() switch, add case "session/detach" → call handleSessionDetach
22. Implement handleSessionDetach: unmarshal SessionDetachParams, reply with nil (placeholder, no clear semantics per research doc)
23. Open `cmd/agentd/main.go` and update ari.New() call to pass new dependencies: Create RuntimeClassRegistry from cfg.RuntimeClasses, Create SessionManager from store, Create ProcessManager from registry/sessions/store/cfg
24. Run `go build ./...` to verify compilation
25. Run `go test ./pkg/ari/... -run TestARIWorkspacePrepare -v` to verify existing tests still pass

## Must-Haves

- [ ] Server struct has sessions, processes, runtimeClasses, config fields
- [ ] New() signature accepts all new dependencies
- [ ] Handle() dispatches all 9 session/* methods
- [ ] session/prompt auto-starts when session.State == "created"
- [ ] session/remove returns InvalidParams with ErrDeleteProtected message for running/paused:warm sessions
- [ ] All handlers follow unmarshal → call → reply pattern from workspace handlers
- [ ] cmd/agentd/main.go passes all dependencies to New()
- [ ] go build ./... passes
- [ ] Existing workspace tests still pass

## Verification

```bash
go build ./...
go test ./pkg/ari/... -run TestARIWorkspacePrepare -v
```

## Observability Impact

Handlers log method calls (session/new, session/prompt auto-start, session/stop) with session_id and result/error. Errors logged with structured fields for diagnosis.

## Inputs

- `pkg/ari/server.go` — existing Server struct and workspace handlers
- `pkg/ari/types.go` — session types from T01
- `pkg/agentd/session.go` — SessionManager API
- `pkg/agentd/process.go` — ProcessManager API
- `pkg/agentd/shim_client.go` — ShimClient Prompt/Cancel API
- `pkg/agentd/runtime.go` — RuntimeClassRegistry API
- `pkg/agentd/config.go` — Config struct
- `pkg/meta/models.go` — Session struct definition
- `cmd/agentd/main.go` — existing main() to update New() call

## Expected Output

- `pkg/ari/server.go` — extended Server and 9 session handlers
- `cmd/agentd/main.go` — updated New() call with new dependencies