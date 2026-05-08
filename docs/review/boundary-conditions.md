# Boundary Conditions Review Report

Date: 2026-04-30

## Summary

Reviewed 6 packages (`pkg/agentrun/runtime/acp`, `pkg/agentd`, `pkg/ari/server`, `pkg/jsonrpc`, `pkg/workspace`, `pkg/watch`) for boundary condition issues. The codebase is generally well-structured with good error handling patterns. Found issues primarily around blocking operations without timeouts, potential nil dereferences in edge cases, and graceful shutdown gaps in the watch/notification subsystems.

## Critical Issues

### C1: WatchServer.Publish blocks indefinitely on slow watchers

**File:** `pkg/watch/server.go:86`

`Publish()` sends to each watcher's unbuffered mailbox channel sequentially while holding `publishMu`. If a watcher's send goroutine is slow (e.g., network congestion on `conn.Send()`), `Publish` blocks indefinitely for ALL watchers, stalling the entire event pipeline. There is no timeout, context, or eviction mechanism.

```go
// Blocks forever if w's goroutine is stuck in conn.Send()
select {
case w.mailbox <- ev:
case <-w.done:
}
```

**Impact:** A single slow consumer can halt all event delivery system-wide.

**Recommendation:** Add a per-watcher send timeout or switch to a buffered mailbox with eviction on overflow (similar to the jsonrpc WatchStream eviction pattern already used elsewhere).

---

### C2: WatchServer watcher goroutine leaks mailbox channel on normal shutdown

**File:** `pkg/watch/server.go:50-65`

The watcher goroutine exits only when (a) `w.mailbox` is closed or (b) `conn.Send()` returns an error. There is no `Stop()` or `Close()` method on `WatchServer` that closes mailbox channels. If a `WatchServer` is discarded without explicitly closing each watcher's connection, the goroutine leaks because it blocks forever on `range w.mailbox`.

**Impact:** Goroutine leak on daemon shutdown or test teardown.

**Recommendation:** Add a `WatchServer.Stop()` method that closes all watcher mailbox channels.

## High Priority Issues

### H1: Manager.done() returns a never-closing channel when conn is nil

**File:** `pkg/agentrun/runtime/acp/runtime.go:411-419`

```go
func (m *Manager) done() <-chan struct{} {
    ...
    if conn != nil {
        return conn.Done()
    }
    return make(chan struct{}) // never closes
}
```

If `Kill()` is called before `Create()` succeeds (e.g., during error cleanup), the `select` in `Kill()` (line 261-269) will always hit the `time.After(5s)` path, adding a 5-second delay even if the process is already dead. This also allocates a channel that is immediately leaked.

**Impact:** Unnecessary 5-second delay during error cleanup; minor memory leak.

**Recommendation:** Return a pre-closed channel or check `cmd.ProcessState` directly.

---

### H2: Missing context propagation in workspace/create async goroutine

**File:** `pkg/ari/server/server.go:154-186`

The workspace preparation goroutine creates its own timeout context but uses `context.Background()` as the parent. If the daemon shuts down, there is no way to cancel in-flight workspace preparations (e.g., a long git clone).

```go
go func() {
    prepareCtx, cancel := context.WithTimeout(context.Background(), prepareTimeout)
    defer cancel()
    path, err := a.manager.Prepare(prepareCtx, wsSpec, targetDir)
    ...
}()
```

**Impact:** Workspace git clone can continue running for up to 300 seconds after daemon shutdown.

**Recommendation:** Pass a daemon-level context (e.g., from `Server.done`) as the parent instead of `context.Background()`.

---

### H3: rollbackAgentToIdle uses UpdateStatus which overwrites all status fields

**File:** `pkg/ari/server/server.go:1053-1061`

When rolling back after a failed task dispatch, `UpdateStatus` replaces the entire status struct with just `Phase: PhaseIdle`. This clears `SocketPath`, `StateDir`, `PID`, `SessionID`, and `EventPath`, making recovery impossible.

```go
if updateErr := s.agents.UpdateStatus(rctx, ws, name, pkgariapi.AgentRunStatus{
    Phase: apiruntime.PhaseIdle,
}); updateErr != nil { ... }
```

**Impact:** Agent metadata is lost after a rollback, breaking subsequent operations that rely on `SocketPath` or `PID`.

**Recommendation:** Use `UpdatePhase()` instead of `UpdateStatus()` to preserve other fields.

---

### H4: Race condition between drainEvents and external consumer

**File:** `pkg/agentd/process.go:693-705`

`drainEvents()` reads from `sp.Events` in a select with `sp.stopDrain` and `sp.Done`. However, after `StopDrain()` closes `stopDrain`, there is a window where events already in the channel could be consumed by both the drain goroutine (during its final iteration) and the external consumer if they start reading simultaneously.

**Impact:** Potential lost events during handoff from drain to external consumer.

**Recommendation:** Ensure `StopDrain()` waits for the drain goroutine to fully exit before returning (e.g., use a done channel).

## Medium Priority Issues

### M1: nextTaskPath wraps around at 10000 without checking for collision

**File:** `pkg/ari/server/server.go:853`

```go
nextNum := (maxNum + 1) % 10000
```

If 10000 tasks have been created, the ID wraps to `task-0000` which likely already exists, causing `os.WriteFile` to silently overwrite the old task file.

**Impact:** Task data loss after 10000 tasks per agent.

**Recommendation:** Check for file existence before writing, or use a monotonically increasing ID without wrapping.

---

### M2: SessionNotification channel silently drops events

