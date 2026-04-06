---
id: T02
parent: S05
milestone: M001-tlbeko
provides: []
requires: []
affects: []
key_files: ["pkg/ari/server.go", "pkg/ari/registry.go"]
key_decisions: ["Added github.com/google/uuid dependency for workspace ID generation"]
patterns_established: []
drill_down_paths: []
observability_surfaces: []
duration: ""
verification_result: "go build ./pkg/ari/... compiles without error — confirms all type definitions, imports, and method implementations are syntactically valid. The uuid dependency resolves correctly, workspace package imports work, and jsonrpc2 library usage matches the pattern from pkg/rpc/server.go."
completed_at: 2026-04-02T19:38:50.239Z
blocker_discovered: false
---

# T02: Created ARI JSON-RPC server with workspace/prepare, workspace/list, workspace/cleanup methods wired to WorkspaceManager

> Created ARI JSON-RPC server with workspace/prepare, workspace/list, workspace/cleanup methods wired to WorkspaceManager

## What Happened
---
id: T02
parent: S05
milestone: M001-tlbeko
key_files:
  - pkg/ari/server.go
  - pkg/ari/registry.go
key_decisions:
  - Added github.com/google/uuid dependency for workspace ID generation
duration: ""
verification_result: passed
completed_at: 2026-04-02T19:38:50.240Z
blocker_discovered: false
---

# T02: Created ARI JSON-RPC server with workspace/prepare, workspace/list, workspace/cleanup methods wired to WorkspaceManager

**Created ARI JSON-RPC server with workspace/prepare, workspace/list, workspace/cleanup methods wired to WorkspaceManager**

## What Happened

Created pkg/ari/registry.go with Registry struct implementing thread-safe workspaceId → WorkspaceMeta mapping. The Registry tracks Id, Name, Path, Spec, Status, RefCount, and Refs for each workspace. Implemented methods: Add, Get, List, Remove, Acquire, Release following the plan specification. The Acquire/Release methods track session references via a Refs list for debugging visibility.

Created pkg/ari/server.go with Server struct holding WorkspaceManager, Registry, baseDir, and socket path. Implemented Serve() to create Unix socket listener and accept loop, handleConn() to wrap connections in jsonrpc2.Conn using AsyncHandler pattern from pkg/rpc/server.go. The connHandler.Handle routes workspace/prepare, workspace/list, workspace/cleanup methods to appropriate handlers.

For workspace/prepare: generates UUID using github.com/google/uuid, creates targetDir under baseDir, calls manager.Prepare, adds to registry, returns WorkspacePrepareResult. Error handling returns InvalidParams with WorkspaceError Phase for prepare failures.

For workspace/list: calls registry.List() and converts WorkspaceMeta to WorkspaceInfo for the response. Accepts optional empty params.

For workspace/cleanup: validates workspaceId exists, checks RefCount > 0 (returns InternalError if refs active), calls manager.Cleanup, removes from registry. Error handling returns InternalError with WorkspaceError Phase for cleanup failures.

Added github.com/google/uuid v1.6.0 dependency via go get.

## Verification

go build ./pkg/ari/... compiles without error — confirms all type definitions, imports, and method implementations are syntactically valid. The uuid dependency resolves correctly, workspace package imports work, and jsonrpc2 library usage matches the pattern from pkg/rpc/server.go.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go get github.com/google/uuid` | 0 | ✅ pass | 2000ms |
| 2 | `go build ./pkg/ari/...` | 0 | ✅ pass | 1500ms |


## Deviations

None — executed exactly as planned. Registry Acquire/Release methods include sessionID parameter for Refs tracking, which enhances observability beyond the plan's RefCount-only specification.

## Known Issues

None.

## Files Created/Modified

- `pkg/ari/server.go`
- `pkg/ari/registry.go`


## Deviations
None — executed exactly as planned. Registry Acquire/Release methods include sessionID parameter for Refs tracking, which enhances observability beyond the plan's RefCount-only specification.

## Known Issues
None.
