---
id: S05
parent: M001-tvc4z0
milestone: M001-tvc4z0
provides:
  - ShimClient for RPC communication with agent-shim
  - ProcessManager.Start for full session startup workflow
  - ProcessManager.Stop for graceful shutdown
  - ProcessManager.State for querying shim state
  - ProcessManager.Connect for direct RPC access
  - ShimProcess struct tracking running shim state
requires:
  - slice: S02
    provides: SQLite metadata store for session/workspace persistence
  - slice: S03
    provides: RuntimeClassRegistry for resolving runtime class names to launch configs
  - slice: S04
    provides: SessionManager for session CRUD and state machine
affects:
  - S06 (ARI Service) - depends on ProcessManager for session operations
key_files:
  - pkg/agentd/shim_client.go
  - pkg/agentd/shim_client_test.go
  - pkg/agentd/process.go
  - pkg/agentd/process_test.go
  - cmd/agent-shim/main.go
key_decisions:
  - Use jsonrpc2.AsyncHandler for event handling to process notifications in separate goroutines, avoiding blocking the main RPC connection
  - Event delivery order not guaranteed due to async handler; tests verify presence of expected events rather than strict ordering
  - Use shorter socket paths (/tmp) for tests to avoid macOS Unix socket path length limits (~107 chars)
  - Stop method calls Shutdown RPC first, then waits for process exit with 10s timeout before killing
  - State and Connect methods return errors if session is not in running state
  - Use net.Dial for Unix socket readiness check (not os.OpenFile which fails on sockets)
  - Shim main cancels context when RPC server exits to enable graceful shutdown via Shutdown RPC
  - watchProcess closes Done channel AFTER all cleanup to prevent race conditions
patterns_established:
  - ShimClient pattern: wrap jsonrpc2.Conn with typed methods for RPC communication
  - ProcessManager pattern: orchestrate session, runtime, bundle creation, process fork, socket wait, client connect, event subscribe in single Start method
  - Cleanup ordering: close Done channel last to signal all cleanup is complete
  - Socket readiness: use net.Dial to verify Unix socket is accepting connections
observability_surfaces:
  - ProcessManager logs: forking shim, session started, shim exited
  - ShimClient logs: RPC method calls and responses
  - Event channel: ShimProcess.Events delivers typed events to subscribers
drill_down_paths:
  - milestones/M001-tvc4z0/slices/S05/tasks/T01-SUMMARY.md
  - milestones/M001-tvc4z0/slices/S05/tasks/T03-SUMMARY.md
  - milestones/M001-tvc4z0/slices/S05/tasks/T04-SUMMARY.md
duration: ""
verification_result: passed
completed_at: 2026-04-06T14:55:06.506Z
blocker_discovered: false
---

# S05: Process Manager

**Process Manager starts shim process, connects to RPC socket, subscribes to events, and manages full lifecycle (Start/Stop/State/Connect); mockagent responds to prompts through the complete pipeline.**

## What Happened

This slice implemented the ProcessManager for managing agent-shim process lifecycle. The work involved creating ShimClient for RPC communication, implementing ProcessManager methods (Start, Stop, State, Connect), and fixing several critical bugs discovered during integration testing.

**ShimClient (T01):** Created ShimClient struct wrapping jsonrpc2.Conn for agent-shim RPC communication. Implemented Prompt, Cancel, Subscribe, GetState, Shutdown methods with "$/event" notification handling. Built comprehensive unit tests with mock JSON-RPC server, covering all RPC methods, event subscription, connection lifecycle, and concurrent calls. Key decision: use jsonrpc2.AsyncHandler for non-blocking event processing.

**ProcessManager Start (T02):** Implemented the full Start workflow: get Session → resolve RuntimeClass → generate config.json → create bundle directory with workspace symlink → fork agent-shim process → wait for socket → connect ShimClient → subscribe events → transition session state to "running". The ShimProcess struct tracks running shim state.

