# S03 Research: Recovery and persistence truth-source

## Summary

S03 must make daemon restart, session recovery, and event replay truthful rather than aspirational. Today, agentd loses all in-memory session state on restart — the `ProcessManager.processes` map vanishes, shim connections are severed, and there's no code to scan for surviving shim sockets, reconcile metadata, or resume event subscriptions. The meta store persists session identity and state but not the bootstrap configuration needed to rebuild a truthful session.

**Depth: Targeted.** The patterns are established (SQLite store, shim RPC, event log), and the design docs explicitly name what's missing. The work is wiring new persistence columns, a startup reconciliation pass, and a gapped-event resume path — using known technology in a known codebase.

## Active Requirements Owned

- **R035** — Runtime event recovery must offer a single resume path that closes the gap between history replay and live subscription. Currently events.Translator + EventLog + shim subscribe all exist but agentd has no code that executes the prescribed recovery sequence (status → history → subscribe) after reconnect.
- **R036** — Runtime must preserve enough session config and identity to rebuild truthful state after restart. Currently `sessions` table stores ID, workspace_id, runtime_class, room, labels, state — but NOT the bootstrap config snapshot (systemPrompt, env overrides, mcpServers, permissions, bundle path, socket path, resolved cwd, ACP sessionId mapping).

## Recommendation

Three tasks, ordered by dependency:

1. **Schema migration + durable session config** — extend `sessions` table (or add `session_config` table) to persist the bootstrap config snapshot, bundle path, socket path, and last known shim PID. This is the foundation everything else depends on.
2. **Startup reconciliation** — add a `RecoverSessions` method to ProcessManager (or a new RecoveryManager) that runs at daemon startup: scan shim sockets, connect, call `runtime/status`, reconcile with metadata, re-establish ShimClient + subscribe, or mark sessions as degraded/stopped.
3. **Event resume path** — wire the recovery flow to use `runtime/status.recovery.lastSeq` + `runtime/history(fromSeq)` + `session/subscribe(afterSeq)` to close event gaps. Add a test that proves no events are lost across a daemon restart.

## Implementation Landscape

### What exists

| Component | File | What it does | What's missing for S03 |
|---|---|---|---|
| Meta store schema | `pkg/meta/schema.sql` | sessions table with id, workspace_id, runtime_class, room, labels, state | No bootstrap config columns (systemPrompt, env, mcpServers, permissions, bundle_path, socket_path, shim_pid, resolved_cwd, acp_session_id) |
| Session CRUD | `pkg/meta/session.go` | CreateSession, GetSession, UpdateSession, DeleteSession | No bootstrap config persistence; UpdateSession only updates state and labels |
| SessionManager | `pkg/agentd/session.go` | State machine validation, transition logging | No recovery/reconciliation logic |
| ProcessManager | `pkg/agentd/process.go` | In-memory `processes` map, Start/Stop/Connect/State | No recovery method; processes map lost on restart. `watchProcess` cleans up on exit. `forkShim` writes bundle, symlinks workspace, waits for socket. |
| ShimClient | `pkg/agentd/shim_client.go` | Clean-break RPC: session/prompt, session/cancel, session/subscribe, runtime/status, runtime/history, runtime/stop | All plumbing exists for recovery calls. No caller orchestrates the recovery sequence. |
| EventLog | `pkg/events/log.go` | JSONL append + read with seq validation | Already supports `ReadEventLog(path, fromSeq)` — ready for replay. |
| Translator | `pkg/events/translator.go` | ACP → Envelope with monotonic seq, fan-out to subscribers, optional EventLog | Subscribe returns (chan, id, nextSeq) — recovery consumer can use this. |
| State persistence | `pkg/spec/state.go` | WriteState/ReadState for state.json, StateDir/ShimSocketPath/EventLogPath helpers | State dir layout is correct: `<baseDir>/<sessionId>/state.json`, `events.jsonl`, `agent-shim.sock` |
| Runtime Manager | `pkg/runtime/runtime.go` | Create/Prompt/Kill/Delete + state.json lifecycle | Writes state.json at each transition. Background goroutine writes stopped on exit. |
| ARI server | `pkg/ari/server.go` | session/new, session/prompt, session/status, session/attach, session/list, etc. | session/new doesn't persist bootstrap config; session/status reads from ProcessManager (in-memory only) |
| Daemon main | `cmd/agentd/main.go` | Init store, registry, sessions, processes, ARI server, signal handling | No recovery pass at startup. Shutdown timeout bug: `context.WithTimeout(context.Background(), 30)` = 30ns not 30s. |
| Integration tests | `tests/integration/restart_test.go` | TestAgentdRestartRecovery — aspirational, tests session existence after restart | Currently only checks session exists in meta store; no shim reconnect or event replay verification |

### State dir layout (already defined in runtime-spec)

```
/run/agentd/shim/<session-id>/
├── state.json        ← runtime state (status, pid, bundle, etc.)
├── agent-shim.sock   ← shim RPC socket
└── events.jsonl      ← durable event log
```

`spec.ShimSocketPath(stateDir)` and `spec.EventLogPath(stateDir)` already compute these paths.

### Recovery sequence (already defined in shim-rpc-spec.md)

The shim-rpc-spec prescribes this sequence for agentd after restart:

1. Scan `/run/agentd/shim/*/agent-shim.sock` or use persisted metadata to find sockets
2. Connect to each shim socket
3. Call `runtime/status` → get current state + `recovery.lastSeq`
4. Call `runtime/history(fromSeq=lastProcessedSeq+1)` → replay missed events
5. Call `session/subscribe(afterSeq=lastProcessedSeq)` → resume live stream
6. If shim socket doesn't exist or connect fails → mark session as stopped/degraded

