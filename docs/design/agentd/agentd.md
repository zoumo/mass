# agentd — runtime realization daemon

agentd is the daemon that realizes already-decided runtime objects.
It owns workspaces, agents, sessions (internal runtime instances), process supervision, and the realized room projection needed for routing and inspection.
It does **not** own orchestrator intent.

## Desired vs Realized

| Concern | Primary owner | agentd role |
|---|---|---|
| Which Room should exist | orchestrator / `docs/design/orchestrator/room-spec.md` | realize it if asked |
| Which agents should be members | orchestrator | store realized `room` / `name` membership on agents |
| When work is complete | orchestrator | expose runtime state only |
| Workspace preparation and cleanup | workspace manager | authoritative runtime execution |
| Agent lifecycle and identity | Agent Manager | external lifecycle authority |
| Session bootstrap and process truth | Session Manager (internal) | internal runtime execution |
| ACP protocol details | runtime / shim | hidden behind shim and translated for ARI |

## Internal Subsystems

### Workspace Manager

Workspace Manager realizes a workspace spec into a runtime workspace identity and path.
It owns:

- source realization (`git`, `emptyDir`, `local`);
- canonical path registration;
- hook execution;
- shared-workspace reference tracking;
- cleanup rules for managed vs unmanaged workspaces.

Important boundary rules:

- **local workspace** paths are host paths and must be validated/canonicalized before use;
- workspace hooks are host commands, not in-agent work;
- managed workspaces may be deleted on cleanup, local workspaces may not;
- shared workspace reuse is explicit and reference-counted.

### Agent Manager

Agent Manager owns the **external** lifecycle of agents.
An agent is the durable, externally-visible runtime object that records:

- identity: `room` + `name` (unique key — all agents belong to a room);
- selected `runtimeClass`;
- attached `workspaceId`;
- `description` and bootstrap inputs (`systemPrompt`, `env`, `mcpServers`, `permissions`, `labels`);
- external lifecycle state (`creating`, `created`, `running`, `stopped`, `error`).

Agent identity (`room` + `name`) is stable across restarts.
`agent/create` is the external entry point; the Agent Manager allocates an `agentId`, writes durable agent metadata, and triggers the internal bootstrap sequence.

### Session Manager (internal)

Session Manager is an **internal** subsystem.
It is not exposed in the external ARI surface.

Session Manager tracks the internal runtime instance (shim process) for each agent:

- internal `sessionId` (runtime-scoped, not externally meaningful);
- shim socket path and state directory;
- shim process PID;
- current runtime process state.

The Session Manager exposes no methods to ARI callers.
Its state is visible only through agent status queries.

### Process Manager

Process Manager realizes an agent into an actual runtime process through the shim.
It owns:

- bundle materialization;
- runtime startup and shutdown;
- runtime-state inspection;
- reconnect to existing shim processes;
- typed event subscription.

### Realized Room Manager

Room handling inside agentd is **realized runtime state**, not orchestrator desired state.
The runtime room record is a projection used for:

- realized Room name / labels / communication mode;
- member-to-agent mapping;
- shared-workspace attachment visibility;
- future routing and inspection APIs.

Room state in agentd does **not** decide desired membership or completion policy.
It exists so the runtime can say what is currently realized.

## Agent Identity

All agents must belong to a room.
The `(room, name)` pair is the stable external identity for an agent.

- `room` — the realized Room this agent is a member of;
- `name` — the member name inside that Room (e.g. `architect`, `coder`).

Together they form a unique key within agentd.
External callers refer to agents by `(room, name)`.
`agentId` is an internal opaque identifier allocated at `agent/create` time.

## Agent State Machine

```
creating ──┐
           ├──> created ──> running ──> stopped
           |
    error <─┴──────────────────┘
```

| State | Meaning |
|---|---|
| `creating` | `agent/create` accepted; background bootstrap in progress |
| `created` | Bootstrap complete; agent is idle, ready for a prompt |
| `running` | Agent is processing an active prompt turn |
| `stopped` | Agent process is stopped; state is preserved |
| `error` | Bootstrap or runtime failure; agent is not operational |

Transition rules:

- `creating → created`: bootstrap succeeds (shim connected, ACP initialized);
- `creating → error`: bootstrap fails;
- `created → running`: `agent/prompt` received;
- `running → created`: prompt turn completes (agent returns to idle);
- `running → stopped`: `agent/stop` received while running;
- `created → stopped`: `agent/stop` received while idle;
- `running → error`: runtime failure during a turn;
- `stopped → creating`: `agent/restart` triggers re-bootstrap from existing state.

The states `paused:warm` and `paused:cold` do not exist in this state machine.

## Bootstrap Contract

The converged bootstrap story for agent creation:

1. `workspace/prepare` returns `workspaceId` plus a realized canonical host path.
2. `agent/create` is **async configuration-only bootstrap**.
3. agentd allocates `agentId`, validates `room`, `name`, `runtimeClass`, `workspaceId`, and records agent metadata.
4. agentd writes the bundle (`config.json`) and wires `agentRoot.path` to the prepared workspace.
5. The runtime resolves bundle-relative `agentRoot.path` to a canonical `cwd`, performs ACP bootstrap (`initialize`, ACP `session/new`), and reaches bootstrap-complete / idle (`created`) state.
6. Actual work arrives later through `agent/prompt`.
7. Callers poll `agent/status` until state transitions out of `creating`.

`agent/create` returns immediately with `state: "creating"`.
It does **not** wait for bootstrap to complete.

## Async Create Semantics

`agent/create` is non-blocking.
The response is:

```json
{ "agentId": "agent-abc123", "state": "creating" }
```

The caller polls `agent/status` to determine when the agent is ready:

```json
{ "agentId": "agent-abc123", "state": "created" }
```

Bootstrap errors surface as:

```json
{ "agentId": "agent-abc123", "state": "error", "errorMessage": "..." }
```

## Stop and Delete Separation

`agent/stop` and `agent/delete` are distinct operations:

| Operation | Effect | Requires |
|---|---|---|
| `agent/stop` | Stops the runtime process; preserves agent metadata and state | Agent in `created` or `running` state |
| `agent/delete` | Removes agent record and releases resources | Agent must be in `stopped` state |

`agent/stop` does not delete.
`agent/delete` requires a prior `agent/stop`.

Workspace references are released by `agent/delete`, not `agent/stop`.

## Restart

`agent/restart` re-bootstraps an agent from `stopped` state:

1. Validates agent is `stopped`.
2. Transitions agent to `creating`.
3. Triggers background re-bootstrap using existing agent metadata.
4. Caller polls `agent/status` until `created` or `error`.

Restart preserves `room`, `name`, `workspaceId`, and bootstrap configuration.
It does not create a new agent identity.

## Environment and Capability Posture

agentd must describe one env precedence order across the design set:

1. inherited daemon/host environment forms the base;
2. `runtimeClass.env` overlays the base;
3. `agent/create` env overrides overlay last.

The resolved env snapshot is runtime bootstrap state and is a follow-on persistence concern under R036.

Capability posture is also explicit:

- ACP remains the inner protocol between shim and agent;
- agentd exposes a curated ARI surface (`agent/*`, `workspace/*`, realized `room/*`, attach notifications);
- raw ACP client responsibilities such as `fs/*`, `terminal/*`, or low-level protocol negotiation remain behind the shim boundary and are governed by permission policy rather than by direct ARI passthrough.

## Shared Workspace Semantics

Multiple agents may intentionally point at one `workspaceId`.
That includes realized Room members.
The runtime guarantees reference tracking and cleanup safety, but **not** per-agent filesystem isolation.
If several members share a workspace, they share read/write impact on the same host path.

## Runtime Bootstrap Flow

```text
orchestrator
  -> workspace/prepare(spec)
  <- { workspaceId, path }

orchestrator
  -> room/create(...)              # realized runtime projection, optional to desired-state model but owned here if used
  <- { name, status }

orchestrator
  -> agent/create(...)             # async configuration-only bootstrap
  <- { agentId, state:"creating" }

agentd (background)
  -> materialize bundle + resolve cwd + ACP initialize + ACP session/new
  -> reach bootstrap-complete / idle state (agent.state = "created")

orchestrator
  -> agent/status(agentId)         # poll until state != "creating"
  <- { agentId, state:"created" }

orchestrator or another runtime caller
  -> agent/prompt(...)
  <- work result / streamed updates
```

## Recovery and Persistence Posture

agentd is authoritative for realized runtime metadata.
After restart, agent identity (`room` + `name`) is the recovery key — not internal `sessionId`.

Persisted recovery state:

- `agentId`, `room`, `name`, `runtimeClass`, `workspaceId`, bootstrap configuration;
- shim socket path, state directory, shim PID for live process reconnect;
- last known agent state.

On daemon restart:

1. Load all agent records from DB.
2. For each agent with `created` or `running` state, attempt shim reconnect.
3. If reconnect succeeds: restore to `created` state (or recover running state via runtime/status).
4. If reconnect fails: mark agent `stopped` (fail-closed per D029).

The Session Manager's internal state is rebuilt from persisted agent metadata.
External callers never see raw `sessionId`.

The design set still leaves several durable-state gaps for later work:

- restart-safe replay, reconnect, cleanup, and cross-client hardening;
- persisted resolved bootstrap env / permissions / MCP inputs (R036);
- cross-client delivery and routing hardening for richer realized Room behavior.

## Security Boundary Summary

- local path attachment is host-impacting and must be canonicalized before registration;
- hooks execute as host commands and can have host-side effects before any agent prompt runs;
- env layering is explicit and must not be treated as an implicit secret fan-out channel;
- shared workspace means shared host-path impact;
- ACP capability exposure is intentionally narrower at the ARI boundary than at the shim boundary.
