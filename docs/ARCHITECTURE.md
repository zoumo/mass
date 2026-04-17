> Auto-generated. Do not edit directly.
> Last updated: 2026-04-15 after M014

# Architecture: Multi-Agent Supervision System (MASS)

## System Overview

MASS is a daemon-based runtime for managing AI agents on a single host. The core process (`mass`) owns agent lifecycle, workspace provisioning, and message routing. An agent is identified by a stable `(workspace, name)` pair rather than an opaque UUID. Each running agent has one associated _session_ (internal runtime instance) backed by a child `agent-run` process that speaks the ACP protocol. An orchestrator (or CLI) drives agentd exclusively through its ARI JSON-RPC 2.0 socket.

### Key design axioms (post-M014)
- **api/ subdirectories contain only pure types** (struct/const/enum). Interfaces and functions live in `server/` or `client/` packages.
- **Shim is the sole post-bootstrap state write authority.** After bootstrap, agentd never writes `idle/running/stopped/error` directly — all transitions flow through `runtime/event_update` shim notifications.
- **Restart truthfulness.** Persisted bbolt metadata + live shim reconnection provides restart recovery. In-memory refcounts are rebuilt from DB; volatile in-memory state is never used as the cleanup safety gate.
- **Fail-closed recovery posture.** `session/prompt` and `session/cancel` are blocked during the recovery window; reads and stops are always available.
- **state.json is a reliable session capability snapshot.** `agentInfo`, `capabilities`, `availableCommands`, `configOptions`, `sessionInfo`, `currentMode` are populated from ACP notifications; `eventCounts` tracks all event types; `updatedAt` is stamped on every write. `writeState` uses read-modify-write closures so Kill/exit never clobbers session metadata.
- **Metadata changes emit runtime_update events.** Each metadata-only change (config_option, available_commands, session_info, current_mode) emits exactly one `runtime_update` event with a `sessionChanged` field. `updatedAt` and `eventCounts` are derived fields that never independently trigger events.

---

## Component Map

```
┌─────────────────────────────────────────────────────────────────────┐
│ Orchestrator / massctl CLI                                        │
│  ARI JSON-RPC 2.0 over Unix socket                                  │
└────────────────────────────┬────────────────────────────────────────┘
                             │
┌────────────────────────────▼────────────────────────────────────────┐
│ agentd  (cmd/mass)                                                  │
│                                                                     │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │ pkg/ari/server/  — ARI service (WorkspaceService,            │   │
│  │                    AgentRunService, AgentService adapters)   │   │
│  │ pkg/ari/api/     — ARI wire types, domain models, methods    │   │
│  │ pkg/ari/client/  — typed + simple ARI clients                │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                                                                     │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │ pkg/agentd/      — ProcessManager, recovery, agent lifecycle │   │
│  │   process.go     — fork shim, watch, restart, generateConfig │   │
│  │   recovery.go    — RecoverSessions, tryReload / alwaysNew    │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                                                                     │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │ pkg/workspace/   — WorkspaceManager, Git/EmptyDir/Local      │   │
│  │                    handlers, hooks, ref-counting             │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                                                                     │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │ pkg/agentd/store/ — bbolt store, Agent, Workspace, Runtime   │   │
│  │                    CRUD; composite key (workspace/name)      │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                                                                     │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │ pkg/jsonrpc/     — transport-agnostic JSON-RPC 2.0 framework │   │
│  │                    (Server + Client, interceptors, Peer)     │   │
│  └──────────────────────────────────────────────────────────────┘   │
└────────────────────────────┬────────────────────────────────────────┘
                             │  Unix socket per session
                   ┌─────────▼──────────┐
                   │  agent-run         │  (self-fork of mass binary)
                   │  pkg/agentrun/     │
                   │   server/  — service impl, Translator,         │
                   │              EventLog, session metadata hooks   │
                   │   client/  — Dial, typed client wrapper        │
                   │   api/     — wire types, methods, event types  │
                   │   runtime/ — ACP runtime (acp/ subpackage)     │
                   └─────────┬──────────┘
                             │  ACP JSON-RPC
                   ┌─────────▼──────────┐
                   │  AI Agent process  │
                   │  (e.g. claude-code,│
                   │   gsd-pi, mockagent│
                   └────────────────────┘
```

