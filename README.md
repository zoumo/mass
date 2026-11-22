<div align="center">

```
 ███╗   ███╗ █████╗ ███████╗███████╗
 ████╗ ████║██╔══██╗██╔════╝██╔════╝
 ██╔████╔██║███████║███████╗███████╗
 ██║╚██╔╝██║██╔══██║╚════██║╚════██║
 ██║ ╚═╝ ██║██║  ██║███████║███████║
 ╚═╝     ╚═╝╚═╝  ╚═╝╚══════╝╚══════╝
```

### Multi-Agent Session Service

*Run and supervise AI agent sessions through a daemon, CLI, and API.*

[![Go](https://img.shields.io/badge/Go-1.26.9+-00ADD8?style=flat-square&logo=go&logoColor=white)](#build)
[![API](https://img.shields.io/badge/API-WebSocket_JSON--RPC-6366F1?style=flat-square)](harness/bindings/jsonrpc.md)
[![Status](https://img.shields.io/badge/Superagent-Experimental-F59E0B?style=flat-square)](#current-status)
[![License](https://img.shields.io/badge/License-Apache_2.0-22C55E?style=flat-square)](LICENSE)

**Create** · **Supervise** · **Interact** · **Observe**

[Quick start](#quick-start) · [Architecture](#architecture) · [Documentation](#documentation)

</div>

---

MASS runs Codex and Claude sessions as a service. Use `massctl` or the API to
send work, answer interaction requests, watch live progress, and read saved
session history.

## What is MASS?

`massd` is the mature runtime at the core of MASS. It manages Runtime and
Session lifecycles and exposes JSON-RPC 2.0 over WebSocket. Its drivers use the
Codex and Claude adapters in Agent SDK Go.

Connect an orchestrator, automation service, or agent interface directly to
`massd`. The repository also includes the experimental Superagent application
for coordinating work across sessions.

## What you get

| | Capability | What it gives you |
|:---:|---|---|
| 🔌 | **Agent as a service** | A named Runtime you can configure, start, inspect, and stop. |
| 💬 | **Session management** | Conversations with a working directory, input, cancellation, close, and resume through the runtime driver. |
| 👁️ | **Live events & history** | Watch progress as it happens and query saved events when you return. |
| 🤝 | **Interactive agents** | Permission requests and questions exposed to the calling application. |
| ⚡ | **CLI + JSON-RPC API** | Use `massctl` at the terminal or connect your own application through JSON-RPC 2.0 over WebSocket. |

See [current status](#current-status) for driver and client limits.

## Architecture

![massd architecture](docs/assets/mass-v2-architecture.svg)

`massctl` and API clients connect to `massd` over WebSocket JSON-RPC. The
transport calls Service, which coordinates session operations and dispatches
work to the Runtime drivers. MetaStore saves Runtime and Session resources in
SQLite by default, with a file backend available. EventStore appends Session
events to files and maintains a rebuildable SQLite query database.

Runtime and Session are the two main resources.

| Concept | Meaning |
|---|---|
| 🔌 **Runtime** | A named agent backend and its configuration, such as a Codex or Claude driver. |
| 💬 **Session** | A conversation owned by a Runtime, with a working directory, state, and event history. |

Subscriptions deliver new events on separate WebSocket connections. History
queries return committed events. You can end a subscription while work continues.
See the [interactive massd diagram](docs/assets/mass-v2-architecture.html) and the
[architecture contract](harness/architecture.md).

![Superagent architecture](docs/assets/superagent-architecture.svg)

Superagent accepts messages from channel adapters. Its Router directs messages
to Topics, and Topics coordinate Issues and Issue Reports. The read-only
Observer queries the business state that Superagent persists in its own SQLite
database. See the [interactive Superagent diagram](docs/assets/superagent-architecture.html).

![Combined MASS and Superagent architecture](docs/assets/mass-superagent-architecture.svg)

When execution is enabled, Superagent uses the public Go client to prepare
and drive Router Sessions, Topic Sessions, and Issue Sessions in `massd`. Other API clients can
connect to the same daemon directly. See the
[interactive combined diagram](docs/assets/mass-superagent-architecture.html).

## Superagent business Core

Superagent is an experimental application for routing messages and coordinating
agent work. You can run it for message intake with execution disabled. Enable
execution to run Issue Sessions through `massd`. Open `/observer/` to inspect message
routing and Issue progress.

Most of Superagent's design and core concepts were inspired by the
[DeepSeek Harness `dsh-superagent` prototype](https://github.com/deepseek-ai/dsh-superagent-prototype).

Build it with `make build-superagent`. For setup and data-directory
compatibility, see the [Superagent guide](superagent/README.md).

## Build

Run build commands from the repository root with **Go 1.26.9 or later**.
`make build-go` builds `massd` and `massctl` as separate executables. The full
build also builds Superagent and its Observer. It requires Node.js
20.19 or later in the 20.x series, or 22.12 or later, and a populated npm cache.

```sh
make build
# Produces bin/massctl, bin/massd and bin/superagent
```

| Binary | Role |
|---|---|
| `bin/massd` | 🛠️ Daemon for the WebSocket JSON-RPC API and health probes. |
| `bin/massctl` | ⌨️ CLI for managing Runtimes and Sessions. |
| `bin/superagent` | Experimental message routing and work coordination service with a read-only Observer. |

To build a single executable, select its package in `cmd/`.

```sh
mkdir -p bin
go build -mod=readonly -o bin/massd ./cmd/massd
go build -mod=readonly -o bin/massctl ./cmd/massctl
go build -mod=readonly -o bin/mass-record-diagnose ./cmd/mass-record-diagnose
```

The repository uses one Go module, [`github.com/zoumo/mass`](go.mod). Command
entries live in [`cmd/`](cmd/). The [`massd/`](massd/README.md) component owns
the session service, public Go client, and in-process assembly. Core, the
[Trigger packages](superagent/triggers/README.md), and Observer live under
[`superagent/`](superagent/README.md).

## Quick start

Run `make prototype-go` for an offline demonstration, or follow the [Quick start guide](docs/quick-start.md) to run MASS locally.

## Current status

Superagent is experimental.

| Area | Available today | Current limit |
|---|---|---|
| 🛠️ Service | WebSocket JSON-RPC API, CLI, and persistent resources and history | Direct remote access requires TLS and a token file. |
| 🧩 Agent drivers | Codex and Claude SDK adapters | The Agent CLI must be on the daemon's `PATH` or configured by absolute path. |
| 🧪 Demonstration | Isolated end-to-end runtime scenarios | Uses mock CLIs. Real-agent checks run separately. |
| 💬 Claude input | Text input, close, and resume | Each input uses one text block. Resume requires an absolute `CLAUDE_CONFIG_DIR`. |
| 🤝 Permissions & questions | JSON-RPC `session.answer` | Answer through the API. |
| Superagent | Message routing, Issue Session coordination, and a read-only Observer | Experimental. Execution requires explicit configuration. |

## Development

| Command | Purpose |
|---|---|
| `make fmt` | Format Go code |
| `make lint` | Run Go lint |
| `make test` | Run offline Go tests with the race detector and run Observer browser tests |
| `make test-unit-go` | Run package-local tests only |
| `make test-integration-go` | Run constructed multi-module scenarios and SDK probes |
| `make test-simulation-go` | Replay recorded Agent material through `agent-sdk-testify` |
| `make test-e2e-go` | Run the separate end-to-end suite with real Codex and Claude CLIs |
| `make build` | Build the daemon, CLI, and Superagent with its Observer |
| `make check` | Check the Go module and Observer with lint, tests, builds, and formatting checks |
| `make prototype-go` | Run the isolated synthetic end-to-end demonstration |

## Documentation

| Guide | Read it for |
|---|---|
| 📘 [Go implementation](massd/README.md) | Configuration, commands, and implementation limits |
| 🧭 [Project harness](harness/index.md) | Authoritative design, principles, and API contracts |
| 🌐 [API contract](harness/bindings/jsonrpc.md) | JSON-RPC 2.0 methods, subscription events, and transport rules |
| [Superagent guide](superagent/README.md) | Configuration, execution, and the Observer |

`massd` exposes `/jsonrpc`, `/livez`, and `/readyz`. The retired `/rpc` and
`/apis/v1` routes return `404` for authenticated requests. See the
[WebSocket endpoint upgrade steps](massd/README.md#upgrade-the-websocket-endpoint).

## License

[Apache License 2.0](LICENSE). See [NOTICE](NOTICE) for third-party notices.
