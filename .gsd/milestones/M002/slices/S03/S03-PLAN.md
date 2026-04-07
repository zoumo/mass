# S03: Recovery and persistence truth-source

**Goal:** Agentd persists enough session config to rebuild truthful state after restart, runs a recovery pass at startup that reconnects to live shims, and resumes event subscriptions without losing events.
**Demo:** After this: TBD

## Tasks
- [x] **T01: Extended sessions table with v2 recovery columns and wired ProcessManager.Start() to persist bootstrap config after shim fork+connect** — Extend the sessions table with columns for durable recovery state (bootstrap_config JSON, shim_socket_path, shim_state_dir, shim_pid). Add a v1→v2 schema migration in initSchema. Add new Session model fields and Store methods for reading/writing the bootstrap config. Wire ProcessManager.Start() to persist socket path, state dir, PID, and bootstrap config after successful shim fork and connect.

The schema migration uses ALTER TABLE statements guarded by isBenignSchemaError for idempotency — the same pattern the codebase already uses for CREATE IF NOT EXISTS. The bootstrap_config is a JSON blob so the schema stays stable as config fields evolve (per D028/D030/D032).

Steps:
1. Add ALTER TABLE statements to schema.sql (or a new migration block in initSchema) for: bootstrap_config TEXT DEFAULT '{}', shim_socket_path TEXT DEFAULT '', shim_state_dir TEXT DEFAULT '', shim_pid INTEGER DEFAULT 0. Update schema_version to 2.
2. In pkg/meta/models.go: add BootstrapConfig (json.RawMessage), ShimSocketPath (string), ShimStateDir (string), ShimPID (int) fields to Session struct.
3. In pkg/meta/session.go: update CreateSession INSERT to include new columns. Update GetSession/ListSessions SELECT + Scan to read new columns. Add UpdateSessionBootstrap(ctx, id, config, socketPath, stateDir, pid) method.
4. In pkg/agentd/process.go: after forkShim succeeds and client connects, call store.UpdateSessionBootstrap with bundlePath, socketPath, stateDir, PID, and a JSON blob of the generated config.
5. Add unit tests in pkg/meta/session_test.go: TestSessionBootstrapConfig (create, update bootstrap, read back), TestSchemaMigrationIdempotency (run initSchema twice on same DB).
  - Estimate: 1.5h
  - Files: pkg/meta/schema.sql, pkg/meta/models.go, pkg/meta/session.go, pkg/meta/session_test.go, pkg/meta/store.go, pkg/agentd/process.go
  - Verify: go test ./pkg/meta -count=1 -run 'TestSessionBootstrapConfig|TestSchemaMigration|TestSessionCRUD' -v && go test ./pkg/agentd -count=1 -v
- [x] **T02: Added RecoverSessions startup pass that reconnects to live shims, replays history, resumes subscriptions, and marks dead shims stopped; wired into daemon startup and fixed shutdown timeout bug** — Add a RecoverSessions method to ProcessManager that runs at daemon startup, reconnects to live shims via persisted socket paths, reconciles state, and resumes event subscriptions using the runtime/status → runtime/history → session/subscribe sequence. Wire it into cmd/agentd/main.go. Fix the shutdown timeout bug.

Recovery sequence per session (from shim-rpc-spec):
1. List all sessions in non-terminal state from meta store
2. For each: read persisted shim_socket_path from DB
3. Try DialWithHandler to connect to shim socket
4. On connect failure → mark session stopped (D012/D029 fail-closed), log session_id + socket_path + error, continue
5. Call runtime/status → get state + recovery.lastSeq
6. Call runtime/history(fromSeq=0) → replay any events into EventLog if needed
7. Call session/subscribe(afterSeq=lastSeq) → resume live notifications
8. Build ShimProcess struct, register in processes map
9. Start watchProcess goroutine for the recovered session

Also fix shutdown timeout bug in cmd/agentd/main.go: change `context.WithTimeout(context.Background(), 30)` to `context.WithTimeout(context.Background(), 30*time.Second)`.

Steps:
1. Create pkg/agentd/recovery.go with RecoverSessions(ctx) error method on ProcessManager.
2. Implement the recovery loop: list non-terminal sessions, attempt connect, reconcile, subscribe, or mark stopped.
3. Add unit test pkg/agentd/recovery_test.go: TestRecoverSessions_LiveShim (mock shim that responds to status/history/subscribe), TestRecoverSessions_DeadShim (connect fails → session marked stopped), TestRecoverSessions_NoSessions (empty DB → no-op).
4. In cmd/agentd/main.go: call processes.RecoverSessions(ctx) after ProcessManager creation, before starting ARI server. Fix the shutdown timeout bug.
5. Add a time import if not already present in main.go for time.Second.
  - Estimate: 2h
  - Files: pkg/agentd/recovery.go, pkg/agentd/recovery_test.go, cmd/agentd/main.go
  - Verify: go test ./pkg/agentd -count=1 -run 'TestRecoverSessions' -v && go build ./cmd/agentd && rg '30 \* time.Second' cmd/agentd/main.go
- [x] **T03: Rewrote TestAgentdRestartRecovery to prove bootstrap config persistence, live shim reconnection, dead-shim fail-closed marking, and event sequence continuity across daemon restart** — Extend the existing tests/integration/restart_test.go to prove that agentd restart recovers sessions with event continuity. The current test is aspirational — it only checks session existence after restart. This task makes it prove real recovery: session config survives, shim reconnects, events have no seq gaps, and dead shims result in stopped sessions.

The test must prove R035 (single resume path closes event gap) and R036 (enough config persisted to rebuild truthful state after restart).

Steps:
1. Refactor TestAgentdRestartRecovery to verify bootstrap config persistence: after restart, session/status returns running state with shim reconnected.
2. Add event continuity verification: after Phase 1 prompt, record the event count/last seq. After restart + recovery, send another prompt. Verify the combined event log has no seq gaps.
3. Add a dead-shim recovery case: create a second session, kill its shim PID before restart, verify it's marked stopped after recovery while the live session remains running.
4. Add a helper function to count events via session/status or a new test utility that reads the events.jsonl file.
5. Verify all existing pkg tests still pass alongside the integration test.

Note: This test requires built binaries (agentd, agent-shim, mockagent). The test setup already handles binary discovery. The test uses real Unix sockets and real process fork/kill.
  - Estimate: 1.5h
  - Files: tests/integration/restart_test.go
  - Verify: go build ./cmd/agentd ./cmd/agent-shim ./cmd/mockagent && go test ./tests/integration -run TestAgentdRestartRecovery -count=1 -v -timeout 120s
