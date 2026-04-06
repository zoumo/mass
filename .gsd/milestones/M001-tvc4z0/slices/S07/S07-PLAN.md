# S07: agentdctl CLI

**Goal:** Build cmd/agentdctl CLI for ARI operations using spf13/cobra. Commands call ARI methods over Unix socket, output results as pretty JSON. Session commands: new/list/status/prompt/stop/remove/attach. Workspace commands: prepare/list/cleanup. Daemon status for health check.
**Demo:** After this: agentdctl CLI can manage sessions through ARI: new/list/prompt/stop/remove

## Tasks
- [x] **T01: Created simplified ARI JSON-RPC client package with NewClient/Call/Close methods for single-shot management commands.** — Create pkg/ari/client.go with reusable JSON-RPC client for ARI socket communication. Simplified from agent-shim-cli pattern: dial() + call() only, no event handling. Single-shot RPC calls for management commands.

## Steps

1. Read `cmd/agent-shim-cli/main.go` to understand JSON-RPC client pattern (dial, call, notify functions)
2. Create `pkg/ari/client.go` with package declaration and imports (net, encoding/json, fmt)
3. Define rpcRequest struct with JSONRPC, ID, Method, Params fields
4. Define rpcResponse struct with JSONRPC, ID, Result, Error fields
5. Define rpcError struct with Code and Message fields
6. Define Client struct with conn (net.Conn), encoder, decoder, mutex, nextID fields
7. Implement NewClient(socketPath string) (*Client, error) — dial Unix socket, initialize encoder/decoder, return client
8. Implement Call(method string, params any, result any) error — send request with ID, wait for response, unmarshal result
9. Implement Close() error — close connection
10. Run `go build ./pkg/ari/...` to verify compilation

## Must-Haves

- [ ] NewClient(socketPath) connects to Unix socket and returns Client
- [ ] Call(method, params, result) sends JSON-RPC request and unmarshals response
- [ ] No event handling (simplified from agent-shim-cli)
- [ ] go build ./pkg/ari/... passes

## Failure Modes

| Dependency | On error | On timeout | On malformed response |
|------------|----------|-----------|----------------------|
| ARI socket | Return connection error | No timeout (blocking) | Return parse error |

## Negative Tests

- Socket file missing: NewClient returns error
- Daemon unavailable: connection refused error
- Malformed JSON response: Call returns parse error
- RPC error response: Call returns error with code/message
  - Estimate: 1h
  - Files: pkg/ari/client.go, pkg/ari/types.go
  - Verify: go build ./pkg/ari/... passes, go test ./pkg/ari/... passes
- [x] **T02: Implemented 7 session subcommands for agentdctl CLI with cobra** — Create cmd/agentdctl/session.go with 7 session subcommands using spf13/cobra. Each command marshals params, calls ARI method via client, pretty-prints JSON result.

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
  - Estimate: 2h
  - Files: cmd/agentdctl/session.go, pkg/ari/client.go, pkg/ari/types.go
  - Verify: go build ./cmd/agentdctl passes, commands execute against running agentd
- [x] **T03: Implemented 3 workspace subcommands for agentdctl CLI with cobra** — Create cmd/agentdctl/workspace.go with 3 workspace subcommands using spf13/cobra. Each command marshals params, calls ARI method via client, pretty-prints JSON result.

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
  - Estimate: 1h
  - Files: cmd/agentdctl/workspace.go, pkg/ari/client.go, pkg/ari/types.go, pkg/workspace/spec.go
  - Verify: go build ./cmd/agentdctl passes, commands execute against running agentd
- [x] **T04: Created daemon status command for agentdctl CLI to check daemon health via session/list RPC** — Create cmd/agentdctl/main.go with root command, daemon status subcommand, and wiring for session/workspace subcommands.

## Steps

1. Create `cmd/agentdctl/main.go` with package main declaration
2. Import cobra, os, pkg/ari/client
3. Define rootCmd with Use: "agentdctl", Short: "CLI for agentd daemon management"
4. Add persistent flag --socket to rootCmd: rootCmd.PersistentFlags().StringVar(&socketPath, "socket", "/var/run/agentd/ari.sock", "ARI socket path")
5. Implement daemonStatusCmd: Use "daemon status", Short "Check daemon health", RunE: call session/list via client, report "running" if success or "not running" if error
6. Add daemonStatusCmd to rootCmd: rootCmd.AddCommand(daemonStatusCmd)
7. Add sessionCmd from session.go to rootCmd: rootCmd.AddCommand(sessionCmd)
8. Add workspaceCmd from workspace.go to rootCmd: rootCmd.AddCommand(workspaceCmd)
9. Execute rootCmd in main(): if err := rootCmd.Execute(); err != nil { os.Exit(1) }
10. Run `go build ./cmd/agentdctl` to verify compilation
11. Test against running agentd: ./agentdctl --socket <path> daemon status

## Must-Haves

- [ ] Root command with --socket persistent flag (default: /var/run/agentd/ari.sock)
- [ ] daemon status subcommand that checks daemon health
- [ ] session subcommand wired from session.go
- [ ] workspace subcommand wired from workspace.go
- [ ] go build ./cmd/agentdctl produces executable
- [ ] agentdctl --socket <path> daemon status works

## Failure Modes

| Dependency | On error | On timeout | On malformed response |
|------------|----------|-----------|----------------------|
| ARI socket | Print connection error | No timeout | Print parse error |
| agentd daemon | daemon status reports "not running" | N/A | N/A |

## Negative Tests

- Socket file missing: connection error, daemon status reports "not running"
- Daemon not running: daemon status reports "not running"
- Invalid socket path: connection error
  - Estimate: 30m
  - Files: cmd/agentdctl/main.go, cmd/agentdctl/session.go, cmd/agentdctl/workspace.go, pkg/ari/client.go
  - Verify: go build ./cmd/agentdctl produces executable, agentdctl --socket <path> daemon status works
