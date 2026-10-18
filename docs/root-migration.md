# Source layout and data ownership

The repository root contains shared documentation, licenses and the delegating Makefile. The production Cargo workspace is `mass-rs/Cargo.toml`; the separate Go experiment is `mass-go/go.mod`.

```text
repository/
  Makefile, README.md, AGENTS.md, LICENSE, NOTICE
  docs/
  mass-rs/
    Cargo.toml, Cargo.lock, .mise.toml
    src/, domain/, runtime/, journal/, events/, codex/, sessions/, http/
    tests/, examples/, vendor/, scripts/
  mass-go/
    go.mod, pkg/runtime/
```

The Rust daemon package/import is `mass-daemon` / `mass_daemon`; its executable is `massd`. Source relocation changes neither public HTTP/SSE nor storage contracts.

## Data location and ownership

Without configuration, the daemon selects `<launch-cwd>/data`. Running Cargo with `--manifest-path` does not change that launch directory. Existing installations must use an explicit config with `dataDir` set to their original absolute directory. Relative configured paths resolve against the config file's parent.

One daemon owns each data directory. Do not run a second daemon against live state. Source migration does not move, convert or delete runtime data, credentials, caches or private evidence. The current registration ledger and matching session binding are required; missing ownership fails closed. Older incompatible formats are not automatically adopted or converted. See [storage](storage.md).

Build output defaults to `mass-rs/target`; release output defaults to root `dist`. `CARGO_TARGET_DIR` and `RELEASE_DIST` may explicitly select owned cache/output locations. These locations are not source files.

## Version control boundary

The v2 source branch starts from public main and contains a file snapshot of the current implementation. It does not merge the development branch's private history. Retired v1 code and documentation are absent from the current tree. Private scratch and planning-state directories are ignored and must not be tracked.

Source rollback must preserve runtime data and its ownership. Rebuild an appropriate prior native version and supply the original absolute data directory; never use a retired binary to interpret an incompatible native format. Data rollback requires its own backup procedure.

## Validation

Use [offline checks](testing.md) for the source tree. Real native/model acceptance is explicit. Native macOS arm64 release verification uses Rust 1.96.0; Linux and cross-compilation remain unverified. Results apply only to the checked source, binary and environment.