### Supporting packages

| Package | Role |
|---------|------|
| `pkg/runtime-spec/api/` | Pure types: `Status`, `EnvVar`, runtime config/state, `SessionState` + all session metadata sub-types (`AgentInfo`, `AgentCapabilities`, `AvailableCommand`, `ConfigOption`, etc.) |
| `pkg/workspace/` | Workspace provisioning: Git/EmptyDir/Local, hooks, ref-counting |
| `pkg/jsonrpc/ndjson/` | NDJSON streaming (shared) |
| `cmd/massctl/` | Management CLI (cobra, resource-first grammar) |

### Binaries produced

| Binary | Purpose |
|--------|---------|
| `bin/mass` | Main daemon + `mass run` self-fork entrypoint + `mass workspace-mcp` |
| `bin/massctl` | Management CLI (workspace, agent, agentrun, compose, daemon subcommands) |

---

## Data Flow

### Agent create (async)

```
orchestrator → agentrun/create
  agentd: write agent row (creating), reply immediately
  goroutine: allocate session, generate config.json, fork shim
             wait for shim Unix socket
             connect ShimClient, subscribe events
             shim bootstraps ACP → runtime_update(idle)
             agentd DB update: status=idle
orchestrator → agentrun/status (poll until idle or error)
```

### Turn delivery

```
orchestrator → agentrun/prompt
  agentd: auto-start if creating; deliverPrompt helper
          ShimClient.Prompt() → shim session/prompt
          shim → ACP agent process
          ACP events → Translator → Envelope (seq, turnId, streamSeq)
          live subscribers receive agent/update events
          runtime_update(running→idle) on completion
  → returns stopReason
```

### Restart recovery

```
agentd start → RecoverSessions()
  for each non-terminal session:
    try runtime/status on persisted shim socket
    if alive: DisconnectNotify watcher; SubscribeFromSeq(lastSeq)
    if dead:  mark stopped (fail-closed)
  set RecoveryPhase=Complete
  rebuild Registry + WorkspaceManager refcounts from DB
```

### Event ordering

Events carry `seq` (global monotonic dedup key), `turnId` (assigned at `turn_start`, cleared at `turn_end`), and `streamSeq` (resets 0 per turn). `runtime/event_update` events are seq-only (not turn-ordered). Replay uses `(turnId, streamSeq)` within a turn, `seq` across turns.

### Session metadata pipeline (post-M014)

```
ACP agent → SessionNotification (config_option, available_commands, etc.)
  Translator.translate() → typed ShimEvent
  Translator.broadcastSessionEvent(ev)  [under Translator.mu]
    → eventCounts[ev.Type]++
    → fan-out to live subscribers
  Translator.mu released
  Translator.maybeNotifyMetadata(ev)    [type-switch gate, 4 types]
    → sessionMetadataHook(ev)
    → Manager.UpdateSessionMetadata(changed, reason, apply)
      → read-modify-write state.json under Manager.mu
      → flush EventCounts
      → emit runtime_update with sessionChanged field
runtime/status → Status()
  → read state.json from disk
  → overlay real-time EventCounts from Translator memory
  → return enriched State
```

---

## Key Constraints

