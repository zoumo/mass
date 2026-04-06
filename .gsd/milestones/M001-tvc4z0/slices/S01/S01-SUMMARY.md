---
id: S01
parent: M001-tvc4z0
milestone: M001-tvc4z0
provides:
  - Shim ExitCode field in State and GetStateResult for process exit status visibility
  - agentd daemon entry point with config parsing and ARI server bootstrap
  - Graceful shutdown handling for agentd daemon
requires:
  []
affects:
  - S02
  - S03
key_files:
  - pkg/spec/state_types.go
  - pkg/runtime/runtime.go
  - pkg/rpc/server.go
  - pkg/agentd/config.go
  - cmd/agentd/main.go
key_decisions:
  - ExitCode is optional (*int) because it's only populated after process exits, not during running state
  - Socket file removed before listening to handle unclean shutdown recovery from previous daemon crashes
  - SIGTERM/SIGINT handled with graceful shutdown via srv.Shutdown() to complete in-flight requests
patterns_established:
  - Optional pointer fields (*int) with omitempty for state that's only populated after lifecycle events (ExitCode)
  - Socket file removal before Listen() for Unix domain socket daemon unclean shutdown recovery
  - Signal-based graceful shutdown pattern for JSON-RPC servers (SIGTERM/SIGINT → srv.Shutdown())
observability_surfaces:
  - none
drill_down_paths:
  - milestones/M001-tvc4z0/slices/S01/tasks/T01-SUMMARY.md
  - milestones/M001-tvc4z0/slices/S01/tasks/T02-SUMMARY.md
duration: ""
verification_result: passed
completed_at: 2026-04-03T01:17:36.061Z
blocker_discovered: false
---

# S01: Scaffolding + Phase 1.3 exitCode

**Added ExitCode to shim State/GetStateResult and created agentd daemon scaffolding with YAML config parsing and ARI server bootstrap with graceful shutdown**

## What Happened

This slice established the foundation for the agentd daemon and completed Phase 1.3 exitCode requirement from the shim spec.

**Task T01** added the ExitCode field to shim state management. The ExitCode is stored as `*int` (pointer) in the State struct because it's only meaningful after the agent process exits — nil indicates the process is still running, while a non-nil value (including zero) indicates the process has exited with that code. The background goroutine in runtime.go that waits for process exit now captures the exit code via `cmd.ProcessState.ExitCode()` and includes it in the stopped state. The GetStateResult struct was updated to expose ExitCode to RPC clients.

**Task T02** created the agentd daemon scaffolding. The Config struct in pkg/agentd/config.go parses YAML configuration with fields for Socket (ARI Unix socket path), WorkspaceRoot (workspace directory root), MetaDB (SQLite database path), and placeholder structs for Runtime, SessionPolicy, and RuntimeClasses for future extensibility. ParseConfig validates required fields after YAML parsing. The daemon entry point in cmd/agentd/main.go initializes workspace manager, registry, and ARI server, removes any existing socket file (unclean shutdown recovery), starts the server, and handles SIGTERM/SIGINT for graceful shutdown.

Both tasks completed without deviations. All existing tests pass (22 tests for pkg/spec, pkg/runtime, pkg/rpc). The daemon successfully starts with a minimal config, listens on the socket, and shuts down cleanly on signal.

## Verification

Slice-level verification performed:

1. **Build verification:** `go build -o bin/agentd ./cmd/agentd` — successful, binary created
2. **Test verification:** `go test ./pkg/spec/... ./pkg/runtime/... ./pkg/rpc/... -v` — all 22 tests pass
3. **Daemon startup verification:** Started daemon with minimal config.yaml containing socket/workspaceRoot/metaDB fields — logs show successful initialization sequence: config loaded → workspace manager initialized → registry initialized → ARI server created → listening on socket
4. **Graceful shutdown verification:** Daemon received SIGTERM (via timeout) and logged "received signal terminated, shutting down" followed by "shutdown complete"
5. **Socket cleanup verification:** Existing socket file would be removed before listening (verified in code: `os.Remove(config.Socket)`)

All slice goals achieved: ExitCode added to shim State/GetStateResult, agentd daemon scaffolding created with config parsing and ARI server bootstrap.

## Requirements Advanced

- R001 — Daemon starts with config.yaml, parses required fields, initializes workspace manager and registry, listens on ARI socket, handles graceful shutdown. Build successful, daemon startup verified with test config.

## Requirements Validated

- R001 — Daemon starts successfully with minimal config.yaml (socket, workspaceRoot, metaDB fields), initializes workspace manager and registry, creates ARI server, listens on Unix socket, and handles SIGTERM graceful shutdown. Verified with: bin/agentd --config /tmp/agentd-test/config.yaml — logs show successful startup and shutdown sequence.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

None. Both tasks followed their plans exactly.

## Known Limitations

None.

## Follow-ups

None.

## Files Created/Modified

- `pkg/spec/state_types.go` — Added ExitCode *int field with omitempty JSON tag to State struct
- `pkg/runtime/runtime.go` — Modified background goroutine to capture exit code via cmd.ProcessState.ExitCode() and include in stopped state WriteState
- `pkg/rpc/server.go` — Added ExitCode *int field to GetStateResult and populated from st.ExitCode in handleGetState
- `pkg/agentd/config.go` — Created Config struct with Socket, WorkspaceRoot, MetaDB fields and ParseConfig function for YAML parsing
- `cmd/agentd/main.go` — Created daemon entry point with config flag, workspace manager/ARI server bootstrap, signal handling, and graceful shutdown
