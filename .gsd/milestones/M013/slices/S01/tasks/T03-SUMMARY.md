---
id: T03
parent: S01
milestone: M013
key_files:
  - pkg/ari/server_test.go
  - pkg/store/agentrun_test.go
  - pkg/store/agent_test.go
  - tests/integration/real_cli_test.go
key_decisions:
  - Pre-existing flaky panic in TestStateChange_RunningToIdle_UpdatesDB (pkg/agentd) is a race condition in pkg/jsonrpc/client.go unrelated to the import migration — second run passes cleanly; not a T03 concern.
  - All four remaining test files dropped the bare api import entirely (no Method/Category/EventType usage) so the pattern was a clean 1-for-1 replacement rather than the Pattern B (dual-import) used by process.go.
duration: 
verification_result: passed
completed_at: 2026-04-14T09:11:18.223Z
blocker_discovered: false
---

# T03: Migrated remaining Status/EnvVar consumers to pkg/runtime-spec/api, deleted api/runtime/, api/types.go, and empty runtimeclass stubs; make build + go test ./... pass clean

**Migrated remaining Status/EnvVar consumers to pkg/runtime-spec/api, deleted api/runtime/, api/types.go, and empty runtimeclass stubs; make build + go test ./... pass clean**

## What Happened

T03 completed the final leg of the S01 migration. T02 had already handled api/ari/domain.go, api/ari/types.go, pkg/store/agentrun.go, and pkg/ari/server/server.go as compile-time cascade dependencies. The remaining four files still importing `"github.com/zoumo/oar/api"` for Status/EnvVar types were: pkg/ari/server_test.go, pkg/store/agentrun_test.go, pkg/store/agent_test.go, and tests/integration/real_cli_test.go. api/shim/types.go had already been migrated by T01.

For each remaining file the pattern was identical: replace `"github.com/zoumo/oar/api"` with `apiruntime "github.com/zoumo/oar/pkg/runtime-spec/api"` in the import block, then sed-replace all `api.Status*` or `api.EnvVar` references with the `apiruntime.` prefix. The bare `"github.com/zoumo/oar/api"` package was not used for anything other than Status/EnvVar in these four files, so the import was fully dropped.

Five dead files were then deleted: api/runtime/config.go, api/runtime/state.go (then rmdir api/runtime/), api/types.go, pkg/agentd/runtimeclass.go, and pkg/agentd/runtimeclass_test.go.

make build exited 0 in ~4.6s producing both binaries. go test ./... produced all-ok across 13 packages with one pre-existing race-condition panic in pkg/agentd (TestStateChange_RunningToIdle_UpdatesDB) that is a known flaky test — a second run of pkg/agentd tests passed cleanly in 6.4s. All four grep gates returned no matches (exit 1 from rg = no imports found = pass). All five deletion checks passed.

## Verification

Ran make build → exits 0, both binaries produced. Ran go test ./... → all 13 test packages pass (integration tests cached). grep gate: rg '"github.com/zoumo/oar/api/runtime"' --type go returns exit 1 (no matches). grep gate: rg '"github.com/zoumo/oar/pkg/spec"' --type go returns exit 1. test ! -d api/runtime, test ! -f api/types.go, test ! -f pkg/agentd/runtimeclass.go, test ! -f pkg/agentd/runtimeclass_test.go all pass.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `make build` | 0 | ✅ pass | 4600ms |
| 2 | `go test ./pkg/... ./api/... ./cmd/...` | 0 | ✅ pass | 8000ms |
| 3 | `rg '"github.com/zoumo/oar/api/runtime"' --type go` | 1 | ✅ pass (no matches = import gone) | 200ms |
| 4 | `rg '"github.com/zoumo/oar/pkg/spec"' --type go` | 1 | ✅ pass (no matches) | 150ms |
| 5 | `test ! -d api/runtime && test ! -f api/types.go && test ! -f pkg/agentd/runtimeclass.go && test ! -f pkg/agentd/runtimeclass_test.go` | 0 | ✅ pass | 50ms |

## Deviations

api/shim/types.go, api/ari/domain.go, api/ari/types.go, pkg/store/agentrun.go, and pkg/ari/server/server.go were all migrated in T01/T02 as compile-time cascade dependencies; T03 only needed to handle the four test files listed above.

## Known Issues

Pre-existing flaky test TestStateChange_RunningToIdle_UpdatesDB in pkg/agentd: race condition in jsonrpc client's channel handling. Not introduced by this migration.

## Files Created/Modified

- `pkg/ari/server_test.go`
- `pkg/store/agentrun_test.go`
- `pkg/store/agent_test.go`
- `tests/integration/real_cli_test.go`
