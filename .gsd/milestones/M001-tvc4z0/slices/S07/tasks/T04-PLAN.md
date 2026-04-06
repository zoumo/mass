---
estimated_steps: 29
estimated_files: 4
skills_used: []
---

# T04: Create main entry and daemon status

Create cmd/agentdctl/main.go with root command, daemon status subcommand, and wiring for session/workspace subcommands.

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

## Inputs

- ``pkg/ari/client.go` — ARI client for RPC calls`
- ``cmd/agentdctl/session.go` — Session subcommands to wire`
- ``cmd/agentdctl/workspace.go` — Workspace subcommands to wire`

## Expected Output

- ``cmd/agentdctl/main.go` — Main entry with root command, daemon status, and subcommand wiring`

## Verification

go build ./cmd/agentdctl produces executable, agentdctl --socket <path> daemon status works

## Observability Impact

None — CLI has no runtime state
