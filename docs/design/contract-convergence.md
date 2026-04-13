# Contract Convergence

This file is the slice-level authority map for the design set. It names which document owns each contract and records the invariants that must stay aligned across all docs.

## Authority Map

| Contract topic | Primary authority | Supporting docs | Current implementation note |
|---|---|---|---|
| Bundle schema and bootstrap inputs | `docs/design/runtime/config-spec.md` | `docs/design/runtime/runtime-spec.md`, `docs/design/runtime/design.md`, `docs/design/agentd/ari-spec.md` | `agentrun/create` is async bootstrap; `agentrun/prompt` is the work-entry path. |
| Runtime lifecycle and state model | `docs/design/runtime/runtime-spec.md` | `docs/design/runtime/design.md`, `docs/design/agentd/agentd.md`, `docs/design/agentd/ari-spec.md` | AgentRun is the external API object; shim session is the internal runtime realization. AgentRun identity, ACP session identity, and runtime process state remain explicitly distinct. |
| Workspace preparation and host-impact rules | `docs/design/workspace/workspace-spec.md` | `docs/design/agentd/agentd.md`, `docs/design/agentd/ari-spec.md` | local workspace, hook execution, env precedence, and shared workspace semantics must tell one safety story. |
| Agent configuration CRUD | `docs/design/agentd/ari-spec.md` | `docs/design/agentd/agentd.md` | `agent/*` methods manage Agent records (set/get/list/delete). No runtime process is involved. |
| AgentRun lifecycle (running instances) | `docs/design/agentd/ari-spec.md` | `docs/design/agentd/agentd.md`, `docs/design/runtime/agent-shim.md` | ARI exposes `agentrun/*` methods for the lifecycle of running agent instances. Workspace-scoped message routing is via `workspace/send`. |
| Shim control, replay, and reconnect contract | `docs/design/runtime/shim-rpc-spec.md` | `docs/design/runtime/runtime-spec.md`, `docs/design/runtime/agent-shim.md`, `docs/design/agentd/agentd.md` | The clean-break shim surface: request/response is `session/*` + `runtime/*` (internal); notification surface is `shim/event`. runtime-spec owns state-dir / socket layout, shim-rpc-spec owns recovery method semantics. |

## Current Implementation Vocabulary

Use these terms consistently across all design documents:

| Term | Meaning |
|---|---|
| **Agent definition** | A reusable named configuration record (`name`, `command`, `args`, `env`, `startupTimeoutSeconds`). Selected by `agentrun/create.agent` using its name. Managed via `agent/*` ARI methods. No runtime process. |
| **AgentRun** | A running (or stopped) instance of an agent. Identified by `(workspace, name)`. Managed via `agentrun/*` ARI methods. Has a shim process. |
| **Workspace** | A prepared working directory (git / emptyDir / local). Managed via `workspace/*` ARI methods. |
| **shim session** | The internal ACP session managed by agent-shim. Uses `session/*` + `runtime/*` RPC. Not exposed externally. |

## ARI Boundary

Three groups of methods, each with a distinct concern:

| Group | Methods | Concern |
|---|---|---|
| `workspace/*` | `workspace/create`, `workspace/status`, `workspace/list`, `workspace/delete`, `workspace/send` | Workspace lifecycle and intra-workspace message routing |
| `agent/*` | `agent/set`, `agent/get`, `agent/list`, `agent/delete` | Agent CRUD |
| `agentrun/*` | `agentrun/create`, `agentrun/prompt`, `agentrun/cancel`, `agentrun/stop`, `agentrun/delete`, `agentrun/restart`, `agentrun/list`, `agentrun/status`, `agentrun/attach` | AgentRun lifecycle |

## Bootstrap Contract

The converged bootstrap story is:

1. `workspace/create` starts async workspace preparation. Returns immediately with `phase: "pending"`.
2. Caller polls `workspace/status` until `phase: "ready"`.
3. `agentrun/create` is **async**: it accepts `workspace` + `name` + `agent` (and optional `systemPrompt`, `labels`, `restartPolicy`). Returns immediately with `state: "creating"`.
4. Callers poll `agentrun/status` until state transitions to `"idle"` or `"error"`.
5. After the agent reaches `idle` state, actual work enters through `agentrun/prompt`.

Invariant wording:

- `agentrun/create` is async configuration-only bootstrap.
- `agentrun/prompt` carries work.
- AgentRun identity = `workspace` + `name` (stable external key).
- OAR AgentRun identity is not ACP session identity.
- `systemPrompt` is bootstrap configuration, not a hidden work turn.
- The shim request/response surface (`session/*` + `runtime/*`) is UNCHANGED; it remains an internal agentd↔shim protocol. The notification surface is unified as `shim/event`.

