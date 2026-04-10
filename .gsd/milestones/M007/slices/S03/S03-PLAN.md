# S03: ARI Surface Rewrite

**Goal:** Implement the full ARI JSON-RPC server surface (workspace/* + agent/* handlers) in pkg/ari/server.go, replacing the current stub. Write a handler test suite in pkg/ari/server_test.go that proves workspace/create→status, agent state validation, workspace/send routing, and the absence of agentId UUIDs across all handler responses.
**Demo:** After this: ARI handler tests over Unix socket prove workspace/create→agent/create→agent/prompt→agent/stop with (workspace,name) identity; workspace/send routes messages between agents.

## Must-Haves

- `go test ./pkg/ari/... -count=1 -timeout 60s` passes; all handler tests green\n- `go build ./...` succeeds\n- workspace/create returns {phase:"pending"}; workspace/status returns {phase:"ready", path:"..."} after prepare completes\n- agent/create returns {state:"creating"} immediately; DB record exists with state=creating\n- agent/prompt rejected (JSON-RPC error) when agent state is creating, stopped, or error\n- workspace/send delivers to idle agent via ShimClient.Prompt goroutine; rejected when target agent state is error\n- No \"agentId\" field appears in any workspace/* or agent/* response\n- CodeRecoveryBlocked (-32001) returned for agent/prompt and workspace/send when IsRecovering()

## Proof Level

- This slice proves: contract — handler tests over real Unix socket with DB-backed dependencies; no real shim binary required (mock ShimProcess injection for workspace/send tests)

## Integration Closure

Upstream: pkg/agentd (AgentManager, ProcessManager, ShimClient), pkg/meta (Store), pkg/workspace (WorkspaceManager), pkg/ari (Registry, Client, types). New wiring: Server.Serve() binds to ARI Unix socket and multiplexes all workspace/* and agent/* methods. What remains before milestone is usable end-to-end: CLI updates (S04) and full integration tests with real shim (S05).

## Verification

- slog INFO/WARN on every handler entry and terminal state (workspace created/prepared/failed, agent created/started/failed, prompt dispatched/rejected); CodeRecoveryBlocked errors observable via JSON-RPC error code -32001

## Tasks

- [x] **T01: Implement ARI server JSON-RPC infrastructure and workspace/* handlers** `est:2h`
  Replace the server.go stub with a working JSON-RPC server that accepts connections on a Unix socket, dispatches requests to handler functions, and implements all workspace/* methods.

**Server infrastructure:**
1. Add `ln net.Listener`, `mu sync.RWMutex`, `conns map[*jsonrpc2.Conn]struct{}`, `shutdownCh chan struct{}` to Server struct.
2. Implement `Serve()`: net.Listen("unix", s.socketPath), loop Accept() in goroutine, per-connection `jsonrpc2.NewConn(ctx, jsonrpc2.NewPlainObjectStream(nc), jsonrpc2.AsyncHandler(s))`. Track active conns; close on Shutdown.
3. Implement `Shutdown(ctx)`: close listener, close all active conns, wait with ctx timeout.
4. Implement `Handle(ctx, conn, req)` (jsonrpc2.Handler interface): switch on req.Method, dispatch to typed handler functions; unknown methods return jsonrpc2.Error{Code: -32601}.
5. Add `replyOK(ctx, conn, req, result any)` and `replyErr(ctx, conn, req, code int64, msg string)` helpers.

**workspace/create:**
6. Parse WorkspaceCreateParams; validate Name non-empty.
7. Create meta.Workspace in store (phase: pending); if already-exists error return JSON-RPC error.
8. Return WorkspaceCreateResult{Name, Phase:"pending"} immediately.
9. Start goroutine: call manager.Prepare(ctx, wsSpec, targetDir) where targetDir = filepath.Join(s.baseDir, "workspaces", params.Name). On success: store.UpdateWorkspaceStatus → ready + path; registry.Add. On failure: store.UpdateWorkspaceStatus → error.
10. Source for prepare: unmarshal params.Source (json.RawMessage) into workspace.Source; build workspace.WorkspaceSpec{OarVersion:"0.1.0", Metadata:{Name}, Source, Hooks}.

**workspace/status:**
11. Parse WorkspaceStatusParams; look up registry.Get(name). If found, return WorkspaceStatusResult with phase/path from registry.
12. If not in registry, fall back to store.GetWorkspace; return phase from DB. Return -32602 if not found at all.

**workspace/list:**
13. Registry.List() → build []WorkspaceInfo → return WorkspaceListResult.

**workspace/delete:**
14. Parse WorkspaceDeleteParams; store.DeleteWorkspace(ctx, name) — store already rejects if agents exist. registry.Remove(name). Return empty result.

**workspace/send:**
15. Parse WorkspaceSendParams; validate Workspace/From/To/Message non-empty.
16. Recovery guard: if s.processes.IsRecovering() return CodeRecoveryBlocked.
17. Load target agent from store; if nil return -32602; if state==error return -32001 with message "target agent is in error state".
18. Connect to target shim: s.processes.Connect(ctx, params.Workspace, params.To); if error return -32001 "target agent is not running".
19. Kick off `go client.Prompt(context.Background(), params.Message)` (fire-and-forget). Return WorkspaceSendResult{Delivered: true}.
  - Files: `pkg/ari/server.go`
  - Verify: cd /Users/jim/code/zoumo/open-agent-runtime && go build ./pkg/ari/... && go vet ./pkg/ari/...

- [x] **T02: Implement agent/* handlers, add InjectProcess helper, write handler test suite** `est:2.5h`
  Add all agent/* handler implementations to server.go, add InjectProcess to ProcessManager for test injection, and write the full pkg/ari/server_test.go suite that proves all contracts over a real Unix socket.

**agent/* handlers (add to server.go):**
1. `handleAgentCreate`: validate Workspace/Name/RuntimeClass non-empty; load workspace from DB — return -32602 if not found, -32001 if phase != ready. Check for existing agent (ErrAgentAlreadyExists → -32001). Call agents.Create(ctx, &meta.Agent{...}) with state=creating. Start background goroutine: call processes.Start(context.Background(), ws, name); on failure, agents.UpdateStatus → {State:error, ErrorMessage:err}. Return AgentCreateResult{Workspace, Name, State:"creating"}.
2. `handleAgentPrompt`: recovery guard. Load agent from DB → -32602 if nil. Validate state == spec.StatusIdle; if creating/running/stopped/error return -32001 "agent not in idle state: <state>". Call processes.Connect → if error return -32001 "agent not running". Fire `go client.Prompt(context.Background(), params.Prompt)`. Return AgentPromptResult{Accepted: true}.
3. `handleAgentCancel`: load agent; processes.Connect(); client.Cancel(ctx). Return empty.
4. `handleAgentStop`: call processes.Stop(ctx, ws, name). Return empty.
5. `handleAgentDelete`: call agents.Delete(ctx, ws, name); map ErrDeleteNotStopped → -32001, ErrAgentNotFound → -32602. Return empty.
6. `handleAgentRestart`: load agent; validate state==stopped or error. agents.UpdateStatus → {State:creating}. Fire goroutine processes.Start. Return AgentRestartResult{Workspace, Name, State:"creating"}.
7. `handleAgentList`: parse AgentListParams; agents.List(ctx, &meta.AgentFilter{Workspace, State}); build []AgentInfo from results. Return AgentListResult.
8. `handleAgentStatus`: load agent → -32602 if nil. Build AgentStatusResult{Agent: AgentInfo{...}}. Optionally call processes.RuntimeStatus for ShimState (best-effort; omit on error). Return result.
9. `handleAgentAttach`: load agent; validate state==idle or running → -32001 otherwise. processes.Connect() → return AgentAttachResult{SocketPath: proc.SocketPath}. Get SocketPath from agent.Status.ShimSocketPath as fallback.

**Add InjectProcess to pkg/agentd/process.go:**
10. Add method `func (m *ProcessManager) InjectProcess(key string, proc *ShimProcess)` that locks mu and writes processes[key] = proc. Used by tests to inject pre-built ShimProcess without going through Start().

**Write pkg/ari/server_test.go (replace stub test):**
Use `package ari_test`. Helper `newTestServer(t)` creates: temp dir, meta.Store, workspace.WorkspaceManager, ari.Registry, agentd.RuntimeClassRegistry(nil), agentd.AgentManager, agentd.ProcessManager, then ari.New(...) with a temp socket path. Returns server + client + store + processes.

11. `TestWorkspaceCreatePending`: call workspace/create {name:"w1", source:{type:"emptyDir"}} → verify result.Phase == "pending" and result.Name == "w1".
12. `TestWorkspaceStatusReady`: after workspace/create(emptyDir), poll workspace/status until phase=="ready" (require.Eventually 5s/50ms). Verify result has non-empty Path.
13. `TestWorkspaceList`: create 2 workspaces (emptyDir), poll both until ready, call workspace/list → verify len(Workspaces) >= 2.
14. `TestWorkspaceDelete`: create workspace (emptyDir), wait until ready, call workspace/delete → verify workspace/status returns -32602 or phase=="error".
15. `TestWorkspaceDeleteBlockedByAgent`: create workspace (emptyDir), wait until ready; seed DB with agent in that workspace (store.CreateAgent); call workspace/delete → must return JSON-RPC error.
16. `TestAgentCreateReturnsCreating`: create workspace (emptyDir), wait until ready; call agent/create {workspace, name, runtimeClass:"default"} → verify result.State=="creating", result.Workspace, result.Name set, no agentId field. Check via JSON: unmarshal raw result, verify no "agentId" key.
17. `TestAgentListAndStatus`: seed DB with 2 agents (idle + stopped) via store.CreateAgent; call agent/list {workspace:ws} → verify 2 agents; call agent/status {workspace, name} → verify state matches.
18. `TestAgentPromptRejectedForBadState`: seed DB with 3 agents (stopped, error, creating); call agent/prompt for each → all must return JSON-RPC error containing "not in idle state".
19. `TestAgentDeleteRejectedForNonTerminal`: seed DB with agent in idle state; call agent/delete → must return JSON-RPC error.
20. `TestWorkspaceSendDelivered`: create workspace (emptyDir), wait until ready; seed DB with idle agent; start in-process mock shim server (TCP/Unix); build ShimProcess with mock shim client; inject via processes.InjectProcess(agentKey(ws, name), shimProc); call workspace/send {workspace, from:"sender", to:name, message:"hello"} → verify result.Delivered==true and mock shim received a prompt.
21. `TestWorkspaceSendRejectedForErrorAgent`: seed DB with error-state agent; call workspace/send → verify JSON-RPC error.
22. `TestNoAgentIDInResponses`: call agent/list, agent/status with seeded agents; marshal each result to map[string]any; assert no key "agentId" at any level.
  - Files: `pkg/ari/server.go`, `pkg/ari/server_test.go`, `pkg/agentd/process.go`
  - Verify: cd /Users/jim/code/zoumo/open-agent-runtime && go test ./pkg/ari/... -count=1 -timeout 60s -v 2>&1 | tail -40

## Files Likely Touched

- pkg/ari/server.go
- pkg/ari/server_test.go
- pkg/agentd/process.go
