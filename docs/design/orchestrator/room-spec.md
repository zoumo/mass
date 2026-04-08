# Room Spec

Room is an **orchestrator-owned desired-state object**. It says which agents should collaborate, which workspace intent they should share, and what communication topology the orchestrator wants.

agentd does **not** own Room intent, completion policy, or business-level orchestration. agentd owns only the **realized runtime projection** needed to run, observe, and route already-decided Room members.

## Desired vs Realized

| Layer | Owns | Does not own |
|---|---|---|
| Orchestrator / Room Spec | Desired membership, desired shared-workspace intent, desired communication policy, completion logic | Runtime process truth, prompt delivery state |
| agentd / ARI | Realized room registration, realized member-to-agent mapping, realized shared-workspace attachment, runtime routing state | Whether the Room should exist in the first place, when business work is complete |
| Runtime / shim | Per-session process state and ACP bootstrap state | Room intent or orchestration policy |

This split is the contract for M002/S01:

- the Room Spec remains the source of truth for **what should exist**;
- ARI `room/*` reflects or registers **what has been realized at runtime**;
- member work still enters through per-agent `agent/prompt`, not through Room creation.

## Top-Level Shape

```json
{
  "oarVersion": "0.1.0",
  "kind": "Room",
  "metadata": {},
  "spec": {}
}
```

## `metadata`

```json
{
  "metadata": {
    "name": "backend-refactor",
    "labels": {
      "project": "auth-service",
      "team": "backend"
    },
    "annotations": {}
  }
}
```

| Field | Type | Required | Meaning |
|---|---|---|---|
| `name` | string | yes | Desired Room name. Stable handle used when projecting to runtime. |
| `labels` | map[string]string | no | Orchestrator-level selection and grouping metadata. |
| `annotations` | map[string]string | no | Free-form metadata. |

## `spec.workspace`

`spec.workspace` describes the **desired shared workspace intent** for the Room.
The orchestrator is responsible for turning that intent into runtime calls.

```json
{
  "spec": {
    "workspace": {
      "source": {
        "type": "local",
        "path": "/home/user/project"
      }
    }
  }
}
```

The object follows [`../workspace/workspace-spec.md`](../workspace/workspace-spec.md).
The orchestrator may inline it or load it from another source, but agentd only sees the prepared runtime result (`workspaceId` + realized path), not the higher-level orchestration source.

### Shared workspace intent

A Room may intentionally project multiple members onto one prepared workspace. That means:

- all members can observe the same files;
- all members can mutate the same files;
- no per-agent filesystem isolation is implied by the Room Spec;
- the orchestrator owns whether that sharing is acceptable for the task.

The runtime projection may therefore contain several agents pointing at the same `workspaceId`.

## `spec.agents`

```json
{
  "spec": {
    "agents": [
      {
        "name": "architect",
        "description": "Designs the module structure and delegates implementation tasks.",
        "runtimeClass": "claude",
        "systemPrompt": "You are the lead architect for this refactor."
      },
      {
        "name": "coder",
        "description": "Implements the changes delegated by the architect.",
        "runtimeClass": "codex"
      },
      {
        "name": "reviewer",
        "description": "Reviews changes for correctness and security.",
        "runtimeClass": "gemini",
        "systemPrompt": "Review changes for correctness and security."
      }
    ]
  }
}
```

| Field | Type | Required | Meaning |
|---|---|---|---|
| `name` | string | yes | Desired agent name inside the Room. Must be unique within the Room. Together with the Room name, forms the stable external identity supplied to `agent/create`. |
| `description` | string | no | Human-readable purpose of this member. Forwarded to `agent/create` as the `description` field. |
| `runtimeClass` | string | yes | Runtime class name supplied to ARI `agent/create`. |
| `systemPrompt` | string | no | Bootstrap configuration for that member agent. Not a work turn. |

## `spec.communication`

```json
{
  "spec": {
    "communication": {
      "mode": "mesh"
    }
  }
}
```

| Field | Type | Required | Meaning |
|---|---|---|---|
| `mode` | string | yes | Desired communication topology for the runtime projection. |

Supported values:

| Mode | Meaning |
|---|---|
| `mesh` | any member may send work or coordination messages to any other member |
| `star` | one leader coordinates work; non-leaders only reply to the leader |
| `isolated` | members share a workspace but runtime message routing is disabled |

