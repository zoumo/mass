# Decisions

This file records architectural, pattern, library, and observability decisions made during development.

## D001 — Source discriminated union with custom JSON marshaling

- **Scope:** architecture
- **Decision:** D001
- **Choice:** Custom UnmarshalJSON/MarshalJSON for Source discriminated union type
- **Rationale:** Go's default JSON unmarshaling cannot handle discriminated unions cleanly. Custom UnmarshalJSON parses the `type` field first, then unmarshals into the appropriate concrete type (GitSource, EmptyDirSource, LocalSource). Custom MarshalJSON ensures correct JSON output based on active type.
- **Revisable:** No
- **When:** M001-tlbeko/S01/T01
- **Made by:** agent

## D002 — GitError structured error type

- **Scope:** architecture
- **Decision:** D002
- **Choice:** GitError type with Phase field for structured git failure diagnostics
- **Rationale:** Git operations can fail at three phases: lookup (git not found), clone (repository access failure), checkout (commit SHA not found). Phase field enables targeted remediation and agent-friendly error inspection. GitError implements Unwrap() for errors.Is/errors.As compatibility.
- **Revisable:** No
- **When:** M001-tlbeko/S01/T02
- **Made by:** agent

## D003 — Git clone working directory strategy

- **Scope:** implementation
- **Decision:** D003
- **Choice:** Run git clone from filepath.Dir(targetDir), not targetDir itself
- **Rationale:** targetDir doesn't exist before clone. Git clone creates the target directory inside the working directory. Running from parent directory allows git to create targetDir correctly.
- **Revisable:** No
- **When:** M001-tlbeko/S01/T02
- **Made by:** agent

## D004 — Shallow clone by default with --single-branch

- **Scope:** implementation
- **Decision:** D004
- **Choice:** Use --single-branch flag for all git clones
- **Rationale:** Minimizes fetch time and disk usage by only fetching the target branch, not all remote branches. Combined with --depth for shallow clones when specified.
- **Revisable:** Yes
- **When:** M001-tlbeko/S01/T02
- **Made by:** agent

## D005 — Local workspace unmanaged semantics

- **Scope:** architecture
- **Decision:** D005
- **Choice:** LocalHandler returns source.Local.Path directly, ignoring targetDir parameter
- **Rationale:** Local workspaces are pre-existing directories that agentd does not create or delete. The handler only validates the path exists and is a directory. This differs from GitHandler and EmptyDirHandler which create the workspace and return targetDir. The ownership semantics are: Git/EmptyDir = managed (created/deleted by agentd), Local = unmanaged (validated only).
- **Revisable:** No
- **When:** M001-tlbeko/S02/T02
- **Made by:** agent

## D006 — ExitCode as optional pointer field

- **Scope:** architecture
- **Decision:** D006
- **Choice:** ExitCode field in State struct is `*int` (pointer) with `omitempty` JSON tag
- **Rationale:** Exit code is only meaningful after process exits. Nil while running indicates "not yet available"; non-nil after exit indicates actual exit code. Zero exit code (success) is semantically different from nil (no exit yet). Pointer with omitempty ensures clean JSON serialization — field omitted while running, present after exit.
- **Revisable:** No
- **When:** M001-tvc4z0/S01/T01
- **Made by:** agent

## D007 — Socket file removal for unclean shutdown recovery

- **Scope:** implementation
- **Decision:** D007
- **Choice:** Remove existing socket file before starting ARI server listener
- **Rationale:** Unix domain socket files persist after daemon crashes. Unlike TCP ports released by OS, socket files remain and block subsequent Listen() calls. Removing socket before listening enables automatic recovery from unclean shutdowns without operator intervention.
- **Revisable:** No
- **When:** M001-tvc4z0/S01/T02
- **Made by:** agent

## D008 — Graceful shutdown with signal handling

- **Scope:** implementation
- **Decision:** D008
- **Choice:** SIGTERM/SIGINT signal handler triggers srv.Shutdown() for graceful ARI server termination
- **Rationale:** Graceful shutdown allows in-flight JSON-RPC requests to complete before closing connections. Abrupt termination could leave clients with incomplete responses and break connection state. Signal handler ensures daemon responds properly to Kubernetes pod termination and operator-initiated stops.
- **Revisable:** No
- **When:** M001-tvc4z0/S01/T02
- **Made by:** agent

## D009 — Optional metadata store initialization

