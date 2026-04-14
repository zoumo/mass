# S01: Runtime-spec consumer migration

**Goal:** Migrate all consumers of api/runtime and api (for Status/EnvVar) to pkg/runtime-spec/api; fix the broken pkg/spec→pkg/runtime-spec imports; delete api/runtime/, api/types.go, and the two empty runtimeclass stubs. Result: make build + go test ./... pass with none of the deleted files referenced.
**Demo:** After this: make build + go test ./... pass with no imports of api/runtime or api (for Status/EnvVar); api/runtime/ and api/types.go deleted; empty runtimeclass stub files deleted.

## Must-Haves

- `make build` exits 0 with no api/runtime or api (Status/EnvVar) imports; `go test ./...` passes; `rg '"github.com/zoumo/oar/api/runtime"' --type go` returns zero matches; `rg '"github.com/zoumo/oar/pkg/spec"' --type go` returns zero matches; `api/runtime/`, `api/types.go`, `pkg/agentd/runtimeclass.go`, `pkg/agentd/runtimeclass_test.go` do not exist.

## Proof Level

- This slice proves: Contract — make build + go test ./... must pass cleanly; grep gates confirm no dead import paths remain.

## Integration Closure

S01 owns only import-path rewrites with no behavioral changes. After S01, the api/ directory still contains api/ari/, api/shim/, api/events.go, api/methods.go — those are migrated in S02/S03. pkg/runtime/ and cmd/ binaries continue to exist and work. The broken build (pkg/spec path) is resolved here, unblocking go test ./... for all packages.

## Verification

- Not provided.

## Tasks

- [x] **T01: Migrate pkg/runtime/* and cmd/agentd/subcommands/shim/command.go** `est:30 min`
  Fix the broken build caused by the pkg/spec→pkg/runtime-spec rename, then migrate all api/runtime imports in the pkg/runtime/ package and the shim subcommand.

Import alias strategy (apply consistently across all five files):
- `apiruntime "github.com/zoumo/oar/api/runtime"` → `apiruntime "github.com/zoumo/oar/pkg/runtime-spec/api"` (keep alias, change path; all existing `apiruntime.*` calls compile unchanged)
- `"github.com/zoumo/oar/pkg/spec"` → `spec "github.com/zoumo/oar/pkg/runtime-spec"` (add alias `spec`; all existing `spec.*` calls compile unchanged)
- `"github.com/zoumo/oar/api"` used for `api.Status*` only → remove the bare `api` import and add `apiruntime` (which now points to pkg/runtime-spec/api and contains Status/EnvVar); change `api.StatusXxx` → `apiruntime.StatusXxx`, `api.Status(...)` → `apiruntime.Status(...)`

### Steps
1. Edit `pkg/runtime/client.go`: change `apiruntime "github.com/zoumo/oar/api/runtime"` → `apiruntime "github.com/zoumo/oar/pkg/runtime-spec/api"`. No other changes needed.
2. Edit `pkg/runtime/runtime.go`: (a) change `apiruntime` target to `pkg/runtime-spec/api`; (b) change `"github.com/zoumo/oar/pkg/spec"` → `spec "github.com/zoumo/oar/pkg/runtime-spec"`; (c) remove `"github.com/zoumo/oar/api"` import; (d) change all bare `api.Status*` / `api.Status(...)` usages → `apiruntime.Status*` / `apiruntime.Status(...)`.
3. Edit `pkg/runtime/client_test.go`: (a) change `apiruntime` target to `pkg/runtime-spec/api`; (b) remove `"github.com/zoumo/oar/api"` (only used for Status*); (c) change `api.Status*` → `apiruntime.Status*`.
4. Edit `pkg/runtime/runtime_test.go`: (a) change `apiruntime` target; (b) remove `"github.com/zoumo/oar/api"` (only used for Status*); (c) change `api.Status*` → `apiruntime.Status*`.
5. Edit `cmd/agentd/subcommands/shim/command.go`: (a) change `apiruntime` target; (b) change pkg/spec → `spec "github.com/zoumo/oar/pkg/runtime-spec"`. (No bare api.Status usage in this file.)
6. Run `go build ./pkg/runtime/... ./cmd/agentd/...` to confirm this task compiles.
  - Files: `pkg/runtime/runtime.go`, `pkg/runtime/client.go`, `pkg/runtime/runtime_test.go`, `pkg/runtime/client_test.go`, `cmd/agentd/subcommands/shim/command.go`
  - Verify: go build ./pkg/runtime/... ./cmd/agentd/... 2>&1 | head -20 && rg '"github.com/zoumo/oar/api/runtime"' pkg/runtime/ cmd/agentd/ --type go && echo 'FAIL: still has old import' || echo 'PASS: no old imports'

- [x] **T02: Migrate pkg/agentd/* consumers** `est:45 min`
  Update all nine pkg/agentd/ files that import api/runtime, api (Status/EnvVar), or pkg/spec.

Two import patterns apply across these nine files:

**Pattern A — file uses Status/EnvVar from `api` but NOT Method*/EventType* constants:**
  - Remove `"github.com/zoumo/oar/api"` import
  - Add `apiruntime "github.com/zoumo/oar/pkg/runtime-spec/api"` if not already present
  - Change `api.Status` / `api.StatusXxx` → `apiruntime.Status` / `apiruntime.StatusXxx`
  - Change `api.EnvVar` → `apiruntime.EnvVar`
  Files: agent.go, agent_test.go, recovery.go, process_test.go, recovery_test.go, recovery_posture_test.go, mock_shim_server_test.go

