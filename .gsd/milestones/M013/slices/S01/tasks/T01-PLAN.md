---
estimated_steps: 12
estimated_files: 5
skills_used: []
---

# T01: Migrate pkg/runtime/* and cmd/agentd/subcommands/shim/command.go

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

## Inputs

- ``pkg/runtime/runtime.go``
- ``pkg/runtime/client.go``
- ``pkg/runtime/runtime_test.go``
- ``pkg/runtime/client_test.go``
- ``cmd/agentd/subcommands/shim/command.go``
- ``pkg/runtime-spec/api/config.go``
- ``pkg/runtime-spec/api/state.go``
- ``pkg/runtime-spec/api/types.go``
- ``pkg/runtime-spec/config.go``
- ``pkg/runtime-spec/state.go``

## Expected Output

- ``pkg/runtime/runtime.go` — imports updated: apiruntime→pkg/runtime-spec/api, spec→pkg/runtime-spec, api removed; api.Status* references changed to apiruntime.Status*`
- ``pkg/runtime/client.go` — apiruntime import path updated`
- ``pkg/runtime/runtime_test.go` — apiruntime import path updated; api.Status* changed to apiruntime.Status*`
- ``pkg/runtime/client_test.go` — apiruntime import path updated; api.Status* changed to apiruntime.Status*`
- ``cmd/agentd/subcommands/shim/command.go` — apiruntime import path updated; pkg/spec aliased to pkg/runtime-spec`

## Verification

go build ./pkg/runtime/... ./cmd/agentd/... 2>&1 | head -20 && rg '"github.com/zoumo/oar/api/runtime"' pkg/runtime/ cmd/agentd/ --type go && echo 'FAIL: still has old import' || echo 'PASS: no old imports'
