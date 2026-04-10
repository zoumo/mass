---
estimated_steps: 106
estimated_files: 5
skills_used: []
---

# T02: Rewrite integration tests for new workspace/agent API

All five files in `tests/integration/` use the old M007-deleted API and do not compile. They must be fully rewritten to use the new API surface.

## What changed in M007

**Old API (deleted):**
- `room/create`, `room/delete` — Room concept deleted
- `workspace/prepare` → replaced by `workspace/create`
- `workspace/cleanup` → replaced by `workspace/delete`
- `agent/create` params: `{workspaceId, room, name, runtimeClass}` → now `{workspace, name, runtimeClass}`
- `agent/prompt`/`agent/stop`/`agent/delete` params: `{agentId}` → now `{workspace, name}`
- `agent/status` params: `{agentId}` → now `{workspace, name}`
- `agent/list` param: `{room}` → now `{workspace}` (optional)
- `ari.RoomCreateResult` — deleted
- `ari.WorkspacePrepareResult` — deleted (use `ari.WorkspaceCreateResult`)
- `ari.AgentInfo.AgentId` — deleted (use `ari.AgentInfo.Workspace` + `ari.AgentInfo.Name`)
- `ari.SessionNewResult`, `ari.SessionPromptResult`, `ari.SessionStatusResult` — Session concept deleted

**New API:**
- `workspace/create {name, source}` → `WorkspaceCreateResult {name, phase:"pending"}`; poll `workspace/status {name}` until `phase == "ready"`
- `workspace/delete {name}` → deletes workspace
- `agent/create {workspace, name, runtimeClass}` → `AgentCreateResult {workspace, name, state:"creating"}`; poll `agent/status {workspace, name}` until `state == "idle"`
- `agent/prompt {workspace, name, prompt}` → `AgentPromptResult {accepted}`; state transitions to `"running"` then back to `"idle"`
- `agent/stop {workspace, name}` → state transitions to `"stopped"`
- `agent/delete {workspace, name}` → deletes agent
- `agent/status {workspace, name}` → `AgentStatusResult {agent: AgentInfo{workspace, name, state, ...}}`
- `agent/list {workspace?}` → `AgentListResult {agents: []AgentInfo}`
- `workspace/send {workspace, from, to, message}` → `WorkspaceSendResult {delivered}`

**State values:**
- After create: poll until `state == "idle"` (not "created" — that no longer exists)
- After prompt dispatch: state goes to `"running"` then back to `"idle"` when turn completes
- After stop: `state == "stopped"`
- Error state: `state == "error"`

## Files to rewrite

All five files must compile and pass. Keep the same test function names where possible for continuity.

### `tests/integration/session_test.go` — Shared test infrastructure
This file provides helpers used by all other test files. Rewrite it entirely:
- Rename helper `prepareTestWorkspace` → `createTestWorkspace` (calls `workspace/create`, then polls `workspace/status` until `phase=="ready"`)
- Remove `createRoom`/`deleteRoom` (room concept deleted)
- Rename `cleanupTestWorkspace` → `deleteTestWorkspace` (calls `workspace/delete`)
- Rename `waitForAgentState` — keep name but change params from `(client, agentId, state, timeout)` to `(client, workspace, name, state, timeout)` — calls `agent/status {workspace, name}`
- Rename `createAgentAndWait` — keep name but params: `(client, workspace, name, runtimeClass)` instead of `(client, workspaceId, room, name)` — calls `agent/create {workspace, name, runtimeClass}`, polls until `state=="idle"`
- Rename `stopAndDeleteAgent` — params: `(client, workspace, name)` instead of agentId
- Keep `setupAgentdTest` and config generation (same binary paths, same env var pattern); remove `createRoom` call from config setup
- Use short socket path `/tmp/oar-<pid>-<counter>.sock` (K025 constraint: macOS ≤104 chars)

### `tests/integration/e2e_test.go` — End-to-end pipeline
Rewrite `TestEndToEndPipeline` to exercise:
`workspace/create → poll ready → agent/create → poll idle → agent/prompt → poll running → agent/stop → poll stopped → agent/delete → workspace/delete`

Remove room/create, room/delete, workspaceId references. Use workspace `name="e2e-workspace"`, agent `name="e2e-agent"`, `workspace="e2e-workspace"`.

### `tests/integration/session_test.go` — Lifecycle tests
`TestAgentLifecycle` — rewrite for `idle→running→stopped` state machine (not `created→running→stopped`)
`TestAgentPromptAndStop` — update params to use `{workspace, name}`
`TestAgentPromptFromCreated` — rename to `TestAgentPromptFromIdle`; wait for `idle` not `created`
`TestMultipleAgentPromptsSequential` — update to use workspace/name; wait for `idle` between prompts (not `created`)

