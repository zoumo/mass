# Native crate workspace

All Rust source, tests, examples, vendor code and Rust scripts live under `mass-rs/`. The root Makefile delegates to `mass-rs/Cargo.toml`.

| Crate | Responsibility |
| --- | --- |
| mass-daemon | Configuration and process composition; executable massd |
| mass-domain | Durable domain data |
| mass-runtime | Provider-neutral attachment and command contracts |
| mass-journal | Atomic metadata and ordered file history |
| mass-events | Commit, publication, handlers and recovery validation |
| mass-codex | Native SDK adaptation, resources and private evidence validation |
| mass-sessions | Session lifecycle, ownership and query coordination |
| mass-http | JSON-RPC, health and live SSE |

The vendored official client is a separate workspace member. Product dependency direction is enforced by `mass-rs/scripts/check_workspace.py`, including exclusion of native SDK knowledge from generic crates. See [module contracts](../design/crate-workspace.md).

Each MASS crate owns public-interface integration tests. The daemon also has isolated process and HTTP tests. The default checks use no real model; explicit native scenarios live with the Codex adapter and daemon. See [testing](../testing.md).

Run `make fmt`, `make lint`, `make test`, `make build` or `make check` at the repository root. Direct Cargo calls require `--manifest-path mass-rs/Cargo.toml` or a working directory inside mass-rs. Rust 1.95+ is required; native macOS arm64 release checks pin Rust 1.96.0. Linux delivery is unverified.

Runtime data is independent of source layout. Existing deployments must explicitly configure their original absolute data directory; see [data ownership](../root-migration.md).
