# S04: CLI + workspace-mcp-server + Design Docs

**Goal:** Rename cmd/room-mcp-server to cmd/workspace-mcp-server with updated internals; remove the stale roomCmd from agentdctl and add workspace send subcommand; rewrite the two design docs (ari-spec.md, agentd.md) to reflect the workspace/agent model. go build ./... clean with no room-mcp-server or Room references in cmd/.
**Demo:** After this: `agentdctl workspace create` and `agentdctl agent create --workspace w --name a` work; `go build ./cmd/workspace-mcp-server` succeeds; design docs reflect new model.

## Must-Haves

- `go build ./cmd/workspace-mcp-server` succeeds; binary starts and validates OAR_WORKSPACE_NAME env var
- `go build ./...` succeeds with no cmd/room-mcp-server directory present
- `agentdctl workspace --help` shows `create`, `list`, `delete`, `send` subcommands; `agentdctl room` no longer exists
- `agentdctl workspace create --name w --source-type emptyDir --help` shows expected flags
- `agentdctl agent create --workspace w --name a --runtime-class mockagent --help` shows expected flags
- docs/design/agentd/ari-spec.md and agentd.md contain no Room/* methods, no agentId, no session/* references in the ARI contract section

## Proof Level

- This slice proves: contract

## Integration Closure

S04 is purely build-time and documentation. No runtime integration is added. The workspace-mcp-server binary is a compile-time artifact verified by go build. CLI commands are verified by cobra flag parsing (no live agentd required). Design docs are prose-only. S05 (integration tests) is the next runtime integration gate.

## Verification

- workspace-mcp-server logs workspace=, agentName=, agentID= on startup (matching room-mcp-server pattern). No new runtime observability surfaces.

## Tasks

- [x] **T01: workspace-mcp-server binary + agentdctl room cleanup** `est:30 min`
  Two Go code changes in one task:

**Part A — workspace-mcp-server:**
Create `cmd/workspace-mcp-server/main.go` as a renamed+updated version of `cmd/room-mcp-server/main.go`:
- Package comment and binary name: `workspace-mcp-server`
- env vars: `OAR_WORKSPACE_NAME` (was `OAR_ROOM_NAME`)
- tool names: `workspace_send`, `workspace_status` (were `room_send`, `room_status`)
- ARI calls: `workspace/send`, `workspace/status` (were `room/send`, `room/status`)
- ARI param type: update `ariWorkspaceSendParams` with `workspace` field (was `room`)
- ARI result/status types updated to workspace model (WorkspaceStatus has members[], phase — match pkg/ari types)
- mcp.Server name: `workspace-mcp-server`
- Log prefix: `workspace-mcp-server:`
- Log file name: `workspace-mcp-server.log`
Do NOT delete cmd/room-mcp-server/ — that deletion is in step 3 of this task to avoid breaking the go.mod module path.

**Part B — agentdctl room cleanup:**
1. Delete `cmd/agentdctl/room.go` entirely.
2. Add `workspace send` subcommand to `cmd/agentdctl/workspace.go`:
   - Command: `agentdctl workspace send`
   - Flags: `--workspace` (required), `--from` (required), `--to` (required), `--text` (required)
   - Calls `workspace/send` via ARI with `WorkspaceSendParams{Workspace, From, To, Message}`
   - Prints `Message delivered: true/false`
3. Update `cmd/agentdctl/main.go`:
   - Remove `rootCmd.AddCommand(roomCmd)` line
   - Update the comment to remove 'session management' and 'room'

**Part C — delete old directory:**
Delete `cmd/room-mcp-server/main.go` (the directory itself — Go build ignores empty dirs, but clean up the file).

**Verification steps:**
```
go build ./cmd/workspace-mcp-server/... # must succeed
go build ./cmd/agentdctl/...             # must succeed  
go build ./...                           # full build must succeed
```
Check that `agentdctl workspace --help` lists send, create, list, delete subcommands.
Check that `agentdctl room --help` returns an error (command not found).
  - Files: `cmd/workspace-mcp-server/main.go`, `cmd/agentdctl/workspace.go`, `cmd/agentdctl/room.go`, `cmd/agentdctl/main.go`
  - Verify: go build ./cmd/workspace-mcp-server/... && go build ./cmd/agentdctl/... && go build ./... && go run ./cmd/agentdctl/ workspace --help 2>&1 | grep -E 'send|create|list|delete'

- [x] **T02: Rewrite design docs: ari-spec.md + agentd.md** `est:30 min`
  Rewrite both design documents to reflect the M007 terminal-state model. No Go code changes. The docs must match the actual implemented API surface (workspace/* + agent/* methods as in pkg/ari/types.go and pkg/ari/server.go).

**docs/design/agentd/ari-spec.md — full rewrite:**
Remove all Room/room/* sections, agentId references, session/* references in the ARI contract. Replace with the current implemented surface:

```
Workspace Methods:
  workspace/create   {name, source}             → {name, phase}
  workspace/status   {name}                     → {name, phase, path?, members[]}
  workspace/list     {}                         → {workspaces[]}
  workspace/delete   {name}                     → {} (blocked if agents exist)
  workspace/send     {workspace, from, to, message} → {delivered}

Agent Methods:
  agent/create    {workspace, name, runtimeClass, restartPolicy?, systemPrompt?} → {workspace, name, state:"creating"}
  agent/prompt    {workspace, name, prompt}      → {accepted}
  agent/cancel    {workspace, name}              → {}
  agent/stop      {workspace, name}              → {}
  agent/delete    {workspace, name}              → {} (requires stopped/error)
  agent/restart   {workspace, name}              → {} (requires stopped/error)
  agent/list      {workspace?, state?}           → {agents[]}
  agent/status    {workspace, name}              → AgentInfo
  agent/attach    {workspace, name}              → {} (returns shim socket path)

Events: agent/update, agent/stateChange
```

Key points to express:
- Transport: JSON-RPC 2.0 over Unix domain socket, default path `/run/agentd/agentd.sock`
- Identity: (workspace, name) pair — no agentId UUID
- workspace/create is async (returns pending, poll workspace/status until ready)
- agent/create is async (returns creating, poll agent/status until idle)
- agent/prompt rejected when state is creating/stopped/error
- workspace/delete blocked when agents exist (JSON-RPC error CodeRecoveryBlocked -32001)
- State values: creating, idle, running, stopped, error (no 'created')
- workspace-mcp-server uses workspace_send and workspace_status tools
- No Room/* methods
- No session/* references in the ARI contract
- Error codes: -32001 (CodeRecoveryBlocked for blocked ops), -32602 (invalid params for not-found)

Include a concrete JSON-RPC example for workspace/create + workspace/status and agent/create + agent/status to show the async polling pattern.

**docs/design/agentd/agentd.md — targeted update:**
Update the Agent Manager section: replace `room + name` identity with `workspace + name`. Remove references to Session Manager as an internal subsystem (agentd no longer has a Session concept — it has AgentManager + ProcessManager). Update state machine values to match spec.Status: creating/idle/running/stopped/error (remove 'created'). Remove room/* method references. Keep the Workspace Manager section intact (it's already correct). Remove any mention of Room projection or realized Room.

**Verification:**
```bash
# No Room methods in ARI contract
! grep -n 'room/create\|room/delete\|room/status\|room/send' docs/design/agentd/ari-spec.md
# No agentId in ARI contract  
! grep -n 'agentId' docs/design/agentd/ari-spec.md
# No Session Manager subsystem
! grep -n 'Session Manager' docs/design/agentd/agentd.md
# workspace+name identity present
grep -n 'workspace.*name\|name.*workspace' docs/design/agentd/agentd.md
```
  - Files: `docs/design/agentd/ari-spec.md`, `docs/design/agentd/agentd.md`
  - Verify: ! grep -n 'room/create\|room/delete\|room/status\|room/send\|agentId\|Session Manager' docs/design/agentd/ari-spec.md docs/design/agentd/agentd.md | grep -v '# ' && grep -q 'workspace/create' docs/design/agentd/ari-spec.md && grep -q 'workspace.*name' docs/design/agentd/agentd.md

## Files Likely Touched

- cmd/workspace-mcp-server/main.go
- cmd/agentdctl/workspace.go
- cmd/agentdctl/room.go
- cmd/agentdctl/main.go
- docs/design/agentd/ari-spec.md
- docs/design/agentd/agentd.md
