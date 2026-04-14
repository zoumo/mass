> Auto-generated. Do not edit directly.
> Last updated: 2026-04-14 after M013

# Architecture: Open Agent Runtime (OAR)

## System Overview

OAR is a daemon-based runtime for managing AI agents on a single host. The core process (`agentd`) owns agent lifecycle, workspace provisioning, and message routing. An agent is identified by a stable `(workspace, name)` pair rather than an opaque UUID. Each running agent has one associated _session_ (internal runtime instance) backed by a child `agent-shim` process that speaks the ACP protocol. An orchestrator (or CLI) drives agentd exclusively through its ARI JSON-RPC 2.0 socket.

### Key design axioms (post-M013)
- **api/ subdirectories contain only pure types** (struct/const/enum). Interfaces and functions live in `server/` or `client/` packages.
- **Shim is the sole post-bootstrap state write authority.** After bootstrap, agentd never writes `idle/running/stopped/error` directly — all transitions flow through `runtime/stateChange` shim notifications.
- **Restart truthfulness.** Persisted bbolt metadata + live shim reconnection provides restart recovery. In-memory refcounts are rebuilt from DB; volatile in-memory state is never used as the cleanup safety gate.
- **Fail-closed recovery posture.** `session/prompt` and `session/cancel` are blocked during the recovery window; reads and stops are always available.

---

## Component Map

```
┌─────────────────────────────────────────────────────────────────────┐
│ Orchestrator / agentdctl CLI                                        │
│  ARI JSON-RPC 2.0 over Unix socket                                  │
└────────────────────────────┬────────────────────────────────────────┘
                             │
┌────────────────────────────▼────────────────────────────────────────┐
│ agentd  (cmd/agentd)                                                │
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
│  │ pkg/meta/        — bbolt store, Agent, Workspace, Runtime    │   │
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
                   │  agent-shim        │  (self-fork of agentd binary)
                   │  pkg/shim/         │
                   │   server/  — ShimService impl, Translator, EventLog
                   │   client/  — Dial, ShimClient typed wrapper        │
                   │   api/     — wire types, methods, event types      │
                   │   runtime/ — ACP runtime (acp/ subpackage)         │
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
| `pkg/runtime-spec/api/` | Pure types: `Status`, `EnvVar`, runtime config/state |
| `pkg/workspace/` | Workspace provisioning: Git/EmptyDir/Local, hooks, ref-counting |
| `pkg/ndjson/` | NDJSON streaming (shared, not shim-specific) |
| `pkg/spec/` | Build-tag socket path limits, runtime spec helpers |
| `cmd/agentdctl/` | Management CLI (cobra, resource-first grammar) |

### Binaries produced

| Binary | Purpose |
|--------|---------|
| `bin/agentd` | Main daemon + `agentd shim` self-fork entrypoint + `agentd workspace-mcp` |
| `bin/agentdctl` | Management CLI (workspace, agent, agentrun, runtime, shim subcommands) |

---

## Data Flow

### Agent create (async)

```
orchestrator → agentrun/create
  agentd: write agent row (creating), reply immediately
  goroutine: allocate session, generate config.json, fork shim
             wait for shim Unix socket
             connect ShimClient, subscribe events
             shim bootstraps ACP → stateChange(idle)
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
          stateChange(running→idle) on completion
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

Events carry `seq` (global monotonic dedup key), `turnId` (assigned at `turn_start`, cleared at `turn_end`), and `streamSeq` (resets 0 per turn). `runtime/stateChange` events are seq-only (not turn-ordered). Replay uses `(turnId, streamSeq)` within a turn, `seq` across turns.

---

## Key Constraints

1. **Unix socket path limit**: 104 bytes (macOS) / 108 bytes (Linux). Socket path overflow is validated at `agentrun/create` entry with JSON-RPC -32602 before any DB write.
2. **Shim self-fork**: `agentd shim` is the shim entrypoint — `os.Executable()` self-fork with `OAR_SHIM_BINARY` env override for tests.
3. **ON DELETE SET NULL**: `sessions.agent_id` FK uses `ON DELETE SET NULL` — session lookup must happen _before_ agent deletion, not after.
4. **Workspace ref_count safety**: `workspace/cleanup` gates on persisted DB `ref_count`, never on volatile in-memory `RefCount`. Recovery guard blocks cleanup during active recovery.
5. **Mutex + file I/O in recovery only**: `Translator.SubscribeFromSeq` holds mutex during log read + subscription registration. This is acceptable at startup/recovery (shim idle) but must not be used in hot paths.
6. **Damaged-tail tolerance**: `ReadEventLog` uses two-pass line classification — corrupt-at-tail (crash mode) is skipped; mid-file corruption errors.
7. **JSON omitempty + zero int**: `StreamSeq` is `*int` (pointer), not `int` — `int(0)` with `omitempty` is silently dropped; `*int(0)` is preserved.
8. **api/ subdirectory rule**: `api/` packages contain only `struct`, `const`, `enum`. No interfaces, no functions — those go to `server/` or `client/`.

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
| Event log format | NDJSON (`pkg/ndjson/`, `pkg/shim/server/log.go`) |
| Testing | Standard `go test`, mockagent binary (`internal/testutil/mockagent/`) |
| Lint | `golangci-lint v2` (0 issues enforced) |
| Build | `make build` → `bin/agentd` + `bin/agentdctl` |

---

## Package Layout (post-M013)

```
pkg/
  jsonrpc/          transport-agnostic JSON-RPC 2.0 server + client
  ari/
    api/            ARI wire types, domain models, method constants
    server/         ARI service interfaces, Registry, dispatch
    client/         typed ARIClient + simple Client
  shim/
    api/            shim wire types, service interface, client wrapper,
                    method constants, event types + constants
    server/         ShimService impl, Translator, EventLog + tests
    client/         Dial + ShimClient typed wrapper
    runtime/
      acp/          ACP runtime Manager (was pkg/runtime/)
  runtime-spec/
    api/            Status, EnvVar, runtime config/state (pure types)
  agentd/           ProcessManager, recovery, session lifecycle
  workspace/        WorkspaceManager, Git/EmptyDir/Local handlers, hooks
  meta/             bbolt store, Agent, Workspace, AgentRun, Runtime CRUD
  spec/             socket path limits (build tags), spec helpers
  ndjson/           NDJSON streaming utility
cmd/
  agentd/           main daemon + shim + workspace-mcp subcommands
  agentdctl/        management CLI
internal/
  testutil/mockagent/  mock ACP agent for integration tests
```
