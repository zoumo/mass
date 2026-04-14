---
id: T02
parent: S01
milestone: M013
key_files:
  - pkg/agentd/agent.go
  - pkg/agentd/agent_test.go
  - pkg/agentd/recovery.go
  - pkg/agentd/process.go
  - pkg/agentd/process_test.go
  - pkg/agentd/recovery_test.go
  - pkg/agentd/recovery_posture_test.go
  - pkg/agentd/mock_shim_server_test.go
  - pkg/agentd/shim_boundary_test.go
  - api/ari/domain.go
  - api/ari/types.go
  - pkg/store/agentrun.go
  - pkg/ari/server/server.go
key_decisions:
  - api/ari/domain.go, api/ari/types.go, pkg/store/agentrun.go, and pkg/ari/server/server.go were updated ahead of their T03 schedule because they are compile-time dependencies of pkg/agentd — the type unification from api.Status (old) to apiruntime.Status (pkg/runtime-spec/api) cascades through the entire type graph. Adding casts at every boundary was rejected in favour of a clean type migration.
  - Pattern B files (process.go, shim_boundary_test.go) keep the bare 'api' import for Method/Category/EventType constants and add a separate apiruntime import for Status/EnvVar types.
duration: 
verification_result: passed
completed_at: 2026-04-14T09:01:01.518Z
blocker_discovered: false
---

# T02: Migrated all pkg/agentd/* files from api/runtime → pkg/runtime-spec/api and api (Status/EnvVar) → apiruntime; also updated api/ari/domain.go, api/ari/types.go, pkg/store/agentrun.go, and pkg/ari/server/server.go to unify the Status type

**Migrated all pkg/agentd/* files from api/runtime → pkg/runtime-spec/api and api (Status/EnvVar) → apiruntime; also updated api/ari/domain.go, api/ari/types.go, pkg/store/agentrun.go, and pkg/ari/server/server.go to unify the Status type**

## What Happened

T02 migrated the nine pkg/agentd/ files as planned, applying Pattern A (replace api import with apiruntime) and Pattern B (keep api for Method/Category/EventType, add apiruntime alongside). All api/runtime → pkg/runtime-spec/api and pkg/spec → pkg/runtime-spec aliases were applied.

A compile-time blocker emerged: apiari.AgentRunStatus.State is typed as api.Status (old), but agentd code was now using apiruntime.Status (new). These are distinct Go named types even though both are string underneath. To resolve the type mismatch without adding casts everywhere, four additional files needed updating as cascade dependencies: api/ari/domain.go, api/ari/types.go, pkg/store/agentrun.go, and pkg/ari/server/server.go. These are technically T03 scope but are required compile-time dependencies of pkg/agentd — same pattern as T01 pulling in api/shim/types.go early.

After those four files were updated, make build exited cleanly and go test ./pkg/agentd/... passed in 6.9s.

## Verification

Ran `go build ./pkg/agentd/... 2>&1 | head -20 && rg '"github.com/zoumo/oar/api/runtime"' pkg/agentd/ --type go && echo FAIL || echo PASS` → printed PASS. Ran `make build` → exits 0 with both binaries built. Ran `go test ./pkg/agentd/...` → ok in 6.938s.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go build ./pkg/agentd/... 2>&1 | head -20 && rg '"github.com/zoumo/oar/api/runtime"' pkg/agentd/ --type go && echo FAIL || echo PASS` | 0 | ✅ pass | 3200ms |
| 2 | `make build` | 0 | ✅ pass | 8500ms |
| 3 | `go test ./pkg/agentd/...` | 0 | ✅ pass | 6938ms |

## Deviations

api/ari/domain.go, api/ari/types.go, pkg/store/agentrun.go, and pkg/ari/server/server.go were migrated in T02 (T03 scope) because they are required compile-time dependencies of pkg/agentd.

## Known Issues

T03 still needs to handle the remaining consumers (cmd/agentdctl/*, pkg/events/*, pkg/shim/server/service.go, pkg/store agent_test + agentrun_test, tests/integration), delete api/runtime/, api/types.go, and the runtimeclass stubs.

## Files Created/Modified

- `pkg/agentd/agent.go`
- `pkg/agentd/agent_test.go`
- `pkg/agentd/recovery.go`
- `pkg/agentd/process.go`
- `pkg/agentd/process_test.go`
- `pkg/agentd/recovery_test.go`
- `pkg/agentd/recovery_posture_test.go`
- `pkg/agentd/mock_shim_server_test.go`
- `pkg/agentd/shim_boundary_test.go`
- `api/ari/domain.go`
- `api/ari/types.go`
- `pkg/store/agentrun.go`
- `pkg/ari/server/server.go`
