---
estimated_steps: 31
estimated_files: 9
skills_used: []
---

# T02: Migrate pkg/agentd/* consumers

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

## Inputs

- ``pkg/agentd/agent.go``
- ``pkg/agentd/agent_test.go``
- ``pkg/agentd/recovery.go``
- ``pkg/agentd/process.go``
- ``pkg/agentd/process_test.go``
- ``pkg/agentd/recovery_test.go``
- ``pkg/agentd/recovery_posture_test.go``
- ``pkg/agentd/mock_shim_server_test.go``
- ``pkg/agentd/shim_boundary_test.go``
- ``pkg/runtime-spec/api/types.go``

## Expected Output

- ``pkg/agentd/agent.go` — api import replaced with apiruntime (pkg/runtime-spec/api); Status* references updated`
- ``pkg/agentd/agent_test.go` — same`
- ``pkg/agentd/recovery.go` — api import replaced; pkg/spec aliased to pkg/runtime-spec`
- ``pkg/agentd/process.go` — api kept for methods; apiruntime updated to pkg/runtime-spec/api; pkg/spec aliased; Status/EnvVar changed to apiruntime.*`
- ``pkg/agentd/process_test.go` — api import replaced with apiruntime; Status/EnvVar updated`
- ``pkg/agentd/recovery_test.go` — apiruntime target updated; pkg/spec aliased; Status* updated`
- ``pkg/agentd/recovery_posture_test.go` — apiruntime target updated; Status* updated`
- ``pkg/agentd/mock_shim_server_test.go` — apiruntime target updated; Status* updated`
- ``pkg/agentd/shim_boundary_test.go` — api kept for method constants; apiruntime added; Status* changed to apiruntime.*`

## Verification

go build ./pkg/agentd/... 2>&1 | head -20 && rg '"github.com/zoumo/oar/api/runtime"' pkg/agentd/ --type go && echo 'FAIL' || echo 'PASS'
