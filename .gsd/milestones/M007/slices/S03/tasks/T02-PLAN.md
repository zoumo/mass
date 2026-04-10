---
estimated_steps: 27
estimated_files: 3
skills_used: []
---

# T02: Implement agent/* handlers, add InjectProcess helper, write handler test suite

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

## Inputs

- `pkg/ari/server.go`
- `pkg/ari/types.go`
- `pkg/ari/registry.go`
- `pkg/ari/client.go`
- `pkg/agentd/process.go`
- `pkg/agentd/agent.go`
- `pkg/agentd/shim_client.go`
- `pkg/agentd/shim_client_test.go`
- `pkg/agentd/recovery_test.go`
- `pkg/meta/models.go`
- `pkg/meta/store.go`

## Expected Output

- `pkg/ari/server.go`
- `pkg/ari/server_test.go`
- `pkg/agentd/process.go`

## Verification

cd /Users/jim/code/zoumo/open-agent-runtime && go test ./pkg/ari/... -count=1 -timeout 60s -v 2>&1 | tail -40

## Observability Impact

agent/create handler logs INFO on agent creation and background Start(); agent/prompt logs INFO on dispatch and WARN on rejection; workspace/send logs INFO on delivery and rejection reasons (recovery blocked, error-state target, not running)
