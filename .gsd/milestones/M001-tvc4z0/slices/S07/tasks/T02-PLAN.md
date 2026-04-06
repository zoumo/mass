---
estimated_steps: 43
estimated_files: 3
skills_used: []
---

# T02: Implement session commands

Create cmd/agentdctl/session.go with 7 session subcommands using spf13/cobra. Each command marshals params, calls ARI method via client, pretty-prints JSON result.

## Steps

1. Create `cmd/agentdctl/session.go` with package main declaration
2. Import cobra, pkg/ari/client, pkg/ari/types, encoding/json, fmt, os
3. Define sessionCmd root command with Use: "session", Short: "Session management commands"
4. Implement sessionNewCmd: flags --workspace-id (required), --runtime-class (required), --labels (optional), call session/new, output SessionNewResult as JSON
5. Add sessionNewCmd to sessionCmd: sessionCmd.AddCommand(sessionNewCmd)
6. Implement sessionListCmd: no flags, call session/list, output SessionListResult as JSON
7. Add sessionListCmd to sessionCmd
8. Implement sessionStatusCmd: positional arg session-id, call session/status, output SessionStatusResult as JSON
9. Add sessionStatusCmd to sessionCmd
10. Implement sessionPromptCmd: positional arg session-id, flag --text (required), call session/prompt, output SessionPromptResult as JSON
11. Add sessionPromptCmd to sessionCmd
12. Implement sessionStopCmd: positional arg session-id, call session/stop, output success message
13. Add sessionStopCmd to sessionCmd
14. Implement sessionRemoveCmd: positional arg session-id, call session/remove, output success message
15. Add sessionRemoveCmd to sessionCmd
16. Implement sessionAttachCmd: positional arg session-id, call session/attach, output SessionAttachResult as JSON
17. Add sessionAttachCmd to sessionCmd
18. Create helper function getClient(socketPath) that calls ari.NewClient(socketPath)
19. Create helper function outputJSON(result any) that pretty-prints to stdout with indentation
20. Create helper function handleError(err error) that prints to stderr and exits

## Must-Haves

- [ ] session new command with --workspace-id, --runtime-class, --labels flags
- [ ] session list command (no flags)
- [ ] session status command with session-id positional arg
- [ ] session prompt command with session-id arg and --text flag
- [ ] session stop command with session-id arg
- [ ] session remove command with session-id arg
- [ ] session attach command with session-id arg
- [ ] All commands output pretty-printed JSON to stdout
- [ ] All commands use shared client helper
- [ ] go build ./cmd/agentdctl passes

## Failure Modes

| Dependency | On error | On timeout | On malformed response |
|------------|----------|-----------|----------------------|
| ARI client | Print error to stderr, exit 1 | No timeout | Print parse error |
| agentd daemon | Connection refused error | N/A | N/A |

## Negative Tests

- Invalid session ID: ARI returns InvalidParams error, CLI prints error
- Missing required flags: cobra validation error
- Workspace not found (FK): ARI returns InvalidParams error
- Session not running for prompt: ARI returns InvalidParams error

## Inputs

- ``pkg/ari/client.go` — ARI client for RPC calls`
- ``pkg/ari/types.go` — Session params/results types`
- ``cmd/agent-shim-cli/main.go` — Cobra pattern reference`

## Expected Output

- ``cmd/agentdctl/session.go` — Session subcommands implementation`

## Verification

go build ./cmd/agentdctl passes, commands execute against running agentd

## Observability Impact

None — CLI commands are single-shot with no runtime state
