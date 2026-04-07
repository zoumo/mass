# agentd — runtime realization daemon

agentd is the daemon that realizes already-decided runtime objects.
It owns workspaces, sessions, runtime bootstrap, process supervision, and the realized room projection needed for routing and inspection.
It does **not** own orchestrator intent.

## Desired vs Realized

| Concern | Primary owner | agentd role |
|---|---|---|
| Which Room should exist | orchestrator / `docs/design/orchestrator/room-spec.md` | realize it if asked |
| Which agents should be members | orchestrator | store realized `room` / `roomAgent` membership on sessions |
| When work is complete | orchestrator | expose runtime state only |
| Workspace preparation and cleanup | workspace manager | authoritative runtime execution |
| Session bootstrap and process truth | session/process managers | authoritative runtime execution |
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
- workspace hooks are host commands, not in-session work;
- managed workspaces may be deleted on cleanup, local workspaces may not;
- shared workspace reuse is explicit and reference-counted.

### Session Manager

Session Manager owns the lifecycle metadata for OAR sessions.
A session is the durable runtime object that records:

- OAR `sessionId`;
- selected `runtimeClass`;
- attached `workspaceId`;
- realized room membership (`room`, `roomAgent`) if present;
- labels and bootstrap inputs;
- lifecycle state (`created`, `running`, `paused:*`, `stopped`).

A session is not the same thing as an ACP peer session. ACP `sessionId` is protocol-local identity behind the shim boundary.

### Process Manager

Process Manager realizes a session into an actual runtime process through the shim.
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
- member-to-session mapping;
- shared-workspace attachment visibility;
- future routing and inspection APIs.

Room state in agentd does **not** decide desired membership or completion policy.
It exists so the runtime can say what is currently realized.

## Bootstrap Contract

The converged bootstrap story is:

1. `workspace/prepare` returns `workspaceId` plus a realized canonical host path.
2. `session/new` is **configuration-only** bootstrap.
3. agentd resolves runtime class, room metadata, env overrides, MCP inputs, and permission posture.
4. agentd writes the bundle (`config.json`) and wires `agentRoot.path` to the prepared workspace.
5. the runtime resolves bundle-relative `agentRoot.path` to a canonical `cwd`, performs ACP bootstrap (`initialize`, ACP `session/new`), and reaches bootstrap-complete / idle state.
6. actual work arrives later through `session/prompt`.

That means `session/new` may still cause runtime bootstrap side effects, but it is not itself a user work turn.

## `session/new`

`session/new` creates the OAR session object and materializes its bootstrap configuration.
It is responsible for:

- allocating OAR `sessionId`;
- validating `runtimeClass` and `workspaceId`;
- recording realized room membership (`room`, `roomAgent`) if present;
- recording `systemPrompt` as bootstrap configuration;
- merging env and capability posture;
- writing bundle state needed by the runtime.

`session/new` does **not** carry task work.
It is configuration-only even if agentd or the runtime starts the process during bootstrap.

## `session/prompt`

`session/prompt` is the runtime work-entry path.
All externally supplied or routed work enters through it after bootstrap:

- first user prompt after session creation;
- subsequent turns on an existing session;
- future room-delivered work routed to a target member session.

This keeps create/bootstrap semantics separate from work execution semantics.

## Environment and capability posture

agentd must describe one env precedence order across the design set:

1. inherited daemon/host environment forms the base;
2. `runtimeClass.env` overlays the base;
3. `session/new` env overrides overlay last.

The resolved env snapshot is runtime bootstrap state and is a follow-on persistence concern under R036.

Capability posture is also explicit:

- ACP remains the inner protocol between shim and agent;
- agentd exposes a curated ARI surface (`session/*`, `workspace/*`, realized `room/*`, attach notifications);
- raw ACP client responsibilities such as `fs/*`, `terminal/*`, or low-level protocol negotiation remain behind the shim boundary and are governed by permission policy rather than by direct ARI passthrough.

## Shared workspace semantics

Multiple sessions may intentionally point at one `workspaceId`.
That includes realized Room members.
The runtime guarantees reference tracking and cleanup safety, but **not** per-session filesystem isolation.
If several members share a workspace, they share read/write impact on the same host path.

## Runtime bootstrap flow

```text
orchestrator
  -> workspace/prepare(spec)
  <- { workspaceId, path }

orchestrator
  -> room/create(...)              # realized runtime projection, optional to desired-state model but owned here if used
  <- { name, status }

orchestrator
  -> session/new(...)             # configuration-only bootstrap
  <- { sessionId, state:"created" }

agentd / runtime
  -> materialize bundle + resolve cwd + ACP initialize + ACP session/new
  -> reach bootstrap-complete / idle state

orchestrator or another runtime caller
  -> session/prompt(...)
  <- work result / streamed updates
```

## Recovery and persistence posture

agentd is authoritative for realized runtime metadata, but the design set still leaves several durable-state gaps for later work:

- persisted OAR ↔ ACP session identity mapping;
- persisted resolved `cwd` and bundle/shim paths;
- persisted realized room projection snapshots and shared-workspace attachment snapshots;
- persisted resolved bootstrap env / permissions / MCP inputs;
- restart-safe replay, reconnect, cleanup, and cross-client hardening.

Those are follow-on items, not hidden assumptions inside the current Room or session contract.

## Security boundary summary

- local path attachment is host-impacting and must be canonicalized before registration;
- hooks execute as host commands and can have host-side effects before any session prompt runs;
- env layering is explicit and must not be treated as an implicit secret fan-out channel;
- shared workspace means shared host-path impact;
- ACP capability exposure is intentionally narrower at the ARI boundary than at the shim boundary.
