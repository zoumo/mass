# agentd — runtime realization daemon

agentd is the daemon that realizes already-decided runtime objects.
It owns workspaces, agents, process supervision, and exposes the ARI control surface.
It does **not** own orchestrator intent.

## Desired vs Realized

| Concern | Primary owner | agentd role |
|---|---|---|
| Which workspaces should exist | orchestrator | realize them when asked |
| Which agents should run | orchestrator | store realized `workspace` / `name` identity on agents |
| When work is complete | orchestrator | expose runtime state only |
| Workspace preparation and cleanup | Workspace Manager | authoritative runtime execution |
| Agent lifecycle and identity | Agent Manager | external lifecycle authority |
| Shim process truth | Process Manager | internal runtime execution |
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

- identity: `workspace` + `name` (unique key — all agents belong to a workspace);
- selected `runtimeClass`;
- restart policy and bootstrap inputs (`systemPrompt`, `labels`);
- external lifecycle state (`creating`, `idle`, `running`, `stopped`, `error`).

Agent identity (`workspace` + `name`) is stable across restarts.
`agent/create` is the external entry point; the Agent Manager validates inputs, writes durable agent metadata, and triggers the internal bootstrap sequence via Process Manager.

### Process Manager

Process Manager realizes an agent into an actual runtime process through the shim.
It owns:

- bundle materialization;
- runtime startup and shutdown;
- runtime-state inspection;
- reconnect to existing shim processes;
- typed event subscription;
- recovery on daemon restart.

Session-level concerns (shim socket path, PID, bundle directory) are tracked directly by Process Manager and surfaced through `agent/status` via `shimState`.

## Agent Identity

The `(workspace, name)` pair is the stable external identity for an agent.

- `workspace` — the workspace this agent is a member of;
- `name` — the agent name within that workspace (e.g. `architect`, `coder`).

Together they form a unique key within agentd.
External callers always refer to agents by `(workspace, name)`.
External callers address agents by `(workspace, name)` only — no opaque UUID is exposed through ARI.

## Agent State Machine

```
creating ──┐
           ├──> idle ──> running ──> stopped
           |              │
    error <─┴─────────────┘
```

| State | Meaning |
|---|---|
| `creating` | `agent/create` accepted; background bootstrap in progress |
| `idle` | Bootstrap complete; agent is ready to accept a prompt |
| `running` | Agent is processing an active prompt turn |
| `stopped` | Agent process is stopped; state is preserved |
| `error` | Bootstrap or runtime failure; agent is not operational |

Transition rules:

- `creating → idle`: shim started successfully (ACP initialized);
- `creating → error`: shim start failed;
- `idle → running`: `agent/prompt` dispatched;
- `running → idle`: prompt turn completes (agent returns to idle);
- `idle → stopped` / `running → stopped`: `agent/stop` received;
- `running → error`: runtime failure during a turn;
- `error → creating` / `stopped → creating`: `agent/restart` triggers re-bootstrap.

## Bootstrap Contract

The converged bootstrap story for agent creation:

1. `workspace/create` is called; agentd prepares the workspace asynchronously.
2. Caller polls `workspace/status` until `phase: "ready"`.
3. `agent/create` is called with `workspace` + `name` + `runtimeClass`.
4. agentd validates the workspace is ready, writes agent metadata with `state: "creating"`, and starts the shim in a background goroutine.
5. The shim materializes the bundle, resolves `cwd`, and completes ACP bootstrap.
6. On success: agent transitions to `idle`. On failure: agent transitions to `error`.
7. Actual work arrives later through `agent/prompt`.
8. Callers poll `agent/status` until state transitions out of `creating`.

`agent/create` returns immediately with `state: "creating"`.
It does **not** wait for bootstrap to complete.

## Async Create Semantics

`agent/create` is non-blocking.
The response is:

```json
{ "workspace": "my-project", "name": "architect", "state": "creating" }
```

The caller polls `agent/status` to determine when the agent is ready:

```json
{ "agent": { "workspace": "my-project", "name": "architect", "state": "idle", ... } }
```

Bootstrap errors surface as:

```json
{ "agent": { "workspace": "my-project", "name": "architect", "state": "error", "errorMessage": "..." } }
```

## Stop and Delete Separation

`agent/stop` and `agent/delete` are distinct operations:

