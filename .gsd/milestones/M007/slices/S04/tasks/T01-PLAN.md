---
estimated_steps: 33
estimated_files: 4
skills_used: []
---

# T01: workspace-mcp-server binary + agentdctl room cleanup

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

## Inputs

- ``cmd/room-mcp-server/main.go` — source to rename/update`
- ``cmd/agentdctl/room.go` — to be deleted`
- ``cmd/agentdctl/workspace.go` — add workspace send subcommand`
- ``cmd/agentdctl/main.go` — remove roomCmd wiring`
- ``pkg/ari/types.go` — WorkspaceSendParams, WorkspaceStatusResult types to match`

## Expected Output

- ``cmd/workspace-mcp-server/main.go` — new file`
- ``cmd/agentdctl/workspace.go` — updated with workspace send subcommand`
- ``cmd/agentdctl/main.go` — updated, roomCmd removed`

## Verification

go build ./cmd/workspace-mcp-server/... && go build ./cmd/agentdctl/... && go build ./... && go run ./cmd/agentdctl/ workspace --help 2>&1 | grep -E 'send|create|list|delete'
