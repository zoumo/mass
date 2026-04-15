---
id: M013
title: "Package Restructure — Clean api/ Boundary + Event/Runtime Colocation"
status: complete
completed_at: 2026-04-14T12:07:26.933Z
key_decisions:
  - D115 — pkg/shim/api/methods.go scoped to shim-only constants: each protocol domain owns its method constants in its own api package, preventing pkg/shim/api from becoming a catch-all methods file
  - D116 — Explicit `shim` alias on migrated test files: preserves all shim.X call sites verbatim when renaming from bare api/shim to pkg/shim/api
  - D117 — Two-task event migration strategy: move wire types to pkg/shim/api first (T01), then move implementation to pkg/shim/server (T02) — ensures types are in final home before consumers move
  - D118 — EventTypeOf(ev Event) string exported accessor: cross-package bridge for the sealed Event interface's unexported eventType() method; minimal backward-compatible addition that preserves sealed interface property
key_files:
  - pkg/ari/api/types.go
  - pkg/ari/api/domain.go
  - pkg/ari/api/methods.go
  - pkg/ari/server/service.go
  - pkg/ari/server/registry.go
  - pkg/ari/client/typed.go
  - pkg/ari/client/simple.go
  - pkg/shim/api/types.go
  - pkg/shim/api/service.go
  - pkg/shim/api/client.go
  - pkg/shim/api/methods.go
  - pkg/shim/api/shim_event.go
  - pkg/shim/api/event_types.go
  - pkg/shim/api/event_constants.go
  - pkg/shim/server/translator.go
  - pkg/shim/server/log.go
  - pkg/shim/server/service.go
  - pkg/shim/runtime/acp/runtime.go
  - pkg/shim/runtime/acp/client.go
  - pkg/runtime-spec/api (stable canonical home for Status/EnvVar)
lessons_learned:
  - Named type cascade migration (K083): when a Go named type moves packages, all files passing it across package boundaries must be migrated in the same build wave — compile errors are the dependency map; planned per-task scoping is aspirational
  - Same-package qualifier stripping (K084, K085): after moving types and consumers into the same sub-package, always strip qualifiers from now-same-package types and functions — mechanical substitution produces wrong code here
  - Sealed interface cross-package bridge (K086): when a sealed interface (unexported method) is owned by one package but consumed by another after migration, add an exported EventTypeOf()-style accessor in the interface-owning package rather than duplicating or exposing the unexported method
  - Two-task migration for event packages (K087): move wire types to api/ package first with all consumers pointing to new path; then move implementation to server/ package using the api qualifier; T01 bridge (JSON round-trip or similar) is explicitly temporary and removed in T02
  - rg exit code semantics (K082): rg exit 1 = no matches = PASS for zero-match gates; always use `! rg PATTERN` or `rg PATTERN && echo FAIL || echo PASS` in verification scripts
  - Dual import Pattern B: files that use both the migrating types AND staying constants get a dual import — keep the old package for constants, add new package for types — avoids disturbing constant references while completing the type migration
---

# M013: Package Restructure — Clean api/ Boundary + Event/Runtime Colocation

**Completed the full package restructure: deleted the api/ directory, established pkg/ari/{api,server,client} and pkg/shim/{api,server,client,runtime/acp} as canonical package homes, eliminated pkg/events/ and pkg/runtime/, and migrated all 50+ consumer files — make build + go test ./... + go vet (first-party) all pass clean.**

## What Happened

M013 executed the package restructure defined in docs/plan/package-restructure-20260414.md across four sequential slices, each building on the stable foundation established by its predecessors.

**S01 — Runtime-spec consumer migration**
Eliminated all references to `api/runtime` and `api.Status`/`api.EnvVar` by migrating every consumer to `pkg/runtime-spec/api`. Applied two migration patterns: Pattern A (files only using Status/EnvVar — clean 1-for-1 replacement) and Pattern B (files also using Method/Category/EventType constants — dual import, keep `api` for constants temporarily). A named-type cascade blocker emerged when `pkg/agentd`'s Status migration conflicted with `apiari.AgentRunStatus.State` typing — resolved by migrating four additional S03-scoped files early as cascade dependencies. Deleted api/runtime/, api/types.go, and two empty runtimeclass stubs. make build + go test ./... clean.

