# ARI — Agent Runtime Interface

ARI is the local control API between an external caller and mass.
It is a runtime API for **realized objects**. It does not replace the caller's desired-state contracts.

## Desired vs Realized

| Concept | Desired-state authority | Realized-state authority |
|---|---|---|
| Workspace intent | `docs/design/workspace/workspace-spec.md` | `workspace/*` in ARI |
| Agent configuration | operator / external caller | `agent/*` in ARI |
| AgentRun bootstrap contract | runtime/config design docs | `agentrun/create` in ARI |
| Work execution | external caller policy | `agentrun/prompt` in ARI |

ARI exposes what callers ask agentd to realize and observe.
It must not smuggle desired-state ownership into mass.

## Transport

- protocol: JSON-RPC 2.0 over Unix domain socket
- default path: `/run/mass/mass.sock`

## Identity

AgentRuns are identified by the pair `(workspace, name)`.
All `agentrun/*` method parameters and results use `workspace` + `name` together as the stable key.
There is no opaque UUID identity exposed to ARI callers.

Resources are addressed using `ObjectKey`:

```json
{ "workspace": "my-project", "name": "architect" }
```

For global resources (Workspace, Agent), only `name` is required:

```json
{ "name": "my-project" }
```

## Domain Types

ARI uses domain types with a `metadata/spec/status` structure.
All CRUD responses return the domain object directly (no wrapper).

### Agent

```json
{
  "metadata": {
    "name": "claude",
    "labels": {},
    "createdAt": "2026-01-01T00:00:00Z",
    "updatedAt": "2026-01-01T00:00:00Z"
  },
  "spec": {
    "command": "/usr/bin/claude-agent",
    "args": [],
    "env": [],
    "startupTimeoutSeconds": 30
  }
}
```

### AgentRun

```json
{
  "metadata": {
    "name": "architect",
    "workspace": "my-project",
    "labels": {},
    "createdAt": "2026-01-01T00:00:00Z",
    "updatedAt": "2026-01-01T00:00:00Z"
  },
  "spec": {
    "agent": "claude",
    "restartPolicy": "always_new"
  },
  "status": {
    "state": "idle",
    "errorMessage": "",
    "shim": {
      "status": "idle",
      "pid": 12345,
      "bundle": "/var/lib/agentd/bundles/my-project-architect",
      "socketPath": "/run/mass/bundles/my-project-architect/shim.sock"
    }
  }
}
```

The `status.shim` field is populated when the agent has a running shim process. It is `null`/omitted otherwise. The `socketPath` in `status.shim` replaces the removed `agentrun/attach` endpoint — callers obtain the shim socket path via `agentrun/get`.

### Workspace

```json
{
  "metadata": {
    "name": "my-project",
    "labels": {},
    "createdAt": "2026-01-01T00:00:00Z",
    "updatedAt": "2026-01-01T00:00:00Z"
  },
  "spec": {
    "source": { "type": "local", "path": "/home/user/project" }
  },
  "status": {
    "phase": "ready",
    "path": "/home/user/project"
  }
}
```

Internal fields (`shimSocketPath`, `shimStateDir`, `shimPid`, `bootstrapConfig` in AgentRun status;
`hooks` in Workspace spec) are not exposed via ARI.

## Client Interface

ARI provides a controller-runtime style typed client with CRUD + domain operations:

```go
type Client interface {
    Create(ctx, obj Object) error
    Get(ctx, key ObjectKey, obj Object) error
    Update(ctx, obj Object) error
    List(ctx, list ObjectList, opts ...ListOption) error
    Delete(ctx, key ObjectKey, obj Object) error

    AgentRuns() AgentRunOps    // Prompt, Cancel, Stop, Restart
    Workspaces() WorkspaceOps  // Send

    Close() error
    DisconnectNotify() <-chan struct{}
}
```

CRUD methods route to the correct wire method via type-switch on the Object type.