- **Scope:** architecture
- **Decision:** D009
- **Choice:** Metadata Store initialization is optional — daemon starts without error when metaDB config field is empty
- **Rationale:** Enables multiple daemon operating modes: persistent (production with SQLite) and ephemeral (testing/development without persistence). Optional initialization provides deployment flexibility without requiring separate daemon configurations or feature flags.
- **Revisable:** Yes
- **When:** M001-tvc4z0/S02/T03
- **Made by:** agent

## D010 — SQLite WAL journal mode with foreign keys

- **Scope:** implementation
- **Decision:** D010
- **Choice:** SQLite connection uses WAL journal mode, foreign keys enabled, and 5-second busy timeout
- **Rationale:** WAL mode provides better concurrency (readers don't block writers) essential for daemon goroutines. Foreign keys enforce referential integrity (sessions reference valid workspaces/rooms). Busy timeout prevents immediate failures under concurrent access. Triggers automate ref_count updates and updated_at timestamps.
- **Revisable:** No
- **When:** M001-tvc4z0/S02/T01
- **Made by:** agent

## D011 — Embedded SQL schema for single-binary deployment

- **Scope:** implementation
- **Decision:** D011
- **Choice:** SQL schema embedded in binary using go:embed directive, no external schema files at runtime
- **Rationale:** Single-binary deployment simplifies installation and operations. Schema embedded in pkg/meta/store.go initializes database on first run. schema_version table enables future migration tracking. No separate database provisioning step required.
- **Revisable:** No
- **When:** M001-tvc4z0/S02/T01
- **Made by:** agent

---

## Decisions Table

| # | When | Scope | Decision | Choice | Rationale | Revisable? | Made By |
|---|------|-------|----------|--------|-----------|------------|---------|
| D001 | M001-tlbeko/S03 | implementation | D007 | HookExecutor sequential abort with first failure stops execution and returns HookError with HookIndex | Hooks must execute in array order with abort-on-failure behavior. First failure stops execution (subsequent hooks not run) and returns HookError with HookIndex identifying exactly which hook failed. This enables targeted remediation — operator knows which setup step failed without inspecting all hooks. Critical for workspace preparation where partial setup leaves inconsistent state. | No | agent |
| D002 | M001-tlbeko/S04/T01 | architecture | WorkspaceError structured error type | WorkspaceError type with Phase field for structured workspace lifecycle failure diagnostics | Workspace operations can fail at multiple phases: prepare-source (handler failure), prepare-hooks (setup hook failure), cleanup-delete (directory deletion failure). Phase field enables targeted remediation and agent-friendly error inspection. WorkspaceError implements Unwrap() for errors.Is/errors.As compatibility. Follows GitError/HookError pattern established in earlier slices. | No | agent |
| D003 | M001-tlbeko/S04/T02 | architecture | Best-effort teardown cleanup semantics | Teardown hook failures logged but cleanup continues; managed directories deleted regardless of hook outcome | Cleanup must be reliable — a failing teardown hook shouldn't leave orphaned workspace directories. Setup hook failures abort preparation because partial state can be safely cleaned up. Teardown is the final cleanup step and must complete to prevent resource leaks. Hook error is logged for diagnostics but cleanup proceeds. | No | agent |
| D004 | M001-tlbeko/S05/T02 | library | UUID generation library | github.com/google/uuid for workspace ID generation | Standard, well-maintained UUID library with RFC 4122 compliance. Used for generating unique workspace IDs in ARI prepare method. Lightweight dependency with no external requirements. | No | agent |
| D005 | M001-tvc4z0/S03/T01 | implementation | RuntimeClass Env substitution strategy | os.Expand(value, os.Getenv) resolves ${VAR} patterns in Env values at registry creation time | Env substitution happens once at registry creation (NewRuntimeClassRegistry), not at runtime Get() calls. This provides consistent resolved values and avoids repeated getenv calls. Uses os.Expand which handles ${VAR} syntax; unresolved variables expand to empty string (os.Getenv returns "" for unset vars). | Yes | agent |
| D006 | M001-tvc4z0/S06 | implementation | session/prompt auto-start behavior | session/prompt handler auto-starts shim process when session.State == "created" before sending prompt to agent | Simplifies CLI UX by eliminating the need for a separate session/start method. The natural workflow is session/new → session/prompt → session/stop → session/remove. Auto-start on first prompt matches user expectation that "prompting" starts the agent. R006 doesn't list session/start as a required method, confirming auto-start is the intended design. | Yes | agent |
| D007 | M001-tvc4z0/S06 | requirement | R006 | validated | All 27 ARI tests pass including 10 session tests covering lifecycle, error cases, auto-start, and state transitions. The 9 session/* method handlers (new/prompt/cancel/stop/remove/list/status/attach/detach) are fully implemented and tested. | No | agent |