**Pattern B — file uses BOTH Status/EnvVar AND Method*/EventType* from `api`:**
  - KEEP `"github.com/zoumo/oar/api"` for Method*/Category*/EventType* constants
  - Add `apiruntime "github.com/zoumo/oar/pkg/runtime-spec/api"` alongside the existing api import
  - Change `api.Status` / `api.StatusXxx` / `api.EnvVar` → `apiruntime.*`
  Files: process.go (uses api.MethodShimEvent, api.CategoryRuntime, api.EventTypeStateChange), shim_boundary_test.go (uses api.MethodShimEvent)

**Additional pkg/spec fix:**
  Files with `"github.com/zoumo/oar/pkg/spec"` import: process.go, recovery.go, recovery_test.go
  Change to: `spec "github.com/zoumo/oar/pkg/runtime-spec"`
  No caller-side changes needed (the alias keeps `spec.ParseConfig(...)` working).

**Additional api/runtime fix:**
  Files with `apiruntime "github.com/zoumo/oar/api/runtime"`: process.go, recovery_test.go, recovery_posture_test.go, mock_shim_server_test.go
  Change `apiruntime` target to `"github.com/zoumo/oar/pkg/runtime-spec/api"`.

### Steps
1. Edit `pkg/agentd/agent.go`: Pattern A — replace `"github.com/zoumo/oar/api"` with `apiruntime "github.com/zoumo/oar/pkg/runtime-spec/api"`; change `api.Status*` → `apiruntime.Status*`.
2. Edit `pkg/agentd/agent_test.go`: same Pattern A transformation.
3. Edit `pkg/agentd/recovery.go`: Pattern A for api.Status*, plus fix pkg/spec alias.
4. Edit `pkg/agentd/process.go`: Pattern B — keep api import; add apiruntime import; change apiruntime target; fix pkg/spec alias; change `api.Status*`/`api.EnvVar` → `apiruntime.*`.
5. Edit `pkg/agentd/process_test.go`: Pattern A — replace api with apiruntime; change Status/EnvVar references.
6. Edit `pkg/agentd/recovery_test.go`: change apiruntime target; fix pkg/spec; Pattern A for api.Status*.
7. Edit `pkg/agentd/recovery_posture_test.go`: change apiruntime target; Pattern A for api.Status*.
8. Edit `pkg/agentd/mock_shim_server_test.go`: change apiruntime target; Pattern A for api.Status*.
9. Edit `pkg/agentd/shim_boundary_test.go`: Pattern B — keep api for Method/Category/EventType; add apiruntime; change api.Status* → apiruntime.Status*.
10. Run `go build ./pkg/agentd/...` to confirm this batch compiles.
  - Files: `pkg/agentd/agent.go`, `pkg/agentd/agent_test.go`, `pkg/agentd/recovery.go`, `pkg/agentd/process.go`, `pkg/agentd/process_test.go`, `pkg/agentd/recovery_test.go`, `pkg/agentd/recovery_posture_test.go`, `pkg/agentd/mock_shim_server_test.go`, `pkg/agentd/shim_boundary_test.go`
  - Verify: go build ./pkg/agentd/... 2>&1 | head -20 && rg '"github.com/zoumo/oar/api/runtime"' pkg/agentd/ --type go && echo 'FAIL' || echo 'PASS'

