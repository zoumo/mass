---
id: S03
parent: M013
milestone: M013
provides:
  - ["api/ directory deleted — no legacy github.com/zoumo/oar/api or github.com/zoumo/oar/api/shim import targets exist", "pkg/shim/api/ — canonical home for all shim wire types (types.go, service.go, client.go, methods.go)", "pkg/events/constants.go — canonical home for EventType*/Category* constants", "All 19 consumer files migrated across 6 package groups"]
requires:
  - slice: S01
    provides: pkg/runtime-spec/api as sole Status/EnvVar home; api/runtime deleted
  - slice: S02
    provides: pkg/ari/api + pkg/ari/server + pkg/ari/client established; api/ari deleted; MethodWorkspace* constants in pkg/ari/api/methods.go
affects:
  - ["S04 — pkg/events/ and pkg/runtime/ migration; S03 establishes pkg/events/constants.go which S04 will keep in place; api/ is now gone so S04 has no further api/ cleanup to do"]
key_files:
  - ["pkg/shim/api/types.go", "pkg/shim/api/service.go", "pkg/shim/api/client.go", "pkg/shim/api/methods.go", "pkg/events/constants.go", "pkg/events/types.go", "pkg/events/shim_event.go", "pkg/events/translator.go", "pkg/shim/client/client.go", "pkg/shim/server/service.go", "pkg/agentd/process.go", "pkg/agentd/recovery.go", "cmd/agentdctl/subcommands/shim/command.go", "cmd/agentdctl/subcommands/shim/chat.go", "cmd/agentd/subcommands/workspacemcp/command.go"]
key_decisions:
  - ["pkg/shim/api/methods.go scoped to shim-only constants (no workspace/agentrun/agent), keeping the package cohesive (D115)", "Explicit `shim` alias on migrated test files (recovery_test, recovery_posture_test, process_test) to preserve shim.X call sites verbatim (D116)", "chat.go did not need pkgariapi import — MethodWorkspaceSend/Status not present in that file (plan deviation, avoided unused-import error)"]
patterns_established:
  - ["pkg/shim tri-split: api/ for pure types+service interface+client+method constants, server/ for implementation, client/ for dial helper", "EventType*/Category* constants in pkg/events — same-package consumers use unqualified names; external consumers use `events.EventType*`", "Explicit import alias strategy: when migrating bare Go package path (e.g. bare 'api/shim' → 'shim'), add explicit alias to preserve all call sites", "Method constants scoping rule: each protocol domain owns its constants in its own api package"]
observability_surfaces:
  - none
drill_down_paths:
  []
duration: ""
verification_result: passed
completed_at: 2026-04-14T10:52:54.572Z
blocker_discovered: false
---

# S03: Shim package restructure + api/ deletion

**Created pkg/shim/api/ as the canonical shim wire types package, moved EventType*/Category* constants into pkg/events/constants.go, migrated all 19 consumer files across 6 package groups, and deleted the api/ directory entirely; make build + go test ./... pass with zero legacy import targets.**

## What Happened

S03 completed the most impactful restructure of M013: it eliminated the legacy `api/` directory entirely, leaving no remaining `github.com/zoumo/oar/api` or `github.com/zoumo/oar/api/shim` import targets in the codebase.

**T01 — Additive destination packages (purely additive, build green throughout):**

Five new files were created without touching any existing code:

1. `pkg/shim/api/methods.go` — shim-only RPC method constants (MethodSession*, MethodRuntime*, MethodShimEvent) in package `api`. Deliberately scoped to just the shim protocol boundary (no workspace/agentrun/agent constants) so pkg/shim/api stays cohesive. (D115)
2. `pkg/shim/api/types.go` — verbatim copy of `api/shim/types.go`, package changed to `api`.
3. `pkg/shim/api/service.go` — verbatim copy of `api/shim/service.go`, package changed to `api`; all types are now same-package, no qualifier changes needed.
4. `pkg/shim/api/client.go` — adapted from `api/shim/client.go`; package changed to `api`; dropped the `"github.com/zoumo/oar/api"` import; all `api.MethodSession*/api.MethodRuntime*` references became unqualified constants (now in the same package).
5. `pkg/events/constants.go` — package `events`; all EventType* and Category* constants moved here from `api/events.go`.

All three build checks passed (go build ./pkg/shim/api/..., go build ./pkg/events/..., make build) with the api/ directory still intact.

**T02 — Consumer migration in 6 ordered groups, then api/ deletion:**

