# Contract Convergence

This file is the slice-level authority map for the design set. It names which document owns each contract and records the invariants that must stay aligned while the rewrite lands.

## Authority Map

| Contract topic | Primary authority | Supporting docs | Converged note |
|---|---|---|---|
| Bundle schema and bootstrap inputs | `docs/design/runtime/config-spec.md` | `docs/design/runtime/runtime-spec.md`, `docs/design/runtime/design.md`, `docs/design/agentd/ari-spec.md` | `agent/create` is async configuration-only bootstrap; `agent/prompt` is the work-entry path. |
| Runtime lifecycle and state model | `docs/design/runtime/runtime-spec.md` | `docs/design/runtime/design.md`, `docs/design/agentd/agentd.md`, `docs/design/agentd/ari-spec.md` | Agent is the external API object; session is the internal runtime realization. OAR agent identity, ACP session identity, and runtime process state remain explicitly distinct. |
| Workspace preparation and host-impact rules | `docs/design/workspace/workspace-spec.md` | `docs/design/agentd/agentd.md`, `docs/design/agentd/ari-spec.md`, `docs/design/orchestrator/room-spec.md` | local workspace, hook execution, env precedence, and shared workspace semantics must tell one safety story. |
| Room desired intent | `docs/design/orchestrator/room-spec.md` | `docs/design/agentd/ari-spec.md`, `docs/design/agentd/agentd.md` | The Room Spec is orchestrator-owned desired state. |
| Room realized runtime projection | `docs/design/agentd/ari-spec.md` | `docs/design/agentd/agentd.md`, `docs/design/orchestrator/room-spec.md` | `room/*` is the runtime projection for realized membership and routing metadata, not desired orchestration policy. |
| Public control API at the orchestrator boundary | `docs/design/agentd/ari-spec.md` | `docs/design/agentd/agentd.md`, `docs/design/runtime/agent-shim.md` | ARI exposes the curated host-facing control surface using `agent/*` methods; it does not expose raw ACP or raw shim internals. |
| Shim control, replay, and reconnect contract | `docs/design/runtime/shim-rpc-spec.md` | `docs/design/runtime/runtime-spec.md`, `docs/design/runtime/agent-shim.md`, `docs/design/agentd/agentd.md` | The clean-break shim surface is `session/*` + `runtime/*` (internal); runtime-spec owns state-dir / socket layout, shim-rpc-spec owns recovery method semantics. |

## Desired vs Realized Room Model

The design set now uses one Room model:

- **Desired Room**: owned by the orchestrator and described by `docs/design/orchestrator/room-spec.md`.
  It says which members should exist, which workspace intent they should share, and which communication topology is intended.
- **Realized Room**: owned by agentd as runtime state and described by `docs/design/agentd/ari-spec.md` / `docs/design/agentd/agentd.md`.
  It records which agents were actually created, which `workspaceId` they currently share, and which runtime communication metadata is active.

This removes the old contradiction where one doc claimed "agentd only sees sessions" while another treated `room/*` as runtime-managed truth.

## Bootstrap Contract

The converged bootstrap story is:

1. `workspace/prepare` produces a `workspaceId` and canonical host path.
2. `room/create`, when used, registers only the **realized runtime projection** of an already-decided Room.
3. `agent/create` is **async**: it selects runtime class, workspace, room membership (`room` + `name` identity pair), `description`, `systemPrompt`, env overrides, MCP servers, labels, and permission posture. The call returns immediately with `state: "creating"`.
4. Callers poll `agent/status` until state transitions to `created` or `error`. agentd materializes the bundle and the runtime resolves `agentRoot.path` into the canonical `cwd` used for process startup and ACP initialization.
5. After agents reach `created` state, actual work enters through `agent/prompt`.

Invariant wording:

- `agent/create` is async configuration-only bootstrap.
- `agent/prompt` carries work.
- Agent identity = `room` + `name` (stable external key).
- OAR agent identity is not ACP session identity.
- `systemPrompt` is bootstrap configuration, not a hidden work turn.
- The shim surface (`session/*` + `runtime/*`) is UNCHANGED by the agent model; it remains an internal agentd↔shim protocol.

## Agent Model Convergence

M005 establishes the following invariants across the design set:

