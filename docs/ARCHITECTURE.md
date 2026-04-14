> Auto-generated. Do not edit directly.
> Last updated: 2026-04-14 after M012

# Architecture

## System Overview

OAR (Open Agent Runtime) is a local daemon (`agentd`) that manages the full lifecycle of AI agents on a single host. It exposes a JSON-RPC 2.0 socket API called ARI (Agent Runtime Interface) to orchestrators, and communicates with running agent processes via a shim binary that implements the ACP (Agent Control Protocol). The system follows the containerd Container/Task conceptual model: **AgentTemplate** is durable configuration (like a container image), **AgentRun** is the ephemeral running instance (like a container), and **Workspace** is the shared filesystem resource (like a volume).

A single `--root` flag (default `/var/run/agentd`) derives all runtime paths — socket, workspace root, bundle root, and bbolt metadata DB — with no config file required.

## Component Map

| Component | Package / Binary | Role |
|-----------|-----------------|------|
| **agentd daemon** | `cmd/agentd` | ARI JSON-RPC server; manages agent and workspace lifecycle; forks shim processes |
| **agentdctl** | `cmd/agentdctl` | CLI client for all ARI operations (agent, agentrun, workspace, runtime) |
| **pkg/agentd** | `pkg/agentd` | Core daemon logic: ProcessManager, AgentManager, RecoveryManager, WorkspaceManager |
| **pkg/ari/server** | `pkg/ari/server` | Typed ARI service implementation with adapter pattern (WorkspaceService, AgentRunService, AgentService) |
| **pkg/ari/client** | `pkg/ari/client` | ARIClient: typed Workspace/AgentRun/Agent sub-clients behind one Close/DisconnectNotify surface |
| **pkg/jsonrpc** | `pkg/jsonrpc` | Transport-agnostic JSON-RPC 2.0 framework (Server + Client + RPCError + Peer); 18 protocol tests |
| **pkg/shim/server** | `pkg/shim/server` | Typed shim service implementation (Prompt, Cancel, Load, Subscribe, Status, History, Stop) |
| **pkg/shim/client** | `pkg/shim/client` | ShimClient: Dial/DialWithHandler helpers; ParseShimEvent for notification dispatch |
| **pkg/events** | `pkg/events` | Event translation: Translator (monotonic seq), Envelope, JSONL event log, SubscribeFromSeq |
| **pkg/meta** | `pkg/meta` (via `pkg/store`) | bbolt metadata store: v1/workspaces, v1/agents (templates), v1/agentruns (instances) |
| **pkg/workspace** | `pkg/workspace` | Workspace preparation: GitHandler, EmptyDirHandler, LocalHandler; hook execution; reference counting |
| **pkg/runtime** | `pkg/runtime` | ACP client implementation; shim-side bootstrap: Initialize, SessionUpdate, RequestPermission |
| **api/ari** | `api/ari` | Canonical ARI domain types (Agent, AgentRun, Workspace) + service interface definitions |
| **api/shim** | `api/shim` | Canonical shim RPC types and service interface |
| **api/runtime** | `api/runtime` | OAR bundle config spec and runtime state types |
| **agent-shim** | `cmd/agentd shim` | Shim process (self-fork from agentd binary); implements shim RPC server; manages single ACP session |
| **workspace-mcp** | `cmd/agentd workspace-mcp` | MCP stdio server exposing `workspace_send` and `workspace_status` tools for agent-initiated routing |

## Data Flow

```
Orchestrator
    │  JSON-RPC 2.0 over Unix socket
    ▼
agentd ARI server (pkg/ari/server, pkg/jsonrpc)
    │
    ├── workspace/* ──► pkg/agentd WorkspaceManager
    │                       ├── pkg/workspace (GitHandler/EmptyDirHandler/LocalHandler)
    │                       └── bbolt v1/workspaces
    │
    ├── agent/* ────► pkg/agentd AgentManager
    │                       └── bbolt v1/agents (templates)
    │
    └── agentrun/* ─► pkg/agentd ProcessManager
                           ├── bbolt v1/agentruns (running instances)
                           ├── fork: agentd shim --bundle <path> --state-dir <path> --id <name>
                           │         └── pkg/runtime (ACP client) ◄──► AI agent process
                           └── pkg/events Translator (seq, turnId, streamSeq)
                                   └── JSONL event log → session/subscribe consumers
```

