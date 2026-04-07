---
id: T03
parent: S07
milestone: M001-tvc4z0
key_files:
  - cmd/agentdctl/workspace.go
  - cmd/agentdctl/main.go
key_decisions:
  - Validated type-specific required flags BEFORE connecting to client (avoids confusing socket errors)
  - Reused helper functions from session.go (getClient, outputJSON, handleError)
duration: 
verification_result: passed
completed_at: 2026-04-06T16:25:35.880Z
blocker_discovered: false
---

# T03: Implemented 3 workspace subcommands for agentdctl CLI with cobra

**Implemented 3 workspace subcommands for agentdctl CLI with cobra**

## What Happened

Created cmd/agentdctl/workspace.go with 3 workspace management subcommands using spf13/cobra: workspace prepare (with --name required, --type default emptyDir, plus git flags --url/--ref/--depth and local flag --path), workspace list (no flags), workspace cleanup (positional workspace-id arg). Updated cmd/agentdctl/main.go to add workspaceCmd to root command. Implemented type-specific flag validation BEFORE client connection to avoid confusing socket errors - validates --url for git type, --path for local type, and rejects invalid types upfront. Reused helper functions from session.go (getClient, outputJSON, handleError) since they're in the same main package.

## Verification

Verified go build ./cmd/agentdctl passes. All 3 workspace subcommands registered under workspace command. Required flags validated by cobra (--name). Type-specific validation works (--url required for git, --path required for local, invalid type rejected). Positional args validated by cobra (workspace-id for cleanup). Connection error handling works with nonexistent socket. All Must-Haves met: workspace prepare with flags, workspace list, workspace cleanup with positional arg, build passes.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go build ./cmd/agentdctl` | 0 | ✅ pass | 1000ms |
| 2 | `./agentdctl --help` | 0 | ✅ pass | 500ms |
| 3 | `./agentdctl workspace --help` | 0 | ✅ pass | 500ms |
| 4 | `./agentdctl workspace prepare --help` | 0 | ✅ pass | 500ms |
| 5 | `./agentdctl workspace list --help` | 0 | ✅ pass | 500ms |
| 6 | `./agentdctl workspace cleanup --help` | 0 | ✅ pass | 500ms |
| 7 | `./agentdctl workspace prepare --name test --type git` | 1 | ✅ pass | 500ms |
| 8 | `./agentdctl workspace prepare --name test --type local` | 1 | ✅ pass | 500ms |
| 9 | `./agentdctl workspace prepare --name test --type invalid` | 1 | ✅ pass | 500ms |
| 10 | `./agentdctl workspace cleanup` | 1 | ✅ pass | 500ms |

## Deviations

Minor implementation improvement: Moved type-specific flag validation BEFORE getClient() call (plan had validation after connection). This avoids confusing socket connection errors when the real issue is missing required flags for the specific source type.

## Known Issues

None.

## Files Created/Modified

- `cmd/agentdctl/workspace.go`
- `cmd/agentdctl/main.go`
