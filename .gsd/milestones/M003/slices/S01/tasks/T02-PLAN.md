---
estimated_steps: 9
estimated_files: 2
skills_used: []
---

# T02: Wire fail-closed guards into ARI handlers and surface recovery info in session/status

Implement the actual fail-closed behavior: when the daemon is in recovery phase, operational methods (session/prompt, session/cancel) are refused with a clear JSON-RPC error, while read-only methods (session/status, session/list, session/attach, session/detach) continue working. Also wire RecoveryInfo into the session/status response.

Steps:
1. Open `pkg/ari/server.go`, add a `recoveryGuard` helper that checks `processes.IsRecovering()` and returns a JSON-RPC error (code -32001, message 'daemon is recovering sessions, operational actions are blocked') if true
2. Call `recoveryGuard` at the top of `handleSessionPrompt` and `handleSessionCancel` — return early if it fires
3. Modify `handleSessionStatus` to include `RecoveryInfo` from the `ShimProcess` in the response when the session has recovery metadata
4. Do NOT guard `handleSessionStop` — stopping must always work for safety (an operator must be able to stop a session even during recovery)
5. Do NOT guard `handleSessionStatus`, `handleSessionList`, `handleSessionAttach`, `handleSessionDetach` — these are read-only inspection methods
6. Define a custom JSON-RPC error code constant for recovery-blocked errors
7. Verify existing test suite still passes with the guards in place (guards are no-ops when phase is idle)

## Inputs

- `pkg/ari/server.go`
- `pkg/ari/types.go`
- `pkg/agentd/recovery_posture.go`
- `pkg/agentd/process.go`

## Expected Output

- `pkg/ari/server.go`
- `pkg/ari/types.go`

## Verification

go test ./pkg/ari/... -count=1 -v 2>&1 | tail -20
