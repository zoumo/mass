---
id: T01
parent: S03
milestone: M002
key_files:
  - pkg/meta/schema.sql
  - pkg/meta/models.go
  - pkg/meta/session.go
  - pkg/meta/store.go
  - pkg/agentd/process.go
  - pkg/meta/session_test.go
  - pkg/meta/store_test.go
key_decisions:
  - Bootstrap config persistence is non-fatal — session continues if persist fails
  - isBenignSchemaError extended for duplicate column name to support idempotent ALTER TABLE migrations
duration: 
verification_result: passed
completed_at: 2026-04-07T15:22:24.434Z
blocker_discovered: false
---

# T01: Extended sessions table with v2 recovery columns and wired ProcessManager.Start() to persist bootstrap config after shim fork+connect

**Extended sessions table with v2 recovery columns and wired ProcessManager.Start() to persist bootstrap config after shim fork+connect**

## What Happened

Added 4 recovery columns to the sessions table (bootstrap_config, shim_socket_path, shim_state_dir, shim_pid) via idempotent ALTER TABLE migrations in schema.sql. Extended the Session model, all CRUD methods, and added UpdateSessionBootstrap(). Wired ProcessManager.Start() step 7b to persist the generated config as a JSON blob after successful shim connection. Bootstrap persistence is non-fatal to avoid blocking session startup. Added 3 new unit tests covering round-trip persistence, non-existent session error, and schema migration idempotency. All 76 tests across pkg/meta and pkg/agentd pass.

## Verification

go test ./pkg/meta -count=1 -run 'TestSessionBootstrapConfig|TestSchemaMigration|TestSessionCRUD' -v: 4 PASS. go test ./pkg/agentd -count=1 -v: 47 PASS. go test ./pkg/meta -count=1 -v: 29 PASS. go build ./pkg/meta/... ./pkg/agentd/...: clean build.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go test ./pkg/meta -count=1 -run TestSessionBootstrapConfig|TestSchemaMigration|TestSessionCRUD -v` | 0 | ✅ pass | 6200ms |
| 2 | `go test ./pkg/agentd -count=1 -v` | 0 | ✅ pass | 5800ms |
| 3 | `go test ./pkg/meta -count=1 -v` | 0 | ✅ pass | 8100ms |
| 4 | `go build ./pkg/meta/... ./pkg/agentd/...` | 0 | ✅ pass | 9700ms |

## Deviations

Updated TestNewStore schema version assertion from 1 to 2 — necessary to prevent test regression after schema v2 migration.

## Known Issues

None.

## Files Created/Modified

- `pkg/meta/schema.sql`
- `pkg/meta/models.go`
- `pkg/meta/session.go`
- `pkg/meta/store.go`
- `pkg/agentd/process.go`
- `pkg/meta/session_test.go`
- `pkg/meta/store_test.go`
