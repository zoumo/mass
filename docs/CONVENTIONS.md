# Coding Conventions

> Auto-generated from GSD knowledge base. Do not edit directly.
> Last synced: 2026-04-07 (M002/S03)

## Rules

- **K002** [workspace]: Git clone working directory must be parent of target — `exec.CommandContext("git", "clone", url, target)` must run from the parent directory of target, not inside target (which doesn't exist yet).
- **K014** [agentd]: Unix domain socket servers must remove existing socket file before calling Listen() to recover from previous daemon crashes. Unlike TCP ports released by OS, socket files persist after process death.
- **K025** [testing]: macOS has a 104-character limit for Unix domain socket paths. Always use short paths like `/tmp/oar-{pid}-{counter}.sock` for integration tests. Never use `t.TempDir()` directly for socket paths.
- **K028** [agentd]: NEVER use `exec.CommandContext` for long-running daemon processes that should outlive the request that started them. Use `exec.Command` instead and manage lifecycle explicitly via `Process.Kill()` or `cmd.Process.Signal()`.
- **K029** [design]: Shim protocol authority is split on purpose — `shim-rpc-spec.md` owns method/notification names and replay/reconnect semantics; `runtime-spec.md` owns socket path and state-dir layout; `agent-shim.md` is descriptive only.
- **K030** [design]: Contract convergence has a two-part proof surface — always run both `scripts/verify-m002-s01-contract.sh` and `go test ./pkg/spec -run TestExampleBundlesAreValid`. Running only one leaves half the contract unchecked.

## Patterns

- **K001** [workspace]: Commit SHA detection requires exactly 40 hex characters (0-9, a-f, A-F) via `isCommitSHA` function.
- **K003** [workspace]: Discriminated union JSON pattern in Go — define struct with `Type` field and embedded concrete types, implement custom `UnmarshalJSON` (parse `type` first, then unmarshal into concrete) and `MarshalJSON`. Reference: `pkg/workspace/spec.go`.
- **K004** [workspace]: SourceHandler interface with `Prepare(ctx, source, targetDir)` method enables polymorphic workspace preparation. Each handler checks `source.Type` and handles its specific source type.
- **K005** [workspace]: SemVer validation pattern — reuse `parseMajor` from `pkg/spec/config.go` for `oarVersion` validation. Validates format and checks major version.
- **K006** [workspace]: Local workspaces are unmanaged — `LocalHandler` returns `source.Local.Path` directly (NOT `targetDir`). Git/EmptyDir are managed (created/deleted by agentd), Local is validated only.
- **K007** [workspace]: WorkspaceError Phase field pattern for lifecycle diagnostics — Phase identifies where in the lifecycle the error occurred: `prepare-source`, `prepare-hooks`, `cleanup-delete`. Follows GitError/HookError pattern.
- **K009** [testing]: Marker file test pattern for proving abort-on-failure — create observable artifacts in later steps that would only exist if execution continued past failure point.
- **K010** [workspace]: Reference counting pattern for shared workspace resources — `Acquire/Release` increment/decrement refCount, cleanup proceeds only when refCount reaches 0.
- **K012** [ari]: Integration tests connect via actual Unix socket using `jsonrpc2.Conn`, not mocked connections, catching protocol issues that mocks miss.
- **K013** [ari]: External test package pattern — use `package ari_test` (not `ari`) to test from consumer perspective, ensuring tests only use exported API.
- **K015** [spec]: Optional pointer fields (`*int`, `*string`) for state populated after lifecycle events. Nil = "not yet applicable", non-nil = value available. JSON `omitempty` distinguishes nil from zero.
- **K016** [agentd]: Optional Store initialization — daemon starts without error when `metaDB` is empty, enabling stateless mode for testing.
- **K017** [meta]: SQLite WAL journal mode for daemon metadata — WAL provides better concurrency (readers don't block writers), essential for daemon goroutines. Connection includes `_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000`.
- **K018** [meta]: Embedded SQL schema via `go:embed` for single-binary deployment. `schema_version` table handles future migrations. No separate database setup step required.
- **K019** [agentd]: Thread-safe registry pattern — `sync.RWMutex` protecting map with `RLock/RUnlock` for read-heavy access (Get/List). Reusable across agentd components. Supersedes K011 (ARI Registry).
- **K020** [agentd]: `os.Expand(value, os.Getenv)` resolves `${VAR}` patterns in configuration values. Single-pass substitution at initialization for consistent values throughout daemon lifetime.
- **K021** [agentd]: State machine validation with transition table pattern — `map[currentState] → []validNextStates` makes rules declarative and auditable. Error messages include valid transitions.
- **K022** [agentd]: Delete protection pattern for active resources — block deletion in "active" states (running, paused:warm) using protected states set. Typed error enables operator to understand why.
- **K024** [ari]: JSON-RPC error code semantics — use `CodeInvalidParams` (-32602) for client input errors, `CodeInternalError` (-32603) for server-side failures. Error code choice matters for client debugging.
- **K026** [testing]: ARI client's mutex only protects ID generation, not the full request/response cycle. Wrap `client.Call()` with `sync.Mutex` for concurrent test scenarios.
- **K027** [testing]: Integration test cleanup with `pkill` for orphaned processes — ensures clean state for subsequent tests even if a test panicked mid-execution.
- **K031** [agentd]: Recovered shims need `DisconnectNotify`, not `Cmd.Wait` — when agentd reconnects to a shim it did not fork, there is no `exec.Cmd` handle; use the JSONRPC disconnect channel instead.
- **K032** [testing]: Mock agent binary lives at `internal/testutil/mockagent`, not `cmd/mockagent`. Build with `go build -o bin/mockagent ./internal/testutil/mockagent`.
- **K033** [meta]: Schema migration with `isBenignSchemaError` for idempotent `ALTER TABLE ADD COLUMN` — SQLite doesn't support `IF NOT EXISTS` for ALTER, so attempt and catch duplicate-column error.
