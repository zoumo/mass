---
estimated_steps: 28
estimated_files: 4
skills_used: []
---

# T03: Implement workspace commands

Create cmd/agentdctl/workspace.go with 3 workspace subcommands using spf13/cobra. Each command marshals params, calls ARI method via client, pretty-prints JSON result.

## Steps

1. Create `cmd/agentdctl/workspace.go` with package main declaration
2. Import cobra, pkg/ari/client, pkg/ari/types, encoding/json, fmt, os, pkg/workspace spec
3. Define workspaceCmd root command with Use: "workspace", Short: "Workspace management commands"
4. Implement workspacePrepareCmd: flags --name (required), --type (required, default "emptydir"), --path (optional), call workspace/prepare, output WorkspacePrepareResult as JSON
5. Add workspacePrepareCmd to workspaceCmd: workspaceCmd.AddCommand(workspacePrepareCmd)
6. Implement workspaceListCmd: no flags, call workspace/list, output WorkspaceListResult as JSON
7. Add workspaceListCmd to workspaceCmd
8. Implement workspaceCleanupCmd: positional arg workspace-id, call workspace/cleanup, output success message
9. Add workspaceCleanupCmd to workspaceCmd
10. Use shared helper functions from session.go (getClient, outputJSON, handleError) or create local versions
11. For workspace prepare: construct WorkspaceSpec from flags (EmptyDir: {Path: flag}, Git: {URL, Ref, Depth})

## Must-Haves

- [ ] workspace prepare command with --name, --type, --path flags
- [ ] workspace list command (no flags)
- [ ] workspace cleanup command with workspace-id positional arg
- [ ] All commands output pretty-printed JSON to stdout
- [ ] go build ./cmd/agentdctl passes

## Failure Modes

| Dependency | On error | On timeout | On malformed response |
|------------|----------|-----------|----------------------|
| ARI client | Print error to stderr, exit 1 | No timeout | Print parse error |
| agentd daemon | Connection refused error | N/A | N/A |

## Negative Tests

- Invalid workspace ID: ARI returns InvalidParams error, CLI prints error
- Missing required flags: cobra validation error
- Workspace has active sessions: cleanup may fail with RefCount error

## Inputs

- ``pkg/ari/client.go` — ARI client for RPC calls`
- ``pkg/ari/types.go` — Workspace params/results types`
- ``pkg/workspace/spec.go` — WorkspaceSpec struct for prepare`

## Expected Output

- ``cmd/agentdctl/workspace.go` — Workspace subcommands implementation`

## Verification

go build ./cmd/agentdctl passes, commands execute against running agentd

## Observability Impact

None — CLI commands are single-shot with no runtime state