| Operation | Effect | Requires |
|---|---|---|
| `agent/stop` | Stops the runtime process; preserves agent metadata and state | Agent in `idle` or `running` state |
| `agent/delete` | Removes agent record and releases resources | Agent must be in `stopped` or `error` state |

`agent/stop` does not delete.
`agent/delete` requires a prior `agent/stop` only for healthy agents.
Agents already in `error` may be deleted directly because they are already non-operational.

## Restart

`agent/restart` re-bootstraps an agent from `stopped` or `error` state:

1. Validates agent is `stopped` or `error`.
2. Transitions agent to `creating`.
3. Triggers background re-bootstrap using existing agent metadata.
4. Caller polls `agent/status` until `idle` or `error`.

Restart preserves `workspace`, `name`, and bootstrap configuration.
It does not create a new agent identity.

## Error State Contract

`error` is a retained-failure state:

- the agent record still exists;
- the current runtime instance is no longer trustworthy;
- callers must not route new work to the agent until it is restarted;
- the primary operator actions are `agent/status`, `agent/restart`, or `agent/delete`.

Operational consequences:

- `agent/prompt` is rejected for `error` agents (must be in `idle` state);
- `workspace/send` is rejected when the target agent is in `error` state;
- `workspace/delete` is blocked (`-32001`) when the workspace has any agents.

## workspace/send and Agent-to-Agent Routing

`workspace/send` routes a message from one agent to another within a workspace.
It is a fire-and-forget delivery: `delivered: true` means the message was dispatched, not that a response was received.

Rejection conditions:
- daemon is in recovery mode (`-32001`);
- target agent not found (`-32602`);
- target agent is in `error` state (`-32001`);
- target shim is not running (`-32001`).

## Recovery and Persistence Posture

agentd is authoritative for realized runtime metadata.
After restart, agent identity (`workspace` + `name`) is the recovery key.

Persisted recovery state:

- `workspace`, `name`, `runtimeClass`, bootstrap configuration;
- shim socket path, state directory, shim PID for live process reconnect;
- last known agent state.

On daemon restart:

1. Load all agent records from DB.
2. For each agent with `idle` or `running` state, attempt shim reconnect.
3. If reconnect succeeds: restore to `idle` state (or recover running state via runtime/status).
4. If reconnect fails: mark agent `stopped` (fail-closed per D029).

External callers never see internal shim process details beyond what `agent/status` surfaces in `shimState`.

The design still leaves several durable-state gaps for later work:

- restart-safe replay, reconnect, cleanup, and cross-client hardening;
- cross-client delivery and routing hardening for multi-agent workspaces.

## Runtime Bootstrap Flow

```text
orchestrator
  -> workspace/create(name, source)
  <- { name, phase: "pending" }

orchestrator
  -> workspace/status(name)          # poll until phase == "ready"
  <- { name, phase: "ready", path }

orchestrator
  -> agent/create(workspace, name, runtimeClass, ...)   # async bootstrap
  <- { workspace, name, state: "creating" }

agentd (background)
  -> materialize bundle + resolve cwd + ACP initialize
  -> reach bootstrap-complete / idle state (agent.state = "idle")

orchestrator
  -> agent/status(workspace, name)   # poll until state != "creating"
  <- { agent: { workspace, name, state: "idle", ... } }

orchestrator or another runtime caller
  -> agent/prompt(workspace, name, prompt)
  <- { accepted: true }
```

## Environment and Capability Posture

agentd runtime bootstrap may depend on daemon/host environment and `runtimeClass.env`,
but those are runtime configuration concerns rather than part of the external `agent/create` contract.

Capability posture is also explicit:

- ACP remains the inner protocol between shim and agent;
- agentd exposes a curated ARI surface (`agent/*`, `workspace/*`, attach notifications);
- raw ACP client responsibilities such as `fs/*`, `terminal/*`, or low-level protocol negotiation remain behind the shim boundary and are governed by permission policy rather than by direct ARI passthrough.

## Shared Workspace Semantics

Multiple agents may intentionally share one workspace.
The runtime guarantees reference tracking and cleanup safety, but **not** per-agent filesystem isolation.
If several agents share a workspace, they share read/write impact on the same host path.

## Security Boundary Summary

- local path attachment is host-impacting and must be canonicalized before registration;
- hooks execute as host commands and can have host-side effects before any agent prompt runs;
- shared workspace means shared host-path impact;
- ACP capability exposure is intentionally narrower at the ARI boundary than at the shim boundary.