**File:** `pkg/agentrun/runtime/acp/client.go:120-128`

The `SessionUpdate` method uses a non-blocking send:
```go
select {
case c.mgr.events <- n:
default:
    // notification dropped, channel full
}
```

The events channel is 1024-buffered but if a long-running prompt generates many notifications without a consumer draining them, events are silently lost.

**Impact:** UI/monitoring consumers may miss important session state transitions.

**Recommendation:** Consider logging at a higher level than TRACE when dropping, or implement backpressure signaling.

---

### M3: Potential nil pointer in RequestPermission for ApproveAll case

**File:** `pkg/agentrun/runtime/acp/client.go:153-154`

```go
allowOpt := pickOption(req.Options, ...)
resp := allowedResponse(req.Options, allowOpt)
c.logger.Debug("permission approved", "optionId", resp.Outcome.Selected.OptionId)
```

If `req.Options` is empty and `allowOpt` is nil, `allowedResponse` returns a response with `Selected.OptionId = ""` (safe). However, the logger accesses `resp.Outcome.Selected.OptionId` which would panic if `Selected` were nil. In practice `allowedResponse` always sets `Selected`, so this is safe but brittle.

**Impact:** Low risk currently, but fragile to future refactoring.

---

### M4: processManager.recoverAgent closes client on status error but not on subsequent failures

**File:** `pkg/agentd/recovery.go:213-216`

If `client.Status()` fails, the client is properly closed. But if the subsequent `client.Load()` call hangs or errors, the client is already registered in `runProc` and could be in a bad state with no cleanup path.

**Impact:** Leaked connections on partial recovery failures.

---

### M5: Git clone with --depth and SHA ref creates unresolvable state

**File:** `pkg/workspace/git.go:64`

When `depth > 0` and `ref` is a SHA, the code does a shallow clone then tries `git checkout <sha>`. A shallow clone may not include the commit at that SHA, causing checkout to fail with a confusing error.

**Impact:** Misleading error message for users combining depth with SHA refs.

**Recommendation:** Skip `--depth` when ref is a commit SHA, or validate this combination in `ValidateWorkspaceSpec`.

## Low Priority Issues

### L1: Store bucket helper functions assume bucket hierarchy always exists

**File:** `pkg/agentd/store/store.go:103-109`

```go
func workspacesBucket(tx *bolt.Tx) *bolt.Bucket {
    return tx.Bucket(bucketV1).Bucket(bucketWorkspaces)
}
```

If `bucketV1` returns nil (corrupt DB), this panics with nil pointer dereference. `initBuckets()` should prevent this, but if the DB is manually corrupted or opened without init, it crashes.

**Impact:** Unrecoverable panic on corrupt DB. Unlikely in normal operation.

---

### L2: jsonrpc.Server.Shutdown does not close existing connections

**File:** `pkg/jsonrpc/server.go:98-101`

`Shutdown` closes the `done` channel to stop accepting new connections, but existing `handleConn` goroutines continue running indefinitely until their peer disconnects.

**Impact:** Daemon shutdown may hang if clients do not disconnect promptly.

**Recommendation:** Track active connections and close them on shutdown (with grace period).

---

### L3: CallAsync leaks goroutine if connection is broken

**File:** `pkg/jsonrpc/client.go:143-146`

```go
func (c *Client) CallAsync(ctx context.Context, method string, params any) error {
    go func() { _ = c.conn.Call(ctx, method, params, nil) }()
    return nil
}
```

If the context never cancels and the connection is broken, the underlying `jsonrpc2.Conn.Call` should return an error when the connection closes. However, if the caller passes `context.Background()`, the goroutine may linger until GC. This is an unlikely edge case since connection closure propagates.

**Impact:** Minimal in practice.

---

### L4: WatchServer.Accept goroutine does not close `w.done` on normal mailbox close

**File:** `pkg/watch/server.go:56-64`

When `w.mailbox` is closed (ending the range loop), the goroutine returns via the deferred `delete()` but never closes `w.done`. Any `Publish()` call racing at that exact moment would block on `w.mailbox <- ev` forever (since the channel is closed, this would actually panic with send-on-closed-channel).

**Impact:** Potential panic if `Publish()` and mailbox close race. Currently mitigated because there is no `Stop()` method that closes mailboxes.

---

### L5: featureEnabled returns false for unknown features (fail-closed)

**File:** `pkg/agentd/features.go:18-24`

This is actually good behavior (fail-closed for unknown features), noted here only because `defaultFeatures` map lookup returns `false` for missing keys without explicit documentation that this is intentional.

**Impact:** None -- this is correct behavior.

## Recommendations

1. **Priority fix:** Add timeout or eviction to `WatchServer.Publish()` to prevent a single slow watcher from blocking all event delivery. Consider the buffered-channel + eviction pattern already used in `pkg/jsonrpc/client.go`.

2. **Add WatchServer.Stop():** Implement a clean shutdown method that closes all watcher mailbox channels and waits for goroutines to exit.

3. **Use UpdatePhase instead of UpdateStatus for rollbacks:** The `rollbackAgentToIdle` helper should use `UpdatePhase` to avoid clearing critical status metadata.

4. **Propagate daemon context to background goroutines:** Workspace preparation and agent-run start goroutines should use a daemon-scoped context so they can be canceled on shutdown.

5. **Add collision detection to task ID generation:** Either remove the modulo wrap or check for existing files before overwriting.

6. **Consider adding a `Shutdown(ctx)` to jsonrpc.Server that forcibly closes idle connections** after a grace period, preventing hanging on daemon exit.
