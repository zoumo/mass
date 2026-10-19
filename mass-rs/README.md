# MASS Rust workspace

This directory owns all Rust product source, manifests, lockfile, native vendor
source, toolchain settings, tests, examples and Rust-specific scripts.
The repository root owns shared docs, licenses and the delegating Makefile.

The daemon crate is `mass-daemon` (Rust import `mass_daemon`), binary `massd`.
The seven responsibility crates are `domain`, `runtime`, `journal`, `events`,
`codex`, `sessions` and `http`. Public-interface integration tests live with each
crate in `tests/`. `tests/native_codex.rs` covers cross-crate HTTP/SSE and real
Codex flows; `tests/http_health.rs` includes production-daemon TCP, copied-data
and standalone release cases. `tests/native_stage.rs` runs and verifies the
explicit real-native matrix using prebuilt test executables.

From the repository root use `make fmt`, `make lint`, `make test`, `make build`,
`make check` and explicit `make test-native`. From here direct Cargo commands
use this workspace normally. Cargo build output defaults to `mass-rs/target`;
`CARGO_TARGET_DIR` can select an existing absolute cache. The root Makefile's
release output defaults to root `dist`; `RELEASE_DIST` can select a fresh directory.
Development requires Rust 1.95+; native macOS release validation uses Rust 1.96.0.
Linux acceptance remains deferred.

The no-config data default is **launch-cwd/data**, not a source-relative path.
Do not launch an existing deployment after changing cwd without explicitly
configuring its original absolute `dataDir`. Relative configured paths remain
relative to the config file. No migration moves user data, credentials, caches
or historical scratch evidence.

后续开发以 [harness](../harness/index.md) 为统一依据。本页仅说明当前 Rust 实现的运行与构建入口，不宣称新版设计已实现。

See [testing principles](../harness/principles/testing.md),
[architecture](../harness/architecture.md),
[HTTP binding draft](../harness/bindings/http.md) and
[storage binding draft](../harness/bindings/storage.md).