- [ ] **T03: Migrate remaining consumers, delete dead files, verify make build + go test** `est:45 min`
  Complete the migration for the remaining packages (api/ari/, api/shim/, pkg/ari/server/, pkg/store/, tests/integration/), delete the five dead files, and verify the full build + test suite passes.

### Files to update (Status/EnvVar migration)

**api/ari/domain.go** and **api/ari/types.go**: These files live at api/ari/ until S02. They import `"github.com/zoumo/oar/api"` for `api.Status` and `api.EnvVar`. S01 must update them in-place because we are deleting `api/types.go`. Add `apiruntime "github.com/zoumo/oar/pkg/runtime-spec/api"` import, change `api.Status` → `apiruntime.Status`, `api.EnvVar` → `apiruntime.EnvVar`, keep all other api-package usage (if any).

**api/shim/types.go**: imports `apiruntime "github.com/zoumo/oar/api/runtime"`. Change apiruntime target to `"github.com/zoumo/oar/pkg/runtime-spec/api"`. (Leaves `pkg/events` import in place — that's S03 scope.)

**pkg/ari/server/server.go**: imports `"github.com/zoumo/oar/api"` for Status. Add `apiruntime "github.com/zoumo/oar/pkg/runtime-spec/api"`; change `api.Status*` → `apiruntime.Status*`; keep api import for method/shim constants if present (check and remove if only Status was used).

**pkg/ari/server_test.go**: same pattern — add apiruntime, change Status* refs, drop api if no other usage.

**pkg/store/agentrun.go**: add apiruntime, change `api.Status` function parameter/return.

**pkg/store/agent_test.go**: add apiruntime, change `api.EnvVar{...}` → `apiruntime.EnvVar{...}`.

**pkg/store/agentrun_test.go**: add apiruntime, change Status* refs.

**tests/integration/real_cli_test.go**: add apiruntime, change `api.EnvVar{...}` → `apiruntime.EnvVar{...}`.

### Files to delete
- `api/runtime/config.go` and `api/runtime/state.go` (then `rmdir api/runtime/`)
- `api/types.go`
- `pkg/agentd/runtimeclass.go` (empty stub — only package declaration)
- `pkg/agentd/runtimeclass_test.go` (empty stub — only package declaration)

### Verification steps
1. After all updates and deletions, run `make build`.
2. Run `go test ./...`.
3. Run grep gates:
   - `rg '"github.com/zoumo/oar/api/runtime"' --type go` → must return 0 matches
   - `rg '"github.com/zoumo/oar/pkg/spec"' --type go` → must return 0 matches
   - Check `api/runtime/` does not exist: `test ! -d api/runtime`
   - Check `api/types.go` does not exist: `test ! -f api/types.go`
   - Check stubs gone: `test ! -f pkg/agentd/runtimeclass.go && test ! -f pkg/agentd/runtimeclass_test.go`
  - Files: `api/ari/domain.go`, `api/ari/types.go`, `api/shim/types.go`, `pkg/ari/server/server.go`, `pkg/ari/server_test.go`, `pkg/store/agentrun.go`, `pkg/store/agent_test.go`, `pkg/store/agentrun_test.go`, `tests/integration/real_cli_test.go`
  - Verify: make build && go test ./... && rg '"github.com/zoumo/oar/api/runtime"' --type go && echo FAIL || echo 'PASS build+test' && test ! -d api/runtime && test ! -f api/types.go && test ! -f pkg/agentd/runtimeclass.go && echo 'PASS deletions'

## Files Likely Touched

- pkg/runtime/runtime.go
- pkg/runtime/client.go
- pkg/runtime/runtime_test.go
- pkg/runtime/client_test.go
- cmd/agentd/subcommands/shim/command.go
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
- api/shim/types.go
- pkg/ari/server/server.go
- pkg/ari/server_test.go
- pkg/store/agentrun.go
- pkg/store/agent_test.go
- pkg/store/agentrun_test.go
- tests/integration/real_cli_test.go
