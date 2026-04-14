# M013: Package Restructure — Clean api/ Boundary + Event/Runtime Colocation

**Gathered:** 2026-04-14
**Status:** Ready for planning

## Project Description

The codebase has accumulated structural debt after M012: `api/` directories contain implementation code (service interfaces, client wrappers), `pkg/events/` is a standalone package when it really belongs to the shim protocol layer, and `pkg/runtime/` (ACP runtime) is detached from the shim package that uses it. `pkg/spec/` was already renamed to `pkg/runtime-spec/` with a new `api/` subdirectory in a partial migration — but the old import paths (`api/runtime`, `api/ari`, `api/shim`, `api/`) are still live and consumers haven't been updated.

## Why This Milestone

- `api/` subdirectories violate the "pure types only" principle — they contain interfaces and functions
- `pkg/events/` and `pkg/runtime/` are misplaced relative to the shim layer that owns them
- ~50+ consumer files still import from old paths that partially duplicate the new pkg/ structure
- The partial migration (pkg/runtime-spec/api/ created, pkg/ari/api/ directory empty) creates confusion

## Current State (Before M013)

Already done (before M013 starts):
- `pkg/runtime-spec/api/` exists with config.go, state.go, types.go (correct content)
- `pkg/runtime-spec/` package is `runtimespec`, imports `pkg/runtime-spec/api` ✅
- `pkg/ari/api/` directory exists but is EMPTY
- `pkg/ari/server/server.go` and `pkg/ari/client/client.go` exist (from M012)
- `pkg/shim/server/service.go` and `pkg/shim/client/client.go` exist (from M012)

Still live (consumers import old paths):
- `api/runtime/` — still imported by ~20 files
- `api/types.go` — Status/EnvVar still imported via `api` package by ~35 files
- `api/ari/` — types.go, domain.go, service.go, client.go still at old location
- `api/shim/` — types.go, service.go, client.go still at old location
- `api/events.go` — event type/category constants
- `api/methods.go` — all method constants (both ARI and shim)
- `pkg/events/` — translator.go, log.go, shim_event.go, types.go, tests
- `pkg/runtime/` — ACP runtime implementation
- `pkg/ari/registry.go`, `pkg/ari/client.go` — at root of pkg/ari (need to move to server/ and client/)
- `pkg/ari/server_test.go` — at root of pkg/ari (needs to move to server/)

Empty stub files to delete:
- `pkg/agentd/runtimeclass.go` — only package decl, no content
- `pkg/agentd/runtimeclass_test.go` — only package decl, no content

## Target Structure (per docs/plan/package-restructure-20260414.md)

```
api/                      ← DELETED entirely
pkg/runtime-spec/
  api/                    ← pure types only (config, state, Status, EnvVar) ← already done
pkg/ari/
  api/                    ← pure types only (wire params/results, domain, ARI methods)
  server/                 ← service interfaces + Register + registry + implementation
  client/                 ← ARIClient + typed wrappers + simple socket client
pkg/shim/
  api/                    ← pure types only (session wire params, event types, shim methods)
  server/                 ← ShimService interface + Register + implementation + translator + log
  client/                 ← Dial helpers + ShimClient typed wrapper
  runtime/acp/            ← ACP runtime (was pkg/runtime/)
pkg/events/               ← DELETED (content moved to pkg/shim/api/ and pkg/shim/server/)
pkg/runtime/              ← DELETED (moved to pkg/shim/runtime/acp/)
```

## Principle

**api/ subdirectories contain only struct/const/enum. Interfaces and funcs go to server/ or client/.**

## Implementation Decisions

1. `pkg/ndjson/` stays at its current path — imported by both `pkg/events/log.go` (moving to `pkg/shim/server/`) and `cmd/agentdctl/subcommands/shim/command.go` (not shim-specific). "Stays if independent" applies.

2. `pkg/ari/server_test.go` (package `ari_test`, 809 lines) moves to `pkg/ari/server/server_test.go` (package `server_test`) in S02/T02.

3. `pkg/ari/client_test.go` (package `ari`, simple client tests) moves to `pkg/ari/client/simple_test.go` (package `client`) in S02/T02.

4. ARI method constants from `api/methods.go` → `pkg/ari/api/methods.go` in S02. Shim method constants → `pkg/shim/api/methods.go` in S03. `api/methods.go` deleted after S03 when both are extracted.

5. `api/events.go` (event type/category constants) → `pkg/shim/api/events.go` in S03. Deleted after S03.

6. After S03, `api/` directory is entirely empty and is deleted.

7. `pkg/ari/server/server.go` has no direct `pkg/events` imports (confirmed grep). `pkg/shim/server/service.go` uses Translator + ReadEventLog — becomes same-package after S04 (import dropped).

8. After S03 moves event types to `pkg/shim/api/`, consumers in `pkg/agentd/` will import `pkg/shim/api` for ShimEvent and event type constants.

9. After S04 moves translator/log to `pkg/shim/server/`, `pkg/ari/server/server_test.go` will import `pkg/shim/server` for any Translator references it has.

## Risks and Unknowns

- Consumer file count large (~50+) — mechanical but typo-prone; each slice must verify with make build + go test ./...
- `api/methods.go` serves both ARI and shim consumers simultaneously — split happens across S02 (ARI) and S03 (shim); during the window between S02 and S03, shim consumers still import the original `api/methods.go`
- `pkg/ari/server_test.go` is 809 lines — moving to pkg/ari/server/ changes import context; may surface hidden pkg/ari root-package dependencies (e.g. `pkg/ari` registry vs `pkg/ari/server` registry)

## Integration Points

- `pkg/agentd/` — heaviest consumer; imports api, api/ari, api/shim, api/runtime, pkg/events, pkg/spec
- `pkg/ari/server/server.go` — imports api/ari, api/shim, pkg/ari (registry)
- `cmd/agentd/subcommands/shim/command.go` — imports api/shim, api/runtime, pkg/runtime, pkg/events
- `pkg/shim/server/service.go` — imports api, api/ari, api/shim, pkg/events, pkg/runtime

## Relevant Requirements

- No functional requirements change. Structural refactor must not regress R001–R009.
- Regression proof: `make build` + `go test ./...` + `go vet ./...` must pass at each slice boundary.

## Scope

### In Scope

- All moves, package renames, and import updates described in docs/plan/package-restructure-20260414.md
- Deletion of api/, pkg/events/, pkg/runtime/ after content moved
- Deletion of empty stub files (runtimeclass.go, etc.)
- Moving test files alongside their source files

### Out of Scope / Non-Goals

- Any behavioral changes to existing code
- Adding new functionality
- `pkg/ndjson/` relocation (stays independent)
- Any changes to binary output or wire protocol
