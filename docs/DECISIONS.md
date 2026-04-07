# Architecture Decisions

> Auto-generated from GSD decision register. Do not edit directly.
> Last synced: 2026-04-07 (M002/S03)

## Active Decisions

### D002: WorkspaceError structured error type

- **When:** M001-tlbeko/S04/T01
- **Choice:** WorkspaceError type with Phase field for structured workspace lifecycle failure diagnostics
- **Rationale:** Workspace operations can fail at multiple phases: prepare-source (handler failure), prepare-hooks (setup hook failure), cleanup-delete (directory deletion failure). Phase field enables targeted remediation and agent-friendly error inspection. WorkspaceError implements Unwrap() for errors.Is/errors.As compatibility. Follows GitError/HookError pattern established in earlier slices.
- **Revisable:** No

### D003: Best-effort teardown cleanup semantics

- **When:** M001-tlbeko/S04/T02
- **Choice:** Teardown hook failures logged but cleanup continues; managed directories deleted regardless of hook outcome
- **Rationale:** Cleanup must be reliable — a failing teardown hook shouldn't leave orphaned workspace directories. Setup hook failures abort preparation because partial state can be safely cleaned up. Teardown is the final cleanup step and must complete to prevent resource leaks. Hook error is logged for diagnostics but cleanup proceeds.
- **Revisable:** No

### D009: Metadata backend direction during contract convergence

- **When:** M002-ssi4mk
- **Choice:** Retain SQLite as the metadata backend for M002 and defer any BoltDB or backend-abstraction work to a later dedicated milestone if a concrete limitation appears.
- **Rationale:** The current model already relies on relational features such as foreign keys, indexes, triggers, and cross-entity bookkeeping. Swapping storage during convergence would add redesign cost without reducing the real model complexity.
- **Revisable:** Yes

### D012: Recovery authority and fail-closed behavior after agentd restart

- **When:** M002-q9r6sg planning
- **Choice:** On daemon restart, recovered session truth comes from live shim state reconciled with persisted SQLite metadata; when the two disagree or certainty is incomplete, the session remains inspectable but operationally blocked.
- **Rationale:** This milestone exists to make restart behavior truthful. Metadata alone lies after daemon restart, while live-shim-only truth ignores durable identity and workspace ownership. Reconciliation plus explicit degraded/blocked posture keeps status readable without letting the runtime guess.
- **Revisable:** Yes

### D015: Room ownership model during contract convergence

- **When:** M002-ssi4mk/S01 planning
- **Choice:** Treat Room Spec as orchestrator-owned desired state and ARI `room/*` as realized runtime state maintained by agentd.
- **Rationale:** The current docs conflict because room-spec says agentd only sees sessions while agentd.md and ari-spec.md already model runtime room objects. A desired-vs-realized split keeps the existing ARI direction without pretending agentd owns orchestration intent.
- **Revisable:** Yes

### D018: Runtime bootstrap and identity contract for the M002/S01 design rewrite

- **When:** M002/S01/T02
- **Choice:** Document `session/new` as configuration-only bootstrap, treat resolved `cwd` as a runtime-derived value from `agentRoot.path`, keep OAR `sessionId` distinct from ACP `sessionId`, and explicitly defer durable ID/bootstrap persistence to S03.
- **Rationale:** The runtime, config, and design docs were contradicting each other about whether `systemPrompt` was a hidden work turn, whether callers supplied cwd directly, and whether OAR and ACP identities were the same object. Converging on one bootstrap-first contract removes those conflicts and leaves the remaining persistence work named instead of implied.
- **Revisable:** Yes

### D019: Room ownership and room/* API semantics during contract convergence

- **When:** M002/S01/T03
- **Choice:** Treat the Room Spec as orchestrator-owned desired state and treat ARI room/* as the realized runtime projection maintained by agentd; keep session/new configuration-only and route work through session/prompt.
- **Rationale:** The design set had conflicting stories about whether agentd only sees sessions or owns room-level semantics. Splitting desired intent from realized runtime state preserves runtime inspection and future routing without claiming agentd owns orchestration policy. Keeping session/new as bootstrap-only prevents Room creation from implying work delivery.
- **Revisable:** Yes

### D020: Shim runtime control surface and recovery authority split

- **When:** M002/S01/T04
- **Choice:** Use a clean-break shim surface with session/* for turn control, runtime/* for process/replay control, and keep runtime-spec authoritative for state-dir/socket layout while shim-rpc-spec owns replay/reconnect semantics.
- **Rationale:** This matches the converged session/new versus session/prompt story, aligns the shim vocabulary with ARI and runtime docs, and removes the old dual-source contradiction where legacy PascalCase methods and $/event notifications were still described as normative.
- **Revisable:** Yes

### D026: How shim live notifications, replay history, and runtime lifecycle changes share one protocol surface

- **When:** M002/S02
- **Choice:** Use a canonical `events.Envelope` with `method` plus typed `params`, assign monotonic `seq` in `events.Translator`, and wire runtime state changes through a post-Create hook so bootstrap traffic stays internal.
- **Rationale:** This makes `runtime/history` replay the exact live notification shape, gives `session/subscribe(afterSeq)` and `runtime/status.recovery.lastSeq` a single sequence authority, and preserves the existing bootstrap visibility boundary by only attaching the state-change hook after `mgr.Create()` succeeds.
- **Revisable:** Yes

### D032: Session recovery config persistence strategy for S03

- **When:** M002/S03 planning
- **Choice:** Add discrete columns (shim_socket_path, shim_state_dir, shim_pid) plus a JSON blob column (bootstrap_config) to the sessions table, with a v1→v2 schema migration using ALTER TABLE and isBenignSchemaError for idempotency.
- **Rationale:** Discrete columns for hot recovery fields (socket path, state dir, PID) enable direct SQL queries during recovery without JSON parsing. The JSON blob for bootstrap_config keeps the schema stable as config fields evolve. The existing isBenignSchemaError pattern handles migration idempotency for existing v1 databases.
- **Revisable:** Yes

### D033: Recovery failure posture for unreachable shims

- **When:** M002/S03 planning
- **Choice:** Mark sessions as stopped (not degraded) when shim socket connection fails during recovery. Continue to next session. Log session_id, socket_path, and error for each failure.
- **Rationale:** Consistent with D012 (fail-closed behavior). A session whose shim is unreachable cannot accept prompts or deliver events, so marking it as stopped is truthful. A degraded state would imply partial functionality that doesn't exist. Continuing to next session ensures one dead shim doesn't block recovery of other sessions.
- **Revisable:** Yes

### D034: Recovered shim watch mechanism

- **When:** M002/S03/T02
- **Choice:** Use DisconnectNotify channel to watch recovered shims instead of Cmd.Wait(), since the daemon did not fork them and has no Cmd handle.
- **Rationale:** Recovered shims were forked by the previous daemon instance; the current process has no exec.Cmd handle. DisconnectNotify fires when the JSONRPC connection drops, which covers both clean shutdown and crash, giving equivalent lifecycle tracking.
- **Revisable:** Yes

### D035: Bootstrap config persistence failure handling

- **When:** M002/S03/T01
- **Choice:** Bootstrap config persistence is non-fatal — session continues if the DB persist call fails after shim fork+connect.
- **Rationale:** The session is already running with a live shim; failing the entire session start because metadata persistence failed would be worse than losing recovery capability. The error is logged for operators to investigate.
- **Revisable:** Yes

## Superseded Decisions

_None._
