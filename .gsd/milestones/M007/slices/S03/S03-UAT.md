# S03: ARI Surface Rewrite — UAT

**Milestone:** M007
**Written:** 2026-04-09T21:38:51.220Z

# S03 UAT — ARI Surface Rewrite

## Preconditions

- `go build ./...` passes (no compilation errors)
- `pkg/ari/server.go` exists and is the full implementation (946 lines)
- `pkg/ari/server_test.go` exists with 18 handler tests
- `pkg/agentd/process.go` exports `InjectProcess`
- Test environment: macOS, Go 1.21+, bbolt store (no real shim binary required for these tests)

---

## Test Cases

### UAT-01: Full ARI test suite passes

**Command:**
```
go test ./pkg/ari/... -count=1 -timeout 60s
```

**Expected outcome:**
- Exit code 0
- `ok github.com/open-agent-d/open-agent-d/pkg/ari` with elapsed time < 60s
- No FAIL lines

---

### UAT-02: workspace/create returns pending immediately

**Test:** `TestWorkspaceCreatePending`

**Steps:**
1. Create test server with temp dir + bbolt store + WorkspaceManager + ARI Registry
2. Call `workspace/create` with `{name:"w1", source:{type:"emptyDir"}}`
3. Inspect synchronous reply

**Expected outcome:**
- Reply arrives before workspace prepare completes
- `result.phase == "pending"`
- `result.name == "w1"`

---

### UAT-03: workspace/status returns ready with path after prepare

**Test:** `TestWorkspaceStatusReady`

**Steps:**
1. Call `workspace/create` with emptyDir source
2. Poll `workspace/status {name:"w1"}` with 5s timeout, 50ms interval
3. Check when phase transitions to "ready"

**Expected outcome:**
- Eventually `result.phase == "ready"`
- `result.path` is non-empty (absolute path to workspace directory)

---

### UAT-04: workspace/list returns all ready workspaces

**Test:** `TestWorkspaceList`

**Steps:**
1. Create two workspaces: `ws-a` and `ws-b` (emptyDir)
2. Wait until both are ready (poll workspace/status for each)
3. Call `workspace/list`

**Expected outcome:**
- `result.workspaces` array contains entries for both `ws-a` and `ws-b`
- Length >= 2

---

### UAT-05: workspace/delete removes workspace; status returns -32602

**Test:** `TestWorkspaceDelete`

**Steps:**
1. Create workspace (emptyDir), wait until ready
2. Call `workspace/delete {name:"del-ws"}`
3. Call `workspace/status {name:"del-ws"}`

**Expected outcome:**
- workspace/delete returns empty result (no error)
- workspace/status returns JSON-RPC error with code -32602 (or phase "error")

---

### UAT-06: workspace/delete is blocked when agents exist

**Test:** `TestWorkspaceDeleteBlockedByAgent`

**Steps:**
1. Create workspace (emptyDir), wait until ready
2. Seed a `meta.Agent` record in the bbolt store pointing at that workspace
3. Call `workspace/delete {name:"blocked-ws"}`

**Expected outcome:**
- workspace/delete returns a JSON-RPC error (non-nil)
- Workspace still exists (not deleted)

---

### UAT-07: agent/create returns creating state synchronously; no agentId in JSON response

**Test:** `TestAgentCreateReturnsCreating`

**Steps:**
1. Create workspace (emptyDir), wait until ready
2. Call `agent/create {workspace:"crt-ws", name:"a1", runtimeClass:"default"}`
3. Marshal the raw JSON result to `map[string]any`
4. Inspect `result.state` and check for `agentId` key

**Expected outcome:**
- `result.state == "creating"`
- `result.workspace == "crt-ws"`
- `result.name == "a1"`
- No `"agentId"` key exists in the JSON response at any level

---

### UAT-08: agent/list and agent/status return seeded agents

**Test:** `TestAgentListAndStatus`

**Steps:**
1. Create workspace (emptyDir), wait until ready
2. Seed DB with two agents: agent-idle (state=idle) and agent-stopped (state=stopped)
3. Call `agent/list {workspace:"ls-ws"}`
4. Call `agent/status {workspace:"ls-ws", name:"agent-idle"}`
5. Call `agent/status {workspace:"ls-ws", name:"agent-stopped"}`

