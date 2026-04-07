---
id: S03
parent: M002
milestone: M002
provides:
  - ["Durable session config persistence (schema v2)", "RecoverSessions startup pass for live shim reconnection", "Fail-closed dead-shim marking", "Event-continuity-preserving reconnection sequence", "Integration test proving restart recovery end-to-end"]
requires:
  []
affects:
  - ["S04"]
key_files:
  - ["pkg/meta/schema.sql", "pkg/meta/models.go", "pkg/meta/session.go", "pkg/meta/session_test.go", "pkg/agentd/recovery.go", "pkg/agentd/recovery_test.go", "pkg/agentd/process.go", "cmd/agentd/main.go", "tests/integration/restart_test.go"]
key_decisions:
  - ["D034: Recovered shims watched via DisconnectNotify, not Cmd.Wait", "D035: Bootstrap config persistence is non-fatal — session continues if persist fails", "D036: R035 validated — single resume path closes event gap", "D037: R036 validated — config persisted for truthful state rebuild after restart"]
patterns_established:
  - ["Recovery sequence: runtime/status → runtime/history → session/subscribe", "DisconnectNotify for adopted shim processes", "ALTER TABLE + isBenignSchemaError for idempotent schema migration", "Non-fatal bootstrap persistence with error logging"]
observability_surfaces:
  - none
drill_down_paths:
  []
duration: ""
verification_result: passed
completed_at: 2026-04-07T15:56:12.304Z
blocker_discovered: false
---

# S03: Recovery and persistence truth-source

**Agentd now persists session bootstrap config in schema v2, runs a RecoverSessions startup pass that reconnects to live shims and marks dead shims stopped, and resumes event subscriptions with proven sequence continuity across daemon restart.**

## What Happened

### What This Slice Delivered

S03 implemented the durable recovery subsystem that makes agentd restart truthful rather than aspirational. Three tasks built the persistence layer, the recovery engine, and the end-to-end proof.

**T01 — Schema v2 and persistence wiring.** Extended the sessions table with 4 recovery columns: `bootstrap_config` (JSON blob), `shim_socket_path`, `shim_state_dir`, and `shim_pid`. Added a v1→v2 schema migration using idempotent ALTER TABLE statements guarded by `isBenignSchemaError`. Extended the Session model, all CRUD methods, and added `UpdateSessionBootstrap()`. Wired `ProcessManager.Start()` step 7b to persist the generated config as a JSON blob after successful shim connection. Bootstrap persistence is deliberately non-fatal (D035) — the session continues even if DB persist fails. Added 3 new unit tests. All 29 pkg/meta tests pass.

**T02 — RecoverSessions startup engine.** Created `pkg/agentd/recovery.go` with `RecoverSessions(ctx)` that lists non-terminal sessions, connects to persisted shim sockets via `DialWithHandler`, calls `runtime/status` → `runtime/history` → `session/subscribe` to reconcile and resume event delivery, and registers recovered shims in the processes map. Dead shims are marked stopped (fail-closed per D012/D029). Added `watchRecoveredProcess` goroutine using `DisconnectNotify` for adopted shims without a Cmd handle (D034). Wired recovery into `cmd/agentd/main.go` before ARI server start. Fixed the shutdown timeout bug (30 nanoseconds → 30*time.Second). Added 6 unit tests. All 53 pkg/agentd tests pass.

**T03 — Integration proof.** Rewrote `TestAgentdRestartRecovery` from an aspirational stub into a comprehensive 6-phase test proving R035 and R036. Phase 1: start two sessions, prompt both to running state, record pre-restart event count. Phase 2: stop agentd, kill session B's shim + remove socket. Phase 3: restart agentd — recovery reconnects A, fails B. Phase 4: assert A recovered to running with shimState. Phase 5: assert B marked stopped. Phase 6: prompt A again, verify 8 events with contiguous sequence [0-7], no gaps. Test passes in ~3.7s.

### Key Patterns Established

1. **Recovery sequence**: `runtime/status` → `runtime/history(fromSeq=0)` → `session/subscribe(afterSeq=lastSeq)` — this is the single resume path that closes the event gap (R035).
2. **Fail-closed posture**: unreachable shims → stopped, not degraded. No partial-functionality states.
3. **Adopted process watching**: `DisconnectNotify` channel for shims the daemon didn't fork.
4. **Schema migration without framework**: ALTER TABLE + isBenignSchemaError for idempotent upgrades.
5. **Non-fatal persistence**: bootstrap config persist failure is logged but doesn't block session startup.

### What the Next Slice Should Know

- S04 (Real CLI integration verification) can now rely on agentd surviving restart and reconnecting to live shims. The recovery path is proven with mockagent but has not been tested with real CLI agents (claude-code, gsd-pi).
- The mockagent binary is at `internal/testutil/mockagent`, NOT `cmd/mockagent`. Build with `go build -o bin/mockagent ./internal/testutil/mockagent`.
- Schema is now v2. Any future column additions should follow the same ALTER TABLE + isBenignSchemaError pattern in `pkg/meta/schema.sql`.
- The integration test uses real Unix sockets and real process fork/kill — it's not hermetic and may leave orphan processes on test failure. The cleanup in the test handles the happy path.

## Verification

### Verification Results

All slice-level verification checks pass:

| # | Command | Exit Code | Verdict |
|---|---------|-----------|---------|
| 1 | `go build -o bin/agentd ./cmd/agentd && go build -o bin/agent-shim ./cmd/agent-shim && go build -o bin/mockagent ./internal/testutil/mockagent` | 0 | ✅ pass |
| 2 | `go test ./pkg/meta -count=1 -v` | 0 | ✅ pass (29 tests) |
| 3 | `go test ./pkg/agentd -count=1 -v` | 0 | ✅ pass (53 tests) |
| 4 | `go test ./tests/integration -run TestAgentdRestartRecovery -count=1 -v -timeout 120s` | 0 | ✅ pass (3.69s) |
| 5 | `rg '30 \* time.Second' cmd/agentd/main.go` | 0 | ✅ pass (2 matches) |

### Requirements Validated

- **R035** (continuity): TestAgentdRestartRecovery Phase 6 proves event sequence [0-7] with zero gaps across daemon restart.
- **R036** (continuity): TestAgentdRestartRecovery proves bootstrap_config, socket_path, state_dir, PID persist and enable truthful state rebuild after restart.

## Requirements Advanced

None.

## Requirements Validated

- R035 — TestAgentdRestartRecovery Phase 6: 8 events with contiguous seq [0-7] across restart, zero gaps
- R036 — TestAgentdRestartRecovery: bootstrap_config, socket_path, state_dir, PID persist; live shim reconnected, dead shim marked stopped

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

T03 verify command in plan referenced `./cmd/mockagent` which doesn't exist; actual path is `./internal/testutil/mockagent`. T02 added 3 extra test cases beyond the 3 specified. T01 updated TestNewStore schema version assertion from 1 to 2.

## Known Limitations

Recovery only proven with mockagent, not real CLI agents. Integration test not hermetic — uses real Unix sockets and process fork/kill. Orphan process cleanup on test failure is best-effort.

## Follow-ups

Recovery tested with mockagent only — real CLI agents (claude-code, gsd-pi) need verification in S04. Integration test cleanup may leave orphan processes on test failure.

## Files Created/Modified

None.
