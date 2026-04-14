---
estimated_steps: 24
estimated_files: 9
skills_used: []
---

# T03: Migrate remaining consumers, delete dead files, verify make build + go test

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

## Inputs

- ``api/ari/domain.go``
- ``api/ari/types.go``
- ``api/shim/types.go``
- ``pkg/ari/server/server.go``
- ``pkg/ari/server_test.go``
- ``pkg/store/agentrun.go``
- ``pkg/store/agent_test.go``
- ``pkg/store/agentrun_test.go``
- ``tests/integration/real_cli_test.go``
- ``pkg/runtime-spec/api/types.go``

## Expected Output

- ``api/ari/domain.go` — api.Status→apiruntime.Status, api.EnvVar→apiruntime.EnvVar; api import updated`
- ``api/ari/types.go` — api.EnvVar→apiruntime.EnvVar; api import updated`
- ``api/shim/types.go` — apiruntime target updated to pkg/runtime-spec/api`
- ``pkg/ari/server/server.go` — Status* references changed to apiruntime.*; api import removed if only Status was used`
- ``pkg/ari/server_test.go` — same`
- ``pkg/store/agentrun.go` — api.Status parameter type changed to apiruntime.Status`
- ``pkg/store/agent_test.go` — api.EnvVar changed to apiruntime.EnvVar`
- ``pkg/store/agentrun_test.go` — Status* changed to apiruntime.*`
- ``tests/integration/real_cli_test.go` — api.EnvVar changed to apiruntime.EnvVar`

## Verification

make build && go test ./... && rg '"github.com/zoumo/oar/api/runtime"' --type go && echo FAIL || echo 'PASS build+test' && test ! -d api/runtime && test ! -f api/types.go && test ! -f pkg/agentd/runtimeclass.go && echo 'PASS deletions'
