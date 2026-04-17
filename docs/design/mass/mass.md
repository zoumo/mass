# agentd — runtime realization daemon

mass is the daemon that realizes already-decided runtime objects.
It owns workspaces, Agents, AgentRuns, process supervision, and exposes the ARI control surface.
It does **not** own external scheduling intent.

## Desired vs Realized

| Concern | Primary owner | agentd role |
|---|---|---|
| Which workspaces should exist | external caller | realize them when asked |
| Which AgentRuns should run | external caller | store realized `workspace` / `name` identity on AgentRuns |
| When work is complete | external caller | expose runtime state only |
| Workspace preparation and cleanup | Workspace Manager | authoritative runtime execution |
| Agent configuration | operator / external caller | store and serve via `agent/*` |
| AgentRun lifecycle and identity | Agent Manager | external lifecycle authority |
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

Agent Manager owns the CRUD lifecycle of reusable named configurations.
An Agent definition is a named runtime configuration template with the following fields:

- `name` (unique key) — the name that `agentrun/create.agent` references to select this template;
- `command` — the executable command for the agent process;
- optional `args` — command arguments;
- optional `env` — environment variables as a list of `{name, value}` objects;
- optional `startupTimeoutSeconds` — bootstrap timeout.

There is no runtime process associated with an Agent definition.
When an AgentRun is created, the Process Manager looks up the Agent definition named by `agentrun/create.agent` and uses its `command`, `args`, and `env` to generate the MASS Runtime Spec `config.json`.
Agents are managed via `agent/create`, `agent/update`, `agent/get`, `agent/list`, `agent/delete`.

### Agent Manager

Agent Manager owns the **external** lifecycle of running agent instances (AgentRuns).
An AgentRun is the durable, externally-visible runtime object that records:

- identity: `workspace` + `name` (unique key — all AgentRuns belong to a workspace);
- selected `agent`;
- restart policy and bootstrap inputs (`systemPrompt`, `labels`);
- external lifecycle state (`creating`, `idle`, `running`, `stopped`, `error`).

AgentRun identity (`workspace` + `name`) is stable across restarts.
`agentrun/create` is the external entry point; the Agent Manager validates inputs, writes durable AgentRun metadata, and triggers the internal bootstrap sequence via Process Manager.

### Process Manager

Process Manager realizes an AgentRun into an actual runtime process through the shim.
It owns:

- bundle materialization;
- runtime startup and shutdown;
- runtime-state inspection;
- reconnect to existing shim processes;
- typed event subscription;
- recovery on daemon restart.

Session-level concerns (shim socket path, PID, bundle directory) are tracked directly by Process Manager and surfaced through `agentrun/get` via `shimState`.

## AgentRun Identity

The `(workspace, name)` pair is the stable external identity for an AgentRun.

- `workspace` — the workspace this AgentRun is a member of;
- `name` — the agent name within that workspace (e.g. `architect`, `coder`).

Together they form a unique key within agentd.
External callers always refer to AgentRuns by `(workspace, name)`.
No opaque UUID is exposed through ARI.

## AgentRun State Machine

```
creating ──┐
           ├──> idle ──> running ──> stopped
           |              │
    error <─┴─────────────┘
```

| State | Meaning |
|---|---|
| `creating` | `agentrun/create` accepted; background bootstrap in progress |
| `idle` | Bootstrap complete; agent is ready to accept a prompt |
| `running` | Agent is processing an active prompt turn |
| `stopped` | Agent process is stopped; state is preserved |
| `error` | Bootstrap or runtime failure; agent is not operational |

Transition rules:

- `creating → idle`: shim started successfully (ACP initialized);
- `creating → error`: shim start failed;
- `idle → running`: `agentrun/prompt` dispatched;
- `running → idle`: prompt turn completes (agent returns to idle);
- `idle → stopped` / `running → stopped`: `agentrun/stop` received;
- `running → error`: runtime failure during a turn;
- `error → creating` / `stopped → creating`: `agentrun/restart` triggers re-bootstrap.

## Bootstrap Contract

The converged bootstrap story for AgentRun creation:

1. `workspace/create` is called; agentd prepares the workspace asynchronously.
2. Caller polls `workspace/get` until `phase: "ready"`.
3. `agentrun/create` is called with `workspace` + `name` + `agent`.
4. agentd validates the workspace is ready, writes AgentRun metadata with `state: "creating"`, and starts the shim in a background goroutine.
5. The shim materializes the bundle, resolves `cwd`, and completes ACP bootstrap.
6. On success: AgentRun transitions to `idle`. On failure: AgentRun transitions to `error`.
7. Actual work arrives later through `agentrun/prompt`.
8. Callers poll `agentrun/get` until state transitions out of `creating`.

`agentrun/create` returns immediately with `state: "creating"`.
It does **not** wait for bootstrap to complete.

## Async Create Semantics

`agentrun/create` is non-blocking.
The response is:

```json
{ "workspace": "my-project", "name": "architect", "state": "creating" }
```

The caller polls `agentrun/get` to determine when the agent is ready:

```json
{ "agent": { "workspace": "my-project", "name": "architect", "state": "idle", ... } }
```

Bootstrap errors surface as:

```json
{ "agent": { "workspace": "my-project", "name": "architect", "state": "error", "errorMessage": "..." } }
```

## Stop and Delete Separation

`agentrun/stop` and `agentrun/delete` are distinct operations:

