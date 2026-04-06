---
id: S07
parent: M001-tvc4z0
milestone: M001-tvc4z0
provides:
  - agentdctl binary for ARI operations
  - pkg/ari/client.go reusable JSON-RPC client package
  - CLI subcommands: session (7), workspace (3), daemon (1)
requires:
  - slice: S06
    provides: ARI JSON-RPC server with session/* and workspace/* methods
affects:
  - S08 (Integration Tests) - CLI available for end-to-end test scenarios
key_files:
  - pkg/ari/client.go
  - cmd/agentdctl/main.go
  - cmd/agentdctl/session.go
  - cmd/agentdctl/workspace.go
  - cmd/agentdctl/daemon.go
key_decisions:
  - Use spf13/cobra for CLI framework (standard Go CLI pattern)
  - Simplified JSON-RPC client without event handling for management commands
  - Pretty-printed JSON output to stdout for all results
  - Daemon health check via session/list RPC (lightweight probe)
  - Shared helper functions in session.go reused across files (getClient, outputJSON, handleError)
  - Type-specific flag validation before connecting to client (prevents unnecessary connections)
patterns_established:
  - JSON-RPC client pattern: NewClient -> Call -> Close for single-shot RPC calls
  - CLI helper pattern: shared getClient/outputJSON/handleError functions across subcommand files
  - Cobra persistent flags for socket path accessible to all subcommands
  - Type-specific flag validation pattern: validate required flags per type before RPC call
observability_surfaces:
  - CLI stderr output for errors with os.Exit(1)
  - Daemon status command reports running/not running state
drill_down_paths:
  []
duration: ""
verification_result: passed
completed_at: 2026-04-06T16:31:20.460Z
blocker_discovered: false
---

# S07: agentdctl CLI

**Built agentdctl CLI with 11 subcommands (7 session, 3 workspace, 1 daemon) for ARI operations using spf13/cobra, with simplified JSON-RPC client package**

## What Happened

Slice S07 built the agentdctl CLI tool for managing agentd daemon through ARI JSON-RPC interface. Four tasks executed in sequence:

**T01: ARI JSON-RPC Client Package**
Created pkg/ari/client.go with simplified JSON-RPC client for single-shot RPC calls over Unix domain sockets. The client provides NewClient(socketPath), Call(method, params, result), and Close() methods. Simplified from agent-shim-cli pattern - no event handling, just blocking request/response. The client handles JSON-RPC 2.0 protocol with ID tracking, response validation, and error unmarshaling.

**T02: Session Subcommands**
Created cmd/agentdctl/session.go with 7 session subcommands using spf13/cobra:
- session new: --workspace-id (required), --runtime-class (required), --labels, --room, --room-agent flags
- session list: no flags
- session status: positional session-id arg
- session prompt: positional session-id arg + --text (required) flag
- session stop: positional session-id arg
- session remove: positional session-id arg  
- session attach: positional session-id arg

Each command marshals params, calls ARI method via client, pretty-prints JSON result. Helper functions (getClient, outputJSON, handleError, parseLabels) defined in session.go and reused by other files.

**T03: Workspace Subcommands**
Created cmd/agentdctl/workspace.go with 3 workspace subcommands:
- workspace prepare: --name (required), --type (default emptyDir), --url/--ref/--depth (git), --path (local) flags. Validates type-specific required flags before connecting to client.
- workspace list: no flags
- workspace cleanup: positional workspace-id arg

Constructs WorkspaceSpec from flags with proper source type handling (Git/EmptyDir/Local).

**T04: Root Command + Daemon Status**
Created cmd/agentdctl/main.go with root command and persistent --socket flag (default: /var/run/agentd/ari.sock). Wired session, workspace, and daemon subcommands. Created cmd/agentdctl/daemon.go with daemon status command that checks health via session/list RPC - reports "running" if success, "not running" if error.

All commands output pretty-printed JSON to stdout. Errors printed to stderr with exit code 1. Build verification passed: go build ./cmd/agentdctl produces executable, go build ./pkg/ari/... passes, go test ./pkg/ari/... passes. CLI help output confirms all 11 subcommands are exposed with correct flags.

## Verification

Build verification:
- go build ./cmd/agentdctl passes, produces executable
- go build ./pkg/ari/... passes
- go test ./pkg/ari/... passes (cached)

CLI structure verification:
- agentdctl --help shows root command with session/workspace/daemon subcommands
- agentdctl session --help shows 7 subcommands: attach, list, new, prompt, remove, status, stop
- agentdctl workspace --help shows 3 subcommands: cleanup, list, prepare
- agentdctl daemon --help shows status subcommand
- agentdctl session new --help shows required flags: --workspace-id, --runtime-class
- agentdctl workspace prepare --help shows required --name flag, type-specific flags

Functional verification:
- CLI executes without error when no socket connection needed (help commands)
- Error handling validated: missing socket file produces connection error
- cobra validation works: missing required flags produce usage error

## Requirements Advanced

- R007 — CLI tool implemented with 11 subcommands for ARI operations (session/workspace/daemon)

## Requirements Validated

None.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

None. All tasks completed as planned.

## Known Limitations

No timeout handling in client.Call() - uses blocking I/O. Daemon status relies on session/list RPC rather than dedicated health endpoint.

## Follow-ups

None.

## Files Created/Modified

- `pkg/ari/client.go` — New file: simplified JSON-RPC client for ARI socket communication
- `cmd/agentdctl/main.go` — New file: root command with --socket persistent flag, wires subcommands
- `cmd/agentdctl/session.go` — New file: 7 session subcommands with flags and ARI method calls
- `cmd/agentdctl/workspace.go` — New file: 3 workspace subcommands with type-specific flag handling
- `cmd/agentdctl/daemon.go` — New file: daemon status command for health check
