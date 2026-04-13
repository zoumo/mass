# OAR Development Roadmap

## Current Implementation Status

The following layers are implemented and in production use:

```
Implemented:
  api/               — Pure API type definitions (Status, EnvVar, meta objects, spec objects);
                       no I/O, no business logic, no external dependencies
  api/meta/          — API types in api/meta: Agent, AgentRun, Workspace, ObjectMeta, phase constants
  api/spec/          — API types in api/spec: Config, State, PermissionPolicy, ACP types
  api/ari/           — ARI JSON-RPC wire types: all Params/Result/Info types for workspace/*, agent/*,
                       agentrun/* methods, plus CodeRecoveryBlocked error code constant
  pkg/spec           — OAR Runtime Spec I/O: config parsing, state file read/write; types in api/spec
  pkg/runtime        — agent process lifecycle, ACP handshake, permissions
  pkg/events         — Typed event stream, EventLog (JSONL), ACP→Event translator
  pkg/rpc            — JSON-RPC 2.0 server over Unix socket (shim RPC: session/* + runtime/*)
  pkg/workspace      — Workspace Manager: git/emptyDir/local source handlers, hook execution,
                       reference counting, cleanup
  pkg/store          — Metadata persistence (bbolt): Agent, AgentRun, Workspace CRUD; types in api/meta
  pkg/agentd         — agentd subsystems: Agent Manager, Process Manager, RuntimeClass Registry,
                       Recovery (shim reconnect on daemon restart), recovery posture gating
  pkg/ari            — ARI JSON-RPC server: workspace/*, agent/*, agentrun/* method handlers,
                       ARI client for agentdctl
  cmd/agentd         — agentd binary with server/shim/workspacemcp subcommands
  cmd/agentdctl      — agentdctl CLI: workspace/agent/agentrun/daemon/shim/agenttemplate subcommands
  tests/integration  — End-to-end integration tests: session lifecycle, restart/recovery,
                       concurrent sessions, real CLI tests
```

### ARI Method Surface (Implemented)

| Group | Methods |
|---|---|
| `workspace/*` | `workspace/create`, `workspace/status`, `workspace/list`, `workspace/delete`, `workspace/send` |
| `agent/*` | `agent/set`, `agent/get`, `agent/list`, `agent/delete` (Agent CRUD) |
| `agentrun/*` | `agentrun/create`, `agentrun/prompt`, `agentrun/cancel`, `agentrun/stop`, `agentrun/delete`, `agentrun/restart`, `agentrun/list`, `agentrun/status`, `agentrun/attach` |

### Shim RPC Surface (Implemented)

Production shim server registers: `session/prompt`, `session/cancel`, `session/subscribe`, `runtime/status`, `runtime/history`, `runtime/stop`.

Live notifications: `shim/event` (unified — replaces `session/update` + `runtime/state_change`).

---

## Gaps and Future Work

### In Progress / Targeted

| Area | Gap |
|---|---|
| Terminal operations | `terminal/execute`, `terminal/read_output`, `terminal/set_timeout` stub in `pkg/runtime/client.go` — not yet wired |
| `session/load` (warm resume) | agentd client calls it in `try_reload` recovery policy; production shim server does not register it yet — calls return `MethodNotFound` |

### Future Work

| Area | Description |
|---|---|
| **Room** | Shared-workspace group management and messaging bus (`room/*` ARI methods). Not implemented; no `pkg/agentd/room`, no room-spec. |
| **workspace task/inbox** | Structured task delegation (`workspace/taskCreate` etc.), auto-reply, Inbox queuing. Not implemented. |
| **ARI-level event fanout** | Streaming `session/update` events directly to ARI clients without requiring shim socket connection. |
| **AgentRun-level env override** | `agentrun/create` has no `env` field; only Agent definition env is used. |
| **Hook output via ARI** | Workspace hook stdout/stderr is captured but not returned through `workspace/status`. |
| **OAR runtime ID ↔ ACP sessionId mapping** | Restart diagnostics: record which inner ACP session belongs to which AgentRun. |
| **Event log rotation** | Currently unbounded append to `events.jsonl`. |
| **Cold pause / warm pause** | Lifecycle states beyond `idle`/`running`/`stopped` for session hibernation. |
| **Agent definition `description` / `capabilities`** | Agent capability discovery via `workspace/status` members. |

---

## Architecture Layers

```
Layer 3 — External Caller (outside OAR scope)
    decides desired state, calls ARI

Layer 2 — agentd + ARI  [implemented]
    workspace/*, agent/*, agentrun/* method handlers
    Workspace Manager, Agent Manager, Agent Manager, Process Manager
    bbolt metadata store, daemon recovery

Layer 1 — agent-shim  [implemented]
    session/* + runtime/* shim RPC
    ACP client, typed event stream, bundle materialization

Layer 0 — Agent process  [external]
    ACP over stdio (claude-acp, pi-acp, gemini, ...)
```