## AgentRun State Machine

```
creating ──┐
           ├──> idle ──> running ──> stopped
           |              │
    error <─┴─────────────┘
```

- `creating` → `idle`: shim started successfully, ACP initialized
- `creating` → `error`: shim start failed
- `idle` → `running`: `agentrun/prompt` dispatched
- `running` → `idle`: prompt turn completes
- `idle` / `running` → `stopped`: `agentrun/stop` received
- `running` → `error`: runtime failure during a turn
- `error` / `stopped` → `creating`: `agentrun/restart` triggers re-bootstrap

The `paused:warm` / `paused:cold` states do not exist in the current state machine.

## Security Boundaries

The design set now names these boundaries explicitly:

- **local workspace**: a host path attachment that must be validated and canonicalized before use and must remain outside agentd-managed deletion.
- **hook execution**: host commands executed by agentd around workspace lifecycle, with observable failure reporting and host-side effects.
- **env precedence**: inherited daemon/host environment → Agent definition env → (no AgentRun-level env override in current implementation). Workspace hooks are outside this chain.
- **shared workspace**: multiple AgentRuns may intentionally share one workspace name; this implies shared filesystem visibility and shared write risk, not per-agent isolation.
- **capability posture**: ACP remains the inner protocol. ARI exposes only the curated control surface using `agentrun/*` and `workspace/*` methods; raw ACP client duties stay behind the shim boundary.

## State Mapping

| Layer | Identity | State owned here | Notes |
|---|---|---|---|
| External Caller | desired workspace/agentrun names | desired membership and completion logic | decides what should exist |
| agentd / ARI | `(workspace, name)` AgentRun identity | AgentRun lifecycle (`creating`/`idle`/`running`/`stopped`/`error`), workspace refs | external-facing; translates to internal shim handles |
| agentd / internal (Process Manager) | shim socket path, shim PID, bundle directory | shim connections, realized workspace attachment | internal-only; surfaced via `agentrun/status` shimState |
| Runtime / shim | process identity, runtime status | process truth, typed notifications, runtime-local failure details | does not own scheduling intent |
| ACP peer session | ACP session identity | agent-protocol session state | separate protocol identity |

## Shim Target Contract

The shim-facing design set is converged on the following target (fully implemented):

- the normative shim method surface is `session/prompt`, `session/cancel`, `session/subscribe`, `runtime/status`, `runtime/history`, and `runtime/stop` (internal agentd↔shim protocol);
- the normative live notification surface is `session/update` plus `runtime/state_change` (internal);
- socket path and state-dir layout are owned by `runtime-spec.md`, while replay / reconnect semantics are owned by `shim-rpc-spec.md`;
- `agent-shim.md` is descriptive only: it explains component responsibilities and the ACP boundary, but it does not redefine method names or recovery rules;
- any remaining references to legacy PascalCase methods or `$/event` in planning docs or historical notes are implementation lag artifacts, not current API contract.

## Durable State — What Is and Is Not Persisted

Currently persisted in `AgentRunStatus`:

- `workspace`, `name`, `agent`, bootstrap configuration (`BootstrapConfig`);
- shim socket path (`ShimSocketPath`), state directory (`ShimStateDir`), shim PID (`ShimPID`) for live process reconnect;
- last known agent state.

Still gaps (future work):

- OAR runtime ID ↔ ACP `sessionId` mapping (restart diagnostics);
- hook stdout/stderr output persistence (currently not returned via ARI);
- AgentRun-level env override (currently no such field in `agentrun/create`);
- ARI-level event fanout to ARI clients (currently events are consumed via shim `session/subscribe` only).

## Future Work / Target Gaps

The following concepts are **not** implemented and must be marked as future work in design docs:

- **Room**: a shared-workspace group with messaging bus. Not implemented; no `room/*` ARI methods, no `pkg/agentd/room`, no room-spec.
- **workspace task / inbox**: structured task delegation and queued delivery. Not implemented.
- **ARI-level event fanout**: streaming events to ARI clients (e.g. via `agentrun/attach` event push). Currently `agentrun/attach` returns the shim socket path; ARI clients consume events via the shim socket directly.
- **AgentRun-level env override**: `agentrun/create` has no `env` field; only Agent definition env is used.
- **Hook output persistence**: workspace hook stdout/stderr is not returned through `workspace/status`.
