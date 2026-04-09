# ARI — Agent Runtime Interface

ARI is the local control API between the orchestrator and agentd.
It is a runtime API for **realized objects**. It does not replace the orchestrator's desired-state contracts.

## Desired vs Realized

| Concept | Desired-state authority | Realized-state authority |
|---|---|---|
| Workspace intent | `docs/design/workspace/workspace-spec.md` | `workspace/*` in ARI |
| Agent bootstrap contract | runtime/config design docs | `agent/create` in ARI |
| Work execution | orchestrator policy | `agent/prompt` in ARI |

ARI exposes what callers ask agentd to realize and observe.
It must not smuggle desired-state ownership into agentd.

## Transport

- protocol: JSON-RPC 2.0 over Unix domain socket
- default path: `/run/agentd/agentd.sock`

## Identity

Agents are identified by the pair `(workspace, name)`.
All method parameters and results use `workspace` + `name` together as the stable key.
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
| `source` | object | no | Workspace source spec (git/emptyDir/local) |
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
- `members`: array of `AgentInfo` objects for agents currently using this workspace

### `workspace/list`

List all ready workspaces.

**Params:** `{}`

**Result:** `{workspaces[]}` — array of `{name, phase, path}` objects.

### `workspace/delete`

Delete a workspace record and release its resources.
Blocked if any agents are currently using the workspace.

**Params:** `{name}`

**Result:** `{}`

**Error:** `-32001` (`CodeRecoveryBlocked`) when agents exist in the workspace.

### `workspace/send`

Route a message from one agent to another within a workspace.
The target agent receives the message as a prompt via its running shim.
This is a fire-and-forget delivery: `delivered: true` means the message was dispatched to the shim, not that a response was received.

**Params:** `{workspace, from, to, message}`

| Field | Type | Required | Meaning |
|---|---|---|---|
| `workspace` | string | yes | Workspace name |
| `from` | string | yes | Sender agent name |
| `to` | string | yes | Recipient agent name |
| `message` | string | yes | Message text to deliver |

**Result:** `{delivered: true}`

**Errors:**
- `-32001` when daemon is recovering, target agent is in error state, or target shim is not running
- `-32602` when target agent is not found

## Agent Methods

### `agent/create`

`agent/create` is **async configuration-only bootstrap**.
It creates the agent record and returns immediately with `state: "creating"`.
Actual bootstrap (shim startup, ACP initialization) happens in the background.
Callers poll `agent/status` until state transitions to `"idle"` or `"error"`.

**Params:**

| Field | Type | Required | Meaning |
|---|---|---|---|
| `workspace` | string | yes | Workspace this agent belongs to (must be ready) |
| `name` | string | yes | Agent name, unique within the workspace |
| `runtimeClass` | string | yes | Selects the registered runtime class |
| `restartPolicy` | string | no | `"never"` \| `"on-failure"` \| `"always"` |
| `systemPrompt` | string | no | Bootstrap system prompt for the agent session |
| `labels` | map | no | Arbitrary metadata |

**Result:** `{workspace, name, state: "creating"}`

**Example:**

```json
{
  "jsonrpc": "2.0",
  "id": 10,
  "method": "agent/create",
  "params": {
    "workspace": "my-project",
    "name": "architect",
    "runtimeClass": "claude",
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
  "method": "agent/status",
  "params": { "workspace": "my-project", "name": "architect" }
}
```

```json
{
  "jsonrpc": "2.0",
  "id": 11,
  "result": {
    "agent": {
      "workspace": "my-project",
      "name": "architect",
      "runtimeClass": "claude",
      "state": "idle",
      "createdAt": "2026-01-01T00:00:00Z"
    }
  }
}
```

### `agent/prompt`

Deliver a prompt turn to an agent.
Rejected when the agent is in `creating`, `stopped`, or `error` state.
Only accepted when state is `idle`.

**Params:** `{workspace, name, prompt}`

**Result:** `{accepted: true}`

### `agent/cancel`

Cancel the active turn for an agent.

**Params:** `{workspace, name}`

**Result:** `{}`

### `agent/stop`

Stop the runtime process for an agent.
Preserves agent metadata and state for inspection.
The agent remains in `stopped` state until `agent/delete` or `agent/restart`.

**Params:** `{workspace, name}`

