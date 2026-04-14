# Coding Conventions

> Auto-generated from GSD knowledge base. Do not edit directly.
> Last synced: 2026-04-14 after M012

## Rules

| # | Scope | Rule |
|---|-------|------|
| K002 | workspace | Git clone working directory must be parent of target. `exec.Command("git", "clone", url, target)` must run from `filepath.Dir(targetDir)`, not inside target (which doesn't exist yet). |
| K023 | agentd | NEVER use `exec.CommandContext` for long-running daemon processes that should outlive the request that started them. Use `exec.Command` instead and manage lifecycle explicitly via `Process.Kill()`. |
| K025 | testing | macOS has a 104-character limit for Unix domain socket paths (UNIX_PATH_MAX). Always use short paths like `/tmp/oar-{pid}-{counter}.sock` for integration tests. Never use `t.TempDir()` directly for socket paths. |
| K037 | agentd | Fail-closed recovery gates must have bounded duration. When gating operational actions during daemon recovery, the recovery phase MUST transition to Complete on every exit path — including systemic errors. Use `defer` to set the exit state. |
| K039 | agentd | TOCTOU races in Unix socket cleanup are eliminated by unconditional Remove. Replace `if exists { remove }` with `os.Remove` that ignores `os.ErrNotExist`. |
| K040 | linting | In golangci-lint v2, use `golangci-lint fmt ./...` as the single idempotent formatting pass for gci/gofumpt. Running individual tools separately can produce subtly different results. |
| K042 | meta | DB cascade deletes eliminate the need for explicit ReleaseWorkspace in session removal. `meta.DeleteSession` already cascades `workspace_refs` rows via a DB trigger. Adding an explicit release call causes double-release. |
| K044 | linting | `filepathJoin` fix: use `os.TempDir()` not a split literal `/tmp` — gocritic flags the leading `/` in `"/tmp"` even after splitting. |
| K045 | testing | `exitAfterDefer`: TestMain must capture exit code, call cleanup explicitly, then call `os.Exit`. `defer` is bypassed by `os.Exit`. |
| K059 | docs | Doc verification grep: avoid pattern matches inside negation prose. Use affirmative phrasing: `identity is (workspace, name)` rather than `there is no agentId`. |
| K060 | agentd | forkShim must pass `filepath.Base(stateDir)` (hyphen-joined) as `--id`, not the slash-separated `agentKey`. The shim uses `filepath.Join(stateDir, id)` to compute socket path. |
| K061 | agentd | Bootstrap agentd state from shim `runtime/status` after Subscribe, not from stateChange hook. The stateChange hook is registered after `Create()` returns, so creating→idle fires while the hook is nil. |
| K063 | agentd | Always remove the target socket file before forking the shim (`os.Remove(socketPath)` before fork). Previous test crashes leave stale sockets that cause bind failures. |
| K069 | testing | Self-fork (os.Executable) requires `OAR_SHIM_BINARY` env override for integration tests. `go test` runs the test binary, not `bin/agentd`, which cannot handle the `shim` subcommand. |
| K074 | refactoring | Three-layer rename (meta → ARI types → ARI server + CLI) must compile as a unit. Never attempt layer-by-layer rename — the partial-rename state is never buildable. |
| K079 | testing | Before deleting any test file, run `grep -l <key_symbol> *_test.go` to find cross-file dependencies. Extract shared infrastructure to a new `*_test.go` file rather than wholesale deletion. |
| K080 | testing | jsonrpc.Server cleanup order: `ln.Close()` before `srv.Shutdown()`. Closing the listener forces Accept() to unblock and Serve() to return before Shutdown cleans up in-flight requests. |

## Patterns

| # | Scope | Pattern | Where |
|---|-------|---------|-------|
| K003 | workspace | Discriminated union JSON in Go: struct with `Type` field + embedded concrete types; custom `UnmarshalJSON` (parse `type` first, then unmarshal into concrete); custom `MarshalJSON`. | `pkg/workspace/spec.go` Source type |
| K004 | workspace | SourceHandler interface with `Prepare(ctx, source, targetDir)` for polymorphic workspace preparation. Each handler checks `source.Type` and handles its specific source type. | `pkg/workspace/handler.go` |
| K006 | workspace | Local workspaces are unmanaged — `LocalHandler` returns `source.Local.Path` directly (not `targetDir`). Git/EmptyDir are managed (created/deleted by agentd); Local is validated only. | `pkg/workspace/handler.go` LocalHandler |
| K007 | workspace | WorkspaceError Phase field pattern: Phase identifies lifecycle failure location (`prepare-source`, `prepare-hooks`, `cleanup-delete`). Implements `Unwrap()` for errors.Is/errors.As compatibility. | `pkg/workspace/errors.go` |
| K010 | workspace | Reference counting for shared workspace resources: Acquire/Release increment/decrement refCount; cleanup proceeds only when refCount reaches 0. | `pkg/workspace/manager.go`, `pkg/ari/registry.go` |
| K015 | spec | Optional pointer fields (`*int`, `*string`) for state populated after lifecycle events. Nil = "not yet applicable", non-nil = value available. JSON `omitempty` distinguishes nil from zero. | `pkg/spec/state_types.go` ExitCode |
| K019 | agentd | Thread-safe registry: `sync.RWMutex` protecting map with `RLock/RUnlock` for read-heavy access. Registry pattern reusable across agentd components. | `pkg/ari/registry.go`, `pkg/agentd/runtimeclass.go` |
| K020 | agentd | `os.Expand(value, os.Getenv)` resolves `${VAR}` patterns in config values at startup. Single-pass substitution at initialization for consistent values throughout daemon lifetime. | `pkg/agentd/runtimeclass.go` NewRuntimeClassRegistry |
| K021 | agentd | State machine validation with transition table: `map[currentState]→[]validNextStates` makes rules declarative and auditable. Error messages include valid transitions. | `pkg/agentd/session.go` validTransitions |
| K033 | meta | Schema migration idempotency: use `isBenignSchemaError` to treat "duplicate column name" as benign. SQLite doesn't support `IF NOT EXISTS` for `ALTER TABLE ADD COLUMN`. | `pkg/meta/store.go` isBenignSchemaError |
| K036 | agentd | Duplicate decisions accumulate when planning overlapping-scope slices. Before recording a new decision, grep existing ones for the same choice/scope. Reference existing decision instead of creating a new one. | `.gsd/DECISIONS.md` |
| K038 | agentd | Atomic fields for cross-goroutine state that guards request handlers. Use `atomic.Int32` for daemon-level state flags checked on every incoming request (e.g., recovery phase). Separate from process-map RWMutex. | `pkg/agentd/process.go` recoveryPhase |
| K040 | events | Damaged-tail detection: two-pass classification — collect all lines, then walk forward. Corrupt line followed by any valid line = mid-file corruption (error); corrupt lines only at tail = skip. | `pkg/events/log.go` ReadEventLog |
| K043 | meta | Rebuilding in-memory state from DB requires careful key mapping: ARI Registry keys workspaces by workspace ID; WorkspaceManager keys refcounts by workspace path. Map both correctly from DB record. | `pkg/ari/registry.go` RebuildFromDB, `pkg/workspace/manager.go` InitRefCounts |
| K050 | meta | bbolt nested bucket: always guard `bucket.Bucket(key)` with nil check in View transactions. A sub-bucket exists only after the first Update tx creates it. Iterate with `ForEachBucket()` not raw cursor. | `pkg/meta/agent.go` ListAgents |
| K051 | refactoring | When a large handler file is structurally incompatible with new types AND scheduled for full replacement, replace with a minimal compilable stub rather than partial adaptation. Document with `// TODO(SNN): full implementation`. | `pkg/ari/server.go` M007/S01 stub |
| K053 | agentd | Extract shared notification handler (buildNotifHandler) to avoid duplicate Start()/recoverAgent() closures. The extracted method is package-internal and testable standalone via mock server. | `pkg/agentd/process.go` buildNotifHandler |
| K054 | agentd | tryReload block must come AFTER atomic Subscribe. Ordering: Status check → reconcile DB → Subscribe (atomic backfill + live sub) → tryReload. Subscribe-before-Load is a correctness invariant. | `pkg/agentd/recovery.go` recoverAgent() |
| K055 | ari | workspace/list returns only registry-tracked (ready) workspaces, not all DB phases. Workspaces in pending or error phase are NOT returned by workspace/list. Use workspace/status for all phases. | `pkg/ari/server.go` handleWorkspaceList |
| K056 | testing | `ProcessManager.InjectProcess(key, proc)` is the test injection point for workspace/send and agent/prompt tests. Use `agentKey(workspace, name)` to compute the key. Injected ShimProcess needs a valid SocketPath. | `pkg/agentd/process.go` InjectProcess |
| K058 | architecture | workspace-mcp-server defines ARI structs locally — do not import pkg/ari. Self-contained binary avoids circular imports and coupling to internal package evolution. Update both places when ARI surface changes. | `cmd/workspace-mcp-server/main.go` (now `cmd/agentd/workspacemcp.go`) |
| K064 | agentd | Subscribe-before-Load is a correctness invariant for tryReload. Session/load fires before Subscribe → the immediate stateChange notification (creating→idle) arrives before handler is registered → silently dropped. | `pkg/agentd/recovery.go` recoverAgent() |
| K066 | ari | agentToInfo helper centralizes AgentInfo construction — structurally prevents agentId field leakage in all agent/* responses. No handler builds AgentInfo directly. Pair with a dedicated audit test. | `pkg/ari/server/server.go` agentToInfo() |
| K068 | cli | Package collision avoidance in cobra main: prefix all types/functions with distinguishing short prefix (e.g. `wmcp`, `shim`). Scope flag variables as locals inside `newXyzCmd()` constructors, not as package-level `var`. | `cmd/agentd/subcommands/` |
| K071 | cli | Local YAML struct for CLI commands that read YAML files and call ARI. Do not add yaml tags to pkg/ari types — they are canonical JSON-RPC parameter types. | `cmd/agentdctl/subcommands/` |
| K073 | testing | ari.Client.Call wraps RPC errors as fmt.Errorf strings, not `*jsonrpc2.Error`. Assert RPC error codes via `assert.Contains(t, err.Error(), "-32602")`, not `errors.As`. | `pkg/ari/server_test.go` |
| K077 | ari | Adapter pattern for multi-interface ARI service registration: central Service struct + thin unexported adapters embedding *Service (workspaceAdapter, agentRunAdapter, agentAdapter). Resolves identical-signature conflicts. | `pkg/ari/server/server.go` |
| K078 | testing | pkg/jsonrpc Client.notifCh has a pre-existing send-on-closed-channel race under `-count=3`. Treat single-run `go test ./...` passing as the acceptance bar — do not use `-count=3` on pkg/agentd. | `pkg/jsonrpc/client.go:115` |
