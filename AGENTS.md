# 开发指南

- Rust workspace、源码、测试、examples、vendor 和 Rust 专属脚本均在 `mass-rs/`。
- 根 Makefile 是多语言仓库入口；直接调用 Cargo 时使用 `--manifest-path mass-rs/Cargo.toml`，或进入 `mass-rs/`。
- 优先各 crate 公开接口集成测试和端到端场景；真实 Codex/模型测试通过 `make test-native` 显式执行，默认 `make test` 不调用模型。

- Run `make fmt` to format Rust code.
- Run `make lint` for workspace/all-target/all-feature lint checks.
- Run `make build` to build the Rust workspace, binaries and examples.
- Run `make check` for structure, provenance, formatting, lint, tests and build.
- Run `make release` / `make verify-release` on native macOS arm64 with Rust 1.96.0. Linux delivery is deferred and unverified.

## Project Intelligence

| File | Description |
|------|-------------|
| [docs/architecture.md](docs/architecture.md) | Current native architecture |
| [docs/design/README.md](docs/design/README.md) | Current design authority and paused plans |
| [docs/README.md](docs/README.md) | Documentation index and historical boundaries |
| [GLOSSARY.md](GLOSSARY.md) | Domain vocabulary entry point |
| [docs/develop/rules/code-principle.md](docs/develop/rules/code-principle.md) | **Must** follow language-independent coding principles |

## 设计一致性

- Code changes **must** align with current `docs/design` and native API/storage contracts.
- Keep runtime command and capability documentation synchronized with implementation.
- Retired v1 CLI and pipeline skills are not current instructions.
- No compatibility layer is required.
- Preserve local runtime data, credentials, scratch evidence and other tasks' changes. Existing installations must explicitly configure their original absolute data directory after source relocation.