### ListOption

List operations accept functional options for filtering:

```go
client.List(ctx, &agentRunList, InWorkspace("my-project"))
client.List(ctx, &agentRunList, WithState("idle"))
client.List(ctx, &workspaceList, WithPhase("ready"))
client.List(ctx, &agentList, WithLabels(map[string]string{"team": "platform"}))
```

On the wire, list options are sent as `ListOptions`:

```json
{
  "fieldSelector": { "workspace": "my-project", "state": "idle" },
  "labels": { "team": "platform" }
}
```

Supported field selectors by resource type:
- **Workspace**: `phase`
- **AgentRun**: `workspace`, `state`

## Workspace Methods

### `workspace/create`

Create a workspace record and begin async preparation.
Returns immediately with `status.phase: "pending"`.
Callers poll `workspace/get` until phase transitions to `"ready"` or `"error"`.

**Params:** Workspace object

```json
{
  "metadata": { "name": "my-project" },
  "spec": { "source": { "type": "local", "path": "/home/user/project" } }
}
```

**Result:** Workspace (phase is always `"pending"` on success)

**Example:**

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "workspace/create",
  "params": {
    "metadata": { "name": "my-project" },
    "spec": { "source": { "type": "local", "path": "/home/user/project" } }
  }
}
```

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "metadata": { "name": "my-project", "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z" },
    "spec": { "source": { "type": "local", "path": "/home/user/project" } },
    "status": { "phase": "pending" }
  }
}
```

Poll until ready:

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "workspace/get",
  "params": { "name": "my-project" }
}
```

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "metadata": { "name": "my-project", "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z" },
    "spec": { "source": { "type": "local", "path": "/home/user/project" } },
    "status": { "phase": "ready", "path": "/home/user/project" }
  }
}
```

### `workspace/get`

Return current workspace state.

**Params:** ObjectKey `{name}`

**Result:** Workspace

- `status.phase`: `"pending"` | `"ready"` | `"error"`
- `status.path`: absolute host path (only present when phase is `"ready"`)

To list workspace members (AgentRuns), use `agentrun/list` with `InWorkspace()` filter.

### `workspace/list`

List all workspaces.

**Params:** ListOptions (optional)

**Result:** `{items: Workspace[]}` — array of Workspace domain objects.

### `workspace/delete`

Delete a workspace record and release its resources.
Blocked if any AgentRuns are currently using the workspace.

**Params:** ObjectKey `{name}`

**Result:** `{}`

**Error:** `-32001` (`CodeRecoveryBlocked`) when AgentRuns exist in the workspace.

### `workspace/send`

Route a message from one agent to another within a workspace.
The target agent receives the message as a prompt via its running shim.
This is a fire-and-forget delivery: `delivered: true` means the message was dispatched to the shim, not that a response was received.

**Params:**

| Field | Type | Required | Meaning |
|---|---|---|---|
| `workspace` | string | yes | Workspace name |
| `from` | string | yes | Sender agent name |
| `to` | string | yes | Recipient agent name |
| `message` | string | yes | Message text to deliver |
| `needsReply` | bool | no | Envelope hint indicating a reply is expected |

**Result:** `{delivered: true}`

**Errors:**
- `-32001` when daemon is recovering, target agent is in error state, or target shim is not running
- `-32602` when target agent is not found

## Agent Methods — Agent CRUD

`agent/*` methods manage **Agent definition** records. An Agent definition is a reusable named configuration that defines how to launch an agent process. It has no runtime process. AgentRuns reference an Agent definition by name via the `agent` field in `agentrun/create`.

### `agent/create`

Create a new Agent record. Returns error if an agent with the same name already exists.

**Params:** Agent object

```json
{
  "metadata": { "name": "claude" },
  "spec": {
    "command": "/usr/bin/claude-agent",
    "args": [],
    "env": [{"name": "API_KEY", "value": "..."}],
    "startupTimeoutSeconds": 30
  }
}
```