*Group 1 — pkg/events/* (5 files)*: Dropped `"github.com/zoumo/oar/api"` import from types.go, shim_event.go, translator.go, log_test.go, translator_test.go. All `api.EventType*` and `api.Category*` references became unqualified identifiers resolved from the new same-package constants.go. `go test ./pkg/events/...` passed.

*Group 2 — pkg/shim/* (2 files)*: Swapped apishim import path in client/client.go. In server/service.go, dropped bare `"github.com/zoumo/oar/api"` import and replaced 2 occurrences of `api.MethodShimEvent` → `apishim.MethodShimEvent`. `go build ./pkg/shim/...` passed.

*Group 3 — pkg/agentd/* (7 files)*: recovery.go — simple apishim path swap. process.go — dropped bare api import, replaced 3 constants (MethodShimEvent, CategoryRuntime, EventTypeStateChange). mock_shim_server_test.go and shim_boundary_test.go — same patterns. recovery_test.go, recovery_posture_test.go, process_test.go — changed bare `"github.com/zoumo/oar/api/shim"` to `shim "github.com/zoumo/oar/pkg/shim/api"` with explicit alias to preserve all `shim.X` call sites verbatim (D116). `go test ./pkg/agentd/...` passed.

*Group 4 — pkg/ari/server/server.go* (1 file): Simple apishim path swap. `go build ./pkg/ari/server/...` passed.

*Group 5 — cmd/agentd/* (2 files)*: shim/command.go — simple apishim path swap. workspacemcp/command.go — dropped bare api import, added `pkgariapi "github.com/zoumo/oar/pkg/ari/api"`, replaced MethodWorkspaceSend and MethodWorkspaceStatus. `go build ./cmd/agentd/...` passed.

*Group 6 — cmd/agentdctl/subcommands/shim/* (2 files)*: command.go — dropped bare api, added shimapi + events imports, replaced all 7 Method* and 6 EventType* references. chat.go — same approach; the plan anticipated MethodWorkspaceSend/Status references but they were absent from the file, so pkgariapi was not added (avoiding unused import error). `go build ./cmd/...` passed.

*Deletion*: `rm api/shim/{types,service,client}.go && rmdir api/shim && rm api/{events,methods}.go && rmdir api`. Directory confirmed absent.

*Final verification*: Both grep gates returned exit 1 (zero matches); `test ! -d api` passed; make build produced both binaries; `go test ./...` — all packages pass including the 104s integration test suite.

## Verification

All 5 must-have checks from the slice plan pass:
1. `make build` → exit 0; both bin/agentd and bin/agentdctl produced
2. `go test ./...` → exit 0; all packages pass (including 104s integration test suite)
3. `rg 'zoumo/oar/api"' --type go` → exit 1 (zero matches — legacy bare api import gone)
4. `rg '"github.com/zoumo/oar/api/shim"' --type go` → exit 1 (zero matches — legacy api/shim import gone)
5. `test ! -d api` → exit 0 (`api/` directory deleted)
6. `ls pkg/shim/api/` → client.go, methods.go, service.go, types.go (all required files present)

## Requirements Advanced

None.

## Requirements Validated

None.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

chat.go in cmd/agentdctl/subcommands/shim/ did not require the pkgariapi import for MethodWorkspaceSend/Status — those references were not present in the file. The plan was conservative in anticipating possible references; the actual file was inspected and the import was correctly omitted.

## Known Limitations

None.

## Follow-ups

S04: pkg/events/ and pkg/runtime/ migration (Events impl + ACP runtime migration + final verification).

## Files Created/Modified

- `pkg/shim/api/methods.go` — New: shim-only RPC method constants
- `pkg/shim/api/types.go` — New: shim wire types (moved from api/shim/types.go)
- `pkg/shim/api/service.go` — New: ShimService interface (moved from api/shim/service.go)
- `pkg/shim/api/client.go` — New: shim client wrapper (adapted from api/shim/client.go, uses same-package constants)
- `pkg/events/constants.go` — New: EventType*/Category* constants (moved from api/events.go)
- `pkg/events/types.go` — Removed api import; EventType* references now unqualified
- `pkg/events/shim_event.go` — Removed api import; EventType*/Category* references now unqualified
- `pkg/events/translator.go` — Removed api import; all constants now unqualified
- `pkg/events/log_test.go` — Removed api import; Category* now unqualified
- `pkg/events/translator_test.go` — Removed api import; Category* now unqualified
- `pkg/shim/client/client.go` — apishim import path swapped to pkg/shim/api
- `pkg/shim/server/service.go` — apishim import path swapped; dropped bare api; MethodShimEvent now via apishim
- `pkg/agentd/recovery.go` — apishim import path swapped
- `pkg/agentd/process.go` — Dropped bare api; MethodShimEvent → apishim; Category/EventType → events
- `pkg/agentd/mock_shim_server_test.go` — apishim import path swapped
- `pkg/agentd/shim_boundary_test.go` — apishim path swapped; dropped bare api; constants via apishim/events
- `pkg/agentd/recovery_test.go` — Explicit shim alias on pkg/shim/api import
- `pkg/agentd/recovery_posture_test.go` — Explicit shim alias on pkg/shim/api import
- `pkg/agentd/process_test.go` — Explicit shim alias on pkg/shim/api import
- `pkg/ari/server/server.go` — apishim import path swapped
- `cmd/agentd/subcommands/shim/command.go` — apishim import path swapped
- `cmd/agentd/subcommands/workspacemcp/command.go` — Dropped bare api; pkgariapi added; MethodWorkspace* via pkgariapi
- `cmd/agentdctl/subcommands/shim/command.go` — Dropped bare api; shimapi + events added; all Method*/EventType* references migrated
- `cmd/agentdctl/subcommands/shim/chat.go` — Dropped bare api; shimapi + events added; all Method*/EventType* references migrated
