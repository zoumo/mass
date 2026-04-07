---
estimated_steps: 18
estimated_files: 3
skills_used: []
---

# T02: RecoverSessions startup pass with event resume and daemon wiring

Add a RecoverSessions method to ProcessManager that runs at daemon startup, reconnects to live shims via persisted socket paths, reconciles state, and resumes event subscriptions using the runtime/status → runtime/history → session/subscribe sequence. Wire it into cmd/agentd/main.go. Fix the shutdown timeout bug.

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

## Inputs

- ``pkg/agentd/process.go` — ProcessManager struct and ShimProcess to extend with recovery`
- ``pkg/agentd/shim_client.go` — DialWithHandler, Status, History, Subscribe methods for recovery calls`
- ``pkg/meta/session.go` — ListSessions and UpdateSessionBootstrap from T01`
- ``pkg/meta/models.go` — Session with ShimSocketPath/ShimStateDir/ShimPID fields from T01`
- ``cmd/agentd/main.go` — daemon startup to wire recovery into`
- ``pkg/events/envelope.go` — Envelope type for history replay`

## Expected Output

- ``pkg/agentd/recovery.go` — RecoverSessions method on ProcessManager`
- ``pkg/agentd/recovery_test.go` — unit tests for recovery scenarios (live shim, dead shim, empty DB)`
- ``cmd/agentd/main.go` — recovery call at startup + shutdown timeout fix`

## Verification

go test ./pkg/agentd -count=1 -run 'TestRecoverSessions' -v && go build ./cmd/agentd && rg '30 \* time.Second' cmd/agentd/main.go