**Recovery flow (daemon restart):**
1. `RecoverSessions`: scan bbolt agentruns → reconnect live shims via `shimclient.DialWithHandler`
2. Reconcile DB state with `runtime/status` from live shim (shim is ground truth)
3. `SubscribeFromSeq(fromSeq=0)` holds Translator mutex during log read + subscription registration (eliminates History→Subscribe gap)
4. `session/load` (tryReload policy) fired AFTER Subscribe is established
5. Creating-phase agents (bootstrap interrupted) marked StatusError

## Key Constraints

- **Shim write authority boundary (D088):** After shim bootstrap, `agentd` NEVER writes `idle/running/stopped/error` to DB directly. All post-bootstrap state transitions flow through `runtime/stateChange` notifications from the shim. DB serves only as a fast admission gate.
- **Agent identity (D087):** Agents are identified by `(workspace, name)` composite key — no opaque UUID. CLI uses `--workspace` / `--name` flags.
- **Workspace cleanup safety (D048):** `workspace/cleanup` gates on DB `ref_count` (persisted truth), not volatile in-memory RefCount. In-memory state is rebuilt from DB after restart via `Registry.RebuildFromDB`.
- **Recovery phase (D041):** `RecoverSessions` MUST transition to `RecoveryPhaseComplete` on every exit path, including systemic failures. A permanent recovery-blocked state is unacceptable.
- **macOS socket path limit:** Unix domain socket paths are capped at 104 bytes on macOS, 108 on Linux. Socket paths validated at agentrun/create entry (-32602 on overflow). Tests use `/tmp/oar-<pid>-<counter>.sock`.
- **Binary consolidation (D103):** Exactly two binaries: `agentd` (server/shim/workspace-mcp subcommands) and `agentdctl` (resource management subcommands).
- **Async agentrun/create (D063):** Returns immediately with `creating` state. Background goroutine handles workspace acquisition, shim fork, ACP bootstrap. Caller polls `agentrun/status` until `idle` or `error`.

## Tech Stack

| Concern | Choice | Decision |
|---------|--------|----------|
| Metadata storage | `go.etcd.io/bbolt` (pure Go KV, single-writer) | D084 |
| JSON-RPC transport | `pkg/jsonrpc` (custom, transport-agnostic) over Unix socket | M012/S01 |
| Shim RPC (legacy transport) | `sourcegraph/jsonrpc2` (retained in shim layer) | D060 |
| State enum | `spec.Status` (creating/idle/running/stopped/error) — single enum everywhere | D085 |
| MCP tools | `github.com/modelcontextprotocol/go-sdk/mcp` SDK (adopted M005/S06) | D080 |
| Event ordering | Monotonic `seq` + per-turn `turnId`/`streamSeq` in `events.Envelope` | D026, D067 |
| Config | `--root` flag only; `Options{}` derives all 5 paths deterministically | D104 |
| Linting | `golangci-lint v2` — 0 issues enforced (M006 baseline) | M006 |
| Testing | `go test ./...` single-count; integration tests in `tests/integration/`; mockagent at `internal/testutil/mockagent` | K032 |

## Package Dependency Graph (simplified)

```
cmd/agentd ──► pkg/agentd ──► pkg/ari/server ──► api/ari
                          ──► pkg/shim/client ──► api/shim
                          ──► pkg/events       ──► api/events.go
                          ──► pkg/workspace
                          ──► pkg/store (bbolt)

cmd/agentdctl ──► pkg/ari/client ──► api/ari
                               ──► pkg/jsonrpc

agent-shim ──► pkg/shim/server ──► api/shim
           ──► pkg/runtime      (ACP client)
           ──► pkg/events
```

See [docs/DECISIONS.md](DECISIONS.md) for all architecture decisions.
