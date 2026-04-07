---
id: T04
parent: S05
milestone: M001-tvc4z0
key_files:
  - pkg/runtime/runtime.go
key_decisions:
  - Move cmd.Wait() to after ACP handshake completes to avoid interfering with pipe reads (per Go exec.Wait documentation)
duration: 
verification_result: mixed
completed_at: 2026-04-06T14:35:22.461Z
blocker_discovered: true
---

# T04: Fixed ACP handshake hang by moving cmd.Wait() after handshake completes; RPC socket creation issue remains

**Fixed ACP handshake hang by moving cmd.Wait() after handshake completes; RPC socket creation issue remains**

## What Happened

Investigated the ACP NewSession hang that was preventing TestProcessManagerStart from passing. Through extensive debugging with logging in both shim and mockagent, I identified the root cause: In runtime.Create(), a background goroutine was calling cmd.Wait() immediately after cmd.Start(), which interferes with pipe reads during the ACP handshake (per Go's exec.Wait documentation). Fixed by moving cmd.Wait() to after the handshake completes. The handshake now succeeds (Initialize + NewSession complete, created state written with PID), but discovered a new issue: the RPC socket is not being created, causing the test to fail with "socket not ready after 5s".

## Verification

ShimClient tests pass (11/11). ProcessManager test fails with "socket not ready after 5s" due to RPC socket not being created. Manual shim execution shows handshake completes successfully.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go build ./...` | 0 | ✅ pass | 500ms |
| 2 | `go test ./pkg/agentd/... -run ShimClient -v` | 0 | ✅ pass | 1400ms |
| 3 | `go test ./pkg/agentd/... -run TestProcessManagerStart -v` | 1 | ❌ fail | 5100ms |

## Deviations

Task scope expanded beyond just fixing the ACP hang - discovered a new issue with RPC socket creation that needs further investigation.

## Known Issues

RPC Socket Not Created: After the ACP handshake completes successfully, the shim's RPC server does not create the Unix socket. The Serve() function is called in a goroutine, but the socket never appears. This causes TestProcessManagerStart to fail with 'socket not ready after 5s'. This is a separate issue from the original ACP handshake hang and requires additional investigation.

## Files Created/Modified

- `pkg/runtime/runtime.go`
