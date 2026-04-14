---
id: S01
parent: M013
milestone: M013
provides:
  - ["pkg/runtime-spec/api is now the sole canonical home for Status and EnvVar types across the entire codebase", "api/runtime/ directory deleted — no residual import targets remain", "api/types.go deleted — zero legacy Status/EnvVar definitions remain in api/", "Build is clean: make build exits 0, go test ./... all-pass", "S02 and S03 can proceed without collision from api/runtime or api.Status/EnvVar references"]
requires:
  []
affects:
  - ["S02 — api/ari/ files already migrated off api.Status/EnvVar; S02 ARI restructure can focus on the method/constant surface", "S03 — api/shim/types.go already migrated; S03 shim restructure starts clean on that file"]
key_files:
  - ["pkg/runtime/runtime.go", "pkg/runtime/client.go", "pkg/runtime/runtime_test.go", "pkg/runtime/client_test.go", "cmd/agentd/subcommands/shim/command.go", "api/shim/types.go", "pkg/agentd/agent.go", "pkg/agentd/process.go", "pkg/agentd/recovery.go", "pkg/agentd/shim_boundary_test.go", "api/ari/domain.go", "api/ari/types.go", "pkg/store/agentrun.go", "pkg/ari/server/server.go", "pkg/ari/server_test.go", "pkg/store/agentrun_test.go", "pkg/store/agent_test.go", "tests/integration/real_cli_test.go"]
key_decisions:
  - ["Named type migrations cascade through all files that pass the type across package boundaries — task-scope boundaries are aspirational; actual migration scope is determined by compile-time dependency graph (K083)", "Pattern B dual-import (keep api for Method/Category/EventType, add apiruntime for Status/EnvVar) applied to process.go and shim_boundary_test.go — avoids disturbing constant references while completing the type migration", "api/shim/types.go migrated in T01 (planned for T03) as a transitive compile dependency of cmd/agentd/subcommands/shim via pkg/shim/server", "ripgrep exit code 1 = no matches found = PASS for zero-match gates — verification scripts must use `! rg PATTERN` or `rg PATTERN && echo FAIL || echo PASS` pattern (K082)"]
patterns_established:
  - ["Named type cascade migration: when a Go named type moves packages, migrate all files that pass it across boundaries in the same build wave — compile errors are the map", "Pattern A / Pattern B dual-import distinction: files only using Status/EnvVar get a clean 1-for-1 replacement; files also using Method/Category/EventType constants get a dual import (keep api, add apiruntime)", "rg exit-code semantics for zero-match gates: always use `! rg PATTERN` or `rg && echo FAIL || echo PASS` — never treat rg exit 1 as a failure"]
observability_surfaces:
  - none
drill_down_paths:
  []
duration: ""
verification_result: passed
completed_at: 2026-04-14T09:21:13.337Z
blocker_discovered: false
---

# S01: Runtime-spec consumer migration

**Migrated all api/runtime and api (Status/EnvVar) consumers to pkg/runtime-spec/api; deleted api/runtime/, api/types.go, and two empty runtimeclass stubs; make build + go test ./... pass clean with all grep gates confirming zero legacy import paths remain.**

## What Happened

S01 completed the first phase of the M013 package restructure: eliminate all consumer references to the deleted api/runtime/ package and the api.Status/api.EnvVar types, then delete the dead files.

**T01 — pkg/runtime/* + cmd/agentd/subcommands/shim/command.go:**
Five files were migrated with the planned alias strategy: `apiruntime "github.com/zoumo/oar/api/runtime"` → `apiruntime "github.com/zoumo/oar/pkg/runtime-spec/api"`, and `"github.com/zoumo/oar/pkg/spec"` → `spec "github.com/zoumo/oar/pkg/runtime-spec"`. The alias preservation meant zero call-site changes for most files. runtime.go and runtime_test.go required replacing bare `api.Status*` references with `apiruntime.Status*` (done via sed for speed). A minor scope deviation: api/shim/types.go was also migrated in T01 because it is a transitive compile dependency of cmd/agentd/subcommands/shim via pkg/shim/server — needed to satisfy T01's own build check.

**T02 — pkg/agentd/* (nine files):**
Two patterns applied: Pattern A (files only using Status/EnvVar — replace api import with apiruntime) and Pattern B (files also using Method*/Category*/EventType* constants — keep api alongside new apiruntime). A compile-time cascade blocker emerged: apiari.AgentRunStatus.State is typed as api.Status, so after pkg/agentd migrated to apiruntime.Status the two named types conflicted at package boundaries. Rather than adding casts at every boundary, four additional T03-scoped files were migrated early as cascade dependencies: api/ari/domain.go, api/ari/types.go, pkg/store/agentrun.go, and pkg/ari/server/server.go. After those four files were updated, make build exited cleanly and go test ./pkg/agentd/... passed in 6.9s.

**T03 — remaining consumers + deletions:**
The four remaining test files (pkg/ari/server_test.go, pkg/store/agentrun_test.go, pkg/store/agent_test.go, tests/integration/real_cli_test.go) followed a clean Pattern A migration — none used Method/Category/EventType constants, so the api import was fully replaced by apiruntime in each. Five dead files were then deleted: api/runtime/config.go, api/runtime/state.go (+ rmdir api/runtime/), api/types.go, pkg/agentd/runtimeclass.go, pkg/agentd/runtimeclass_test.go. make build exited 0 producing both binaries. go test ./... ran all 13 test packages clean.

