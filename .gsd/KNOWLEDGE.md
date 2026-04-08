# Knowledge

This file records patterns, gotchas, and non-obvious lessons learned that would save future agents from repeating investigation or hitting the same issues.

## K001 — Commit SHA detection requires exactly 40 hex characters

- **Pattern:** The `isCommitSHA` function validates commit SHAs by checking for exactly 40 hex characters (0-9, a-f, A-F).
- **Gotcha:** Test strings in git_test.go had incorrect lengths (labeled as "39 chars" but actually 40, labeled as "40 chars" but actually 41). Fixed by correcting string lengths to match test intent.
- **Lesson:** Always verify string lengths when testing boundary conditions. Use explicit length checks or known-good test fixtures.
- **When:** M001-tlbeko/S01/T02

## K002 — Git clone working directory must be parent of target

- **Pattern:** `exec.CommandContext("git", "clone", url, target)` must run from the parent directory of target, not inside target (which doesn't exist yet).
- **Gotcha:** Initially tried `cloneCmd.Dir = targetDir` which failed because targetDir doesn't exist at clone time.
- **Lesson:** For commands that create directories, set working directory to the parent where the new directory should be created.
- **When:** M001-tlbeko/S01/T02

## K003 — Discriminated union JSON pattern in Go

- **Pattern:** For types with a `type` field that determines structure (like Source with git/emptyDir/local variants):
  1. Define a struct with a `Type` field and embedded concrete type structs
  2. Implement custom `UnmarshalJSON`: parse raw JSON, extract `type` field, then unmarshal into appropriate concrete type
  3. Implement custom `MarshalJSON`: output `type` field plus fields from active concrete type
- **Reference:** pkg/workspace/spec.go Source type
- **When:** M001-tlbeko/S01/T01

## K004 — SourceHandler interface pattern for workspace handlers

- **Pattern:** SourceHandler interface with Prepare(ctx, source, targetDir) method enables polymorphic workspace preparation. Each handler (GitHandler, EmptyDirHandler, LocalHandler) checks source.Type and handles its specific source type.
- **Reference:** pkg/workspace/handler.go
- **When:** M001-tlbeko/S01/T02

## K005 — SemVer validation pattern reuse

- **Pattern:** Reused parseMajor pattern from pkg/spec/config.go for oarVersion validation. Validates SemVer format and checks major version (must be 0 for current spec version).
- **Reference:** pkg/workspace/spec.go ValidateWorkspaceSpec
- **When:** M001-tlbeko/S01/T01

## K006 — Local workspaces are unmanaged by agentd

- **Pattern:** LocalHandler returns source.Local.Path directly (NOT targetDir parameter) because local workspaces are pre-existing directories that agentd does not create or delete. EmptyDirHandler and GitHandler return targetDir because they create the workspace directory.
- **Gotcha:** The targetDir parameter is only meaningful for handlers that create directories (Git, EmptyDir). For Local sources, targetDir is ignored and the existing path is returned.
- **Lesson:** Different source types have different ownership semantics. Git and EmptyDir are managed (created/deleted by agentd), Local is unmanaged (validated but not modified).
- **When:** M001-tlbeko/S02/T02

## K007 — WorkspaceError Phase field pattern for lifecycle diagnostics

- **Pattern:** WorkspaceError type follows GitError/HookError pattern with Phase field identifying where in the lifecycle the error occurred: "prepare-source", "prepare-hooks", "cleanup-delete". Enables targeted diagnostics — operator knows exactly which phase failed.
- **Reference:** pkg/workspace/errors.go WorkspaceError type
- **When:** M001-tlbeko/S04/T01

## K009 — Marker file test pattern for proving abort-on-failure

- **Pattern:** Use a marker file to prove abort-on-failure behavior in sequential hook execution. If first hook fails, second hook should NOT run — marker file from second hook should NOT exist.
- **Implementation:** In TestExecuteHooksSequentialAbort: first hook fails (exit 1), second hook would create marker file if it ran. Test verifies marker file does NOT exist, proving execution aborted at first failure.
- **Lesson:** When testing abort behavior, create observable artifacts in later steps that would only exist if execution continued past failure point.
- **When:** M001-tlbeko/S03/T02

## K010 — Reference counting pattern for shared workspace resources

- **Pattern:** WorkspaceManager uses Acquire/Release reference counting to prevent premature cleanup when multiple sessions share a workspace. Acquire increments refCount, Release decrements and returns new count. Cleanup only proceeds when refCount reaches 0.
- **Implementation:** Registry in pkg/ari tracks workspaceId → WorkspaceMeta with Refs list. Acquire adds sessionID to Refs, Release removes. workspace/cleanup validates RefCount=0 before calling manager.Cleanup.
- **Lesson:** Shared resources need reference counting to prevent race conditions between cleanup and active usage.
- **Reference:** pkg/workspace/manager.go Acquire/Release methods; pkg/ari/registry.go
- **When:** M001-tlbeko/S04/T02, M001-tlbeko/S05/T01

## K011 — ARI Registry pattern for workspace tracking

- **Pattern:** Registry tracks workspaceId → WorkspaceMeta mapping with thread-safe operations (mutex protection). Provides Add/Get/List/Remove/Acquire/Release methods for workspace lifecycle management.
- **Implementation:** Registry struct with sync.RWMutex, map[workspaceId]WorkspaceMeta. WorkspaceMeta includes Name, Path, Status, Refs (sessionIDs). Acquire/Release record session references for debugging.
- **Lesson:** Centralized registry simplifies resource tracking across JSON-RPC methods and enables reference counting for cleanup safety.
- **Reference:** pkg/ari/registry.go
- **When:** M001-tlbeko/S05/T01

## K012 — Integration test over Unix socket for ARI methods

- **Pattern:** ARI integration tests connect via actual Unix socket using jsonrpc2.Conn, not mocked connections. This provides realistic verification of JSON-RPC protocol behavior.
- **Implementation:** Tests create temp Unix socket, start ARI server, connect with jsonrpc2.NewConn, send JSON-RPC requests, verify responses. Covers workspace/prepare, workspace/list, workspace/cleanup methods.
- **Lesson:** Real socket tests catch protocol issues that mocks miss (e.g., connection handling, message framing).
- **Reference:** pkg/ari/server_test.go TestARIWorkspacePrepareEmptyDir, TestARIWorkspaceLifecycleRoundTrip
- **When:** M001-tlbeko/S05/T02

## K013 — External test package pattern for internal Go packages

- **Pattern:** Use external test package (e.g., ari_test) to test internal packages without accessing internal symbols. This follows Go best practices and enables testing from consumer perspective.
- **Implementation:** pkg/ari/server_test.go uses package ari_test (not ari), imports github.com/open-agent-d/open-agent-d/pkg/ari as external package.
- **Lesson:** External test packages ensure tests only use exported API, catching visibility issues early.
- **Reference:** pkg/ari/server_test.go, pkg/rpc/server_test.go (established pattern)
- **When:** M001-tlbeko/S05/T02

## K014 — Socket file removal before listening for unclean shutdown recovery

- **Pattern:** Unix domain socket servers must remove existing socket file before calling Listen() to recover from previous daemon crashes. If the daemon crashed without cleaning up, the socket file remains and causes "address already in use" error on restart.
- **Implementation:** In cmd/agentd/main.go: `os.Remove(config.Socket)` before `srv.Serve()`. The removal is silent — no error if file doesn't exist.
- **Lesson:** Unix socket daemons need proactive cleanup of leftover socket files. Unlike TCP ports which are released by the OS, socket files persist after process death.
- **Reference:** cmd/agentd/main.go
- **When:** M001-tvc4z0/S01/T02

## K015 — Optional pointer fields for state populated after lifecycle events

- **Pattern:** Use pointer fields (`*int`, `*string`) for state that's only meaningful after specific lifecycle events. Nil indicates "not yet applicable", non-nil indicates value is available.
- **Implementation:** ExitCode in State struct is `*int`. Nil while process is running (exit code doesn't exist yet), populated after cmd.Wait() returns. JSON `omitempty` tag ensures nil values are excluded from serialization.
- **Lesson:** Pointer fields with `omitempty` distinguish "not yet available" from "available but zero". For exit codes, nil ≠ 0 — nil means process hasn't exited, 0 means exited successfully.
- **Reference:** pkg/spec/state_types.go ExitCode field
- **When:** M001-tvc4z0/S01/T01

## K016 — Optional Store initialization for daemon flexibility

- **Pattern:** Metadata Store initialization is optional — agentd daemon starts without error when metaDB config field is empty. This enables "stateless" daemon mode for testing or scenarios where persistence isn't required.
- **Implementation:** In cmd/agentd/main.go: check cfg.MetaDB before calling meta.NewStore. Empty string logs "metadata store not configured" and continues. Non-empty path initializes Store with parent directory creation.
- **Lesson:** Optional initialization pattern allows daemon to operate in multiple modes: persistent (with SQLite) or ephemeral (without). Useful for testing, development, and production flexibility.
- **Reference:** cmd/agentd/main.go Store initialization block
- **When:** M001-tvc4z0/S03/T03

## K017 — SQLite WAL journal mode for daemon metadata

- **Pattern:** Use WAL (Write-Ahead Logging) journal mode for SQLite metadata store in long-running daemon processes. WAL provides better concurrency (readers don't block writers) and crash recovery (committed transactions preserved).
- **Implementation:** Connection string includes `_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000`. Foreign keys enforce referential integrity (session→workspace, session→room). Busy timeout prevents immediate SQLITE_BUSY errors.
- **Lesson:** WAL mode is essential for daemon processes that may have concurrent reads/writes from different goroutines. Default DELETE journal mode blocks readers during writes. Foreign keys prevent orphaned records.
- **Reference:** pkg/meta/store.go NewStore connection parameters
- **When:** M001-tvc4z0/S02/T01

## K018 — Embedded SQL schema for single-binary deployment

- **Pattern:** Embed SQL schema files using go:embed directive to include database initialization in compiled binary. No external schema files needed at runtime.
- **Implementation:** pkg/meta/schema.sql embedded with `//go:embed schema.sql`. NewStore reads embedded schema and executes SQL statements for table creation, indexes, triggers.
- **Lesson:** Embedded resources enable true single-binary deployment. Schema versioning (schema_version table) handles migrations for future schema changes. No separate database setup step required.
- **Reference:** pkg/meta/store.go embedFS and schema initialization
- **When:** M001-tvc4z0/S02/T01

## K019 — RuntimeClassRegistry thread-safe registry pattern

- **Pattern:** RuntimeClassRegistry follows same thread-safe pattern as ARI Registry (K011) — sync.RWMutex protecting map with RLock/RUnlock for Get/List methods. Registry is read-heavy (Get/List called frequently during session creation, registry populated once at daemon startup), so RWMutex optimizes for read concurrency.
- **Implementation:** RuntimeClassRegistry struct with sync.RWMutex and map[string]*RuntimeClass. Get(name) uses RLock, List() uses RLock, constructor populates map without lock (single-threaded startup).
- **Lesson:** Registry pattern is reusable across agentd components. Centralized lookup with mutex protection enables concurrent access without race conditions. RWMutex preferred over Mutex when reads dominate writes.
- **Reference:** pkg/agentd/runtimeclass.go RuntimeClassRegistry struct; pkg/ari/registry.go (established pattern)
- **When:** M001-tvc4z0/S03/T01

## K020 — os.Expand for environment variable substitution

- **Pattern:** Use os.Expand(value, os.Getenv) to resolve ${VAR} patterns in configuration values. os.Expand handles ${VAR} syntax, calling provided function for each variable reference. Unresolved variables expand to empty string (os.Getenv returns "" for unset vars).
- **Implementation:** In NewRuntimeClassRegistry: `env[key] = os.Expand(value, os.Getenv)` resolves each Env value at registry creation time. Substitution happens once at startup, not at runtime Get() calls, providing consistent resolved values.
- **Lesson:** os.Expand is standard library solution for config env substitution. Prefer single-pass substitution at initialization over per-call resolution — avoids repeated getenv overhead and ensures consistent values throughout daemon lifetime.
- **Reference:** pkg/agentd/runtimeclass.go NewRuntimeClassRegistry env resolution
- **When:** M001-tvc4z0/S03/T01

## K021 — State machine validation with transition table pattern

- **Pattern:** Use a transition table (map[currentState] → []validNextStates) for state machine validation. isValidTransition function checks if target state is in the valid transitions list for current state. Centralized table makes state machine logic declarative and easily auditable.
- **Implementation:** In pkg/agentd/session.go: validTransitions map defines all allowed state changes for session lifecycle. Terminal state (stopped) has empty transition list. ErrInvalidTransition error includes valid transitions list for debugging. Update method validates transitions before applying changes.
- **Lesson:** Transition table pattern makes state machine rules explicit and testable. Each transition rule is a single map entry, easy to verify against design spec. Error messages should include valid transitions to help operators understand what actions are allowed.
- **Reference:** pkg/agentd/session.go validTransitions map and isValidTransition function
- **When:** M001-tvc4z0/S04/T02

## K022 — Delete protection pattern for active resources

- **Pattern:** Block deletion of resources in "active" states using a protected states set. Check state before deletion, return typed error if resource is active. This prevents accidental cleanup of running/paused resources that may have ongoing processes or state.
- **Implementation:** In pkg/agentd/session.go: deleteProtectedStates map[SessionState]bool defines which states block deletion (running, paused:warm). Delete method checks state first, returns ErrDeleteProtected if session is active. Sessions in created, paused:cold, or stopped states can be deleted.
- **Lesson:** Active resource protection prevents race conditions between cleanup and ongoing work. The protected states set should match states where the resource has external side effects (running processes, memory state, etc.). Typed error enables operator to understand why deletion failed and what action to take.
- **Reference:** pkg/agentd/session.go deleteProtectedStates and Delete method
- **When:** M001-tvc4z0/S04/T02

## K023 — exec.CommandContext kills process when context cancelled

- **Pattern:** NEVER use exec.CommandContext for long-running daemon processes that should outlive the request that started them. When the context is cancelled, Go kills the process immediately.
- **Gotcha:** In ProcessManager.forkShim, using `exec.CommandContext(ctx, shimBinary, args...)` tied the shim process to the request context. When the caller cancelled the context after Start() returned (normal pattern for request-scoped operations), Go killed the shim process immediately. This caused "signal: killed" errors and all prompt operations to fail.
- **Lesson:** Use `exec.Command` (not CommandContext) for processes that should run independently of the request lifecycle. The process lifecycle should be managed explicitly via Process.Kill() or cmd.Process.Signal() in a cleanup/shutdown path, not tied to a request context that gets cancelled.
- **Reference:** pkg/agentd/process.go forkShim function
- **When:** M001-tvc4z0/S06

## K024 — JSON-RPC error code semantics for client vs server errors

- **Pattern:** Use CodeInvalidParams (-32602) when the client provides invalid input (e.g., sessionId not in valid state for operation). Use CodeInternalError (-32603) for server-side failures (e.g., database errors, unexpected conditions).
- **Gotcha:** Initially handleSessionPrompt returned CodeInternalError when Connect failed for a stopped session. But this is semantically a client error — the client provided a sessionId that is not in a valid state for the operation. The fix was to check error message for "not running" and return CodeInvalidParams instead.
- **Lesson:** Error code choice matters for client debugging. CodeInvalidParams tells client "your input was wrong, fix it". CodeInternalError tells client "server had a problem, retry or report". Check if the error is due to client-provided state (InvalidParams) vs system failure (InternalError).
- **Reference:** pkg/ari/server.go handleSessionPrompt error handling
- **When:** M001-tvc4z0/S06

## K025 — macOS Unix socket path length limitation

- **Pattern:** macOS has a 104-character limit for Unix domain socket paths (SUN_PATH_MAX including null terminator). Tests that use long t.TempDir() paths can exceed this limit and fail silently or with cryptic errors.
- **Gotcha:** Integration tests initially used t.TempDir() for socket paths, which on macOS can exceed 100+ characters. The Listen() call would fail with "address too long" or similar errors.
- **Implementation:** Use short socket paths like `/tmp/oar-{pid}-{counter}.sock` for integration tests. The testSocketCounter ensures unique paths per test, and cleanup removes files from /tmp.
- **Lesson:** Unix socket path length varies by platform. macOS limit is 104 chars, Linux is 108 chars. Always use short paths for integration tests, especially on macOS. Never use t.TempDir() directly for socket paths.
- **Reference:** tests/integration/session_test.go setupAgentdTest helper
- **When:** M001-tvc4z0/S08/T02

## K026 — ARI client serialization for concurrent access

- **Pattern:** The ARI client's mutex only protects ID generation, not the entire request/response cycle. Concurrent client.Call() operations can interleave and cause protocol issues.
- **Gotcha:** TestMultipleConcurrentSessions initially sent concurrent prompts without serialization. The goroutines would interleave JSON-RPC requests, causing connection errors or malformed responses.
- **Implementation:** Wrap client.Call() with sync.Mutex for concurrent test scenarios: `clientMu.Lock(); err := client.Call(...); clientMu.Unlock()`. This serializes the full request/response cycle.
- **Lesson:** JSON-RPC clients are often not thread-safe for concurrent calls. The ID mutex is insufficient — the entire message exchange needs serialization. Either create separate clients per goroutine or serialize calls with a mutex.
- **Reference:** tests/integration/concurrent_test.go TestMultipleConcurrentSessions
- **When:** M001-tvc4z0/S08/T04

## K027 — Integration test cleanup with pkill for orphaned processes

- **Pattern:** Integration tests that fork subprocesses (agentd, agent-shim, mockagent) need robust cleanup to handle test failures that leave orphan processes. The cleanup function should use pkill to terminate any leftover processes matching the binary names.
- **Implementation:** In test cleanup: `exec.Command("pkill", "-f", "agent-shim").Run(); exec.Command("pkill", "-f", "mockagent").Run()`. This ensures clean state for subsequent tests even if a test panicked or failed mid-execution.
- **Lesson:** Integration tests that spawn processes need aggressive cleanup. Process isolation (t.TempDir) alone isn't sufficient — test failures can leave processes running. pkill in cleanup ensures subsequent tests start clean. Ignore pkill errors (process might not exist).
- **Reference:** tests/integration/session_test.go cleanup function
- **When:** M001-tvc4z0/S08/T02

## K028 — Shim process must run independently of request context

- **Pattern:** Long-running daemon processes that should outlive the request that started them must NOT use exec.CommandContext. Use exec.Command instead and manage lifecycle explicitly.
- **Gotcha:** ProcessManager.forkShim was using `exec.CommandContext(ctx, shimBinary, args...)` which tied the shim process to the request context. When the caller cancelled the context after Start() returned (normal pattern for request-scoped operations), Go killed the shim process immediately. This caused "signal: killed" errors and all prompt operations to fail.
- **Lesson:** The context in exec.CommandContext is for cancellation propagation, not just timeout. When context is cancelled, Go sends SIGKILL to the process. For daemon-style processes that should survive beyond the initiating request, use exec.Command and manage lifecycle via explicit Stop/Shutdown methods.
- **Reference:** pkg/agentd/process.go forkShim function
- **When:** M001-tvc4z0/S06/T02

## K029 — Shim protocol authority is split on purpose

- **Pattern:** `docs/design/runtime/shim-rpc-spec.md` is the sole normative owner of shim method names, notification names, and replay/reconnect semantics. `docs/design/runtime/runtime-spec.md` owns socket path and state-dir layout. `docs/design/runtime/agent-shim.md` is descriptive only.
- **Gotcha:** Letting `agent-shim.md` or implementation-lag notes restate current protocol details reintroduces dual-source contract drift, especially around the retired PascalCase / `$/event` surface.
- **Lesson:** When changing shim-facing docs or implementation, check `docs/design/contract-convergence.md` first, update the single authority doc for the concept you are touching, and keep other docs referential rather than parallel.
- **Reference:** docs/design/contract-convergence.md; docs/design/runtime/shim-rpc-spec.md; docs/design/runtime/runtime-spec.md; docs/design/runtime/agent-shim.md
- **When:** M002/S01/T04

## K030 — Contract convergence has a two-part proof surface

- **Pattern:** After editing the M002 design-contract surfaces, always run both `bash scripts/verify-m002-s01-contract.sh` and `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1`.
- **Gotcha:** The shell verifier catches cross-doc authority drift and retired wording, but it does not catch broken checked-in example bundles. The bundle test catches fixture/spec drift, but it does not detect contradictory authority wording.
- **Lesson:** Treat the shell verifier and example bundle test as one proof surface. Running only one of them leaves half the contract unchecked.
- **Reference:** scripts/verify-m002-s01-contract.sh; pkg/spec/example_bundles_test.go
- **When:** M002/S01/T01

## K031 — Recovered shims need DisconnectNotify, not Cmd.Wait

- **Pattern:** When agentd reconnects to a shim it did not fork (recovery after restart), the process has no `exec.Cmd` handle. Use the JSONRPC `DisconnectNotify()` channel to detect when the shim goes away instead of `Cmd.Wait()`.
- **Gotcha:** Calling `Cmd.Wait()` on a nil Cmd panics. The `watchProcess` goroutine used for freshly-forked shims assumes a Cmd handle exists. Recovered shims need a separate `watchRecoveredProcess` goroutine.
- **Lesson:** When designing process lifecycle watchers, always account for the "adopted process" case where you reconnected to something you didn't spawn.
- **Reference:** pkg/agentd/recovery.go watchRecoveredProcess
- **When:** M002/S03/T02

## K032 — mockagent binary lives at internal/testutil/mockagent, not cmd/mockagent

- **Pattern:** The mock agent used by integration tests is built from `./internal/testutil/mockagent` and placed at `bin/mockagent`. It is NOT under `cmd/`.
- **Gotcha:** Writing `go build ./cmd/mockagent` fails with "directory not found". The correct command is `go build -o bin/mockagent ./internal/testutil/mockagent`.
- **Lesson:** Integration test binaries are test utilities, not production commands, so they live under `internal/testutil/` rather than `cmd/`.
- **Reference:** internal/testutil/mockagent/main.go; tests/integration/restart_test.go line 143
- **When:** M002/S03/T03

## K033 — Schema migration with isBenignSchemaError for idempotent ALTER TABLE

- **Pattern:** The SQLite schema migration uses `ALTER TABLE ... ADD COLUMN` statements that fail with "duplicate column name" on re-run. The `isBenignSchemaError` function treats these errors as benign, making migrations idempotent.
- **Gotcha:** Standard SQLite doesn't support `IF NOT EXISTS` for `ALTER TABLE ADD COLUMN`. You must attempt the ALTER and catch the duplicate-column error.
- **Lesson:** For SQLite schema migrations without a proper migration framework, the "attempt and ignore benign error" pattern is the simplest idempotent approach. Extend the benign-error checker as new error patterns emerge.
- **Reference:** pkg/meta/schema.sql; pkg/meta/store.go isBenignSchemaError
- **When:** M002/S03/T01

## K034 — Design-first convergence de-risks multi-package protocol migrations

- **Pattern:** Settling the authority map and design contract (S01) before implementing the protocol migration (S02) made the migration surprisingly clean — S02/T03 required zero ARI changes because the design boundaries were already correct.
- **Lesson:** For protocol-breaking changes across multiple packages, invest in a design convergence slice first. The one-time cost of resolving contradictions in docs pays back as implementation confidence — the implementer knows which document to follow and never has to guess.
- **Reference:** M002/S01 → M002/S02 dependency chain
- **When:** M002

## K035 — Real CLI integration tests need graceful degradation, not strict prerequisites

- **Pattern:** `TestRealCLI_GsdPi` and `TestRealCLI_ClaudeCode` skip gracefully when ANTHROPIC_API_KEY or CLI binaries are missing, rather than failing CI. The tests structurally prove the lifecycle works by exercising the setup, connect, and teardown paths even when they skip.
- **Gotcha:** Making API keys a hard requirement would break CI for every contributor who doesn't have credentials. Making tests completely absent would lose the structural proof that the contract supports real agents.
- **Lesson:** Integration tests with external dependencies should use `t.Skip()` with clear skip messages, not conditional compilation or test tags. This keeps them visible in test output and easy to run locally with credentials.
- **Reference:** tests/integration/real_cli_test.go
- **When:** M002/S04

## K036 — Duplicate decisions accumulate when planning slices with overlapping scope

- **Pattern:** M002 accumulated duplicate decisions (D028/D030/D032 are identical recovery config decisions; D029/D031/D033 are identical recovery posture decisions) because each slice planning pass independently recorded the same architectural choices.
- **Lesson:** Before recording a new decision, grep existing decisions for the same choice/scope. If the decision already exists, reference it instead of creating a new one. Decision duplication makes the decisions file harder to audit and gives false impression of distinct choices.
- **Reference:** .gsd/DECISIONS.md D028-D033
- **When:** M002/S03

## K037 — Fail-closed recovery gates must have bounded duration

- **Pattern:** When gating operational actions (prompt, cancel) during daemon recovery, the recovery phase MUST transition to Complete on every exit path — including systemic errors like metadata DB failures. Otherwise the daemon enters a permanent recovery-blocked state where no operational action can ever succeed.
- **Gotcha:** If `RecoverSessions` encounters an error before processing any sessions (e.g. `ListSessions` fails) and returns without setting phase to Complete, the `recoveryGuard` blocks all prompt/cancel calls indefinitely. This is worse than the problem it was meant to solve.
- **Lesson:** Fail-closed posture is a time-bounded safety mechanism, not a permanent trap. Use `defer m.SetRecoveryPhase(RecoveryPhaseComplete)` or ensure every exit path explicitly transitions. Test the systemic failure case, not just the happy path.
- **Reference:** pkg/agentd/recovery.go RecoverSessions; pkg/agentd/recovery_posture_test.go TestRecoverSessions_Phase*
- **When:** M003/S01/T03

## K038 — Atomic fields for cross-goroutine state that guards request handlers

- **Pattern:** Use `atomic.Int32` for daemon-level state flags that are checked on every incoming request (like recovery phase). This avoids acquiring the process map RWMutex on every handler invocation just to check a single flag.
- **Gotcha:** Putting recovery phase under the process map lock would create unnecessary contention — every prompt/cancel/status call would compete with session creation/deletion for the same lock.
- **Lesson:** Separate the concurrency domain of "phase gating" (high-frequency reads from every handler) from "process map operations" (lower-frequency reads/writes). Atomic fields are ideal for single-value flags read by many goroutines.
- **Reference:** pkg/agentd/process.go recoveryPhase field; pkg/agentd/recovery_posture.go RecoveryPhase type
- **When:** M003/S01/T01

## K038 — Shim-vs-DB state reconciliation uses string comparison, not shared enums

- **Pattern:** The shim reports status using `spec.Status` (creating/created/running/stopped) while the DB stores `meta.SessionState` (created/running/paused:warm/paused:cold/stopped). These are different types with overlapping but non-identical value sets.
- **Gotcha:** You cannot use `==` between the two types directly. The reconciliation in `recoverSession` converts both to strings for comparison, with explicit switch cases for the actionable states (stopped → fail-close, running+created → transition).
- **Lesson:** When comparing state across system boundaries (shim process vs DB), prefer an explicit switch on the authoritative side (shim) and handle each case individually, rather than trying to build a mapping table. The "other mismatch" catch-all is important for states like paused:warm that exist only on one side.
- **Reference:** pkg/agentd/recovery.go reconciliation block between Status() and History() calls
- **When:** M003/S02/T01

## K039 — TOCTOU races in Unix socket cleanup are eliminated by unconditional Remove

- **Pattern:** When starting a Unix domain socket listener, stale socket files from a previous daemon run must be cleaned up. The naive `os.Stat → os.Remove → net.Listen` sequence has a TOCTOU race.
- **Gotcha:** Between Stat confirming the file exists and Remove executing, another process could create/remove the socket, causing either spurious errors or missed cleanup.
- **Lesson:** Replace `if exists { remove }` with unconditional `os.Remove` that ignores `os.ErrNotExist`. This is atomic from the caller's perspective — it succeeds whether or not the file exists, with no window for races.
- **Reference:** cmd/agentd/main.go socket cleanup before Serve()
- **When:** M003/S02/T01

## K040 — Damaged-tail detection must re-classify after append

- **Pattern:** ReadEventLog's damaged-tail tolerance classifies corrupt lines at the end of a JSONL file as non-fatal. But after a new valid entry is appended past the corrupt line, the same corrupt line becomes mid-file corruption (valid data follows it).
- **Gotcha:** T01's plan expected ReadEventLog to return all valid entries after appending past a damaged tail. The correct behavior is to error — the corrupt line is now mid-file, not tail. Test was adapted accordingly.
- **Lesson:** Damaged-tail is a file-position-relative concept, not a property of the corrupt line itself. Any algorithm that classifies corruption must re-evaluate after the file changes. Don't hardcode "this line is damaged-tail" — always check what follows.
- **Reference:** pkg/events/log.go ReadEventLog, pkg/events/log_test.go TestEventLog_AppendAfterDamagedTail
- **When:** M003/S03/T01

## K041 — File I/O under mutex is acceptable for recovery-only paths

- **Pattern:** Translator.SubscribeFromSeq holds t.mu while performing ReadEventLog (file I/O). This is intentional — it eliminates the History→Subscribe gap by making log-read and subscription-registration atomic with respect to event broadcasting.
- **Gotcha:** This would be a performance problem in hot paths (mutex blocks all event broadcasts during file reads). It's safe only because SubscribeFromSeq is called during recovery/startup, not during normal operation.
- **Lesson:** Document the intended call-site constraint in godoc when accepting a lock-scope tradeoff. The method signature alone doesn't communicate "recovery only, not hot path." Future callers who use it in a broadcast loop will create latency issues.
- **Reference:** pkg/events/translator.go SubscribeFromSeq godoc
- **When:** M003/S03/T02

## K042 — DB cascade deletes eliminate the need for explicit ReleaseWorkspace in session removal

- **Pattern:** `meta.DeleteSession` already cascades `workspace_refs` rows via a DB trigger, which decrements `ref_count` automatically. Adding an explicit `store.ReleaseWorkspace` call in `handleSessionRemove` would cause a double-release, potentially driving ref_count negative.
- **Gotcha:** When wiring new DB operations into existing handlers, always check whether the existing delete path already has cascade side-effects. The workspace_refs trigger was not obvious from reading `handleSessionRemove` alone — you have to trace through `meta.DeleteSession` → SQL triggers.
- **Lesson:** Before adding a release/decrement call, verify the existing delete path doesn't already handle it via DB triggers. Double-release bugs are subtle — ref_count going to -1 doesn't error but silently breaks the "ref_count > 0 means active" invariant.
- **Reference:** pkg/ari/server.go handleSessionRemove, pkg/meta/session.go DeleteSession
- **When:** M003/S04/T01

## K043 — Rebuilding in-memory state from DB requires careful key mapping between subsystems

- **Pattern:** The ARI Registry keys workspaces by workspace ID, but the WorkspaceManager keys refcounts by workspace path (targetDir). When rebuilding from DB, you need to know which key each subsystem uses and map correctly from the DB record.
- **Gotcha:** If you accidentally use workspace ID as the WorkspaceManager refcount key, Release calls (which use the path) will never find the refcount entry, silently allowing premature cleanup.
- **Lesson:** When multiple subsystems maintain parallel state about the same resource, document the key each one uses. DB records typically have all the fields needed to populate both, but the caller must map correctly.
- **Reference:** pkg/ari/registry.go RebuildFromDB (uses ws.ID), pkg/workspace/manager.go InitRefCounts (uses ws.Path)
- **When:** M003/S04/T02
## K014 — Two-level state gating pattern for recovery

- **Pattern:** Use an atomic daemon-wide phase (single int32 compare) for fast guards in RPC handlers, combined with per-entity metadata structs for detailed inspection on demand. The guard is a single atomic read in the hot path; metadata is only assembled when a specific status/inspect call is made.
- **Gotcha:** If you only use per-entity metadata for gating, you must iterate all entities on every guarded call. If you only use the daemon-wide phase, you can't tell callers which specific entity is recovering.
- **Lesson:** Separate the "should I block?" question (atomic phase) from the "what's the recovery detail?" question (per-entity metadata). They have different performance profiles and different consumers.
- **Reference:** pkg/agentd/recovery_posture.go (RecoveryPhase), pkg/agentd/process.go (RecoveryInfo on ShimProcess)
- **When:** M003/S01

## K015 — Always exit blocking states on every code path

- **Pattern:** When setting a daemon into a blocking/gating state (e.g. "recovering"), ensure every exit path — including systemic errors, panics via defer, and early returns — transitions out of the blocking state.
- **Gotcha:** If RecoverSessions encounters a ListSessions DB error and returns early without setting phase to Complete, the daemon is permanently blocked — no operational actions can ever succeed.
- **Lesson:** Use `defer` to set the exit state, or verify every return path manually. A fail-closed posture must be time-bounded, not a permanent trap.
- **Reference:** pkg/agentd/recovery.go RecoverSessions (D041)
- **When:** M003/S01

## K016 — Shim truth wins over DB truth in recovery

- **Pattern:** During daemon recovery, the running shim process is ground truth for liveness. If the shim reports "running" but the DB says "created", update the DB to match the shim. If the DB state machine rejects the transition, log and proceed — the shim is still alive and reachable.
- **Gotcha:** Failing recovery because the DB state machine rejects an edge-case transition would leave a live, reachable shim permanently disconnected.
- **Lesson:** In recovery, the process that's actually running is more trustworthy than a record that may have been written during a previous daemon's crash window. Reconcile toward liveness, not toward historical DB state.
- **Reference:** pkg/agentd/recovery.go recoverSession reconciliation switch (D042)
- **When:** M003/S02

## K017 — Two-pass line classification for damaged-tail tolerance

- **Pattern:** When reading JSONL files that may have been partially written during a crash, collect all lines first, then walk forward classifying each as valid or corrupt. A corrupt line followed by any valid line is mid-file corruption (error); corrupt lines only at the file tail are damaged-tail (skip and return partial results).
- **Gotcha:** Single-pass approaches can't distinguish tail damage from mid-file corruption without lookahead. Appending a valid entry past a corrupt line correctly reclassifies the corruption as mid-file.
- **Lesson:** The two-pass approach is simpler, correct, and the file is already fully read by the scanner. Don't try to get clever with single-pass heuristics.
- **Reference:** pkg/events/log.go ReadEventLog (D046)
- **When:** M003/S03

## K018 — DB-as-truth for destructive operations after restart

- **Pattern:** When in-memory state doesn't survive daemon restarts, gate destructive operations (like workspace cleanup) on persisted DB state, not volatile in-memory counters. After restart, in-memory RefCount is 0, which would incorrectly allow cleanup of workspaces with active sessions.
- **Gotcha:** If you rely on in-memory ref_count for cleanup gating and the daemon restarts, all workspaces appear to have zero references and cleanup succeeds unsafely.
- **Lesson:** Any state used to gate destructive operations must survive restarts. DB ref_count persists; in-memory RefCount doesn't.
- **Reference:** pkg/ari/server.go handleWorkspaceCleanup (D049), pkg/ari/registry.go RebuildFromDB (D050)
- **When:** M003/S04

## K019 — Extract shared helpers before the second consumer exists

- **Pattern:** When building a new code path (room/send) that needs the same logic as an existing one (session/prompt), extract the shared helper (deliverPrompt) immediately rather than duplicating. The extraction is cheap during implementation and prevents divergent behavior.
- **Gotcha:** If you duplicate the auto-start→connect→prompt flow into room/send and later fix a bug in session/prompt, you must remember to fix room/send too. The duplication creates a silent correctness risk.
- **Lesson:** The cost of extraction during initial implementation is negligible. The cost of maintaining duplicated delivery paths grows with every fix and feature addition to either consumer.
- **Reference:** pkg/ari/server.go deliverPrompt helper (D058)
- **When:** M004/S02

## K020 — Hand-rolled protocol surfaces for tiny tool counts

- **Pattern:** When the protocol surface is tiny (e.g., 3 MCP methods, 2 tools), hand-rolling the implementation (~300 lines) is simpler than adding an SDK dependency. The hand-rolled code is self-contained, debuggable, and has no transitive dependency risk.
- **Gotcha:** This only works when the surface is genuinely small. If the tool count grows beyond 3-4, the boilerplate for schema generation, parameter validation, and error formatting justifies an SDK.
- **Lesson:** Set a clear threshold (e.g., "revisit if >3 tools") and document it in the decision record so future contributors know when to switch.
- **Reference:** cmd/room-mcp-server/main.go (D055)
- **When:** M004/S02

## K021 — Teardown guard tests catch composition bugs

- **Pattern:** After building happy-path integration tests, add adversarial-ordering tests that attempt operations in the wrong order (e.g., delete room before stopping sessions, remove session before stopping it). These catch composition bugs where individual guards work in isolation but fail when combined.
- **Gotcha:** Happy-path tests always execute operations in the "right" order, so they never exercise the error paths that users will inevitably trigger.
- **Lesson:** Teardown guards are only proven by tests that violate them. TestARIRoomTeardownGuards found that the interaction between active-member guards and session delete protection worked correctly — but only because it tested the wrong ordering explicitly.
- **Reference:** pkg/ari/server_test.go TestARIRoomTeardownGuards (S03/T02)
- **When:** M004/S03

## K022 — Contract verifier scripts: scope forbidden patterns to JSON method strings, not prose

- **Pattern:** When a design doc legitimately references deprecated method names in explanatory prose (e.g., "the shim continues to use session/*"), a naive grep-based forbidden-pattern check will false-fail. Scope forbidden patterns to JSON method-string format (`"method": "session/new"`) rather than bare strings.
- **Gotcha:** agentd.md and ari-spec.md explicitly describe the shim-internal boundary using the old session/* names — exactly what the forbidden-pattern check needs to *not* flag. Broad patterns fire on the very explanation text that makes the doc correct.
- **Lesson:** Write forbidden patterns to match the form that would be harmful in production code or normative examples (JSON key-value), not the form that appears in explanatory prose. The M005/S01 verifier uses extended-regex `"method":\s*"session/` to achieve this.
- **Reference:** scripts/verify-m005-s01-contract.sh (D068), modeled on verify-m002-s01-contract.sh
- **When:** M005/S01

## K023 — agent/event boundaries: translate at the perimeter, not in the shim

- **Pattern:** When renaming an external protocol surface (session/* → agent/*), do the translation at the outermost perimeter (agentd→orchestrator boundary), not in intermediate layers. The shim retains its existing surface; agentd translates event names as they cross the ARI surface.
- **Gotcha:** If you rename session/* inside the shim to match the new external surface, you create a version-skew window where shim and agentd use different names for the same events during deployment. You also break other consumers of the shim surface.
- **Lesson:** Keep translation at the boundary that faces the changing consumer (orchestrator). Inner layers (shim→agentd) remain stable and can be evolved independently. This is the "translate at the perimeter" pattern (D065).
- **Reference:** docs/design/agentd/ari-spec.md, agent/update and agent/stateChange (D065)
- **When:** M005/S01

## K024 — SQLite ALTER TABLE with FK columns: use DEFAULT NULL, not DEFAULT ''

- **Pattern:** When adding a new FK column to an existing SQLite table via `ALTER TABLE ... ADD COLUMN`, use `DEFAULT NULL` (or omit DEFAULT entirely) rather than `DEFAULT ''`. An empty string (`''`) fails the FK constraint check at insert time, while NULL bypasses FK constraint checks and represents "not set" correctly.
- **Gotcha:** `agent_id TEXT DEFAULT '' REFERENCES agents(id)` will cause every existing row to fail FK validation on the next `PRAGMA foreign_keys` check because `''` is not a valid agents.id. `DEFAULT NULL` correctly represents "this session has no agent yet."
- **Lesson:** FK nullable columns that default to "not linked" should always use NULL, not empty string. This aligns with SQL semantics where NULL means "unknown/absent relationship" and FK constraints do not fire on NULL.
- **Reference:** pkg/meta/schema.sql v4 sessions.agent_id (D064, T02 deviation note)
- **When:** M005/S02

## K025 — Converging a state machine: split removal across two tasks at build-green boundaries

- **Pattern:** When removing deprecated state constants (paused:warm/paused:cold) that are referenced across multiple files, do it in two tasks: Task N adds a TODO comment and the replacement constants; Task N+1 removes the old constants after updating all call sites. Never remove a constant in the same commit where you add its replacement if callers span package boundaries.
- **Gotcha:** If you remove paused:* in the same task as you add AgentState/SessionStateCreating/SessionStateError, the build breaks mid-task because pkg/agentd still references the constants from pkg/meta. The two-task split ensures the build is always green at every task boundary.
- **Lesson:** Cross-package constant removal is a two-phase operation: (1) add replacement + mark for deletion, (2) remove after fixing all consumers. Build-green at task boundaries is the invariant to preserve.
- **Reference:** T01 adds TODO(T02) comment; T02 removes constants (D069)
- **When:** M005/S02

## K026 — ON DELETE SET NULL FK breaks same-transaction sibling lookup: always pre-fetch before delete

- **Pattern:** When deleting an object (agent) that has a sibling with a nullable FK pointing to it (`sessions.agent_id REFERENCES agents(id) ON DELETE SET NULL`), look up the sibling *before* the delete, not after.
- **Gotcha:** Calling `agents.Delete(agentId)` first NULLs out `sessions.agent_id` immediately. Any subsequent `store.ListSessions(AgentID: agentId)` returns empty — the session is orphaned from the perspective of agentId-based lookup even though it still exists.
- **Lesson:** The correct order is: (1) pre-fetch sibling IDs, (2) delete the parent, (3) use the pre-fetched IDs to clean up siblings. Never rely on FK reverse-lookup after a parent delete with SET NULL semantics.
- **Reference:** handleAgentDelete in pkg/ari/server.go (D072), M005/S03/T02
- **When:** M005/S03

## K027 — RESTRICT FK requires manual child cleanup before parent delete; schema default is not CASCADE

- **Pattern:** When a child table uses `REFERENCES parent(id) ON DELETE RESTRICT` (or the implicit default), deleting the parent fails with a FK violation unless all child rows are removed first.
- **Gotcha:** agents.room → rooms(name) uses RESTRICT. A `room/delete` operation that skips agent cleanup will fail at the DB layer. Unlike CASCADE (which auto-removes children), RESTRICT requires the caller to enumerate and delete children explicitly.
- **Lesson:** Review FK action (CASCADE vs RESTRICT vs SET NULL) when writing delete handlers. RESTRICT is the safest schema default — it surfaces missing cleanup logic as an error instead of silently orphaning or cascading. The handler must implement the cleanup loop.
- **Reference:** handleRoomDelete in pkg/ari/server.go (D073), M005/S03/T02
- **When:** M005/S03

## K028 — CLI helper extraction is a prerequisite for deleting a command file that shares utilities

- **Pattern:** When deleting a CLI source file (e.g. session.go) that mixes command logic with shared helper functions (getClient, outputJSON, parseLabels), extract helpers to a separate file first before deleting.
- **Gotcha:** If you delete session.go without moving helpers first, all other commands (room.go, workspace.go, daemon.go) that import those helpers break immediately. The build reveals the missing symbols only after deletion.
- **Lesson:** Plan file deletion in two phases: (1) extract shared code to a new file (helpers.go), verify build passes, (2) delete the old file. This is the same two-phase pattern as cross-package constant removal (K025).
- **Reference:** cmd/agentdctl/helpers.go creation in T03, M005/S03/T03
- **When:** M005/S03

## K029 — Async-create window requires room/delete guard on agent state, not just session count

- **Pattern:** When `agent/create` is async (agent exists in `creating` state before its session row is created), `room/delete` must check agent state rather than merely checking for linked sessions.
- **Gotcha:** After async create returns, there is a brief window where the agent is `creating` but no session row exists yet (the goroutine hasn't run). A `room/delete` check that only queries sessions would find zero sessions and succeed, leaving the background goroutine to start a session for a deleted room — a resource leak with no owner.
- **Lesson:** Any delete handler for a parent object (room, workspace) must enumerate direct child agents by state, not just by session existence. Blocking on non-stopped agents (including `creating`) closes the async race entirely.
- **Reference:** handleRoomDelete in pkg/ari/server.go (D074), M005/S04/T01
- **When:** M005/S04

## K030 — agents.UpdateState has no transition guard; stopped→creating and error→creating work freely

- **Pattern:** `pkg/agentd/agent.go` AgentManager.UpdateState does not validate state transitions (unlike SessionManager which has a `validTransitions` map). Any from→to pair succeeds.
- **Gotcha:** If you assumed a validTransitions guard existed (as the task plan did), you might add unnecessary transition entries. No changes to agent.go were needed.
- **Lesson:** Before adding state-machine transition entries to an object manager, check whether it actually validates transitions. The agent manager is transition-free by design for now — validation is deferred to a future policy layer.
- **Reference:** T02 key_decisions, M005/S04/T02
- **When:** M005/S04

## K031 — 90-second goroutine timeout is the only lifecycle bound for async bootstrap; set it explicitly

- **Pattern:** Both `handleAgentCreate` and `handleAgentRestart` background goroutines use `context.WithTimeout(context.Background(), 90*time.Second)` as their execution bound.
- **Gotcha:** The original request context is dead after `conn.Reply` — using it in the goroutine causes silent context-cancelled failures. `context.Background()` plus an explicit timeout is the correct pattern.
- **Lesson:** Background goroutines spawned after an RPC reply must not inherit the request context. Always derive a new context from `context.Background()` with an explicit timeout that bounds the worst-case bootstrap time. Log outcome with structured fields (agentId, sessionId) at both Info (success) and Error (failure) levels.
- **Reference:** handleAgentCreate and handleAgentRestart in pkg/ari/server.go, M005/S04/T01/T02
- **When:** M005/S04
