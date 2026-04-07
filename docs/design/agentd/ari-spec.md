# ARI — Agent Runtime Interface

ARI is the local control API between the orchestrator and agentd.
It is a runtime API for **realized objects**. It does not replace the orchestrator’s desired-state contracts.

## Desired vs Realized

| Concept | Desired-state authority | Realized-state authority |
|---|---|---|
| Room intent | `docs/design/orchestrator/room-spec.md` | `room/*` in ARI when the orchestrator registers or inspects the runtime projection |
| Workspace intent | `docs/design/workspace/workspace-spec.md` | `workspace/*` in ARI |
| Session bootstrap contract | runtime/config design docs | `session/new` in ARI |
| Work execution | orchestrator policy | `session/prompt` in ARI |

ARI therefore exposes what callers ask agentd to realize and observe. It must not smuggle desired-state ownership into agentd.

## Transport

- protocol: JSON-RPC 2.0 over Unix domain socket
- default path: `/run/agentd/agentd.sock`

## Workspace methods

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

## Session methods

### `session/new`

`session/new` is **configuration-only bootstrap**.
It creates the OAR session object and establishes bootstrap inputs required to reach idle runtime state.
It is not the path for user work.

```json
{
  "jsonrpc": "2.0",
  "id": 10,
  "method": "session/new",
  "params": {
    "runtimeClass": "claude",
    "workspaceId": "ws-abc123",
    "systemPrompt": "You are a coding agent.",
    "env": ["GITHUB_TOKEN=${GITHUB_TOKEN}"],
    "mcpServers": [
      { "type": "http", "url": "http://localhost:3000/mcp" }
    ],
    "permissions": "approve-reads",
    "labels": {
      "task": "auth-refactor"
    },
    "room": "backend-refactor",
    "roomAgent": "architect"
  }
}
```

```json
{
  "jsonrpc": "2.0",
  "id": 10,
  "result": {
    "sessionId": "session-abc123",
    "state": "created"
  }
}
```

| Field | Type | Required | Meaning |
|---|---|---|---|
| `runtimeClass` | string | yes | Selects the registered runtime-class configuration |
| `workspaceId` | string | yes | Attaches the session to a realized workspace |
| `systemPrompt` | string | no | Bootstrap configuration for ACP session creation |
| `env` | []string | no | Per-session env overrides applied at bootstrap |
| `mcpServers` | []McpServer | no | Bootstrap MCP server configuration |
| `permissions` | string | no | Permission posture exposed to the runtime/shim boundary |
| `labels` | map | no | Arbitrary metadata |
| `room` | string | no | Realized Room name if the session is a Room member |
| `roomAgent` | string | no | Member name inside that realized Room |

### Environment precedence

The env contract is:

1. inherited daemon/host environment,
2. runtime-class env,
3. `session/new` env overrides.

Workspace hooks are outside this precedence chain; they are host commands, not session bootstrap fields.

### Internal meaning of `session/new`

`session/new` may cause bootstrap side effects such as bundle creation, `cwd` resolution, process startup, and ACP `session/new` behind the shim boundary. Even so, its contract remains configuration-only because no task work is being delivered yet.

### `session/prompt`

`session/prompt` is the work-entry path for a created session.

```json
{
  "jsonrpc": "2.0",
  "id": 11,
  "method": "session/prompt",
  "params": {
    "sessionId": "session-abc123",
    "prompt": "Refactor the auth module to use JWT tokens."
  }
}
```

Every externally supplied turn enters here:

- the first work turn after `session/new`;
- later user turns;
- future Room-routed work delivered to another member session.

### `session/cancel`

Cancel the active turn for a session.

### `session/stop`

Stop the runtime process for a session.

### `session/remove`

Remove a stopped session and release its workspace reference.

### `session/list`

List realized session records and filters such as `runtimeClass`, `room`, `state`, and labels.

### `session/status`

Return current realized session state, process state, attached workspace, and realized Room membership.
OAR `sessionId` must not be implied to equal ACP `sessionId`.

### `session/attach` / `session/detach`

Attach is an observation and prompt-injection surface for ARI clients.
It is **not** ACP client takeover.

ARI attach exposes a curated surface:

- streamed `session/update` notifications;
- `session/stateChange` notifications;
- `session/prompt` injection;
- `session/cancel`.

Raw ACP client-side requests such as `fs/*` and `terminal/*` remain behind the shim boundary.

## Realized Room methods

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
      { "agentName": "architect", "sessionId": "session-abc123", "state": "running" },
      { "agentName": "coder", "sessionId": "session-def456", "state": "created" }
    ],
    "workspaceId": "ws-abc123",
    "communication": { "mode": "mesh" }
  }
}
```

`room/status` answers “what is realized now?”, not “what should exist next?”.

### `room/delete`

Remove the realized runtime room record after member sessions are stopped or detached according to runtime rules.
Deleting the runtime projection does not delete the orchestrator’s desired-state Room object.

## Shared workspace semantics

When multiple realized sessions point at the same `workspaceId`, ARI is modeling a **shared workspace**.
That implies shared visibility and shared write risk on one realized host path.
ARI does not imply per-member filesystem isolation.

## Capability posture

The ARI contract intentionally exposes less than raw ACP:

- exposed: workspace preparation, session bootstrap, prompt delivery, cancellation, status, attach notifications, realized room inspection;
- not exposed as direct public contract: raw ACP negotiation, raw `session/new` payload shapes, `fs/*`, `terminal/*`, or other client-side ACP duties;
- governed by runtime permission posture: what the shim may approve or deny while acting as ACP client.

This is the design-set authority for the phrase **capability** at the ARI boundary.

## Events

### `session/update`

Typed runtime event stream for attached ARI clients.

### `session/stateChange`

Lifecycle transition notification for attached ARI clients.

## Follow-on gaps

Later work under R036 / R044 still needs to harden:

- durable bootstrap snapshots for truthful restart;
- durable OAR ↔ ACP identity mapping;
- replay / reconnect correctness;
- restart-safe cleanup and shared-workspace safety;
- cross-client delivery and routing hardening for richer realized Room behavior.
