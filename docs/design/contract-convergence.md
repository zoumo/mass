# Contract Convergence

This document is the slice-level authority map for the design set. It does not replace the normative specs; it names which document owns each contract and records the invariants that must stay aligned while the rewrite lands.

## Authority Map

| Contract topic | Primary authority | Supporting docs | Current convergence note |
|---|---|---|---|
| Bundle schema and bootstrap inputs | `docs/design/runtime/config-spec.md` | `docs/design/runtime/runtime-spec.md`, `docs/design/runtime/design.md`, `docs/design/agentd/ari-spec.md` | `oarVersion`, `agentRoot.path`, `acpAgent.*`, and permission policy must tell one startup story. |
| Runtime lifecycle and state model | `docs/design/runtime/runtime-spec.md` | `docs/design/runtime/design.md`, `docs/design/agentd/agentd.md`, `docs/design/agentd/ari-spec.md` | OAR session identity, ACP session identity, and runtime/process/session states must stay explicitly separated. |
| Shim control surface | `docs/design/runtime/shim-rpc-spec.md` | `docs/design/runtime/agent-shim.md`, `docs/design/agentd/agentd.md` | Shim RPC is the runtime control boundary; ACP stays behind the shim boundary. |
| Workspace preparation and ownership | `docs/design/workspace/workspace-spec.md` | `docs/design/agentd/agentd.md`, `docs/design/orchestrator/room-spec.md` | Workspace preparation, hook execution, and cleanup semantics must match agentd and Room sharing rules. |
| Room intent and realized membership | `docs/design/orchestrator/room-spec.md` | `docs/design/agentd/ari-spec.md`, `docs/design/agentd/agentd.md` | Orchestrator owns desired room intent; agentd owns realized runtime membership and routing state. |
| External control API | `docs/design/agentd/ari-spec.md` | `docs/design/runtime/runtime-spec.md`, `docs/design/runtime/shim-rpc-spec.md` | ARI describes what callers ask agentd to do; it must not redefine runtime or shim semantics independently. |

## Bootstrap Contract

The converged bootstrap story for this milestone is:

1. `workspace/prepare` produces a workspace identity plus a host path under workspace-manager rules.
2. `session/new` is configuration-only. It selects runtime class, workspace, labels, room metadata, and session bootstrap inputs. It does not represent user work execution.
3. agentd materializes the bundle by writing `config.json` and wiring `agentRoot.path` to the prepared workspace.
4. The runtime resolves `agentRoot.path` to the canonical host `cwd` used for process startup and ACP session bootstrap.
5. After creation completes, externally supplied work enters through `session/prompt`.

Invariants that later tasks will refine:

- `agentRoot.path` is the bundle-relative input; resolved `cwd` is the runtime-derived output.
- `systemPrompt` belongs to session bootstrap semantics, not to a user work turn.
- Runtime-class process settings, request env overrides, MCP server wiring, and permission policy must merge in one documented order.

## State Mapping

The design set must maintain one explicit mapping across these layers:

| Layer | Identity | State owned here | Notes |
|---|---|---|---|
| Orchestrator | Room name, desired agent name | Desired room membership and completion logic | Orchestrator decides what should exist. |
| agentd / ARI | OAR `sessionId`, `workspaceId`, realized room membership | Session lifecycle, workspace refs, persisted metadata | agentd is the authority for realized runtime objects. |
| Runtime / shim | Runtime process identity and `status` (`creating`/`created`/`running`/`stopped`) | Process state, typed event history, runtime-local failure details | Runtime does not redefine orchestration ownership. |
| ACP peer session | ACP `sessionId` | Agent-protocol session state | Separate protocol identity; never implied to equal OAR session ID. |

Durable gaps that S03 must close explicitly:

- the persisted OAR `sessionId` ↔ ACP `sessionId` mapping
- the resolved `cwd` derived from `agentRoot.path`
- bundle path and shim socket path needed for reconnect and inspection
- the resolved bootstrap snapshot (`systemPrompt`, env overrides, `mcpServers`, permissions)
- last known runtime/process transition metadata for recovery and diagnostics

## Security Boundaries

The design docs must describe the following boundaries consistently:

- **Local workspace attachment**: local paths are host paths, must be validated and canonicalized before use, and remain outside agentd-managed deletion.
- **Hook execution**: workspace hooks are host commands executed by agentd with workspace-side effects and observable failure reporting.
- **Environment injection**: runtime-class env, request env overrides, and inherited host environment need one precedence story without leaking secrets into docs or verification output.
- **Shared workspace access**: Room members may share one workspace path; the contract must say who owns isolation expectations and cleanup safety.
- **ACP capability posture**: ACP remains the inner protocol. The public contract must say which capabilities are exposed through shim/ARI boundaries versus left as implementation detail.

## Shim Target Contract

The clean target for the runtime/shim design set is:

- one normative shim control surface that matches the runtime lifecycle actually being specified
- one event model for subscribers, with no legacy surface kept as if it were current contract
- one recovery story for socket discovery, reconnect, and state inspection
- one statement of where ACP is translated, hidden, or passed through

Later tasks in this slice will replace contradictory legacy wording in the runtime and shim docs. Until that rewrite lands, this file is the cross-doc checklist future verification should enforce.