The orchestrator chooses the topology as desired state. agentd may enforce or expose the realized topology once the Room is projected into runtime state.

## Projection to Runtime

The Room Spec is not consumed directly by agentd. The orchestrator projects it into ARI calls.

Typical flow:

1. Read the Room Spec.
2. Call `workspace/prepare` for `spec.workspace` to obtain a `workspaceId`.
3. Call `room/create` to register the **realized runtime projection** of the Room (name, labels, communication mode) in agentd.
4. For each desired member, call `agent/create` with:
   - `room` = `metadata.name`
   - `name` = `spec.agents[i].name`
   - `description` = `spec.agents[i].description`
   - `runtimeClass`
   - `workspaceId`
   - `systemPrompt`
   - any labels / MCP / permission bootstrap fields
5. Poll `agent/status` for each agent until state transitions to `created` or `error`.
6. After agents reach `created` state, deliver actual work through `agent/prompt`.
7. Use `room/status` or `agent/list` to inspect realized runtime membership.
8. Call `agent/stop` then `agent/delete` for each member, then `room/delete`, then `workspace/cleanup`.

## `agent/create` vs `agent/prompt`

The desired-state Room contract depends on one bootstrap story across the design set:

- `agent/create` is **async configuration-only bootstrap**.
  It selects runtime class, workspace attachment, room membership, bootstrap `systemPrompt`, env overrides, MCP servers, and permission posture.
  The call returns immediately with `state: "creating"`; callers must poll `agent/status` until the state reaches `created` or `error`.
- `agent/prompt` is the **work-entry path**.
  Whether the work comes from an external caller or another Room member, the runtime turn enters through the target agent's prompt path.

Room creation never implies that business work has already been delivered.

## Realized Runtime Room Semantics

Once projected into agentd, the runtime may track:

- realized Room name and labels;
- communication mode;
- realized member list (`agentName` → `agentState`);
- shared workspace attachment for those members;
- runtime routing metadata used for future Room delivery features.

The `room/status` response for members includes:

| Field | Meaning |
|---|---|
| `agentName` | Member name (stable external identity within the Room) |
| `description` | Human-readable purpose of this member |
| `runtimeClass` | Runtime class used at bootstrap |
| `agentState` | Current agent lifecycle state (`creating`, `created`, `running`, `stopped`, `error`) |

That realized runtime record is **not** the same thing as orchestrator intent:

- it does not decide whether new members should be added;
- it does not define completion policy;
- it does not replace the Room Spec as the desired-state source of truth.

## Security and Trust Boundaries

A Room amplifies host impact because it can intentionally place several agents on one workspace. The contract is therefore explicit:

- local workspace paths remain host paths and must be validated/canonicalized by workspace rules before attachment;
- shared workspace means shared write access unless a later runtime feature adds stronger isolation;
- hook execution remains a workspace concern and may perform host-side effects before any member receives a prompt;
- env injection is bootstrap configuration for each agent, not a Room-level secret-distribution channel;
- ACP stays behind the shim boundary; Room intent does not expose raw ACP capabilities directly.

## Follow-On Scope

This spec defines the desired-state contract only. Rich realized-room delivery behavior (for example direct `room/send` / `room/broadcast` semantics with durable routing guarantees) remains future runtime capability work rather than hidden scope inside the Room object itself.

## Example

```json
{
  "oarVersion": "0.1.0",
  "kind": "Room",
  "metadata": {
    "name": "backend-refactor",
    "labels": {
      "project": "auth-service"
    }
  },
  "spec": {
    "workspace": {
      "source": {
        "type": "local",
        "path": "/home/user/project"
      }
    },
    "agents": [
      {
        "name": "architect",
        "description": "Designs the module structure and delegates implementation tasks.",
        "runtimeClass": "claude",
        "systemPrompt": "Break the refactor into work items and delegate implementation."
      },
      {
        "name": "coder",
        "description": "Implements the changes delegated by the architect.",
        "runtimeClass": "codex"
      },
      {
        "name": "reviewer",
        "description": "Reviews changes for correctness, security, and style.",
        "runtimeClass": "gemini",
        "systemPrompt": "Review changes for correctness, security, and style."
      }
    ],
    "communication": {
      "mode": "mesh"
    }
  }
}
```
