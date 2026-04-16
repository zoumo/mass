<div align="center">

```
 ███╗   ███╗ █████╗ ███████╗███████╗
 ████╗ ████║██╔══██╗██╔════╝██╔════╝
 ██╔████╔██║███████║███████╗███████╗
 ██║╚██╔╝██║██╔══██║╚════██║╚════██║
 ██║ ╚═╝ ██║██║  ██║███████║███████║
 ╚═╝     ╚═╝╚═╝  ╚═╝╚══════╝╚══════╝
```

### Multi-Agent Supervision System

*An OCI-inspired runtime for AI agent lifecycle management.*

[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat-square&logo=go&logoColor=white)](https://go.dev)
[![Architecture](https://img.shields.io/badge/Architecture-OCI--Inspired-blueviolet?style=flat-square)](docs/design/README.md)
[![JSON-RPC](https://img.shields.io/badge/Protocol-JSON--RPC%202.0-orange?style=flat-square)](https://www.jsonrpc.org)

---

**Create** · **Supervise** · **Recover** · **Scale**

</div>

## What You Get

With MASS, you get a **production-grade runtime** that turns AI agents into manageable, observable, recoverable services — not fragile scripts you babysit.

<table>
<tr>
<td width="50%">

### 🔌 Run Any ACP Agent as a Service
Launch Claude Code, Codex, or any [ACP](https://github.com/coder/acp-go-sdk)-compatible agent as a long-running, supervised process. One command to create, prompt, cancel, restart, or stop — all through a unified API.

```bash
massctl agentrun create --agent claude-code
massctl agentrun prompt --prompt "Fix the auth bug"
massctl agentrun stop
```

</td>
<td width="50%">

### 👁️ Full Observability Over Agent Behavior
Every thought, tool call, message, and state transition is captured as a typed, sequenced event stream. Connect to a running agent's shim at any time — reconnect and replay from any point without losing a single event.

```bash
massctl shim chat --sock /path/to/shim.sock
# Or connect via workspace + agent name:
massctl agentrun chat -w myproject --name my-agent
# Interactive TUI: thinking → tool_call → agent_message → ...
```

</td>
</tr>
<tr>
<td width="50%">

### 🔄 Crash Recovery Without Agent Downtime
Daemon crashes? Agents keep running. On restart, MASS automatically reconnects to surviving agent processes and replays missed events — zero data loss, zero manual intervention.

</td>
<td width="50%">

### 📂 Managed Workspaces for Multi-Agent Collaboration
Clone repos, provision scratch dirs, or mount local paths — then share them across multiple agents with automatic ref-counted cleanup. Agents collaborate on the same codebase without stepping on each other.

</td>
</tr>
<tr>
<td width="50%">

### 🧩 Programmatic Control via JSON-RPC
Everything is an API call. Build orchestrators, CI pipelines, or custom UIs on top of ARI (Agent Runtime Interface) — the same way Kubernetes builds on CRI.

```json
{"method": "agentrun/create", "params": {"workspace": "myproject", "agent": "claude-code"}}
{"method": "agentrun/prompt", "params": {"prompt": "Refactor the auth module"}}
```

</td>
<td width="50%">

### ⚡ Single Binary, Instant Deployment
No Docker. No databases. No message queues. Just `make build` and you have two binaries (`mass` + `massctl`) that manage everything via Unix sockets and an embedded key-value store.

```bash
make build
bin/mass server &
bin/massctl agentrun create --agent my-agent
```

</td>
</tr>
</table>

---

## What is MASS?

MASS manages AI coding agents the way **containerd manages containers** — with clean layering, spec-driven contracts, and recovery built into the foundation.

Instead of reinventing the wheel, MASS borrows the battle-tested architecture of the [OCI](https://opencontainers.org/) container ecosystem and maps it directly onto the agent domain:

```
OCI (Containers)                    MASS (Agents)
─────────────────                   ─────────────────
runc + containerd-shim         →    agent-shim
containerd                     →    agentd
CRI (Container Runtime Interface)→  ARI (Agent Runtime Interface)
OCI Runtime Spec               →    MASS Runtime Spec
OCI Image Spec                 →    MASS Workspace Spec
crictl                         →    massctl
```

> Containers solved a structurally isomorphic problem: how to standardize describing, preparing, and executing isolated workloads. Agents face the same layered concerns — minus the kernel isolation.

## Why "MASS"?

**M**ulti-**A**gent **S**upervision **S**ystem.

The name reflects what it does: supervise multiple AI agents on a single host with the rigor of a production runtime. Not a framework. Not an SDK. A **supervision system** — like systemd for agents, but with agent-native semantics baked in.

## Design Principles

| # | Principle | What it means |
|---|-----------|---------------|
| 1 | **Spec-First** | Define interfaces and wire formats before writing code. Specs are contracts; components are swappable implementations. |
| 2 | **No Container Baggage** | Borrow OCI's architecture, not its kernel isolation. No namespaces, cgroups, seccomp, or pivot_root. Agents are processes, not sandboxes. |
| 3 | **Agent-Native Concerns** | Focus on what agents actually need: workspace preparation, protocol communication (ACP), skill/knowledge injection, inter-agent messaging. |
| 4 | **Layered Separation** | Each layer does one thing. agent-shim runs the process and holds ACP. agentd manages lifecycle. External callers decide what to run. |
| 5 | **Simplicity First** | Design for current needs. Extension points exist but stay empty until real requirements emerge. |

## Architecture

```
┌──────────────────────────────────────────────────────────────┐
│  Orchestrator / massctl CLI                                  │
│  ─────────────────────────────────────────────────────────── │
│  ARI JSON-RPC 2.0 over Unix socket                           │
└───────────────────────┬──────────────────────────────────────┘
                        │
┌───────────────────────▼──────────────────────────────────────┐
│  agentd                                                      │
│  ┌────────────────┐ ┌─────────────────┐ ┌─────────────────┐ │
│  │ Workspace Mgr  │ │  Agent Manager  │ │ Process Manager │ │
│  │ git/emptyDir/  │ │  CRUD + specs   │ │ fork/watch/     │ │
│  │ local sources  │ │                 │ │ recover/restart │ │
│  └────────────────┘ └─────────────────┘ └────────┬────────┘ │
│                                                   │          │
│  bbolt metadata store ──── recovery engine        │          │
└───────────────────────────────────────────────────┼──────────┘
                        Unix socket per agent       │
                  ┌─────────────────────────────────▼──────┐
                  │  agent-shim                            │
                  │  ┌──────────────────────────────────┐  │
                  │  │ ACP Protocol (JSON-RPC / stdio)  │  │
                  │  │ Event Translator + EventLog      │  │
                  │  │ Session metadata hooks            │  │
                  │  └──────────────┬───────────────────┘  │
                  └─────────────────┼──────────────────────┘
                                    │ stdin/stdout
                  ┌─────────────────▼──────────────────────┐
                  │  AI Agent Process                      │
                  │  (Claude Code, Codex, custom agents)   │
                  └────────────────────────────────────────┘
```

**Three layers, clear boundaries:**

| Layer | Component | Analogous to | Responsibility |
|-------|-----------|-------------|----------------|
| **L0** | agent-shim | runc + containerd-shim | Run one agent process, hold ACP stdio, translate events |
| **L1** | agentd | containerd | Multi-agent lifecycle, workspace provisioning, metadata, recovery |
| **L2** | ARI | CRI | External control plane interface (JSON-RPC 2.0) |

## Key Advantages

### Recovery-First Design

Daemon crash? No problem. On restart, agentd reconnects to surviving shim processes, replays event history via K8s-style List-Watch (`session/watch_event` with `fromSeq`), and resumes exactly where it left off. Zero agent downtime.

### Shim Write Authority

After bootstrap, **only the shim drives state transitions** — agentd never writes `idle/running/stopped` directly. This eliminates an entire class of race conditions between the control plane and the runtime.

### Typed Event Streaming

Every event carries a globally monotonic `seq` (like K8s `resourceVersion`), a `turnId` for conversation scoping, and content block streaming status. Clients can replay from any point, dedup automatically, and never miss an event.

### Async Bootstrap

`agentrun/create` returns immediately. The agent initializes in the background — workspace cloning, ACP handshake, capability discovery — all happen asynchronously. Poll `agentrun/status` or watch events to track progress.

### Workspace Sharing

Multiple agents can share a workspace with ref-counted lifecycle management. Three source types out of the box:

| Source | Use case |
|--------|----------|
| `git` | Clone a repo (shallow, single-branch) |
| `emptyDir` | Ephemeral scratch space |
| `local` | Pre-existing directory (unmanaged) |

### Crash-Resilient Event Log

Events are persisted to NDJSON with **damaged-tail tolerance**: corrupt lines at the end of the log (from a crash mid-write) are automatically skipped. Mid-file corruption is caught and reported. No manual repair needed.

### Single Binary, Zero Dependencies

Pure Go. Two binaries: `mass` (daemon + shim) and `massctl` (CLI). No external databases, no container runtime, no message queues. Just Unix sockets and bbolt.

## Quick Start

```bash
# Build
make build

# Start the daemon
bin/mass server --state-dir /tmp/mass-state

# In another terminal — create a workspace and run an agent
bin/massctl workspace create --name myproject --source-type local --source-path /path/to/code
bin/massctl agentrun create --workspace myproject --agent claude-code
bin/massctl agentrun prompt --workspace myproject --name claude-code-xxxx --prompt "Explain main.go"
```

## ARI Method Surface

| Group | Methods |
|-------|---------|
| `workspace/*` | `create` · `get` · `list` · `delete` · `send` |
| `agent/*` | `create` · `update` · `get` · `list` · `delete` |
| `agentrun/*` | `create` · `prompt` · `cancel` · `stop` · `delete` · `restart` · `list` · `get` |

## Project Structure

```
cmd/
  mass/              daemon + agent-shim + workspace-mcp (single binary)
  massctl/           management CLI
pkg/
  ari/               Agent Runtime Interface (server + client + API types)
  agentd/            Process Manager, recovery, agent lifecycle
  shim/              agent-shim (server + client + API + ACP runtime)
  workspace/         Workspace Manager (Git/EmptyDir/Local, hooks, ref-counting)
  meta/              bbolt metadata store
  jsonrpc/           transport-agnostic JSON-RPC 2.0 framework
  runtime-spec/      MASS Runtime Spec types
docs/
  design/            spec documents + architecture decisions
```

## Tech Stack

| Layer | Technology |
|-------|-----------|
| Language | Go 1.26+ |
| RPC | JSON-RPC 2.0 (`sourcegraph/jsonrpc2`) |
| Storage | bbolt (embedded key-value) |
| Agent Protocol | ACP (JSON-RPC over stdio) |
| CLI | cobra |
| Event Log | NDJSON |

## Documentation

- **[Design Specs](docs/design/README.md)** — Architecture overview, OCI mapping, all spec documents
- **[Architecture](docs/ARCHITECTURE.md)** — Component map, data flow, package layout
- **[Decisions](.gsd/DECISIONS.md)** — Architectural decision records (D001–D112+)

---

<div align="center">

*Built with the belief that AI agents deserve the same operational rigor as containers.*

**MASS** — because managing agents shouldn't be harder than managing containers.

</div>