**Cascade observation (K083):** Named type migrations propagate further than import paths suggest. Files need to be migrated together whenever they pass the named type across a package boundary. The planned per-task scoping was aspirational; in practice each task completed its own scope plus the minimal cascade dependencies needed to make its build gate pass. This is the correct pragmatic approach.

**Pre-existing flaky test:** TestStateChange_RunningToIdle_UpdatesDB in pkg/agentd has a race condition in pkg/jsonrpc/client.go unrelated to this migration. It surfaces occasionally on first run but passes on retry. Not introduced by S01.

## Verification

All slice-level verification checks pass:

1. `make build` → exit 0, both binaries (agentd, agentdctl) produced
2. `go test ./...` → all 13 test packages pass including 103s integration tests (cached on final run)
3. `rg '"github.com/zoumo/oar/api/runtime"' --type go` → exit 1 (no matches = import path gone)
4. `rg '"github.com/zoumo/oar/pkg/spec"' --type go` → exit 1 (no matches = old spec path gone)
5. `test ! -d api/runtime` → pass (directory deleted)
6. `test ! -f api/types.go` → pass (file deleted)
7. `test ! -f pkg/agentd/runtimeclass.go` → pass (empty stub deleted)
8. `test ! -f pkg/agentd/runtimeclass_test.go` → pass (empty stub deleted)

Note: rg exit code 1 = no matches found = verification PASS for "must have zero matches" gates. The auto-fix trigger on the first closer attempt was a false positive from misinterpreting ripgrep's inverted exit code semantics.

## Requirements Advanced

None.

## Requirements Validated

None.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

["api/shim/types.go migrated in T01 (planned T03): required as transitive compile dependency of cmd/agentd/subcommands/shim — no behavioral change, one import line", "api/ari/domain.go, api/ari/types.go, pkg/store/agentrun.go, pkg/ari/server/server.go migrated in T02 (planned T03): required as cascade dependencies of the Status type unification in pkg/agentd — no behavioral changes, only type reference updates"]

## Known Limitations

["Pre-existing flaky test TestStateChange_RunningToIdle_UpdatesDB (pkg/agentd): race condition in pkg/jsonrpc/client.go send-on-closed-channel; not introduced by S01; passes on retry", "Pattern B files (process.go, shim_boundary_test.go) still import the bare 'api' package for Method/Category/EventType constants — these will be migrated in S02/S03 when those constants move to their new homes"]

## Follow-ups

["S02: migrate api/ari/ package structure — types.go+domain.go+methods.go layout; eliminate remaining bare 'api' imports for Method/Category/EventType constants", "S03: migrate api/shim/ package structure; delete api/ directory entirely; migrate api/events.go and api/methods.go consumers"]

## Files Created/Modified

- `pkg/runtime/client.go` — apiruntime import path: api/runtime → pkg/runtime-spec/api
- `pkg/runtime/runtime.go` — apiruntime target updated; pkg/spec → pkg/runtime-spec alias; api.Status* → apiruntime.Status*; bare api import removed
- `pkg/runtime/client_test.go` — apiruntime target updated; api.EnvVar → apiruntime.EnvVar; api import removed
- `pkg/runtime/runtime_test.go` — apiruntime target updated; api.Status* → apiruntime.Status*; api import removed
- `cmd/agentd/subcommands/shim/command.go` — apiruntime target updated; pkg/spec → pkg/runtime-spec alias
- `api/shim/types.go` — apiruntime import path updated (T01 early migration)
- `pkg/agentd/agent.go` — Pattern A: api import replaced with apiruntime; api.Status* → apiruntime.Status*
- `pkg/agentd/agent_test.go` — Pattern A
- `pkg/agentd/recovery.go` — Pattern A + pkg/spec alias fix
- `pkg/agentd/process.go` — Pattern B: keep api for constants; add apiruntime; api.Status*/EnvVar → apiruntime.*; pkg/spec alias fix
- `pkg/agentd/process_test.go` — Pattern A
- `pkg/agentd/recovery_test.go` — apiruntime target updated; pkg/spec alias fix; Pattern A
- `pkg/agentd/recovery_posture_test.go` — apiruntime target updated; Pattern A
- `pkg/agentd/mock_shim_server_test.go` — apiruntime target updated; Pattern A
- `pkg/agentd/shim_boundary_test.go` — Pattern B: keep api for MethodShimEvent; add apiruntime; api.Status* → apiruntime.Status*
- `api/ari/domain.go` — T02 cascade: apiruntime added; api.Status/EnvVar → apiruntime.*
- `api/ari/types.go` — T02 cascade: same migration
- `pkg/store/agentrun.go` — T02 cascade: apiruntime added; api.Status → apiruntime.Status
- `pkg/ari/server/server.go` — T02 cascade: apiruntime added; api.Status* → apiruntime.Status*
- `pkg/ari/server_test.go` — T03: api import replaced with apiruntime; Status* refs updated
- `pkg/store/agentrun_test.go` — T03: api import replaced with apiruntime; Status* refs updated
- `pkg/store/agent_test.go` — T03: api import replaced with apiruntime; EnvVar refs updated
- `tests/integration/real_cli_test.go` — T03: api import replaced with apiruntime; EnvVar refs updated
- `api/runtime/config.go` — DELETED — api/runtime package removed
- `api/runtime/state.go` — DELETED — api/runtime package removed
- `api/types.go` — DELETED — Status/EnvVar now live solely in pkg/runtime-spec/api
- `pkg/agentd/runtimeclass.go` — DELETED — empty package-declaration-only stub
- `pkg/agentd/runtimeclass_test.go` — DELETED — empty package-declaration-only stub
