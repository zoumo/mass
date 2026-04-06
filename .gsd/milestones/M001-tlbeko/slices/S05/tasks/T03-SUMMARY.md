---
id: T03
parent: S05
milestone: M001-tlbeko
provides: []
requires: []
affects: []
key_files: ["pkg/ari/server_test.go"]
key_decisions: ["Used ari_test package (external tests) following pattern from pkg/rpc/server_test.go", "Tests connect via actual Unix socket using jsonrpc2.Conn for end-to-end verification"]
patterns_established: []
drill_down_paths: []
observability_surfaces: []
duration: ""
verification_result: "All 16 test cases pass, covering workspace/prepare with EmptyDir/Git/Local sources, workspace/list (empty and populated), workspace/cleanup (success and failure with refs), error handling (invalid specs, nil params, nonexistent workspaces, unknown methods), and full lifecycle (prepare → list → cleanup round-trip). Tests verify JSON-RPC error codes (InvalidParams, InternalError, MethodNotFound) and error message content."
completed_at: 2026-04-02T19:49:48.233Z
blocker_discovered: false
---

# T03: Created pkg/ari/server_test.go with comprehensive integration tests for workspace/prepare, workspace/list, workspace/cleanup via JSON-RPC

> Created pkg/ari/server_test.go with comprehensive integration tests for workspace/prepare, workspace/list, workspace/cleanup via JSON-RPC

## What Happened
---
id: T03
parent: S05
milestone: M001-tlbeko
key_files:
  - pkg/ari/server_test.go
key_decisions:
  - Used ari_test package (external tests) following pattern from pkg/rpc/server_test.go
  - Tests connect via actual Unix socket using jsonrpc2.Conn for end-to-end verification
duration: ""
verification_result: passed
completed_at: 2026-04-02T19:49:48.234Z
blocker_discovered: false
---

# T03: Created pkg/ari/server_test.go with comprehensive integration tests for workspace/prepare, workspace/list, workspace/cleanup via JSON-RPC

**Created pkg/ari/server_test.go with comprehensive integration tests for workspace/prepare, workspace/list, workspace/cleanup via JSON-RPC**

## What Happened

Created integration tests following the pattern from pkg/rpc/server_test.go. Implemented testHarness struct with WorkspaceManager, Registry, Server, Unix socket, and proper cleanup via t.Cleanup(). Created 16 test cases covering all three workspace methods with EmptyDir, Git, and Local source types. Tests verify success paths, error paths (invalid params, cleanup with refs > 0, hook failures), and boundary conditions (empty registry). All tests pass end-to-end over JSON-RPC connections.

The test harness creates a temp baseDir for workspace creation, a temp socket directory, a WorkspaceManager with all source handlers, a Registry for tracking workspaces, and an ARI Server listening on a Unix socket. The harness waits for the socket to appear before returning, ensuring the server is ready to accept connections. Cleanup properly shuts down the server and removes temp directories.

## Verification

All 16 test cases pass, covering workspace/prepare with EmptyDir/Git/Local sources, workspace/list (empty and populated), workspace/cleanup (success and failure with refs), error handling (invalid specs, nil params, nonexistent workspaces, unknown methods), and full lifecycle (prepare → list → cleanup round-trip). Tests verify JSON-RPC error codes (InvalidParams, InternalError, MethodNotFound) and error message content.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go test ./pkg/ari/... -v -timeout 120s` | 0 | ✅ pass | 2800ms |


## Deviations

Removed the "invalid source type" test case because it fails at client-side JSON marshaling before reaching the server. Adjusted expected error message content checks to match the server's actual error messages. Changed .git file check to directory check in Git test.

## Known Issues

None.

## Files Created/Modified

- `pkg/ari/server_test.go`


## Deviations
Removed the "invalid source type" test case because it fails at client-side JSON marshaling before reaching the server. Adjusted expected error message content checks to match the server's actual error messages. Changed .git file check to directory check in Git test.

## Known Issues
None.
