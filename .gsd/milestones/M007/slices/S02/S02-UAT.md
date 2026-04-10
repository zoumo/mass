# S02: agentd Core Adaptation — UAT

**Milestone:** M007
**Written:** 2026-04-09T21:00:14.867Z

## UAT: S02 — agentd Core Adaptation

### Preconditions

- `go build ./...` exits 0 (green build)
- `pkg/agentd` package is the sole modified package
- mockShimServer available in `pkg/agentd/shim_client_test.go`

---

### Test Case 1: stateChange creating→idle drives DB state update

**What it proves:** D088 boundary — shim (via runtime/stateChange notification) is the sole post-bootstrap DB state writer.

**Steps:**
1. Create a bbolt store and register an agent at `StatusCreating` in workspace `default`, name `sc-creating-idle`
2. Start a `mockShimServer` that queues a `runtime/stateChange` notification with `prev=creating, new=idle` to emit after Subscribe
3. Build a notification handler via `buildNotifHandler` and wire it to the mock shim's DialWithHandler
4. Trigger the Subscribe path; wait for the stateChange notification to be delivered
5. Read agent from store

**Expected:** Agent status in DB == `StatusIdle`; no direct `UpdateStatus(StatusRunning)` call observed

**Test:** `TestStateChange_CreatingToIdle_UpdatesDB` — **PASS** ✅

---

### Test Case 2: Two successive stateChange notifications drive both DB transitions

**What it proves:** Notification handler correctly processes multiple sequential stateChanges in the correct order.

**Steps:**
1. Create agent at `StatusIdle`
2. Mock shim emits `idle→running`, then `running→idle`
3. Both notifications are delivered through `buildNotifHandler`
4. Read agent from store after both

**Expected:** DB state == `StatusIdle` (final state); intermediate `StatusRunning` was also correctly applied

**Test:** `TestStateChange_RunningToIdle_UpdatesDB` — **PASS** ✅

---

### Test Case 3: Malformed stateChange params are dropped without panic

**What it proves:** The handler is defensive against malformed notifications from a buggy or adversarial shim.

**Steps:**
1. Mock shim emits a `runtime/stateChange` with invalid JSON params (array instead of object)
2. Notification handler receives the malformed payload

**Expected:** WARN log line `"stateChange: malformed notification dropped"` emitted; no panic; DB state unchanged

**Test:** `TestStateChange_MalformedParamsDropped` — **PASS** ✅

---

### Test Case 4: Start() does not write StatusRunning directly after Subscribe

**What it proves:** The direct `UpdateStatus(StatusRunning)` call has been removed from `Start()` step 9 (D088 enforcement).

**Steps:**
1. Create store with agent at `StatusCreating`
2. Do not emit any stateChange notification from mock shim
3. Verify DB state is NOT `StatusRunning` at any point without a stateChange arriving

**Expected:** DB remains at `StatusCreating` (no direct StatusRunning write); StatusIdle is only set when a stateChange notification arrives

**Test:** `TestStart_DoesNotWriteStatusRunning` — **PASS** ✅

---

### Test Case 5: tryReload attempts session/load with correct sessionId

**What it proves:** D089 — tryReload reads state.json and calls `session/load` with the persisted ACP sessionId.

**Steps:**
1. Create agent with `RestartPolicy = "tryReload"` and `ShimStateDir` pointing to a directory with a valid `state.json` containing `"id": "reload-session-abc123"`
2. Mock shim is configured to accept `session/load` (returns success)
3. Run `RecoverSessions`

**Expected:**
- `mockShimServer.loadCalled == true`
- `mockShimServer.loadCalledWith == "reload-session-abc123"`
- Log: `"tryReload: session/load succeeded"` with `session_id=reload-session-abc123`
- Agent appears in processes map (recovery succeeded)

**Test:** `TestRecovery_TryReload_AttemptsSessionLoad` — **PASS** ✅

---

### Test Case 6: tryReload falls back gracefully when session/load RPC fails

**What it proves:** tryReload does not propagate session/load errors — recovery always completes.

**Steps:**
1. Create agent with `RestartPolicy = "tryReload"` and valid state.json
2. Mock shim returns error `-32603 "runtime does not support session/load"` for session/load
3. Run `RecoverSessions`

**Expected:**
- Log: `"tryReload: session/load failed, continuing"` with agent_key and error fields
- `recoverAgent()` returns no error
- Agent appears in processes map

**Test:** `TestRecovery_TryReload_FallsBackOnLoadFailure` — **PASS** ✅

---

### Test Case 7: tryReload falls back when state.json is missing

**What it proves:** Missing/inaccessible state file does not cause a crash or unrecoverable error.

**Steps:**
1. Create agent with `RestartPolicy = "tryReload"` and `ShimStateDir = "/tmp/nonexistent-state-dir-tryreload-test"` (path does not exist)
2. Run `RecoverSessions`

**Expected:**
- Log: `"tryReload: could not read sessionId from state file, skipping"` with error field
- `recoverAgent()` returns no error
- No panic
- Agent appears in processes map

**Test:** `TestRecovery_TryReload_FallsBackOnMissingStateFile` — **PASS** ✅

---

### Test Case 8: alwaysNew (default) skips session/load entirely

**What it proves:** D089 — alwaysNew and empty RestartPolicy never issue session/load.

**Steps:**
1. Create agent with `RestartPolicy = ""` (empty / default = alwaysNew) or `RestartPolicy = "alwaysNew"`
2. Mock shim is instrumented to track session/load calls
3. Run `RecoverSessions`

**Expected:**
- `mockShimServer.loadCalled == false`
- No session/load RPC issued
- Agent recovers successfully

**Test:** `TestRecovery_AlwaysNew_SkipsSessionLoad` — **PASS** ✅

---

### Test Case 9: ShimClient.Load() success path

**What it proves:** The session/load RPC is correctly serialized and dispatched.

**Steps:**
1. Start a mock shim server that handles `session/load` and returns `{}`
2. Call `client.Load(ctx, "some-session-id")`

**Expected:** Returns nil; mock records the call with correct sessionId

**Test:** `TestShimClient_Load_Success` — **PASS** ✅

---

### Test Case 10: ShimClient.Load() error path

**What it proves:** RPC errors from session/load are surfaced as Go errors (not panics).

**Steps:**
1. Mock shim returns `{"error": {"code": -32603, "message": "not supported"}}` for session/load
2. Call `client.Load(ctx, "x")`

**Expected:** Returns non-nil error wrapping the RPC error message

**Test:** `TestShimClient_Load_RpcError` — **PASS** ✅

---

### Regression Check

Run full suite excluding pre-existing failure:
```
go test ./pkg/agentd/... -count=1 -timeout 60s 2>&1 | grep "FAIL:"
```
Expected output: only `TestProcessManagerStart` (pre-existing, requires real shim binary).

```
go build ./...
```
Expected: exit 0, no output.

