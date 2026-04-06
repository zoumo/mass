---
id: T02
parent: S06
milestone: M001-tvc4z0
provides: []
requires: []
affects: []
key_files: ["pkg/ari/server.go", "cmd/agentd/main.go", "pkg/ari/server_test.go"]
key_decisions: ["session/prompt auto-starts with 10s timeout for Start operation and 30s timeout for Prompt RPC", "session/list does not filter by labels because meta.SessionFilter lacks Labels field (future enhancement)", "main.go requires metadata store (metaDB) for session management - fails fast if not configured"]
patterns_established: []
drill_down_paths: []
observability_surfaces: []
duration: ""
verification_result: "go build ./... passes with no errors. go test ./pkg/ari/... passes all 17 tests including workspace tests. go test ./pkg/agentd/... passes all agentd tests. All must-haves verified: Server struct has new fields, New() accepts dependencies, Handle() dispatches all 9 session methods, session/prompt auto-starts, session/remove returns InvalidParams for ErrDeleteProtected."
completed_at: 2026-04-06T15:19:55.158Z
blocker_discovered: false
---

# T02: Extended ARI Server struct with session management dependencies and implemented 9 session/* method handlers following the existing workspace handler pattern

> Extended ARI Server struct with session management dependencies and implemented 9 session/* method handlers following the existing workspace handler pattern

## What Happened
---
id: T02
parent: S06
milestone: M001-tvc4z0
key_files:
  - pkg/ari/server.go
  - cmd/agentd/main.go
  - pkg/ari/server_test.go
key_decisions:
  - session/prompt auto-starts with 10s timeout for Start operation and 30s timeout for Prompt RPC
  - session/list does not filter by labels because meta.SessionFilter lacks Labels field (future enhancement)
  - main.go requires metadata store (metaDB) for session management - fails fast if not configured
duration: ""
verification_result: passed
completed_at: 2026-04-06T15:19:55.160Z
blocker_discovered: false
---

# T02: Extended ARI Server struct with session management dependencies and implemented 9 session/* method handlers following the existing workspace handler pattern

**Extended ARI Server struct with session management dependencies and implemented 9 session/* method handlers following the existing workspace handler pattern**

## What Happened

Extended the ARI JSON-RPC server to support session lifecycle methods:

1. Extended Server struct with 4 new fields: sessions (*agentd.SessionManager), processes (*agentd.ProcessManager), runtimeClasses (*agentd.RuntimeClassRegistry), config (agentd.Config)

2. Updated New() signature to accept all new dependencies

3. Implemented 9 session/* handlers following the unmarshal → call → reply pattern:
   - session/new: Creates session with generated UUID, initial state "created"
   - session/prompt: Auto-starts session if state=="created", connects to shim, calls Prompt RPC with 30s timeout
   - session/cancel: Connects to shim, calls Cancel RPC
   - session/stop: Calls ProcessManager.Stop for graceful shutdown
   - session/remove: Calls SessionManager.Delete, returns InvalidParams for ErrDeleteProtected
   - session/list: Lists all sessions (label filtering not supported)
   - session/status: Returns session info, populates shim state if running
   - session/attach: Returns shim RPC socket path for direct communication
   - session/detach: Placeholder with nil result

4. Updated cmd/agentd/main.go to create and pass all dependencies to ari.New()

5. Updated test harness in server_test.go to create temp database and all dependencies

## Verification

go build ./... passes with no errors. go test ./pkg/ari/... passes all 17 tests including workspace tests. go test ./pkg/agentd/... passes all agentd tests. All must-haves verified: Server struct has new fields, New() accepts dependencies, Handle() dispatches all 9 session methods, session/prompt auto-starts, session/remove returns InvalidParams for ErrDeleteProtected.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go build ./...` | 0 | ✅ pass | 2000ms |
| 2 | `go test ./pkg/ari/... -run TestARIWorkspacePrepare -v` | 0 | ✅ pass | 3209ms |
| 3 | `go test ./pkg/ari/... -v` | 0 | ✅ pass | 2615ms |
| 4 | `go test ./pkg/agentd/... -v` | 0 | ✅ pass | 6727ms |


## Deviations

Label filtering removed from session/list because meta.SessionFilter doesn't have a Labels field. Future enhancement: add Labels field to meta.SessionFilter.

## Known Issues

session/list does not support label filtering. The meta.SessionFilter struct only supports State, WorkspaceID, Room, and HasRoom filters.

## Files Created/Modified

- `pkg/ari/server.go`
- `cmd/agentd/main.go`
- `pkg/ari/server_test.go`


## Deviations
Label filtering removed from session/list because meta.SessionFilter doesn't have a Labels field. Future enhancement: add Labels field to meta.SessionFilter.

## Known Issues
session/list does not support label filtering. The meta.SessionFilter struct only supports State, WorkspaceID, Room, and HasRoom filters.