**S02 — ARI package restructure**
Created the pkg/ari tri-split: pkg/ari/api/ (types.go, domain.go, methods.go — pure wire types and ARI method constants), pkg/ari/server/ (service.go, registry.go — service interfaces and Registry), pkg/ari/client/ (typed.go, simple.go — typed ARIClient and simple Client). Migrated 35+ consumer files across 9 groups using four migration rules. Key adaptations: same-package types in client.go and same-package Register functions in server.go needed qualifier stripping after mechanical substitution. Deleted api/ari/ directory and pkg/ari root files. make build + go test ./... clean.

**S03 — Shim package restructure + api/ deletion**
Created the pkg/shim tri-split: pkg/shim/api/ (types, service interface, client, method constants), pkg/shim/server/ (service implementation), pkg/shim/client/ (dial helper). Also created pkg/events/constants.go as canonical home for EventType*/Category* constants. Migrated 19 consumer files across 6 package groups. Key adaptations: explicit `shim` alias on test files preserved call sites verbatim; pkg/events/constants.go was the intermediate landing for event constants before S04 moved them. Deleted api/shim/ and the api/ root directory. make build + go test ./... clean.

**S04 — Events impl + ACP runtime migration + final verification**
Completed the final phase: moved all event wire types (ShimEvent, typed events, EventType*/Category* constants) from pkg/events/ into pkg/shim/api/ as three new files (shim_event.go, event_types.go, event_constants.go). Moved translator.go and log.go from pkg/events/ to pkg/shim/server/, and relocated the entire pkg/runtime/ package to pkg/shim/runtime/acp/. Key obstacle: the sealed Event interface uses an unexported eventType() method — resolved by adding EventTypeOf(ev Event) string as an exported cross-package accessor. A T01 temporary JSON-round-trip bridge was used for type compatibility during the two-task migration and cleanly removed in T02. Deleted pkg/events/ and pkg/runtime/ directories. make build + go test ./... + go vet (first-party) all pass.

**Cumulative outcome**: 93 files changed across 8 commits. The entire api/ directory is gone. pkg/ari and pkg/shim now each follow the api/server/client tri-split pattern. All implementation code lives in typed server/ or client/ packages. Zero legacy import paths remain.

## Success Criteria Results

## Success Criteria Results

All success criteria were derived from the "After this" column of each slice in the roadmap.

### S01: Runtime-spec consumer migration
- ✅ `make build` exits 0 — both agentd and agentdctl produced
- ✅ `go test ./...` all packages pass
- ✅ Zero `api/runtime` imports: `rg '"github.com/zoumo/mass/api/runtime"' --type go` → exit 1 (no matches)
- ✅ Zero `api.Status`/`api.EnvVar` bare imports: verified by zero bare `api` import results
- ✅ `api/runtime/` directory deleted: `test ! -d api/runtime` → pass
- ✅ `api/types.go` deleted: `test ! -f api/types.go` → pass
- ✅ Empty runtimeclass stubs deleted: `test ! -f pkg/agentd/runtimeclass.go` + `runtimeclass_test.go` → both pass

### S02: ARI package restructure
- ✅ `make build` + `go test ./...` pass
- ✅ `pkg/ari/api/` has types.go, domain.go, methods.go: `ls pkg/ari/api/` confirmed
- ✅ `pkg/ari` root has only api/, server/, client/ subdirs: `ls pkg/ari/` → api client server
- ✅ Zero `api/ari` imports: `rg '"github.com/zoumo/mass/api/ari"' --type go` → exit 1 (no matches)
- ✅ Zero bare `pkg/ari` imports: `rg '"github.com/zoumo/mass/pkg/ari"[^/]' --type go` → exit 1 (no matches)

### S03: Shim package restructure + api/ deletion
- ✅ `make build` + `go test ./...` pass
- ✅ `pkg/shim/api/` has all shim type files: client.go, event_constants.go, event_types.go, methods.go, service.go, shim_event.go, types.go
- ✅ Zero `api/shim` imports: `rg '"github.com/zoumo/mass/api/shim"' --type go` → exit 1 (no matches)
- ✅ Zero bare `api` imports: `rg '"github.com/zoumo/mass/api"[^/]' --type go` → exit 1 (no matches)
- ✅ `api/` directory gone: `test ! -d api` → pass

