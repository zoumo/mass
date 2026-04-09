# ARI — Agent Runtime Interface

ARI is the local control API between the orchestrator and agentd.
It is a runtime API for **realized objects**. It does not replace the orchestrator's desired-state contracts.

## Desired vs Realized

| Concept | Desired-state authority | Realized-state authority |
|---|---|---|
| Room intent | `docs/design/orchestrator/room-spec.md` | `room/*` in ARI when the orchestrator registers or inspects the runtime projection |
| Workspace intent | `docs/design/workspace/workspace-spec.md` | `workspace/*` in ARI |
| Agent bootstrap contract | runtime/config design docs | `agent/create` in ARI |
| Work execution | orchestrator policy | `agent/prompt` in ARI |

ARI therefore exposes what callers ask agentd to realize and observe. It must not smuggle desired-state ownership into agentd.

## Transport

- protocol: JSON-RPC 2.0 over Unix domain socket
- default path: `/run/agentd/agentd.sock`

## Workspace Methods

### `workspace/prepare`

Prepare a workspace from a Workspace Spec and return the realized runtime identity.

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "workspace/prepare",
  "params": {
    "spec": {
      "oarVersion": "0.1.0",
      "metadata": { "name": "my-project" },
      "source": {
        "type": "local",
        "path": "/home/user/project"
      }
    }
  }
}
```

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "workspaceId": "ws-abc123",
    "path": "/home/user/project",
    "status": "ready"
  }
}
```

The returned `path` is the realized canonical path. For a **local workspace**, cleanup later detaches but does not delete that host directory.

### `workspace/list`

List realized workspaces and their current reference state.

### `workspace/cleanup`

Release and clean up a workspace when reference rules allow it.
Managed workspaces may be deleted; local workspaces may not.

## Agent Methods

### `agent/create`

`agent/create` is **async configuration-only bootstrap**.
It creates the agent record and returns immediately with `state: "creating"`.
Actual bootstrap (bundle creation, shim startup, ACP initialization) happens in the background.
Callers poll `agent/status` until state transitions to `created` or `error`.

All agents must belong to a room. `room` and `name` together form the stable external identity for the agent.

```json
{
  "jsonrpc": "2.0",
  "id": 10,
  "method": "agent/create",
  "params": {
    "room": "backend-refactor",
    "name": "architect",
    "description": "Designs the module structure.",
    "runtimeClass": "claude",
    "workspaceId": "ws-abc123",
    "systemPrompt": "You are a coding agent.",
    "labels": {
      "task": "auth-refactor"
    }
  }
}
```

```json
{
  "jsonrpc": "2.0",
  "id": 10,
  "result": {
    "agentId": "agent-abc123",
    "state": "creating"
  }
}
```

| Field | Type | Required | Meaning |
|---|---|---|---|
| `room` | string | yes | Realized Room this agent belongs to (must already exist) |
| `name` | string | yes | Member name inside that Room (unique within the room) |
| `description` | string | no | Human-readable description of the agent's role |
| `runtimeClass` | string | yes | Selects the registered runtime-class configuration |
| `workspaceId` | string | yes | Attaches the agent to a realized workspace |
| `systemPrompt` | string | no | Bootstrap configuration for ACP session creation |
| `labels` | map | no | Arbitrary metadata |

### Internal Meaning of `agent/create`

`agent/create` may trigger background bootstrap side effects such as bundle creation, `cwd` resolution, process startup, and ACP `session/new` behind the shim boundary.
The contract remains configuration-only because no task work is being delivered yet.
The shim-internal protocol still uses `session/*` at the shim→agentd boundary; this is an internal detail not exposed through ARI.

### `agent/prompt`

`agent/prompt` is the work-entry path for a created agent.

```json
{
  "jsonrpc": "2.0",
  "id": 11,
  "method": "agent/prompt",
  "params": {
    "agentId": "agent-abc123",
    "prompt": "Refactor the auth module to use JWT tokens."
  }
}
```

Every externally supplied turn enters here:

- the first work turn after `agent/create` completes bootstrap;
- later user turns;
- future Room-routed work delivered to another member agent.

Requests to `agent/prompt` are rejected when the agent is in `creating`, `stopped`, or `error`.
If a runtime failure happens during the turn, the agent transitions to `error` rather than back to `created`.

### `agent/cancel`

Cancel the active turn for an agent.

Requests to `agent/cancel` are rejected when the agent is in `error`.

```json
{
  "jsonrpc": "2.0",
  "id": 12,
  "method": "agent/cancel",
  "params": {
    "agentId": "agent-abc123"
  }
}
```

### `agent/stop`

Stop the runtime process for an agent.
Preserves agent metadata and state for inspection.
The agent remains in `stopped` state until `agent/delete` or `agent/restart`.

```json
{
  "jsonrpc": "2.0",
  "id": 13,
  "method": "agent/stop",
  "params": {
    "agentId": "agent-abc123"
  }
}
```

### `agent/delete`

Remove a non-operational agent record and release its workspace reference.
Requires the agent to be in `stopped` or `error` state.

```json
{
  "jsonrpc": "2.0",
  "id": 14,
  "method": "agent/delete",
  "params": {
    "agentId": "agent-abc123"
  }
}
```

Returns an error if the agent is still active. Healthy agents must be stopped first; agents already in `error` may be deleted directly.

### `agent/restart`

