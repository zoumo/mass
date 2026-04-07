# S03: Recovery and persistence truth-source — UAT

**Milestone:** M002
**Written:** 2026-04-07T15:56:12.304Z

## UAT: S03 — Recovery and persistence truth-source

### Preconditions
- Go toolchain available (go 1.22+)
- Working directory: project root
- All binaries buildable: `go build -o bin/agentd ./cmd/agentd && go build -o bin/agent-shim ./cmd/agent-shim && go build -o bin/mockagent ./internal/testutil/mockagent`

---

### TC-01: Schema v2 migration creates recovery columns

**Steps:**
1. Run `go test ./pkg/meta -count=1 -run TestNewStore -v`
2. Observe test creates a fresh DB with schema v2

**Expected:** Test passes. Schema version is 2. Sessions table includes `bootstrap_config`, `shim_socket_path`, `shim_state_dir`, `shim_pid` columns.

---

### TC-02: Schema migration is idempotent

**Steps:**
1. Run `go test ./pkg/meta -count=1 -run TestSchemaMigration -v`
2. This test runs `initSchema` twice on the same DB

**Expected:** No errors on second run. ALTER TABLE "duplicate column name" errors are handled as benign.

---

### TC-03: Bootstrap config round-trip persistence

**Steps:**
1. Run `go test ./pkg/meta -count=1 -run TestSessionBootstrapConfig -v`
2. Test creates a session, updates bootstrap config + socket path + state dir + PID, reads back

**Expected:** All fields round-trip correctly. JSON blob is preserved exactly. Non-existent session update returns error.

---

### TC-04: RecoverSessions reconnects to live shim

**Steps:**
1. Run `go test ./pkg/agentd -count=1 -run TestRecoverSessions_LiveShim -v`
2. Test starts a mock shim server, persists session with its socket path, calls RecoverSessions

**Expected:** Session recovered. `runtime/status`, `runtime/history`, `session/subscribe` all called. Session registered in processes map.

---

### TC-05: RecoverSessions marks dead shim stopped (fail-closed)

**Steps:**
1. Run `go test ./pkg/agentd -count=1 -run TestRecoverSessions_DeadShim -v`
2. Test persists session with non-existent socket path, calls RecoverSessions

**Expected:** Session state transitions to "stopped". Error logged with session_id, socket_path, and connect error.

---

### TC-06: RecoverSessions is no-op with empty DB

**Steps:**
1. Run `go test ./pkg/agentd -count=1 -run TestRecoverSessions_NoSessions -v`

**Expected:** No errors. Recovery pass completes instantly with recovered=0, failed=0.

---

### TC-07: RecoverSessions skips already-stopped sessions

**Steps:**
1. Run `go test ./pkg/agentd -count=1 -run TestRecoverSessions_StoppedSession -v`
2. Test creates a session in "stopped" state, calls RecoverSessions

**Expected:** Stopped session is not a recovery candidate. No connect attempt made.

---

### TC-08: RecoverSessions handles mixed live and dead shims

**Steps:**
1. Run `go test ./pkg/agentd -count=1 -run TestRecoverSessions_MixedSessions -v`

**Expected:** Live shim recovered, dead shim marked stopped. One failure doesn't block recovery of the other.

---

### TC-09: End-to-end restart recovery with event continuity

**Steps:**
1. Build all binaries: `go build -o bin/agentd ./cmd/agentd && go build -o bin/agent-shim ./cmd/agent-shim && go build -o bin/mockagent ./internal/testutil/mockagent`
2. Run `go test ./tests/integration -run TestAgentdRestartRecovery -count=1 -v -timeout 120s`
3. Observe 6 phases:
   - Phase 1: Two sessions created and prompted
   - Phase 2: agentd stopped, session B's shim killed + socket removed
   - Phase 3: agentd restarted with recovery pass
   - Phase 4: Session A recovered to running
   - Phase 5: Session B marked stopped
   - Phase 6: Session A prompted again, events verified

**Expected:**
- Session A state = "running" after recovery
- Session B state = "stopped" after recovery (fail-closed)
- Session A has 8 events with contiguous sequence [0,1,2,3,4,5,6,7] — no gaps
- Recovery log shows "recovered=1 failed=1 total=2"

---

### TC-10: Shutdown timeout is correct

**Steps:**
1. Run `rg '30 \* time.Second' cmd/agentd/main.go`

**Expected:** Two matches — both the recovery context and the shutdown context use `30 * time.Second`, not bare `30` (which would be 30 nanoseconds).

---

### Edge Cases

- **TC-11: Session with empty shim_socket_path** — RecoverSessions handles this gracefully (skips or marks stopped, no panic). Covered by `TestRecoverSessions_MissingSocketPath`.
- **TC-12: Schema v1 database upgraded to v2** — Opening an existing v1 DB runs ALTER TABLE migrations. Covered by `TestSchemaMigration` which simulates v1→v2 upgrade path.
- **TC-13: ProcessManager.Start() persist failure** — Bootstrap config persistence failure is logged but session continues (non-fatal). Decision D035 documents this posture.