**ProcessManager Stop/State/Connect (T03):** Implemented Stop method (Shutdown RPC, wait for process exit with timeout, kill if needed), State method (GetState RPC), and Connect method (return ShimClient for direct RPC access). Added shimProcesses map with sync.RWMutex for concurrent access.

**Bug Fixes (T04):** Fixed three critical bugs discovered during integration testing:
1. **ACP handshake hang**: Moved cmd.Wait() to after handshake completes (per Go exec.Wait documentation) to avoid interfering with pipe reads.
2. **RPC socket detection**: Fixed waitForSocket to use net.Dial("unix", socketPath) instead of os.OpenFile (which fails with "operation not supported on socket" on macOS for Unix sockets).
3. **Shutdown race condition**: Fixed shim main to cancel the signal context when RPC server exits, enabling graceful shutdown via Shutdown RPC.
4. **Bundle cleanup race condition**: Fixed watchProcess to close shimProc.Done AFTER all cleanup (not before) to avoid race condition where test checks bundle directory before cleanup completes.

The TestProcessManagerStart integration test now passes, demonstrating the complete lifecycle: session created → shim started → ACP handshake → RPC connected → prompt sent → events received → shutdown → session stopped → bundle cleaned up.

## Verification

**ShimClient Unit Tests (11 tests passing):**
- TestShimClientDial, TestShimClientDialFail
- TestShimClientPrompt, TestShimClientCancel
- TestShimClientSubscribe
- TestShimClientGetState, TestShimClientShutdown, TestShimClientClose
- TestShimClientDisconnectNotify
- TestShimClientMultipleMethods, TestShimClientConcurrentCalls
- TestParseEvent (7 sub-tests for event type parsing)

**ProcessManager Integration Test:**
- TestProcessManagerStart passes (5.08s runtime)
- Demonstrates full lifecycle: created → running → stopped
- Verifies: shim process starts, socket connects, GetState works, Prompt returns response, events received, graceful shutdown, session state transitions, bundle cleanup

**Verification Commands:**
- `go test ./pkg/agentd/... -run ShimClient -v` → 11 tests pass
- `go test ./pkg/agentd/... -run TestProcessManagerStart -v` → PASS
- `go test ./pkg/... -v` → all tests pass

## Requirements Advanced

- R005 — Process Manager can fork agent-shim, connect to shim socket, subscribe to events, and manage process lifecycle (Start/Stop/State/Connect). Integration test demonstrates full lifecycle with mockagent.

## Requirements Validated

- R005 — TestProcessManagerStart passes: shim process starts, socket connects, GetState RPC works, Prompt RPC returns response, events received, graceful shutdown completes, session transitions created→running→stopped, bundle cleaned up

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

T02 had no summary recorded (executor task completion artifact missing), but implementation was verified through T03 and T04 testing.

T04 scope expanded beyond original plan - discovered and fixed multiple bugs:
1. ACP handshake hang (original blocker)
2. RPC socket detection issue (os.OpenFile vs net.Dial)
3. Shutdown race condition (shim main not exiting on Shutdown RPC)
4. Bundle cleanup race condition (Done channel closed before cleanup)

## Known Limitations

None. All functionality works as designed.

## Follow-ups

None. The slice goal is fully achieved.

## Files Created/Modified

- `pkg/agentd/shim_client.go` — ShimClient struct with Dial, Prompt, Cancel, Subscribe, GetState, Shutdown methods and event parsing
- `pkg/agentd/shim_client_test.go` — 11 unit tests for ShimClient with mock JSON-RPC server
- `pkg/agentd/process.go` — ProcessManager with Start, Stop, State, Connect methods; ShimProcess struct; watchProcess cleanup goroutine
- `pkg/agentd/process_test.go` — TestProcessManagerStart integration test
- `cmd/agent-shim/main.go` — Fixed shutdown: cancel context when RPC server exits for graceful Shutdown RPC handling
- `pkg/runtime/runtime.go` — Fixed cmd.Wait() timing: move to after ACP handshake to avoid pipe read interference