**Result:** `{}`

### `agent/delete`

Remove an agent record and release its resources.
Requires the agent to be in `stopped` or `error` state.

**Params:** `{workspace, name}`

**Result:** `{}`

**Errors:**
- `-32001` (`CodeRecoveryBlocked`) when agent is not in stopped/error state
- `-32602` when agent is not found

### `agent/restart`

Re-bootstrap a stopped or errored agent from existing state.
Transitions the agent back to `creating` and starts background bootstrap.
Caller polls `agent/status` until `idle` or `error`.

**Params:** `{workspace, name}`

**Result:** `{workspace, name, state: "creating"}`

### `agent/list`

List agent records with optional filters.

**Params:** `{workspace?, state?, labels?}`

**Result:** `{agents[]}` — array of `AgentInfo` objects.

Filters:
- `workspace`: restrict to a single workspace
- `state`: restrict to agents in a given state
- `labels`: restrict to agents matching all provided labels

### `agent/status`

Return current agent state and optional shim runtime state.

**Params:** `{workspace, name}`

**Result:**
```json
{
  "agent": {
    "workspace": "my-project",
    "name": "architect",
    "runtimeClass": "claude",
    "state": "idle",
    "labels": {},
    "createdAt": "2026-01-01T00:00:00Z"
  },
  "shimState": {
    "status": "running",
    "pid": 12345,
    "bundle": "/var/lib/agentd/bundles/my-project/architect"
  }
}
```

`shimState` is omitted when the agent has no running shim process.

Agent state values: `creating`, `idle`, `running`, `stopped`, `error`.

### `agent/attach`

Return the shim's Unix socket path so the caller can connect directly.
Agent must be in `idle` or `running` state.

**Params:** `{workspace, name}`

**Result:** `{socketPath}` — absolute path to the shim's Unix domain socket.

## AgentInfo Schema

All methods that return agent data use the `AgentInfo` shape:

| Field | Type | Meaning |
|---|---|---|
| `workspace` | string | Workspace this agent belongs to |
| `name` | string | Agent name within the workspace |
| `runtimeClass` | string | Selected runtime class |
| `state` | string | Current agent state |
| `errorMessage` | string? | Error details when `state` is `"error"` |
| `labels` | map? | Arbitrary metadata |
| `createdAt` | string | RFC3339 creation timestamp |

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
| `error` | Bootstrap or runtime failure; agent must be restarted or deleted |

Transition rules:
- `creating → idle`: shim started successfully, ACP initialized
- `creating → error`: shim start failed
- `idle → running`: `agent/prompt` dispatched
- `running → idle`: prompt turn completes (agent returns to idle)
- `idle → stopped` / `running → stopped`: `agent/stop` received
- `running → error`: runtime failure during a turn
- `error → creating` / `stopped → creating`: `agent/restart` triggers re-bootstrap

## Events

### `agent/update`

Typed runtime event stream for attached ARI clients.
Streamed when a client is connected via `agent/attach`.

### `agent/stateChange`

Agent lifecycle transition notification.
Streamed to clients attached via `agent/attach`.

## Error Codes

| Code | Name | When |
|---|---|---|
| `-32001` | `CodeRecoveryBlocked` | Operation blocked: daemon recovering, workspace has agents, agent not in required state |
| `-32602` | `CodeInvalidParams` | Resource not found (workspace or agent), or required parameter missing |
| `-32603` | `CodeInternalError` | Internal server error |
| `-32601` | `CodeMethodNotFound` | Unknown method name |

## workspace-mcp-server

The `workspace-mcp-server` binary exposes two MCP tools that wrap ARI calls:

- `workspace_status` — calls `workspace/status`
- `workspace_send` — calls `workspace/send`

It connects to agentd's Unix socket (default `/run/agentd/agentd.sock`) and
logs startup with `workspace=`, `agentName=`, and `agentID=` structured fields.

## Capability Posture

The ARI contract intentionally exposes less than raw ACP:

- **exposed**: workspace management, agent bootstrap, prompt delivery, cancellation, status, attach (shim socket path), agent/workspace listing;
- **not exposed as direct public contract**: raw ACP negotiation, `fs/*`, `terminal/*`, or other client-side ACP duties;
- **governed by runtime permission posture**: what the shim may approve or deny while acting as ACP client.