| Operation | Effect | Requires |
|---|---|---|
| `agentrun/stop` | Stops the runtime process; preserves AgentRun metadata and state | AgentRun in `idle` or `running` state |
| `agentrun/delete` | Removes AgentRun record and releases resources | AgentRun must be in `stopped` or `error` state |

`agentrun/stop` does not delete.
`agentrun/delete` requires a prior `agentrun/stop` only for healthy agents.
Agents already in `error` may be deleted directly because they are already non-operational.

## Restart

`agentrun/restart` re-bootstraps an AgentRun from `stopped` or `error` state:

1. Validates agent is `stopped` or `error`.
2. Transitions agent to `creating`.
3. Triggers background re-bootstrap using existing AgentRun metadata.
4. Caller polls `agentrun/get` until `idle` or `error`.

Restart preserves `workspace`, `name`, and bootstrap configuration.
It does not create a new AgentRun identity.

## Error State Contract

`error` is a retained-failure state:

- the AgentRun record still exists;
- the current runtime instance is no longer trustworthy;
- callers must not route new work to the agent until it is restarted;
- the primary operator actions are `agentrun/get`, `agentrun/restart`, or `agentrun/delete`.

Operational consequences:

- `agentrun/prompt` is rejected for `error` agents (must be in `idle` state);
- `workspace/send` is rejected when the target agent is in `error` state;
- `workspace/delete` is blocked (`-32001`) when the workspace has any AgentRuns.

## workspace/send and Agent-to-Agent Routing

`workspace/send` routes a message from one agent to another within a workspace.
It is a fire-and-forget delivery: `delivered: true` means the message was dispatched, not that a response was received.

Rejection conditions:
- daemon is in recovery mode (`-32001`);
- target agent not found (`-32602`);
- target agent is in `error` state (`-32001`);
- target shim is not running (`-32001`).

## Recovery and Persistence Posture

mass is authoritative for realized runtime metadata.
After restart, AgentRun identity (`workspace` + `name`) is the recovery key.

Persisted recovery state:

- `workspace`, `name`, `agent`, bootstrap configuration (`BootstrapConfig`);
- shim socket path (`ShimSocketPath`), state directory (`ShimStateDir`), shim PID (`ShimPID`) for live process reconnect;
- last known agent state.

On daemon restart:

1. Load all AgentRun records from DB.
2. For each AgentRun with `idle` or `running` state, attempt shim reconnect.
3. If reconnect succeeds: restore to `idle` state (or recover running state via runtime/status).
4. If reconnect fails: mark agent `error` (fail-closed).

AgentRuns in `creating` state at daemon restart are marked `error` ("daemon restarted during creating phase") — they never completed bootstrap.

External callers never see internal shim process details beyond what `agentrun/get` surfaces in `shimState`.

## Runtime Bootstrap Flow

```text
ARI client
  -> workspace/create(name, source)
  <- { name, phase: "pending" }

ARI client
  -> workspace/get(name)              # poll until phase == "ready"
  <- { name, phase: "ready", path }

ARI client
  -> agentrun/create(workspace, name, runtimeClass, ...)   # async bootstrap
  <- { workspace, name, state: "creating" }

agentd (background)
  -> materialize bundle + resolve cwd + ACP initialize
  -> reach bootstrap-complete / idle state (agentRun.state = "idle")

ARI client
  -> agentrun/get(workspace, name)      # poll until state != "creating"
  <- { agent: { workspace, name, state: "idle", ... } }

ARI client
  -> agentrun/prompt(workspace, name, prompt)
  <- { accepted: true }
```

## Environment and Capability Posture

The agent process env is built from: parent process (agentd host) environment + Agent definition env.
There is no AgentRun-level env override in `agentrun/create`; env is fixed by the Agent definition and runtime class configuration.

Workspace hooks run in agentd's host process environment and are not affected by Agent definition env.

Capability posture is also explicit:

- ACP remains the inner protocol between shim and agent;
- agentd exposes a curated ARI surface (`agentrun/*`, `agent/*`, `workspace/*`);
- raw ACP client responsibilities such as `fs/*`, `terminal/*`, or low-level protocol negotiation remain behind the shim boundary and are governed by permission policy rather than by direct ARI passthrough.

## Shared Workspace Semantics

Multiple AgentRuns may intentionally share one workspace.
The runtime guarantees reference tracking and cleanup safety, but **not** per-agent filesystem isolation.
If several AgentRuns share a workspace, they share read/write impact on the same host path.

## Security Boundary Summary

- local path attachment is host-impacting and must be canonicalized before registration;
- hooks execute as host commands and can have host-side effects before any agent prompt runs;
- shared workspace means shared host-path impact;
- ACP capability exposure is intentionally narrower at the ARI boundary than at the shim boundary.

## Shim File Layout

For each AgentRun, agentd stores bundle, state, and socket co-located under the bundle root:

```
<bundleRoot>/<workspace>-<name>/
├── config.json          ← mass writes (MASS Runtime Spec)
├── workspace -> <path>  ← agentd symlinks to the workspace directory
├── state.json           ← shim writes
├── agent-run.sock      ← shim creates (Unix domain socket)
└── events.jsonl         ← shim appends
```

`ShimSocketPath` and `ShimStateDir` are persisted in the AgentRun metadata so mass can reconnect after restart without scanning the filesystem.
