# S07 Research: agentdctl CLI

## Summary

Build `cmd/agentdctl/main.go` — a management CLI for ARI operations using spf13/cobra. Copy and simplify agent-shim-cli's JSON-RPC client (strip event handling). Commands call ARI methods over Unix socket, output results as pretty JSON.

## Recommendation

Create a separate binary (not extending agent-shim-cli) because:
1. **Different socket**: agentdctl connects to ARI socket (agentd daemon); agent-shim-cli connects to shim socket (per-session)
2. **Different purpose**: Management tool (session/workspace lifecycle) vs interactive client (prompt/chat)

Copy agent-shim-cli's `dial()` + `call()` pattern (already proven), strip event handling since management commands are single-shot RPCs.

## Requirements Owned

| ID | Description | Delivery Approach |
|---|---|---|
| R007 | CLI tool for ARI operations: session new/list/status/prompt/stop/remove, daemon status | Implement all session/* commands + workspace commands for complete workflow |

## Implementation Landscape

### Existing CLI Pattern (cmd/agent-shim-cli/main.go)

- Uses **spf13/cobra** with subcommands
- Persistent flag `--socket` for Unix socket path
- Custom JSON-RPC client (~100 lines): `dial()`, `call()`, `notify()`, event handling
- Commands: `state`, `prompt`, `chat`, `shutdown`

### JSON-RPC Client Approach

Two options:
1. **Copy agent-shim-cli client** (simplify, remove events) — simpler, proven
2. **Use jsonrpc2 library** — more robust, what tests use

Recommendation: **Copy and simplify agent-shim-cli client**. Management commands don't need event streaming.

### ARI Method Mapping

| Command | ARI Method | Params | Result |
|---|---|---|---|
| `session new` | `session/new` | SessionNewParams | SessionNewResult (sessionId, state) |
| `session list` | `session/list` | SessionListParams | SessionListResult (sessions array) |
| `session status <id>` | `session/status` | SessionStatusParams | SessionStatusResult (session + shimState) |
| `session prompt <id>` | `session/prompt` | SessionPromptParams | SessionPromptResult (stopReason) |
| `session stop <id>` | `session/stop` | SessionStopParams | (empty result) |
| `session remove <id>` | `session/remove` | SessionRemoveParams | (empty result) |
| `session attach <id>` | `session/attach` | SessionAttachParams | SessionAttachResult (socketPath) |
| `workspace prepare` | `workspace/prepare` | WorkspacePrepareParams | WorkspacePrepareResult |
| `workspace list` | `workspace/list` | WorkspaceListParams | WorkspaceListResult |
| `workspace cleanup <id>` | `workspace/cleanup` | WorkspaceCleanupParams | (empty result) |
| `daemon status` | (health check) | — | connection status |

### Types Available (pkg/ari/types.go)

All session/workspace structs already defined:
- SessionNewParams/Result, SessionPromptParams/Result, SessionListParams/Result, etc.
- WorkspacePrepareParams/Result, WorkspaceListParams/Result, WorkspaceCleanupParams

### Socket Configuration

- agentd config.yaml specifies socket path (field: `socket`)
- Default in cmd/agentd/main.go: `--config /etc/agentd/config.yaml`
- CLI should accept `--socket` flag (like agent-shim-cli) or `--config` to derive path

### Output Format

Pretty-print JSON to stdout. User can pipe through `jq` for custom formatting.

## Natural Seam: Task Decomposition

### T01: JSON-RPC Client Package (pkg/ari/client.go)

Create reusable ARI client package:
- `NewClient(socketPath) (*Client, error)` — connect to ARI socket
- `Call(method, params, result)` — single-shot RPC call
- No event handling (simpler than agent-shim-cli)
- ~50 lines, extracted from agent-shim-cli pattern

**Why package, not inline:** Tests can reuse it; future tools can reuse it; cleaner separation.

### T02: Session Commands (cmd/agentdctl/session.go)

Implement session subcommands:
- `session new --workspace-id <id> --runtime-class <class> [--labels key=value]`
- `session list`
- `session status <session-id>`
- `session prompt <session-id> --text "prompt"`
- `session stop <session-id>`
- `session remove <session-id>`
- `session attach <session-id>` — returns shim socket path

Each command calls one ARI method, outputs result.

### T03: Workspace Commands (cmd/agentdctl/workspace.go)

Implement workspace subcommands (needed for session workflow):
- `workspace prepare --name <name> --type emptydir`
- `workspace list`
- `workspace cleanup <workspace-id>`

### T04: Daemon Command + Main Entry (cmd/agentdctl/main.go)

Root command + daemon status:
- `agentdctl --socket <path> <command>`
- `daemon status` — health check (try to call session/list)

## Constraints & Gotchas

1. **FK constraint**: session/new requires workspaceId to exist in database. CLI workflow must include workspace prepare first.
2. **session/prompt non-interactive**: Returns stopReason only; doesn't stream agent output. For interactive chat, use session/attach + agent-shim-cli.
3. **session/list no label filtering**: S06 summary notes this limitation.
4. **Error codes**: InvalidParams (client error) vs InternalError (system failure).
5. **Socket path**: Default could be `/etc/agentd/ari.sock` or require `--socket` flag.

## Dependencies

- spf13/cobra (already in go.mod)
- sourcegraph/jsonrpc2 (tests use it, but CLI can use simpler client)

## Verification

- Manual testing: start agentd, run commands
- Build verification: `go build ./cmd/agentdctl`
- Integration: commands work against live agentd