**Expected outcome:**
- agent/list returns 2 agents
- agent/status for agent-idle returns `{state:"idle"}`
- agent/status for agent-stopped returns `{state:"stopped"}`

---

### UAT-09: agent/prompt rejected for stopped, error, and creating states

**Test:** `TestAgentPromptRejectedForBadState`

**Steps:**
1. Create workspace (emptyDir), wait until ready
2. Seed three agents: stopped, error, creating states
3. Call `agent/prompt {workspace, name:"agent-stopped", prompt:"hello"}` 
4. Call `agent/prompt` for agent-error and agent-creating

**Expected outcome:**
- All three calls return a JSON-RPC error (non-nil)
- Error messages contain "not in idle state"

---

### UAT-10: agent/delete blocked for non-terminal (idle) agent

**Test:** `TestAgentDeleteRejectedForNonTerminal`

**Steps:**
1. Create workspace (emptyDir), wait until ready
2. Seed DB with an agent in `idle` state
3. Call `agent/delete {workspace:"del-ws", name:"active-agent"}`

**Expected outcome:**
- Returns a JSON-RPC error (non-nil)
- Agent still exists in DB

---

### UAT-11: workspace/send delivers prompt via injected mock shim

**Test:** `TestWorkspaceSendDelivered`

**Steps:**
1. Create workspace (emptyDir), wait until ready
2. Seed DB with an idle agent `recv-agent`
3. Start an in-process miniShimServer on a temp Unix socket
4. Build a `ShimProcess` pointing at the miniShimServer socket
5. Call `processes.InjectProcess(agentKey("send-ws","recv-agent"), shimProc)`
6. Call `workspace/send {workspace:"send-ws", from:"sender", to:"recv-agent", message:"hello"}`
7. Check miniShimServer received a prompt call

**Expected outcome:**
- workspace/send returns `{delivered:true}`
- miniShimServer recorded exactly one prompt call with message "hello"

---

### UAT-12: workspace/send rejected for error-state target agent

**Test:** `TestWorkspaceSendRejectedForErrorAgent`

**Steps:**
1. Create workspace (emptyDir), wait until ready
2. Seed DB with an agent in `error` state
3. Call `workspace/send {workspace:"serr-ws", from:"sender", to:"err-agent", message:"test"}`

**Expected outcome:**
- Returns a JSON-RPC error (non-nil)
- WARN slog line logged: "workspace/send: target agent in error state"

---

### UAT-13: No agentId field in agent/list or agent/status responses at any nesting level

**Test:** `TestNoAgentIDInResponses`

**Steps:**
1. Create workspace (emptyDir), wait until ready
2. Seed DB with two agents (a1, a2) in different states
3. Call `agent/list {workspace:"noid-ws"}` — marshal to `[]map[string]any`
4. Call `agent/status` for a1 and a2 — marshal to `map[string]any`
5. Recursively walk all maps and slices checking for key `"agentId"`

**Expected outcome:**
- Zero occurrences of `"agentId"` key at any nesting level in any response

---

### UAT-14: Server Serve/Shutdown lifecycle clean

**Test:** `TestServerServeShutdown`

**Steps:**
1. Create test server
2. Call workspace/list (any request to verify server is serving)
3. Call server.Shutdown(ctx)

**Expected outcome:**
- Serve starts without error
- Request succeeds
- Shutdown completes within timeout (no hang)

---

## Edge Cases

- **Workspace not found on agent/create**: returns -32602 (workspace lookup returns nil)
- **Workspace not ready on agent/create**: returns -32001 (phase != ready)
- **Agent already exists on agent/create**: returns -32001 (ErrAgentAlreadyExists)
- **Agent not found on agent/prompt/status/delete**: returns -32602
- **Recovery active (IsRecovering=true) on workspace/send or agent/prompt**: returns CodeRecoveryBlocked (-32001)
- **Unknown method**: returns jsonrpc2.Error{Code: -32601}
- **Stale socket file on server start**: Serve() removes old socket file before Listen (K014 pattern applied)