Re-bootstrap a stopped or errored agent from existing state.
Transitions the agent back to `creating` and starts background bootstrap.
Caller polls `agent/status` until `created` or `error`.

```json
{
  "jsonrpc": "2.0",
  "id": 15,
  "method": "agent/restart",
  "params": {
    "agentId": "agent-abc123"
  }
}
```

### `agent/list`

List realized agent records with optional filters such as `runtimeClass`, `room`, `state`, and labels.

```json
{
  "jsonrpc": "2.0",
  "id": 16,
  "method": "agent/list",
  "params": {
    "room": "backend-refactor",
    "state": "running"
  }
}
```

### `agent/status`

Return current realized agent state, process state, attached workspace, and realized Room membership.

```json
{
  "jsonrpc": "2.0",
  "id": 17,
  "method": "agent/status",
  "params": {
    "agentId": "agent-abc123"
  }
}
```

```json
{
  "jsonrpc": "2.0",
  "id": 17,
  "result": {
    "agentId": "agent-abc123",
    "room": "backend-refactor",
    "name": "architect",
    "description": "Designs the module structure.",
    "runtimeClass": "claude",
    "workspaceId": "ws-abc123",
    "state": "running",
    "labels": { "task": "auth-refactor" }
  }
}
```

Agent state values: `creating`, `created`, `running`, `stopped`, `error`.
The internal `sessionId` is not surfaced here. See agent state machine in `agentd.md`.

### `agent/attach` / `agent/detach`

Attach is an observation and prompt-injection surface for ARI clients.
It is **not** ACP client takeover.

ARI attach exposes a curated surface:

- streamed `agent/update` notifications;
- `agent/stateChange` notifications;
- `agent/prompt` injection;
- `agent/cancel`.

Raw ACP client-side requests such as `fs/*` and `terminal/*` remain behind the shim boundary.

```json
{
  "jsonrpc": "2.0",
  "id": 18,
  "method": "agent/attach",
  "params": {
    "agentId": "agent-abc123"
  }
}
```

## Realized Room Methods

### `room/create`

Register the **realized runtime projection** of a Room that the orchestrator has already decided should exist.
This method does not replace the desired-state Room Spec.

```json
{
  "jsonrpc": "2.0",
  "id": 20,
  "method": "room/create",
  "params": {
    "name": "backend-refactor",
    "labels": { "project": "auth-service" },
    "communication": { "mode": "mesh" }
  }
}
```

`room/create` exists so agentd can track realized runtime metadata such as member mapping and communication mode. It does not define desired completion policy or business-level orchestration intent.

### `room/status`

Inspect realized runtime membership.

```json
{
  "jsonrpc": "2.0",
  "id": 21,
  "method": "room/status",
  "params": {
    "name": "backend-refactor"
  }
}
```

```json
{
  "jsonrpc": "2.0",
  "id": 21,
  "result": {
    "name": "backend-refactor",
    "members": [
      {
        "agentName": "architect",
        "description": "Designs the module structure.",
        "runtimeClass": "claude",
        "agentState": "running"
      },
      {
        "agentName": "coder",
        "description": "Implements the changes.",
        "runtimeClass": "claude",
        "agentState": "created"
      }
    ],
    "workspaceId": "ws-abc123",
    "communication": { "mode": "mesh" }
  }
}
```

`room/status` answers "what is realized now?", not "what should exist next?".
Members show `agentName`, `description`, `runtimeClass`, and `agentState`.
Internal `sessionId` is not surfaced.

### `room/delete`

Remove the realized runtime room record after member agents are stopped, errored, or detached according to runtime rules.
Deleting the runtime projection does not delete the orchestrator's desired-state Room object.

## Shared Workspace Semantics

When multiple realized agents point at the same `workspaceId`, ARI is modeling a **shared workspace**.
That implies shared visibility and shared write risk on one realized host path.
ARI does not imply per-agent filesystem isolation.

## Capability Posture

The ARI contract intentionally exposes less than raw ACP:

- exposed: workspace preparation, agent bootstrap, prompt delivery, cancellation, status, attach notifications, realized room inspection;
- not exposed as direct public contract: raw ACP negotiation, raw `session/new` payload shapes, `fs/*`, `terminal/*`, or other client-side ACP duties;
- governed by runtime permission posture: what the shim may approve or deny while acting as ACP client.

This is the design-set authority for the phrase **capability** at the ARI boundary.

## Events

### `agent/update`

Typed runtime event stream for attached ARI clients.
At the agentd→orchestrator boundary, runtime updates are surfaced as `agent/update`.
Internally, agentd translates shim-level `session/update` events to `agent/update` before delivery.

### `agent/stateChange`

Agent lifecycle transition notification for attached ARI clients.
At the agentd→orchestrator boundary, state transitions are surfaced as `agent/stateChange`.
Internally, agentd translates shim-level `runtime/stateChange` events to `agent/stateChange` before delivery.

The shim→agentd boundary continues using `session/update` and `runtime/stateChange` (see `shim-rpc-spec.md`).
This is an internal boundary that ARI callers do not see.

## Follow-on Gaps

Later work under R036 / R044 still needs to harden:

- durable bootstrap snapshots for truthful restart;
- durable agent ↔ internal session identity mapping;
- replay / reconnect correctness;
- restart-safe cleanup and shared-workspace safety;
- cross-client delivery and routing hardening for richer realized Room behavior.