- **Agent is the external API object.** All orchestrator-facing methods use `agent/*`. Session is internal runtime realization only.
- **Agent identity = room + name.** The `(room, name)` pair is the stable external key. `agent/create` requires both fields; `agent/status`, `agent/prompt`, `agent/stop`, `agent/delete`, and `agent/restart` resolve agents by this identity.
- **All agents belong to a room.** There is no free-standing (room-less) agent in the external API. Room membership is required at creation time.
- **Agent state machine:** `creating` → `created` → `running` → `stopped`; `error` is reachable from `creating`, `created`, or `running`. The `paused:warm` / `paused:cold` states are removed from the active state machine.
- **Shim surface unchanged.** The internal shim protocol (`session/*` + `runtime/*`) is not affected by this renaming. agentd translates between external agent identity and internal session handles internally.
- **Events at the agentd boundary** are `agent/update` and `agent/stateChange`. Shim-internal events remain `session/update` and `runtime/stateChange`.

## Security Boundaries

The design set now names these boundaries explicitly:

- **local workspace**: a host path attachment that must be validated and canonicalized before use and must remain outside agentd-managed deletion.
- **hook execution**: host commands executed by agentd around workspace lifecycle, with observable failure reporting and host-side effects.
- **env precedence**: inherited daemon/host environment → runtime-class env → `agent/create` env overrides. Workspace hooks are outside this chain.
- **shared workspace**: multiple agents may intentionally share one realized `workspaceId`; this implies shared visibility and shared write risk, not per-agent filesystem isolation.
- **capability posture**: ACP remains the inner protocol. ARI exposes only the curated control surface using `agent/*` methods; raw ACP client duties stay behind the shim boundary and are governed by permission policy.
- **workspace refs shift to agent level**: workspace attachment and cleanup lifecycle is tracked per agent (not per session) at the ARI surface.

## State Mapping

| Layer | Identity | State owned here | Notes |
|---|---|---|---|
| Orchestrator | Room name, desired agent names | desired Room membership and completion logic | decides what should exist |
| agentd / ARI (Agent Manager) | `(room, name)` agent identity, `workspaceId` | agent lifecycle (`creating`/`created`/`running`/`stopped`/`error`), realized room membership, workspace refs | external-facing; translates to internal session handles |
| agentd / internal (Session Manager) | OAR session handle (internal), `workspaceId` | session lifecycle, shim connections, realized workspace attachment | internal-only; not exposed through ARI |
| Runtime / shim | process identity, runtime status | process truth, typed notifications, runtime-local failure details | does not own orchestration intent |
| ACP peer session | ACP session identity | agent-protocol session state | separate protocol identity |

## Follow-on gaps reserved for S03 / later hardening

### R036 — truthful restart and rebuild gaps

Later work still needs durable storage or restart-safe reconstruction for:

- agent identity (`room` + `name`) ↔ internal session handle mapping;
- resolved `cwd` derived from `agentRoot.path`;
- realized Room projection snapshots and shared-workspace attachment snapshots;
- resolved bootstrap env / permissions / MCP server inputs;
- bundle path and shim socket path needed for reconnect and inspection.

### R044 — replay, cleanup, and cross-client hardening gaps

Later work still needs explicit hardening for:

- restart-safe replay and attach consistency;
- cleanup safety when shared workspace members disconnect or fail mid-lifecycle;
- richer cross-client and cross-member routing guarantees;
- stronger observability around hook side effects, env resolution, and room/workspace recovery decisions.

## Shim Target Contract

The shim-facing design set is now converged on the following target:

- the normative shim method surface is `session/prompt`, `session/cancel`, `session/subscribe`, `runtime/status`, `runtime/history`, and `runtime/stop` (internal agentd↔shim protocol, unchanged by M005);
- the normative live notification surface is `session/update` plus `runtime/stateChange` (internal);
- socket path and state-dir layout are owned by `runtime-spec.md`, while replay / reconnect semantics are owned by `shim-rpc-spec.md`;
- `agent-shim.md` is descriptive only: it explains component responsibilities and the ACP boundary, but it does not redefine method names or recovery rules;
- any remaining references to legacy PascalCase methods or `$/event` in implementation code or planning docs are implementation lag, not dual-source contract.

## Current slice proof target

For M005/S01, the docs are converged when they all say the same thing about:

- agent as external object, session as internal realization;
- `agent/create` (async) bootstrap and `agent/prompt` work-entry semantics;
- agent state machine (`creating`/`created`/`running`/`stopped`/`error`; no paused states);
- agent identity = `room` + `name`;
- shim surface unchanged (`session/*` + `runtime/*` remains internal);
- desired vs realized Room ownership;
- local workspace and hook host impact;
- env precedence and shared workspace implications;
- the capability/security boundary between ARI and ACP.
