# macOS native release delivery

交付边界见[语言无关设计合同](design/single-file-delivery.md)；完整工具链依据见[发布实现记录](develop/release-implementation.md)。本页保留当前构建与验证操作。


Build from the repository root. Only `dist/<target>/massd` is needed for MASS deployment;
the adjacent `manifest.json` is a provenance/checksum report, not a runtime input.
The release remains an ordinary executable, with unwind-based Handler panic
isolation. No source, Cargo target/cache, Rust/Python/Node runtime or Homebrew
installation is needed on the deployment machine.

| Target | Declared deployment baseline | Build environment |
|---|---|---|
| `aarch64-apple-darwin` | Apple Silicon, macOS 14.0+ | Rust 1.96.0, Apple command line tools/SDK, Python 3.9+, make, file/otool, C compiler |

macOS uses OS-provided dylibs/frameworks. Its release gate rejects third-party
dylibs, non-system install names and rpaths. The baseline is distinct from
the exact OS versions exercised below; older baselines are not separately
runtime-tested by this ticket. Other architectures are not claimed. Linux and
cross-compilation are deferred by the user's latest scope instruction.

Build on macOS arm64 with native Rust 1.96.0, Apple build tools and Python 3.9+:

```sh
make release
make verify-release
```

The entry point accepts no compilation target override and rejects a non-native
Rust host. It uses `--locked --release --target aarch64-apple-darwin --bin massd`,
release opt level 2, no incremental/debug info, `panic=unwind`, and
`MACOSX_DEPLOYMENT_TARGET=14.0`. Hidden Rust flags/profile environment overrides
are rejected. `CARGO_TARGET_DIR` may point to an owned cache. The manifest includes
the actual C compiler and Apple SDK versions. These are repeatable locked builds,
not a promise of bit-for-bit identity across SDK versions or build paths.

After copying the single executable into a deployment directory:

```sh
./massd --help
./massd --listen 127.0.0.1:24368 --config /absolute/config.json
```

Use the [configuration/API](http-api.md) for native provider bindings and
`/livez`, `/readyz`, `/api/v1/agentd/jsonrpc`. Configuration, writable session data,
auth/provider settings, and an explicitly configured native Codex 0.160.1 package
are separate from MASS. Preserve the external Codex native package layout and
its resources. No Node
launcher or ACP adapter is needed. Offline history reads do not launch Codex.
Do not distribute personal credentials with the executable or manifest.
For real acceptance, set `MASS_TEST_MASSD` to the absolute dist executable with
its adjacent manifest, `MASS_TEST_CODEX` and `MASS_TEST_CODEX_SHA256` to the reviewed
native identity, then run `NATIVE_TEST=migrated_release make test-native`.
`MASS_TEST_CODEX_HOME` can select an existing absolute native auth/provider home;
it defaults to `CODEX_HOME` or the user's `.codex`. Credentials remain external.
The production daemon uses a private HOME and PATH `/usr/bin:/bin`; inherited
proxy/CA settings and the explicitly selected Codex home remain deployment inputs.
The Rust test performs two online inputs, offline history reads with an unavailable
Codex path, then explicit cold resume and one further real input. It checks
native identity, sequential append, unchanged old Journal bytes and live-only SSE.

The SDK client/protocol source revision is
`d27764b82f7118f674371e6d6e76271d9d606edb`. The necessary local bounds/shutdown
patch remains in `mass-rs/vendor/codex-app-server-client/MASS.patch`; UPSTREAM, pristine
hashes, LICENSE/NOTICE and the manifest preserve its origin. `make check` and
the release build verify reversible provenance using `check_official_client.py`.
The client patch SHA is
`b3e7fdf81008de61e832c3750465199ecef3abcb2413dbf02b7b8586fca43260`.
The release retains the reviewed official-client patch and production `panic=unwind`.