**Result:** Agent

### `agent/update`

Update an existing Agent record. Returns error if the agent does not exist.

**Params:** Agent object (same format as create)

**Result:** Agent

### `agent/get`

Retrieve an Agent definition by name.

**Params:** ObjectKey `{name}`

**Result:** Agent

### `agent/list`

List all Agent records.

**Params:** ListOptions (optional)

**Result:** `{items: Agent[]}` — array of Agent domain objects.

### `agent/delete`

Delete an Agent record.

**Params:** ObjectKey `{name}`

**Result:** `{}`

### Agent Domain Shape

| Path | Type | Meaning |
|---|---|---|
| `metadata.name` | string | Unique name (referenced by `agentrun/create.spec.agent`) |
| `metadata.labels` | map? | Arbitrary metadata |
| `metadata.createdAt` | string | RFC3339 creation timestamp |
| `metadata.updatedAt` | string | RFC3339 last-updated timestamp |
| `spec.command` | string | Executable command for the agent process |
| `spec.args` | []string? | Command arguments |
| `spec.env` | [{name, value}]? | Environment variables |
| `spec.startupTimeoutSeconds` | int? | Bootstrap timeout in seconds |

Note: `agentrun/create` `spec.agent` selects an Agent definition by name.

## AgentRun Methods

`agentrun/*` methods manage the **lifecycle of running agent instances**.
An AgentRun is identified by `(workspace, name)` and has a shim process.

### `agentrun/create`

`agentrun/create` is **async configuration-only bootstrap**.
It creates the AgentRun record and returns immediately with `status.state: "creating"`.
Actual bootstrap (shim startup, ACP initialization) happens in the background.
Callers poll `agentrun/get` until state transitions to `"idle"` or `"error"`.

**Params:** AgentRun object

```json
{
  "metadata": { "workspace": "my-project", "name": "architect" },
  "spec": {
    "agent": "claude",
    "restartPolicy": "always_new",
    "systemPrompt": "You are a coding agent."
  }
}
```

**Result:** AgentRun (state is always `"creating"` on success)

**Example:**

```json
{
  "jsonrpc": "2.0",
  "id": 10,
  "method": "agentrun/create",
  "params": {
    "metadata": { "workspace": "my-project", "name": "architect" },
    "spec": { "agent": "claude", "systemPrompt": "You are a coding agent." }
  }
}
```

```json
{
  "jsonrpc": "2.0",
  "id": 10,
  "result": {
    "metadata": { "name": "architect", "workspace": "my-project", "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z" },
    "spec": { "agent": "claude" },
    "status": { "state": "creating" }
  }
}
```

Poll until idle:

```json
{
  "jsonrpc": "2.0",
  "id": 11,
  "method": "agentrun/get",
  "params": { "workspace": "my-project", "name": "architect" }
}
```

```json
{
  "jsonrpc": "2.0",
  "id": 11,
  "result": {
    "metadata": { "name": "architect", "workspace": "my-project", "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z" },
    "spec": { "agent": "claude" },
    "status": {
      "state": "idle",
      "shim": {
        "status": "idle",
        "pid": 12345,
        "bundle": "/var/lib/agentd/bundles/my-project-architect",
        "socketPath": "/run/mass/bundles/my-project-architect/shim.sock"
      }
    }
  }
}
```

### `agentrun/prompt`

Deliver a prompt turn to an AgentRun.
Rejected when the agent is in `creating`, `stopped`, or `error` state.
Only accepted when state is `idle`.

**Params:** `{workspace, name, prompt}`

**Result:** `{accepted: true}`

### `agentrun/cancel`

Cancel the active turn for an AgentRun.

**Params:** ObjectKey `{workspace, name}`

**Result:** `{}`

### `agentrun/stop`

Stop the runtime process for an AgentRun.
Preserves AgentRun metadata and state for inspection.
The agent remains in `stopped` state until `agentrun/delete` or `agentrun/restart`.

