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