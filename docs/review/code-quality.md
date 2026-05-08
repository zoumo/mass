# Code Quality Review Report

Date: 2026-04-30

## Summary

Reviewed all non-test Go source files across the MASS codebase (cmd/, pkg/, internal/). The codebase is generally well-structured with good separation of concerns, consistent error handling patterns, and solid concurrency design (K8s-style watch pattern, mutex discipline, atomic operations). However, several issues were identified across different severity levels.

**Statistics:**
- Files reviewed: ~110 non-test Go files
- Critical issues: 1
- High priority issues: 4
- Medium priority issues: 7
- Low priority issues: 6

## Critical Issues

### C1. Goroutine Leak in `done()` Method - Creates Unclosed Channel on Each Call

**File:** `/Users/jim/code/zoumo/mass/pkg/agentrun/runtime/acp/runtime.go` (line 419)
**Category:** Resource Leak / Goroutine Leak
**Severity:** Critical

The `done()` method returns a new `make(chan struct{})` every time `m.conn` is nil. Any caller doing `select { case <-m.done(): ... }` will permanently block on a fresh channel that is never closed. While current callers (Kill) use `time.After` as a safety net, this is a latent bug that will cause goroutine leaks if any future caller relies on this channel without a timeout.

```go
func (m *Manager) done() <-chan struct{} {
    m.mu.Lock()
    conn := m.conn
    m.mu.Unlock()
    if conn != nil {
        return conn.Done()
    }
    return make(chan struct{}) // leaked channel - never closed
}
```

**Suggested Fix:** Return a pre-closed channel (or a package-level never-closing channel singleton) instead of allocating a new one each call:

```go
var neverClosedCh = make(chan struct{})

func (m *Manager) done() <-chan struct{} {
    m.mu.Lock()
    conn := m.conn
    m.mu.Unlock()
    if conn != nil {
        return conn.Done()
    }
    return neverClosedCh // single allocation, same semantics
}
```

## High Priority Issues

### H1. Notification Dropped Silently When Events Channel Is Full

**File:** `/Users/jim/code/zoumo/mass/pkg/agentrun/runtime/acp/client.go` (line 120-128)
**Category:** Concurrency / Data Loss
**Severity:** High

The `SessionUpdate` method uses a non-blocking send with a silent `default` case. When the 1024-buffer channel fills up (e.g., during a burst of tool calls), ACP notifications are permanently lost with only a trace-level log. This can cause the TUI to miss tool results or turn-end events.

```go
func (c *acpClient) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
    select {
    case c.mgr.events <- n:
        c.logger.Log(context.Background(), logging.LevelTrace, "notification received")
    default:
        c.logger.Log(context.Background(), logging.LevelTrace, "notification dropped, channel full")
    }
    return nil
}
```

**Suggested Fix:** At minimum, log at Warn level when dropping. Consider increasing buffer size or implementing backpressure (blocking send with a timeout).

### H2. Context Not Propagated to Background Goroutines in ARI Server

**File:** `/Users/jim/code/zoumo/mass/pkg/ari/server/server.go` (lines 397-410, 599-627)
**Category:** Context Cancellation Not Respected
**Severity:** High

Multiple goroutines spawned with `go func()` use `context.Background()` instead of deriving from the request context or a server-level context. If the daemon shuts down, these goroutines continue running indefinitely until they complete or hit their internal timeouts.

```go
go func() {
    bgCtx := context.Background()
    if _, err := a.processes.Start(bgCtx, wsName, agName); err != nil {
        // ...
    }
}()
```