**Params:** ObjectKey `{workspace, name}`

**Result:** `{}`

### `agentrun/delete`

Remove an AgentRun record and release its resources.
Requires the agent to be in `stopped` or `error` state.

**Params:** ObjectKey `{workspace, name}`

**Result:** `{}`

**Errors:**
- `-32001` (`CodeRecoveryBlocked`) when agent is not in stopped/error state
- `-32602` when agent is not found

### `agentrun/restart`

Re-bootstrap a stopped or errored AgentRun from existing state.
Transitions the agent back to `creating` and starts background bootstrap.
Caller polls `agentrun/get` until `idle` or `error`.

**Params:** ObjectKey `{workspace, name}`

**Result:** AgentRun (state is `"creating"`)

### `agentrun/list`

List AgentRun records with optional filters.

**Params:** ListOptions (optional)

```json
{
  "fieldSelector": { "workspace": "my-project", "state": "idle" },
  "labels": { "team": "platform" }
}
```

**Result:** `{items: AgentRun[]}` — array of AgentRun domain objects.

Field selectors:
- `workspace`: restrict to a single workspace
- `state`: restrict to agents in a given state

### `agentrun/get`

Return current AgentRun state including optional shim runtime state.

**Params:** ObjectKey `{workspace, name}`

**Result:** AgentRun (with `status.shim` populated when shim is running)

```json
{
  "metadata": { "name": "architect", "workspace": "my-project", "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z" },
  "spec": { "agent": "claude" },
  "status": {
    "state": "idle",
    "shim": {
      "status": "idle",
      "pid": 12345,
      "bundle": "/var/lib/agentd/bundles/my-project-architect",
      "socketPath": "/run/mass/bundles/my-project-architect/shim.sock"
    }
  }
}
```

`status.shim` is omitted when the agent has no running shim process.

AgentRun state values: `creating`, `idle`, `running`, `stopped`, `error`.

## AgentRun Domain Shape

| Path | Type | Meaning |
|---|---|---|
| `metadata.name` | string | Agent name within the workspace |
| `metadata.workspace` | string | Workspace this AgentRun belongs to |
| `metadata.labels` | map? | Arbitrary metadata |
| `metadata.createdAt` | string | RFC3339 creation timestamp |
| `metadata.updatedAt` | string | RFC3339 last-updated timestamp |
| `spec.agent` | string | Selected agent definition name |
| `spec.restartPolicy` | string? | Session continuation policy on restart |
| `spec.description` | string? | Human-readable description |
| `spec.systemPrompt` | string? | Agent system prompt |
| `status.state` | string | Current agent state |
| `status.errorMessage` | string? | Error details when `state` is `"error"` |
| `status.shim` | object? | Shim runtime info (populated when shim is running) |
| `status.shim.status` | string | Shim status |
| `status.shim.pid` | int | Shim process ID |
| `status.shim.bundle` | string | Bundle directory path |
| `status.shim.socketPath` | string | Shim Unix socket path (for direct shim RPC connection) |

Internal fields (`shimSocketPath`, `shimStateDir`, `shimPid`, `bootstrapConfig`) are present in
the store but are not serialized in ARI responses.

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
| `error` | Bootstrap or runtime failure; agent must be restarted or deleted |

Transition rules:
- `creating → idle`: shim started successfully, ACP initialized
- `creating → error`: shim start failed
- `idle → running`: `agentrun/prompt` dispatched
- `running → idle`: prompt turn completes (agent returns to idle)
- `idle → stopped` / `running → stopped`: `agentrun/stop` received
- `running → error`: runtime failure during a turn
- `error → creating` / `stopped → creating`: `agentrun/restart` triggers re-bootstrap

## Events (Shim RPC)

Events are not streamed directly over the ARI connection. Instead:

