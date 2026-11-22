# MASS

MASS runs Codex and Claude Sessions as a service. Use `massctl` or the WebSocket
JSON-RPC API to send input, watch live events, and read saved history. Use the
API to answer permission requests and questions. A Runtime configures an agent
backend. A Session belongs to a Runtime and has its own working directory and
history.

[Quick start](docs/quick-start.md) · [API reference](harness/bindings/jsonrpc.md)

## Components

| Component | Role |
|---|---|
| [`massd`](massd/README.md) | Manages Runtime and Session lifecycles, coordinates agent drivers, and stores events. |
| [`massctl`](docs/quick-start.md#start-a-local-daemon) | Connects to `massd` to manage Runtimes and Sessions from the terminal. |
| [Superagent](superagent/README.md) | Experimental application that routes Messages to Topics and coordinates Issue Sessions through `massd`. The read-only Observer shows routing and work status. |

## Architecture

See the [MASS architecture contract](harness/architecture.md) or explore the
[massd](docs/assets/mass-v2-architecture.html),
[Superagent](docs/assets/superagent-architecture.html), and
[combined](docs/assets/mass-superagent-architecture.html) diagrams.

Superagent draws on the [DeepSeek Harness Superagent prototype](https://github.com/deepseek-ai/dsh-superagent-prototype).

## Development

Use the [Makefile](Makefile) for build and test targets. See the
[project harness](harness/index.md) for development principles and design contracts.

## License

[Apache License 2.0](LICENSE). See [NOTICE](NOTICE) for third-party notices.
