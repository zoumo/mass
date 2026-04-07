---
estimated_steps: 8
estimated_files: 6
skills_used: []
---

# T01: Schema v2 migration and bootstrap config persistence in ProcessManager.Start

Extend the sessions table with columns for durable recovery state (bootstrap_config JSON, shim_socket_path, shim_state_dir, shim_pid). Add a v1→v2 schema migration in initSchema. Add new Session model fields and Store methods for reading/writing the bootstrap config. Wire ProcessManager.Start() to persist socket path, state dir, PID, and bootstrap config after successful shim fork and connect.

The schema migration uses ALTER TABLE statements guarded by isBenignSchemaError for idempotency — the same pattern the codebase already uses for CREATE IF NOT EXISTS. The bootstrap_config is a JSON blob so the schema stays stable as config fields evolve (per D028/D030/D032).

Steps:
1. Add ALTER TABLE statements to schema.sql (or a new migration block in initSchema) for: bootstrap_config TEXT DEFAULT '{}', shim_socket_path TEXT DEFAULT '', shim_state_dir TEXT DEFAULT '', shim_pid INTEGER DEFAULT 0. Update schema_version to 2.
2. In pkg/meta/models.go: add BootstrapConfig (json.RawMessage), ShimSocketPath (string), ShimStateDir (string), ShimPID (int) fields to Session struct.
3. In pkg/meta/session.go: update CreateSession INSERT to include new columns. Update GetSession/ListSessions SELECT + Scan to read new columns. Add UpdateSessionBootstrap(ctx, id, config, socketPath, stateDir, pid) method.
4. In pkg/agentd/process.go: after forkShim succeeds and client connects, call store.UpdateSessionBootstrap with bundlePath, socketPath, stateDir, PID, and a JSON blob of the generated config.
5. Add unit tests in pkg/meta/session_test.go: TestSessionBootstrapConfig (create, update bootstrap, read back), TestSchemaMigrationIdempotency (run initSchema twice on same DB).

## Inputs

- ``pkg/meta/schema.sql` — current v1 schema to extend`
- ``pkg/meta/models.go` — Session struct to add fields to`
- ``pkg/meta/session.go` — CRUD methods to extend with new columns`
- ``pkg/meta/store.go` — initSchema method to add migration logic to`
- ``pkg/agentd/process.go` — Start method to wire persistence into`

## Expected Output

- ``pkg/meta/schema.sql` — v2 schema with bootstrap columns and ALTER TABLE migration`
- ``pkg/meta/models.go` — Session struct with BootstrapConfig, ShimSocketPath, ShimStateDir, ShimPID fields`
- ``pkg/meta/session.go` — updated CRUD + new UpdateSessionBootstrap method`
- ``pkg/meta/session_test.go` — tests for bootstrap config persistence and migration idempotency`
- ``pkg/meta/store.go` — initSchema with v2 migration path`
- ``pkg/agentd/process.go` — Start() persists bootstrap config after fork+connect`

## Verification

go test ./pkg/meta -count=1 -run 'TestSessionBootstrapConfig|TestSchemaMigration|TestSessionCRUD' -v && go test ./pkg/agentd -count=1 -v
