# ARI — Agent Runtime Interface

ARI is the local control API between an external caller and agentd.
It is a runtime API for **realized objects**. It does not replace the caller's desired-state contracts.

## Desired vs Realized

| Concept | Desired-state authority | Realized-state authority |
|---|---|---|
| Workspace intent | `docs/design/workspace/workspace-spec.md` | `workspace/*` in ARI |
| Agent configuration | operator / external caller | `agent/*` in ARI |
| AgentRun bootstrap contract | runtime/config design docs | `agentrun/create` in ARI |
| Work execution | external caller policy | `agentrun/prompt` in ARI |

ARI exposes what callers ask agentd to realize and observe.
It must not smuggle desired-state ownership into agentd.

## Transport

- protocol: JSON-RPC 2.0 over Unix domain socket
- default path: `/run/agentd/agentd.sock`

## Identity

AgentRuns are identified by the pair `(workspace, name)`.
All `agentrun/*` method parameters and results use `workspace` + `name` together as the stable key.
There is no opaque UUID identity exposed to ARI callers.

## Domain Types

ARI results use domain types with a `metadata/spec/status` structure:

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
    "errorMessage": ""
  }
}
```

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
`hooks` in Workspace spec) are not exposed via ARI. The shim socket path is exposed only via
`agentrun/attach`.

## Workspace Methods

### `workspace/create`

Create a workspace record and begin async preparation.
Returns immediately with `status.phase: "pending"`.
Callers poll `workspace/status` until phase transitions to `"ready"` or `"error"`.

**Params:**

| Field | Type | Required | Meaning |
|---|---|---|---|
| `name` | string | yes | Unique workspace name |
| `source` | object | yes | Workspace source spec (must include `type`). See [workspace-spec.md](../workspace/workspace-spec.md) |
| `labels` | map | no | Arbitrary metadata |

**Result:** `{workspace: Workspace}` where `workspace.status.phase` is always `"pending"` on success.

**Example:**

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "workspace/create",
  "params": {
    "name": "my-project",
    "source": { "type": "local", "path": "/home/user/project" }
  }
}
```

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "workspace": {
      "metadata": { "name": "my-project", "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z" },
      "spec": { "source": { "type": "local", "path": "/home/user/project" } },
      "status": { "phase": "pending" }
    }
  }
}
```

Poll until ready:

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "workspace/status",
  "params": { "name": "my-project" }
}
```

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "workspace": {
      "metadata": { "name": "my-project", "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z" },
      "spec": { "source": { "type": "local", "path": "/home/user/project" } },
      "status": { "phase": "ready", "path": "/home/user/project" }
    },
    "members": []
  }
}
```

### `workspace/status`

Return current workspace phase and membership.

**Params:** `{name}`

**Result:** `{workspace: Workspace, members: AgentRun[]}`

- `workspace.status.phase`: `"pending"` | `"ready"` | `"error"`
- `workspace.status.path`: absolute host path (only present when phase is `"ready"`)
- `members`: array of `AgentRun` domain objects for AgentRuns currently using this workspace

### `workspace/list`

List all ready workspaces.

**Params:** `{}`

**Result:** `{workspaces: Workspace[]}` — array of `Workspace` domain objects.

### `workspace/delete`

Delete a workspace record and release its resources.
Blocked if any AgentRuns are currently using the workspace.

**Params:** `{name}`

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

### `agent/set`

Create or update an Agent record.

**Params:**

| Field | Type | Required | Meaning |
|---|---|---|---|
| `name` | string | yes | Unique template name |
| `command` | string | yes | Executable command for the agent process |
| `args` | []string | no | Command arguments |
| `env` | [{name, value}] | no | Environment variables as a list of `{name, value}` objects |
| `startupTimeoutSeconds` | int | no | Bootstrap timeout in seconds |

**Result:** `{agent: Agent}`

### `agent/get`

Retrieve an Agent definition by name.

**Params:** `{name}`

**Result:** `{agent: Agent}`

### `agent/list`

List all Agent records.

**Params:** `{}`

**Result:** `{agents: Agent[]}`

### `agent/delete`

Delete an Agent record.

**Params:** `{name}`

**Result:** `{}`

### Agent Domain Shape

| Path | Type | Meaning |
|---|---|---|
| `metadata.name` | string | Unique name (referenced by `agentrun/create.agent`) |
| `metadata.labels` | map? | Arbitrary metadata |
| `metadata.createdAt` | string | RFC3339 creation timestamp |
| `metadata.updatedAt` | string | RFC3339 last-updated timestamp |
| `spec.command` | string | Executable command for the agent process |
| `spec.args` | []string? | Command arguments |
| `spec.env` | [{name, value}]? | Environment variables |
| `spec.startupTimeoutSeconds` | int? | Bootstrap timeout in seconds |

Note: `agentrun/create.agent` selects an Agent definition by name.

## AgentRun Methods

`agentrun/*` methods manage the **lifecycle of running agent instances**.
An AgentRun is identified by `(workspace, name)` and has a shim process.

### `agentrun/create`

`agentrun/create` is **async configuration-only bootstrap**.
It creates the AgentRun record and returns immediately with `status.state: "creating"`.
Actual bootstrap (shim startup, ACP initialization) happens in the background.
Callers poll `agentrun/status` until state transitions to `"idle"` or `"error"`.

**Params:**

| Field | Type | Required | Meaning |
|---|---|---|---|
| `workspace` | string | yes | Workspace this AgentRun belongs to (must be ready) |
| `name` | string | yes | Agent name, unique within the workspace |
| `agent` | string | yes | Selects the registered runtime class |
| `restartPolicy` | string | no | `"try_reload"` \| `"always_new"` (default: `"always_new"`) |
| `systemPrompt` | string | no | Bootstrap system prompt for the agent session |
| `labels` | map | no | Arbitrary metadata |

**Result:** `{agentRun: AgentRun}` where `agentRun.status.state` is always `"creating"` on success.

**Example:**

```json
{
  "jsonrpc": "2.0",
  "id": 10,
  "method": "agentrun/create",
  "params": {
    "workspace": "my-project",
    "name": "architect",
    "agent": "claude",
    "systemPrompt": "You are a coding agent."
  }
}
```

```json
{
  "jsonrpc": "2.0",
  "id": 10,
  "result": {
    "agentRun": {
      "metadata": { "name": "architect", "workspace": "my-project", "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z" },
      "spec": { "agent": "claude" },
      "status": { "state": "creating" }
    }
  }
}
```

Poll until idle:

```json
{
  "jsonrpc": "2.0",
  "id": 11,
  "method": "agentrun/status",
  "params": { "workspace": "my-project", "name": "architect" }
}
```

```json
{
  "jsonrpc": "2.0",
  "id": 11,
  "result": {
    "agentRun": {
      "metadata": { "name": "architect", "workspace": "my-project", "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z" },
      "spec": { "agent": "claude" },
      "status": { "state": "idle" }
    },
    "shimState": {
      "status": "idle",
      "pid": 12345,
      "bundle": "/var/lib/agentd/bundles/my-project-architect"
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

**Params:** `{workspace, name}`

**Result:** `{}`

### `agentrun/stop`

Stop the runtime process for an AgentRun.
Preserves AgentRun metadata and state for inspection.
The agent remains in `stopped` state until `agentrun/delete` or `agentrun/restart`.

**Params:** `{workspace, name}`

**Result:** `{}`

### `agentrun/delete`

Remove an AgentRun record and release its resources.
Requires the agent to be in `stopped` or `error` state.

**Params:** `{workspace, name}`

**Result:** `{}`

**Errors:**
- `-32001` (`CodeRecoveryBlocked`) when agent is not in stopped/error state
- `-32602` when agent is not found

### `agentrun/restart`

Re-bootstrap a stopped or errored AgentRun from existing state.
Transitions the agent back to `creating` and starts background bootstrap.
Caller polls `agentrun/status` until `idle` or `error`.

**Params:** `{workspace, name}`

**Result:** `{agentRun: AgentRun}` where `agentRun.status.state` is `"creating"`.

### `agentrun/list`

List AgentRun records with optional filters.

**Params:** `{workspace?, state?, labels?}`

**Result:** `{agentRuns: AgentRun[]}` — array of `AgentRun` domain objects.

Filters:
- `workspace`: restrict to a single workspace
- `state`: restrict to agents in a given state
- `labels`: restrict to agents matching all provided labels

### `agentrun/status`

Return current AgentRun state and optional shim runtime state.

**Params:** `{workspace, name}`

**Result:**

```json
{
  "agentRun": {
    "metadata": { "name": "architect", "workspace": "my-project", "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z" },
    "spec": { "agent": "claude" },
    "status": { "state": "idle" }
  },
  "shimState": {
    "status": "idle",
    "pid": 12345,
    "bundle": "/var/lib/agentd/bundles/my-project-architect"
  }
}
```

`shimState` is omitted when the agent has no running shim process.

AgentRun state values: `creating`, `idle`, `running`, `stopped`, `error`.

### `agentrun/attach`

Return the shim's Unix socket path so the caller can connect directly and consume shim RPC events.
AgentRun must be in `idle` or `running` state.

**Params:** `{workspace, name}`

**Result:** `{socketPath}` — absolute path to the shim's Unix domain socket.

After receiving `socketPath`, the caller connects to the shim and consumes events via
`session/subscribe` on the shim RPC. See [shim-rpc-spec.md](../runtime/shim-rpc-spec.md).

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

Internal fields (`shimSocketPath`, `shimStateDir`, `shimPid`, `bootstrapConfig`) are present in
the store but are not serialized in ARI responses. The shim socket path is exposed only via
`agentrun/attach`.

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

- `agentrun/attach` returns the shim socket path.
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

The `agentd workspacemcp` subcommand starts a workspace-scoped MCP server.
It exposes two MCP tools that wrap ARI calls:

- `workspace_status` — calls `workspace/status`
- `workspace_send` — calls `workspace/send`

Configuration is read from environment variables:

| Variable | Required | Meaning |
|---|---|---|
| `OAR_AGENTD_SOCKET` | yes | Path to agentd's Unix socket |
| `OAR_WORKSPACE_NAME` | yes | The workspace this server instance belongs to |
| `OAR_AGENT_NAME` | no | The agent name for sender identification |
| `OAR_STATE_DIR` | no | State directory for log output |
| `OAR_LOG_LEVEL` | no | Log level (debug, info, warn, error); defaults to `info` |
| `OAR_LOG_FORMAT` | no | Log format (pretty, text, json); defaults to `pretty` |

## Capability Posture

The ARI contract intentionally exposes less than raw ACP:

- **exposed**: workspace management, Agent CRUD, AgentRun bootstrap, prompt delivery, cancellation, status, attach (shim socket path), AgentRun/workspace listing;
- **not exposed as direct public contract**: raw ACP negotiation, `fs/*`, `terminal/*`, or other client-side ACP duties;
- **governed by runtime permission posture**: what the shim may approve or deny while acting as ACP client.

## Future Work

The following are target gaps, not current capabilities:

- **Room methods** (`room/*`): shared-workspace group management, messaging bus. Not implemented.
- **ARI-level event fanout**: pushing `session/update` events directly to ARI clients without requiring shim socket connection.
- **AgentRun env override**: `agentrun/create` currently has no `env` field; only Agent definition env is used.
- **Hook output in workspace/status**: workspace preparation hook stdout/stderr is not currently returned.
