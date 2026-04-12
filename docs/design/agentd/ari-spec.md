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

## Workspace Methods

### `workspace/create`

Create a workspace record and begin async preparation.
Returns immediately with `phase: "pending"`.
Callers poll `workspace/status` until phase transitions to `"ready"` or `"error"`.

**Params:**

| Field | Type | Required | Meaning |
|---|---|---|---|
| `name` | string | yes | Unique workspace name |
| `source` | object | yes | Workspace source spec (must include `type`). See [workspace-spec.md](../workspace/workspace-spec.md) |
| `labels` | map | no | Arbitrary metadata |

**Result:** `{name, phase}` where `phase` is always `"pending"` on success.

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
  "result": { "name": "my-project", "phase": "pending" }
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
    "name": "my-project",
    "phase": "ready",
    "path": "/home/user/project",
    "members": []
  }
}
```

### `workspace/status`

Return current workspace phase and membership.

**Params:** `{name}`

**Result:** `{name, phase, path?, members[]}`

- `phase`: `"pending"` | `"ready"` | `"error"`
- `path`: absolute host path (only present when `phase` is `"ready"`)
- `members`: array of `AgentRunInfo` objects for AgentRuns currently using this workspace

### `workspace/list`

List all ready workspaces.

**Params:** `{}`

**Result:** `{workspaces[]}` — array of `{name, phase, path}` objects.

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

**Result:** `AgentInfo`

### `agent/get`

Retrieve an Agent definition by name.

**Params:** `{name}`

**Result:** `{agent: AgentInfo}`

### `agent/list`

List all Agent records.

**Params:** `{}`

**Result:** `{agents: AgentInfo[]}`

### `agent/delete`

Delete an Agent record.

**Params:** `{name}`

**Result:** `{}`

### Agent definition Schema

| Field | Type | Meaning |
|---|---|---|
| `name` | string | Unique template name (referenced by `agentrun/create.agent`) |
| `command` | string | Executable command for the agent process |
| `args` | []string? | Command arguments |
| `env` | [{name, value}]? | Environment variables as a list of `{name, value}` objects |
| `startupTimeoutSeconds` | int? | Bootstrap timeout in seconds |
| `createdAt` | string | RFC3339 creation timestamp |
| `updatedAt` | string | RFC3339 last-updated timestamp |

Note: `agentrun/create.agent` selects an Agent definition by name. The Agent definition itself is the named runtime configuration — it does not contain a `agent` field.

## AgentRun Methods

`agentrun/*` methods manage the **lifecycle of running agent instances**.
An AgentRun is identified by `(workspace, name)` and has a shim process.

### `agentrun/create`

`agentrun/create` is **async configuration-only bootstrap**.
It creates the AgentRun record and returns immediately with `state: "creating"`.
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

**Result:** `{workspace, name, state: "creating"}`

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
  "result": { "workspace": "my-project", "name": "architect", "state": "creating" }
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
      "workspace": "my-project",
      "name": "architect",
      "agent": "claude",
      "state": "idle",
      "createdAt": "2026-01-01T00:00:00Z"
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

**Result:** `{workspace, name, state: "creating"}`

### `agentrun/list`

List AgentRun records with optional filters.

**Params:** `{workspace?, state?, labels?}`

**Result:** `{agentRuns[]}` — array of `AgentRunInfo` objects.

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
    "workspace": "my-project",
    "name": "architect",
    "agent": "claude",
    "state": "idle",
    "labels": {},
    "createdAt": "2026-01-01T00:00:00Z"
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

## AgentRunInfo Schema

All `agentrun/*` methods that return AgentRun data use the `AgentRunInfo` shape:

| Field | Type | Meaning |
|---|---|---|
| `workspace` | string | Workspace this AgentRun belongs to |
| `name` | string | Agent name within the workspace |
| `agent` | string | Selected runtime class |
| `state` | string | Current agent state |
| `errorMessage` | string? | Error details when `state` is `"error"` |
| `labels` | map? | Arbitrary metadata |
| `createdAt` | string | RFC3339 creation timestamp |

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