- `agentrun/get` returns `status.shim.socketPath` — the shim's Unix socket path.
- Callers connect directly to the shim socket and call `session/subscribe` to receive `session/update` and `runtime/state_change` notifications.
- See [shim-rpc-spec.md](../runtime/shim-rpc-spec.md) for the full notification surface.

## Error Codes

| Code | Name | When |
|---|---|---|
| `-32001` | `CodeRecoveryBlocked` | Operation blocked: daemon recovering, workspace has agents, agent not in required state |
| `-32602` | `CodeInvalidParams` | Resource not found (workspace or agent), or required parameter missing |
| `-32603` | `CodeInternalError` | Internal server error |
| `-32601` | `CodeMethodNotFound` | Unknown method name |

## workspace-mcp-server

The `mass workspace-mcp` subcommand starts a workspace-scoped MCP server.
It exposes two MCP tools that wrap ARI calls:

- `workspace_status` — calls `workspace/get` for workspace state and `agentrun/list` (with workspace filter) for member agents
- `workspace_send` — calls `workspace/send`

Configuration is read from environment variables:

| Variable | Required | Meaning |
|---|---|---|
| `MASS_SOCKET` | yes | Path to mass's Unix socket |
| `MASS_WORKSPACE_NAME` | yes | The workspace this server instance belongs to |
| `MASS_AGENT_NAME` | no | The agent name for sender identification |
| `MASS_STATE_DIR` | no | State directory for log output |
| `MASS_LOG_LEVEL` | no | Log level (debug, info, warn, error); defaults to `info` |
| `MASS_LOG_FORMAT` | no | Log format (pretty, text, json); defaults to `pretty` |

## Capability Posture

The ARI contract intentionally exposes less than raw ACP:

- **exposed**: workspace management, Agent CRUD, AgentRun bootstrap, prompt delivery, cancellation, status, shim socket path (via `agentrun/get`), AgentRun/workspace listing;
- **not exposed as direct public contract**: raw ACP negotiation, `fs/*`, `terminal/*`, or other client-side ACP duties;
- **governed by runtime permission posture**: what the shim may approve or deny while acting as ACP client.

## Wire Protocol Summary

| Method | Params | Result |
|---|---|---|
| `workspace/create` | Workspace object | Workspace |
| `workspace/get` | ObjectKey `{name}` | Workspace |
| `workspace/list` | ListOptions (optional) | `{items: Workspace[]}` |
| `workspace/delete` | ObjectKey `{name}` | — |
| `workspace/send` | WorkspaceSendParams | `{delivered}` |
| `agent/create` | Agent object | Agent |
| `agent/update` | Agent object | Agent |
| `agent/get` | ObjectKey `{name}` | Agent |
| `agent/list` | ListOptions (optional) | `{items: Agent[]}` |
| `agent/delete` | ObjectKey `{name}` | — |
| `agentrun/create` | AgentRun object | AgentRun |
| `agentrun/get` | ObjectKey `{workspace, name}` | AgentRun (with `status.shim`) |
| `agentrun/list` | ListOptions (optional) | `{items: AgentRun[]}` |
| `agentrun/delete` | ObjectKey `{workspace, name}` | — |
| `agentrun/prompt` | `{workspace, name, prompt}` | `{accepted}` |
| `agentrun/cancel` | ObjectKey `{workspace, name}` | — |
| `agentrun/stop` | ObjectKey `{workspace, name}` | — |
| `agentrun/restart` | ObjectKey `{workspace, name}` | AgentRun |

## Future Work

The following are target gaps, not current capabilities:

- **Room methods** (`room/*`): shared-workspace group management, messaging bus. Not implemented.
- **ARI-level event fanout**: pushing `session/update` events directly to ARI clients without requiring shim socket connection.
- **AgentRun env override**: `agentrun/create` currently has no `env` field; only Agent definition env is used.
- **Hook output in workspace/get**: workspace preparation hook stdout/stderr is not currently returned.