### `tests/integration/restart_test.go` — Restart recovery
Rewrite `TestAgentdRestartRecovery`:
- Remove `room/create` call
- Change agent identity from agentId to `(workspace, name)` pair
- Phase 1: `createTestWorkspace`, `createAgentAndWait(client, "test-ws", "agent-a", "mockagent")`, etc.
- Phase 2: kill agentd + all shims
- Phase 3: restart agentd, wait 2s for recovery
- Phase 4: verify `agent-a` is in `"error"` state via `agent/status {workspace:"test-ws", name:"agent-a"}`
- Phase 5: verify `agent-b` is in `"error"` state
- Phase 6: `agent/list {workspace:"test-ws"}` shows both agents
- Phase 7: cleanup via `agent/stop` + `agent/delete` by name

### `tests/integration/concurrent_test.go` — Concurrent agents
Rewrite `TestMultipleConcurrentAgents`:
- Remove room creation
- Track agents by name (not agentId)
- Use `agent/prompt {workspace:"concurrent-ws", name:"concurrent-agent-N", prompt:"..."}`
- Status checks use `{workspace, name}`

### `tests/integration/real_cli_test.go` — Real CLI lifecycle
The `runRealCLILifecycle` function must be rewritten:
- Replace `workspace/prepare` → `workspace/create` + poll until ready
- Replace `session/new` → `agent/create {workspace, name, runtimeClass}` + poll until idle
- Replace `session/prompt` (blocking) → `agent/prompt {workspace, name, prompt}` (async dispatch) + poll until idle/stopped
- Replace `session/status` → `agent/status {workspace, name}`
- Replace `session/stop`/`session/remove` → `agent/stop`/`agent/delete {workspace, name}`
- Replace `workspace/cleanup` → `workspace/delete {name}`
- Remove `ari.SessionNewResult`, `ari.SessionPromptResult`, `ari.SessionStatusResult` references
- `TestRealCLI_GsdPi` and `TestRealCLI_ClaudeCode` function names can stay; just update the helper they call

## Config template for setupAgentdTest

```yaml
socket: <socketPath>
workspaceRoot: <workspaceRoot>
metaDB: <metaDB>
bundleRoot: <bundleRoot>
runtimeClasses:
  mockagent:
    command: <mockagentBin>
    args: []
    env:
      PATH: /usr/bin:/bin
```

(no `room:` field — rooms are deleted)

## Verification strategy

After rewriting:
1. `go vet ./tests/integration/` — must produce 0 errors
2. Build all binaries: `go build -o bin/agentd ./cmd/agentd && go build -o bin/agent-shim ./cmd/agent-shim` (mockagent already exists at bin/mockagent)
3. `go test ./tests/integration/... -v -timeout 120s` — all tests must pass
4. `go test ./tests/integration/... -v -timeout 120s -short` — skips the slow tests cleanly

## Important constraints

- Socket paths must use `/tmp/oar-<pid>-<counter>.sock` pattern (K025: macOS ≤104 char limit)
- The `ari.Client` is NOT thread-safe for concurrent calls — use `sync.Mutex` for concurrent tests
- `agent/prompt` is ASYNC — does not wait for turn completion. Poll `agent/status` for state transitions.
- `workspace/create` is ASYNC — returns `phase:"pending"` immediately. Poll `workspace/status` until `phase:"ready"`.
- `agent/create` starts the shim asynchronously — poll until `state=="idle"`, not `"creating"`
- State after prompt dispatch: `"running"` → after turn completes: `"idle"` (poll for `"idle"` between sequential prompts)
- `agent/delete` requires state `"stopped"` or `"error"` first; call `agent/stop` before delete
- `workspace/delete` requires no agents using the workspace — delete all agents first

## Inputs

- `pkg/ari/types.go`
- `pkg/ari/server.go`
- `pkg/ari/server_test.go`
- `pkg/spec/state_types.go`
- `tests/integration/session_test.go`
- `tests/integration/e2e_test.go`
- `tests/integration/restart_test.go`
- `tests/integration/concurrent_test.go`
- `tests/integration/real_cli_test.go`

## Expected Output

- `tests/integration/session_test.go`
- `tests/integration/e2e_test.go`
- `tests/integration/restart_test.go`
- `tests/integration/concurrent_test.go`
- `tests/integration/real_cli_test.go`

## Verification

go vet ./tests/integration/ && go test ./tests/integration/... -v -timeout 120s -run 'TestEndToEndPipeline|TestAgentLifecycle|TestAgentdRestartRecovery|TestMultipleConcurrentAgents'
