# Open Agent Runtime (OAR)

A layered agent runtime architecture inspired by [containerd/runc](https://github.com/containerd/containerd). Manages agent processes through a shim layer, high-level daemon, and orchestrator layer for multi-agent coordination.

## Overview

Open Agent Runtime enables reliable, observable, headless agent execution with proper lifecycle management — from single agent sessions to multi-agent rooms with shared workspaces and inter-agent communication.

## Architecture

```
orchestrator (room lifecycle, multi-agent coordination)
    ↓ ARI protocol
agentd (session/workspace/process/room management)
    ↓ shim RPC
agent-shim (single agent process management)
    ↓ ACP protocol
agent (claude-code, gemini-cli, gsd, etc.)
```

This layered architecture mirrors containerd's approach:
- **Session** = metadata, **Process** = execution (containerd Container/Task separation)
- **RuntimeClass** registry (K8s RuntimeClass pattern for agent type resolution)
- **Typed events** (ACP is implementation detail, typed events are core protocol)
- **Unix socket RPC** (agentd ↔ shim, orchestrator ↔ agentd)

## Components

### Phase 1 — agent-shim Layer
- `pkg/spec` — OAR Runtime Spec types, config parsing, state management
- `pkg/runtime` — Manager: agent process lifecycle, ACP handshake, permissions
- `pkg/events` — Typed event stream, EventLog (JSONL), ACP→Event translator
- `pkg/rpc` — JSON-RPC 2.0 server over Unix socket (shim RPC)
- `cmd/agent-shim` — CLI entry point with full startup flow
- `cmd/agent-shim-cli` — Interactive management client

### Phase 2 — agentd Core
- `cmd/agentd` — High-level daemon with config parsing, signal handling, ARI server
- `pkg/meta` — SQLite metadata store with WAL mode, embedded schema
- `pkg/agentd` — RuntimeClassRegistry, SessionManager, ProcessManager
- `pkg/ari` — ARI JSON-RPC server with session/workspace methods
- `cmd/agentdctl` — CLI for ARI operations

### Phase 3 — Workspace Manager
- `pkg/workspace` — WorkspaceSpec types, source handlers, hook execution
- Git, EmptyDir, Local source handlers
- Setup/teardown hooks with sequential execution

## Build

```bash
# Build all binaries
go build ./...

# Binaries are output to bin/
# - bin/agent-shim: Shim layer executable
# - bin/agentd: High-level daemon
# - bin/mockagent: Test agent implementation
```

## Quick Start

### 1. Start agentd daemon

```bash
# Create a config file
cat > config.yaml << 'EOF'
socket: /tmp/agentd.sock
workspaceRoot: /tmp/oar-workspaces
metadataDB: /tmp/agentd.db
runtimeClasses:
  mockagent:
    command: bin/mockagent
    args: []
    env: {}
EOF

# Start agentd
bin/agentd -config config.yaml
```

### 2. Use agentdctl to manage sessions

```bash
# List sessions
bin/agentdctl session list -socket /tmp/agentd.sock

# Create a session
bin/agentdctl session new -socket /tmp/agentd.sock -workspace /path/to/workspace -runtime-class mockagent

# Prompt a session
bin/agentdctl session prompt -socket /tmp/agentd.sock -session <session-id> -message "Hello"

# Stop a session
bin/agentdctl session stop -socket /tmp/agentd.sock -session <session-id>

# Remove a session
bin/agentdctl session remove -socket /tmp/agentd.sock -session <session-id>
```

## Current Status

| Phase | Status | Description |
|-------|--------|-------------|
| Phase 1 | ✅ Complete | agent-shim layer with ACP protocol |
| Phase 2 | ✅ Complete | agentd daemon with ARI interface |
| Phase 3 | ✅ Complete | Workspace Manager with Git/EmptyDir/Local handlers |
| Phase 4 | 🔜 Planned | Orchestrator (room lifecycle, multi-agent coordination) |
| Phase 5 | 🔜 Planned | Warm/Cold pause lifecycle optimization |

### Integration Tests

11 integration tests pass covering:
- Full pipeline: agentd → agent-shim → mockagent
- Session lifecycle: created → running → stopped
- Concurrent sessions
- Error handling (prompt on stopped session, remove on running session)

## ARI Protocol

The Agent Runtime Interface (ARI) is a JSON-RPC 2.0 protocol for managing agents:

### Session Methods
- `session/new` — Create a new session
- `session/prompt` — Send a prompt to the session
- `session/stop` — Stop a running session
- `session/remove` — Remove a session
- `session/list` — List all sessions
- `session/status` — Get session status

### Workspace Methods
- `workspace/prepare` — Prepare a workspace from spec
- `workspace/list` — List prepared workspaces
- `workspace/cleanup` — Cleanup a workspace

## Requirements

See `.gsd/REQUIREMENTS.md` for the full capability contract and validation status.

## License

MIT