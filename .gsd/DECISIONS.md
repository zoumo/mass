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
| D008 | M002-ssi4mk | protocol | Shim RPC compatibility posture for the convergence milestone | Perform a clean break to `session/*` + `runtime/*` and do not preserve legacy shim-rpc names or event shapes in this wave. | The user is the sole operator, explicitly does not want compatibility work carried forward, and the old surface would only preserve protocol drift at the ACP boundary. | Yes | collaborative |
| D009 | M002-ssi4mk | architecture | Metadata backend direction during contract convergence | Retain SQLite as the metadata backend for M002 and defer any BoltDB or backend-abstraction work to a later dedicated milestone if a concrete limitation appears. | The current model already relies on relational features such as foreign keys, indexes, triggers, and cross-entity bookkeeping. Swapping storage during convergence would add redesign cost without reducing the real model complexity. | Yes | collaborative |
| D010 | M002-ssi4mk | validation | Real ACP proof targets for the primary convergence milestone | Use `gsd-pi` and `claude-code` as the required real ACP validation surfaces for M002; keep Codex as an explicit later compatibility target rather than a mandatory M002 end-to-end proof surface. | Real bundle configs already exist for `gsd-pi` and `claude-code`, making them the strongest available proof surfaces now. Codex remains important, but forcing full proof immediately would dilute the contract-convergence milestone. | Yes | collaborative |
| D011 | M002-ssi4mk | scope | Terminal capability roadmap posture | Remove `M001-terminal` from the near-term roadmap and defer terminal capability until after the runtime contract converges. | The user explicitly said `M001-terminal` is no longer needed. Keeping terminal in the near-term plan would distort sequencing by reviving a direction the user has intentionally dropped. | Yes | collaborative |
| D012 | M002-q9r6sg planning | architecture | Recovery authority and fail-closed behavior after agentd restart | On daemon restart, recovered session truth comes from live shim state reconciled with persisted SQLite metadata; when the two disagree or certainty is incomplete, the session remains inspectable but operationally blocked. | This milestone exists to make restart behavior truthful. Metadata alone lies after daemon restart, while live-shim-only truth ignores durable identity and workspace ownership. Reconciliation plus explicit degraded/blocked posture keeps status readable without letting the runtime guess. | Yes | collaborative |
| D013 | M002-q9r6sg planning | validation | Codex proof scope for the recovery-hardening milestone | Remove Codex end-to-end validation from M002-q9r6sg and defer it to a later milestone; this milestone will focus on restart truth, event recovery, and cleanup safety. | The user explicitly changed scope during planning. Keeping Codex in this milestone would add external setup and validation work that is no longer required, while diluting the recovery/safety hardening objective. | Yes | human |
| D014 | M002-q9r6sg planning | scope | Codex proof scope for the recovery-hardening milestone | Remove Codex end-to-end validation from M002-q9r6sg and defer it to a later milestone; this milestone focuses on restart truth, event recovery, and cleanup safety. | The user explicitly changed scope during planning. Keeping Codex in this milestone would add external setup and validation work that is no longer required, while diluting the recovery and safety hardening objective. | Yes | human |
| D015 | M002-ssi4mk/S01 planning | architecture | Room ownership model during contract convergence | Treat Room Spec as orchestrator-owned desired state and ARI `room/*` as realized runtime state maintained by agentd. | The current docs conflict because room-spec says agentd only sees sessions while agentd.md and ari-spec.md already model runtime room objects. A desired-vs-realized split keeps the existing ARI direction without pretending agentd owns orchestration intent. | Yes | agent |
| D016 | M002-ssi4mk/S01 planning | protocol | Bootstrap work semantics at the ARI/runtime boundary | Make `session/new` configuration-only, remove bootstrap task input from its contract, and require work to enter through later `session/prompt`; `systemPrompt` remains session configuration rather than overlapping task input. | The implementation already creates sessions without a bootstrap prompt, while the docs currently describe overlapping `prompt` and `systemPrompt` meanings. A configuration-only `session/new` gives `agentRoot.path`, resolved `cwd`, ACP session creation, and recovery semantics one authoritative story. | Yes | agent |
| D017 | M002/S01 planning | validation | Proof surface for the design-contract convergence slice | Use a repo-root contract verifier script plus checked-in bundle example validation tests as the mandatory proof surface for M002/S01 instead of prose review alone. | S01 is a documentation-heavy convergence slice, but later runtime work depends on these contracts being mechanically stable. A shell verifier catches contradictory normative phrases and missing authority sections, while bundle example tests prevent real-client proof from being blocked by broken checked-in fixtures such as the `claude-code` config typo. | Yes | agent |
| D018 | M002/S01/T02 | architecture | Runtime bootstrap and identity contract for the M002/S01 design rewrite | Document `session/new` as configuration-only bootstrap, treat resolved `cwd` as a runtime-derived value from `agentRoot.path`, keep OAR `sessionId` distinct from ACP `sessionId`, and explicitly defer durable ID/bootstrap persistence to S03. | The runtime, config, and design docs were contradicting each other about whether `systemPrompt` was a hidden work turn, whether callers supplied cwd directly, and whether OAR and ACP identities were the same object. Converging on one bootstrap-first contract removes those conflicts and leaves the remaining persistence work named instead of implied. | Yes | agent |
| D019 | M002/S01/T03 | architecture | Room ownership and room/* API semantics during contract convergence | Treat the Room Spec as orchestrator-owned desired state and treat ARI room/* as the realized runtime projection maintained by agentd; keep session/new configuration-only and route work through session/prompt. | The design set had conflicting stories about whether agentd only sees sessions or owns room-level semantics. Splitting desired intent from realized runtime state preserves runtime inspection and future routing without claiming agentd owns orchestration policy. Keeping session/new as bootstrap-only prevents Room creation from implying work delivery. | Yes | agent |
| D020 | M002/S01/T04 | architecture | Shim runtime control surface and recovery authority split | Use a clean-break shim surface with session/* for turn control, runtime/* for process/replay control, and keep runtime-spec authoritative for state-dir/socket layout while shim-rpc-spec owns replay/reconnect semantics. | This matches the converged session/new versus session/prompt story, aligns the shim vocabulary with ARI and runtime docs, and removes the old dual-source contradiction where legacy PascalCase methods and $/event notifications were still described as normative. | Yes | agent |
| D021 | M002/S01 | requirement | R032 | validated | M002/S01 converged the Room, Session, Runtime, Workspace, and shim recovery docs onto one authority map. Final slice verification passed via `bash scripts/verify-m002-s01-contract.sh`, and example bundle proof still passed via `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1`. | Yes | agent |
| D022 | M002/S01 | requirement | R033 | validated | T02 rewrote runtime-spec/config-spec/design docs so `agentRoot.path`, resolved `cwd`, `session/new`, `systemPrompt`, and bootstrap semantics have one authoritative meaning, and the final cross-doc verifier passed at slice close. | Yes | agent |
| D023 | M002/S01 | requirement | R038 | validated | T03 made local workspace attachment, hook execution, env precedence, and shared workspace reuse boundaries explicit across room, agentd, ARI, workspace, and convergence docs. The final slice verifier passed with those boundary rules in the authoritative design set. | Yes | agent |
| D024 | M002/S01 | requirement | R033 | validated | M002/S01 converged `agentRoot.path`, resolved `cwd`, `session/new`, `systemPrompt`, and bootstrap semantics across runtime-spec, config-spec, design.md, and contract-convergence.md. Final slice verification passed via `bash scripts/verify-m002-s01-contract.sh` and `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1`. | Yes | agent |
| D025 | M002/S01 | requirement | R038 | validated | M002/S01 documented explicit host-impact rules for local workspace attachment, hook execution, env precedence, and shared workspace reuse across room-spec, agentd, ARI, workspace, and contract-convergence docs. Final slice verification passed via `bash scripts/verify-m002-s01-contract.sh` and `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1`. | Yes | agent |
| D026 |  | architecture | How shim live notifications, replay history, and runtime lifecycle changes share one protocol surface | Use a canonical `events.Envelope` with `method` plus typed `params`, assign monotonic `seq` in `events.Translator`, and wire runtime state changes through a post-Create hook so bootstrap traffic stays internal | This makes `runtime/history` replay the exact live notification shape, gives `session/subscribe(afterSeq)` and `runtime/status.recovery.lastSeq` a single sequence authority, and preserves the existing bootstrap visibility boundary by only attaching the state-change hook after `mgr.Create()` succeeds | Yes | agent |