1. **Unix socket path limit**: 104 bytes (macOS) / 108 bytes (Linux). Socket path overflow is validated at `agentrun/create` entry with JSON-RPC -32602 before any DB write.
2. **Agent-run self-fork**: `mass run` is the agent-run entrypoint — `os.Executable()` self-fork with `MASS_SHIM_BINARY` env override for tests.
3. **ON DELETE SET NULL**: `sessions.agent_id` FK uses `ON DELETE SET NULL` — session lookup must happen _before_ agent deletion, not after.
4. **Workspace ref_count safety**: `workspace/cleanup` gates on persisted DB `ref_count`, never on volatile in-memory `RefCount`. Recovery guard blocks cleanup during active recovery.
5. **Mutex + file I/O in recovery only**: `Translator.SubscribeFromSeq` holds mutex during log read + subscription registration. This is acceptable at startup/recovery (shim idle) but must not be used in hot paths.
6. **Damaged-tail tolerance**: `ReadEventLog` uses two-pass line classification — corrupt-at-tail (crash mode) is skipped; mid-file corruption errors.
7. **JSON omitempty + zero int**: `StreamSeq` is `*int` (pointer), not `int` — `int(0)` with `omitempty` is silently dropped; `*int(0)` is preserved.
8. **api/ subdirectory rule**: `api/` packages contain only `struct`, `const`, `enum`. No interfaces, no functions — those go to `server/` or `client/`.
9. **runtime-spec/api independence**: `pkg/runtime-spec/api` must NOT import `pkg/agentrun/api`. Union types are copied with `state:` error prefixes, not shared.
10. **writeState closure invariant**: All state.json mutations go through `func(*apiruntime.State)` closures — never construct State literals directly. `UpdatedAt` and `EventCounts` are derived fields stamped after the closure runs.
11. **Lock order for metadata pipeline**: `Translator.mu → release → Manager.mu → release → Translator.mu` (via NotifyStateChange). No nested lock acquisition.
12. **EventCounts recursion guard**: `updatedAt` and `eventCounts` never appear in `sessionChanged` and never cause independent `runtime_update` emission (avoids infinite recursion).

---

## Tech Stack

| Layer | Technology |
|-------|-----------|
| Language | Go (1.21+) |
| RPC framework | `pkg/jsonrpc/` (transport-agnostic, wraps `sourcegraph/jsonrpc2`) |
| Metadata store | bbolt (pure-Go, embedded key-value; bucket hierarchy `v1/workspaces`, `v1/agents`, `v1/agentruns`, `v1/runtimes`) |
| Agent protocol | ACP (Agent Communication Protocol) JSON-RPC over stdio |
| Shim ↔ agentd | Custom JSON-RPC 2.0 over Unix socket (`session/*`, `runtime/*` methods) |
| CLI | `spf13/cobra` (resource-first grammar) |
| UUID generation | `github.com/google/uuid` (workspace IDs) |
| Event log format | NDJSON (`pkg/jsonrpc/ndjson/`) |
| Testing | Standard `go test`, mockagent binary (`internal/testutil/mockagent/`) |
| Lint | `golangci-lint v2` (0 issues enforced) |
| Build | `make build` → `bin/mass` + `bin/massctl` |

---

## Package Layout

```
pkg/
  jsonrpc/          transport-agnostic JSON-RPC 2.0 server + client
    ndjson/         NDJSON streaming utility
  ari/
    api/            ARI wire types, domain models, method constants
    server/         ARI service interfaces, Registry, dispatch
    client/         typed ARIClient + simple Client
  agentrun/
    api/            wire types, method constants, event types + constants
    server/         service impl, Translator, EventLog
    client/         Dial, typed client wrapper, Watcher
    runtime/
      acp/          ACP runtime Manager
  runtime-spec/
    api/            Status, EnvVar, runtime config/state (pure types),
                    SessionState, AgentInfo, AgentCapabilities,
                    AvailableCommand, ConfigOption, SessionInfo
  agentd/           ProcessManager, recovery, bbolt metadata store
  workspace/        WorkspaceManager, Git/EmptyDir/Local handlers, hooks
  tui/              Terminal UI components
cmd/
  mass/             main daemon + run + workspace-mcp subcommands
    commands/
      run/          agent-run bootstrap wiring
      server/       daemon server
      workspacemcp/ workspace MCP server
  massctl/          management CLI
    commands/
      agentrun/     agentrun chat/debug
      agent/        agent CRUD
      workspace/    workspace management
      compose/      multi-agent compose
      daemon/       daemon control
internal/
  testutil/mockagent/  mock ACP agent for integration tests
```