**Suggested Fix:** Pass a server-level context (derived from the daemon's lifecycle context) to these goroutines so they respect shutdown signals.

### H3. `StopDrain()` Has a Race Condition

**File:** `/Users/jim/code/zoumo/mass/pkg/agentd/process.go` (lines 709-716)
**Category:** Concurrency Bug
**Severity:** High

`StopDrain()` uses a select-default pattern to close a channel, but this is not safe for concurrent calls. Two goroutines calling `StopDrain()` simultaneously could both enter the `default` case and attempt to close the channel twice, causing a panic.

```go
func (sp *RunProcess) StopDrain() {
    select {
    case <-sp.stopDrain:
        // already stopped
    default:
        close(sp.stopDrain)
    }
}
```

**Suggested Fix:** Use `sync.Once` like other close patterns in the codebase:

```go
type RunProcess struct {
    // ...
    stopDrainOnce sync.Once
}

func (sp *RunProcess) StopDrain() {
    sp.stopDrainOnce.Do(func() { close(sp.stopDrain) })
}
```

### H4. Potential Nil Pointer Dereference in `rollbackAgentToIdle`

**File:** `/Users/jim/code/zoumo/mass/pkg/ari/server/server.go` (line 1053-1062)
**Category:** Error Handling
**Severity:** High

`rollbackAgentToIdle` calls `UpdateStatus` which overwrites the entire Status struct with only Phase set. This erases SocketPath, StateDir, PID, SessionID, and EventPath from the DB record. After rollback, the agent's run metadata is lost.

```go
func (s *Service) rollbackAgentToIdle(ws, name, op string, cause error) error {
    rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    if updateErr := s.agents.UpdateStatus(rctx, ws, name, pkgariapi.AgentRunStatus{
        Phase: apiruntime.PhaseIdle, // All other fields zeroed out!
    }); updateErr != nil {
        // ...
    }
    return jsonrpc.ErrInternal(cause.Error())
}
```

**Suggested Fix:** Use `UpdatePhase` instead of `UpdateStatus` to preserve other status fields:

```go
if updateErr := s.agents.UpdatePhase(rctx, ws, name, apiruntime.PhaseIdle, ""); updateErr != nil {
```

## Medium Priority Issues

### M1. `Shutdown` Does Not Close Existing Connections

**File:** `/Users/jim/code/zoumo/mass/pkg/jsonrpc/server.go` (lines 98-101)
**Category:** Resource Leak
**Severity:** Medium

`Shutdown` only signals the server to stop accepting new connections but does not force-close existing connections. There is no mechanism to drain active connections, potentially leaving goroutines hanging indefinitely.

```go
func (s *Server) Shutdown(ctx context.Context) error {
    s.once.Do(func() { close(s.done) })
    return nil // ctx parameter unused
}
```

**Suggested Fix:** Track active connections and close them when the context expires or all RPCs complete.

### M2. `WatchServer.Publish` Can Deadlock When a Watcher Goroutine Exits

**File:** `/Users/jim/code/zoumo/mass/pkg/watch/server.go` (lines 71-92)
**Category:** Concurrency Bug
**Severity:** Medium

`Publish` uses an unbuffered mailbox channel. If a watcher's send goroutine exits (due to a `conn.Send` error) between the snapshot and the send, the `w.mailbox <- ev` send will block forever since nobody is reading from the mailbox. The `w.done` select case prevents this only if `close(w.done)` executes before `Publish` reaches that watcher.

There is a subtle race: the watcher goroutine calls `close(w.done)` after `conn.Close()`, but `Publish` snapshots watchers before sending. If the watcher goroutine has not yet closed `done` when `Publish` reaches it, `Publish` will block on the unbuffered send.

**Suggested Fix:** Use a buffered mailbox of size 1, or add a timeout on the send.

### M3. Missing Error Check on `os.RemoveAll` in Hook Failure Path

**File:** `/Users/jim/code/zoumo/mass/pkg/workspace/manager.go` (line 112)
**Category:** Error Handling
**Severity:** Medium

When setup hooks fail for a managed workspace, `os.RemoveAll` is called but its error is silently discarded. A failed cleanup leaves orphaned directories that will cause `ErrAlreadyExists` on retry.

```go
if managed {
    os.RemoveAll(targetDir) // error discarded
}
```

**Suggested Fix:** Log the error or include it in the returned `WorkspaceError`.

### M4. `RawClient.Call` Uses `context.Background()` - No Cancellation Support

**File:** `/Users/jim/code/zoumo/mass/pkg/ari/client/simple.go` (line 31)
**Category:** API Design
**Severity:** Medium

`RawClient.Call` hardcodes `context.Background()`, making it impossible for callers to set timeouts or cancel long-running calls.

```go
func (c *RawClient) Call(method string, params, result any) error {
    return c.c.Call(context.Background(), method, params, result)
}
```

**Suggested Fix:** Accept a `context.Context` parameter or provide a `CallContext` variant.

### M5. `routeEvent` Leaks Context on Multiple Early Return Paths

**File:** `/Users/jim/code/zoumo/mass/pkg/agentd/process.go` (lines 154-186)
**Category:** Resource Leak
**Severity:** Medium

In `routeEvent`, a context with timeout is created, but `cancel()` is called on different paths via explicit `cancel()` calls and one `defer`. The explicit calls before `return` are correct, but if a panic occurs between context creation and `cancel()`, the context leaks.

```go
updateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
current, err := m.agents.Get(updateCtx, workspace, name)
if err != nil {
    logger.Warn(...)
    cancel()
    return
}
// ... more code ...
cancel()
return
```

**Suggested Fix:** Use `defer cancel()` immediately after creation and remove the explicit `cancel()` calls.

### M6. `lastValidOffset` Scanner Buffer Size Limits Line Length

**File:** `/Users/jim/code/zoumo/mass/pkg/agentrun/server/log.go` (line 202)
**Category:** Correctness
**Severity:** Medium

The scanner buffer is capped at 64KB (`scanner.Buffer(make([]byte, 64*1024), 64*1024)`), meaning event log lines longer than 64KB will cause a scan error. This conflicts with the NDJSON reader which has no upper bound on line size.

```go
scanner.Buffer(make([]byte, 64*1024), 64*1024)
```

**Suggested Fix:** Use a larger max size (e.g., 1MB) or match the unbounded behavior of `ndjson.Reader`.

### M7. `recordPromptDeliveryFailure` Has Duplicated "stopped" Check Logic

**File:** `/Users/jim/code/zoumo/mass/pkg/ari/server/server.go` (lines 1066-1111)
**Category:** Code Smell / Duplication
**Severity:** Medium

The function queries `GetAgentRun` twice with the same "is it stopped?" check, once at the top and once after the runtime status fallback path. This makes the logic hard to follow and the repeated DB reads are wasteful.

**Suggested Fix:** Refactor to query the agent state once at the start and use it throughout the function.

## Low Priority Issues

### L1. Magic Number for Event Channel Buffer Sizes

**File:** Multiple files
**Category:** Code Smell
**Severity:** Low

Buffer sizes are scattered as magic numbers: 256 (jsonrpc client notifCh), 1024 (Manager.events, Translator subs, RunProcess.Events, RetryWatcher), 4096 (chat watcher), 100 (recovery). Consider extracting these as named constants.

### L2. `CallAsync` Leaks Goroutine Until Response/Disconnect

**File:** `/Users/jim/code/zoumo/mass/pkg/jsonrpc/client.go` (lines 143-146)
**Category:** Resource Leak
**Severity:** Low

`CallAsync` spawns a goroutine that blocks on `c.conn.Call()`. If the server never responds and the connection stays open, this goroutine lives forever. The discarded error also means callers cannot know if the request itself failed to serialize.

```go
func (c *Client) CallAsync(ctx context.Context, method string, params any) error {
    go func() { _ = c.conn.Call(ctx, method, params, nil) }()
    return nil
}
```

**Suggested Fix:** Document this limitation clearly. Consider using the context's Done channel to abandon the goroutine.

### L3. Unused `ctx` Parameter in `Shutdown`

**File:** `/Users/jim/code/zoumo/mass/pkg/jsonrpc/server.go` (line 98)
**Category:** API Design
**Severity:** Low

The `ctx` parameter is accepted but never used, violating the principle of least surprise.

### L4. `splitN` Reimplements `strings.SplitN`

**File:** `/Users/jim/code/zoumo/mass/pkg/workspace/spec.go` (lines 329-346)
**Category:** Code Smell
**Severity:** Low

A custom `splitN` function and `isAbs` function are implemented to "avoid importing strings/filepath package in validation code." However, the file already imports `fmt` and `encoding/json`. The stdlib avoidance provides no real benefit and adds maintenance cost.

### L5. Inconsistent Error Wrapping Style

**File:** Multiple files across `pkg/agentd/`, `pkg/ari/server/`
**Category:** Error Handling
**Severity:** Low

Some errors use `fmt.Errorf("...%w", err)` for wrapping while others use `fmt.Errorf("...%v", err)` or just `.Error()`. The `%v` form breaks the error chain, making `errors.Is` / `errors.As` less useful. Examples include `jsonrpc.ErrInternal(err.Error())` which strips error metadata.

### L6. `hook.go` Redundant Nil Check

**File:** `/Users/jim/code/zoumo/mass/pkg/workspace/hook.go` (line 99)
**Category:** Code Smell
**Severity:** Low

```go
if len(hooks) == 0 || hooks == nil {
```

The `len(hooks) == 0` already covers the `nil` case (len of nil slice is 0). The `|| hooks == nil` is redundant.

## Recommendations

1. **Adopt `defer cancel()` consistently** - Every `context.WithTimeout` / `context.WithCancel` should have `defer cancel()` immediately after creation. Remove explicit `cancel()` calls on individual paths.

2. **Add a server-level context** - The daemon's `runStart` should create a root context that is canceled on shutdown and passed to all background goroutines spawned by ARI handlers.

3. **Use `sync.Once` for all close-channel patterns** - Several places use select-default to guard against double-close. Replace with `sync.Once` (already used elsewhere in the codebase) for guaranteed safety.

4. **Extract buffer size constants** - Define named constants like `defaultEventBufferSize`, `defaultNotifBufferSize` etc. in a shared location.

5. **Consider structured error types for ARI failures** - Instead of `jsonrpc.ErrInternal(err.Error())` which loses the error chain, consider wrapping the original error or using a custom error type that preserves it while still conforming to RPC error format.

6. **Add graceful connection draining to `jsonrpc.Server`** - Track active connections and implement proper drain logic in `Shutdown` that respects the context deadline.

7. **Increase `lastValidOffset` scanner buffer** - Align with the NDJSON reader's unbounded line support to prevent silent data loss on large events.