### S04: Events impl + ACP runtime migration + final verification
- ✅ `make build` exits 0
- ✅ `go test ./...` all packages pass (pkg/shim/server, pkg/shim/runtime/acp, integration tests all green)
- ✅ `go vet ./pkg/... ./cmd/...` exits 0 (pre-existing third_party vet issue in csync/maps.go is out-of-scope)
- ✅ `pkg/events/` does not exist: `test ! -d pkg/events` → pass
- ✅ `pkg/runtime/` does not exist: `test ! -d pkg/runtime` → pass
- ✅ `pkg/shim/server/` has translator.go and log.go: `ls pkg/shim/server/translator.go pkg/shim/server/log.go` → both present
- ✅ `pkg/shim/runtime/acp/` has ACP runtime: `ls pkg/shim/runtime/acp/runtime.go` → present

## Definition of Done Results

## Definition of Done Results

- ✅ All 4 slices complete: S01 ✅, S02 ✅, S03 ✅, S04 ✅ (confirmed via gsd_milestone_status)
- ✅ All 9 tasks complete: S01/T01+T02+T03, S02/T01+T02, S03/T01+T02, S04/T01+T02 (all show done=total in DB)
- ✅ All 4 slice summaries exist on disk: S01-SUMMARY.md, S02-SUMMARY.md, S03-SUMMARY.md, S04-SUMMARY.md
- ✅ Cross-slice integration verified: S04 explicitly verified make build + go test ./... + go vet as final milestone-level check after all package moves were complete
- ✅ Zero legacy import paths: api/runtime, api/ari, api/shim, bare api — all confirmed zero with rg
- ✅ No orphaned import targets: api/ directory deleted, pkg/events/ deleted, pkg/runtime/ deleted
- ✅ Requirements R020, R026, R027, R028 transitioned to 'validated' with test evidence from S04
- ✅ Decisions D115–D118 recorded in DECISIONS.md
- ✅ Knowledge entries K083–K087 recorded in KNOWLEDGE.md
- ✅ PROJECT.md updated to reflect M013 completion

## Requirement Outcomes

## Requirement Outcomes

### Validated this milestone

| Requirement | Transition | Evidence |
|-------------|-----------|---------|
| R020 — CreateTerminal | active → validated | pkg/shim/runtime/acp/runtime.go preserves full terminal manager; `go test ./pkg/shim/runtime/acp/...` passes including TestTerminalManager_Create_* tests |
| R026 — TerminalOutput | active → validated | TerminalOutput implementation preserved in pkg/shim/runtime/acp; TestTerminalManager_Output_* tests pass |
| R027 — KillTerminalCommand | active → validated | KillTerminalCommand preserved in pkg/shim/runtime/acp; TestTerminalManager_Kill_* tests pass |
| R028 — ReleaseTerminal | active → validated | ReleaseTerminal preserved in pkg/shim/runtime/acp; TestTerminalManager_Release_* tests pass |

### No requirements invalidated or re-scoped

All previously validated requirements remain valid. M013 was a pure structural migration with no behavioral changes — all existing functionality was preserved and verified by passing the full test suite.

## Deviations

S01: api/shim/types.go migrated in T01 (planned T03) as required transitive compile dependency. api/ari/domain.go, api/ari/types.go, pkg/store/agentrun.go, pkg/ari/server/server.go migrated in T02 (planned T03) as Status-type cascade dependencies — no behavioral changes, only import line updates. S02: pkg/ari/registry.go (pre-deletion) needed a temporary api/ari → pkgariapi update to compile alongside the updated pkg/store interface; this pre-deletion update was not in the plan. S03: pkg/events/constants.go was created as an intermediate landing zone for EventType*/Category* constants; S04 then moved these into pkg/shim/api/ directly. S04: A temporary legacyEventsToAPI JSON-round-trip bridge was added in service.go in T01 to handle type incompatibility during the two-task migration; cleanly removed in T02 as planned.

## Follow-ups

Future milestones can now focus on behavioral features rather than structural cleanup. The package layout is stable: pkg/ari/{api,server,client}, pkg/shim/{api,server,client,runtime/acp}, pkg/runtime-spec/api. The pre-existing flaky test TestStateChange_RunningToIdle_UpdatesDB (race condition in pkg/jsonrpc/client.go send-on-closed-channel) is not introduced by M013 and should be addressed in a future code quality milestone. The pre-existing `go vet` issue in third_party/charmbracelet/crush/csync/maps.go (lock-copy in JSONSchemaAlias) is out-of-scope for M013 but could be patched as a low-effort quality item.