### What needs to change

#### 1. Schema migration (new columns on `sessions`)

Add columns for durable bootstrap config:

```sql
ALTER TABLE sessions ADD COLUMN bootstrap_config TEXT DEFAULT '{}';
-- JSON blob: { systemPrompt, env, mcpServers, permissions, bundlePath, socketPath, stateDir, resolvedCwd, shimPid, acpSessionId }

ALTER TABLE sessions ADD COLUMN shim_socket_path TEXT DEFAULT '';
ALTER TABLE sessions ADD COLUMN shim_state_dir TEXT DEFAULT '';
ALTER TABLE sessions ADD COLUMN shim_pid INTEGER DEFAULT 0;
```

Alternatively, a JSON blob column `bootstrap_config` keeps the schema stable as bootstrap fields evolve. The socket_path, state_dir, and PID are hot recovery fields that benefit from being discrete columns for direct query.

The schema version table already exists (`schema_version`). Migration from v1 → v2 can be handled in `initSchema` by checking current version and running ALTER TABLE statements.

#### 2. Persist bootstrap config at session creation

In `ProcessManager.Start()` after `createBundle`:
- Capture the resolved config (systemPrompt, env, mcpServers, permissions, bundlePath, socketPath, stateDir, resolvedCwd)
- Store in meta.Store via a new `UpdateSessionConfig` method
- After shim connect succeeds, update shimPid

In `handleSessionNew` (ARI server) — currently only stores workspaceId, runtimeClass, room, labels. After ProcessManager.Start succeeds, the bootstrap config should be persisted.

#### 3. RecoverSessions at daemon startup

New method on ProcessManager or a dedicated RecoveryManager:

```go
func (m *ProcessManager) RecoverSessions(ctx context.Context) error {
    // 1. List all sessions in non-terminal state from meta store
    sessions, _ := m.sessions.List(ctx, &meta.SessionFilter{})
    
    for _, session := range sessions {
        if session.State == meta.SessionStateStopped {
            continue
        }
        
        // 2. Try to connect to shim socket (from persisted socket_path)
        socketPath := session.ShimSocketPath // new field
        if socketPath == "" {
            // No socket path persisted — mark degraded
            m.sessions.Transition(ctx, session.ID, meta.SessionStateStopped)
            continue
        }
        
        // 3. Connect + runtime/status
        client, err := DialWithHandler(ctx, socketPath, handler)
        if err != nil {
            // Shim not running — mark stopped
            m.sessions.Transition(ctx, session.ID, meta.SessionStateStopped)
            continue
        }
        
        // 4. Reconcile state
        status, err := client.Status(ctx)
        // ... reconcile status with persisted state
        
        // 5. Subscribe for live events
        // ... runtime/history + session/subscribe
        
        // 6. Re-register in processes map
    }
}
```

#### 4. Event resume test

Prove that events are not lost across daemon restart:
1. Start agentd, create session, prompt (generates events with known seq range)
2. Kill agentd (keep shim alive)
3. Restart agentd with same config
4. Recovery connects to shim, calls runtime/history, subscribes
5. Send another prompt → new events arrive
6. Verify event log has complete seq sequence with no gaps

### Seams and task boundaries

**Task 1 (Schema + persistence):** `pkg/meta/schema.sql`, `pkg/meta/models.go`, `pkg/meta/session.go`, `pkg/meta/store.go` for schema migration; `pkg/agentd/process.go` for persisting config at Start time. Unit tests in `pkg/meta/session_test.go`.

**Task 2 (Startup recovery):** `pkg/agentd/process.go` or new `pkg/agentd/recovery.go` for RecoverSessions; `cmd/agentd/main.go` to call it after startup. Also fix the shutdown timeout bug (`30` → `30*time.Second`). Unit tests via mock shim clients.

**Task 3 (Event resume + integration test):** Wire the runtime/status → runtime/history → session/subscribe recovery flow in the recovery path. Extend `tests/integration/restart_test.go` to prove event continuity. This task depends on T1 (persisted socket paths) and T2 (recovery runs at startup).

### Risks

1. **Shim may have exited while agentd was down.** The socket file might exist but be stale. Recovery must handle connect failures gracefully (mark session stopped, clean up state dir).

2. **Schema migration on existing databases.** If someone has an existing `agentd.db` at v1, ALTER TABLE must handle the "column already exists" case. The existing `isBenignSchemaError` pattern handles this.

3. **ProcessManager.processes map is the single source of truth for "running" sessions.** Recovery must re-populate this map atomically, and the ARI server must handle the window between daemon startup and recovery completion.

4. **ACP sessionId is opaque.** The shim knows it but doesn't expose it through `runtime/status`. For now, persisting the ACP sessionId requires either extending `runtime/status` response or reading it from the shim's ACP connection state. This can be deferred — the OAR sessionId is sufficient for recovery; ACP sessionId is diagnostic.

### Shutdown timeout bug

`cmd/agentd/main.go` line: `ctx, cancel := context.WithTimeout(context.Background(), 30)` — the second argument is `time.Duration` in nanoseconds. `30` = 30 nanoseconds. Must be `30 * time.Second`. This should be fixed in T2 alongside the startup recovery work.

### Design doc verification

After S03 lands, the "Durable State Gaps for S03" table in `docs/design/runtime/design.md` should be updated to reflect which gaps are closed vs remaining. The contract verifier script (`scripts/verify-m002-s01-contract.sh`) and example bundle test should still pass.

## Skills Discovered

No new skills needed. This is Go backend work using established patterns (SQLite, JSON-RPC, Unix sockets) already in the codebase.
