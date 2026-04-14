---
id: S02
parent: M013
milestone: M013
provides:
  - ["pkg/ari/api — canonical home for all ARI wire types (types.go, domain.go) and method constants (methods.go)", "pkg/ari/server — ARIService interfaces, Registry, and server wiring", "pkg/ari/client — typed ARIClient and simple Client; both exported from pkg/ari/client", "api/ari/ directory completely deleted", "No bare pkg/ari root imports remain — all consumers use typed sub-paths"]
requires:
  - slice: S01
    provides: pkg/runtime-spec/api.Status replaces api.Status; api/runtime/ deleted — S02 builds on these type paths being stable
affects:
  - ["S03 — api/ directory still has api/methods.go (shim method constants) and api/shim/; S03 deletes these after migrating their consumers"]
key_files:
  - ["pkg/ari/api/types.go", "pkg/ari/api/domain.go", "pkg/ari/api/methods.go", "pkg/ari/server/service.go", "pkg/ari/server/registry.go", "pkg/ari/server/server.go", "pkg/ari/server/server_test.go", "pkg/ari/server/registry_test.go", "pkg/ari/client/typed.go", "pkg/ari/client/simple.go", "pkg/ari/client/client.go", "pkg/ari/client/simple_test.go"]
key_decisions:
  - ["Retained bare `api` import in pkg/agentd/process.go and shim_boundary_test.go for shim method constants — these are S03 scope", "same-package types in pkg/ari/client/typed.go (WorkspaceClient etc.) need no pkgariapi qualifier in client.go — mechanical substitution would introduce a bug here", "RegisterWorkspaceService and siblings in service.go are same-package calls from server.go — no qualifier after consolidation", "pkg/ari/api/methods.go contains only ARI method constants (Workspace*, AgentRun*, Agent*); shim constants (MethodSession*, MethodRuntime*, MethodShimEvent*) stay in api/methods.go until S03"]
patterns_established:
  - ["pkg/ari/ now follows the api/server/client tri-split: pkg/ari/api (pure types + constants), pkg/ari/server (interfaces + registry + RPC dispatch), pkg/ari/client (typed and simple clients)", "All ARI method constants for workspace/agentrun/agent surface live in pkg/ari/api/methods.go — callers use pkgariapi.MethodX"]
observability_surfaces:
  - []
drill_down_paths:
  []
duration: ""
verification_result: passed
completed_at: 2026-04-14T10:17:35.317Z
blocker_discovered: false
---

# S02: ARI package restructure

**Fully restructured the ARI package: api/ari/ deleted, pkg/ari root files deleted, pkg/ari/api/ + pkg/ari/server/ + pkg/ari/client/ established; all 35+ consumers migrated; make build + go test ./... both pass.**

## What Happened

S02 completed the ARI package restructure in two tasks.

**T01 — Create destination packages (additive only)**
Created 7 new files across three sub-packages:
- `pkg/ari/api/`: `types.go` (ARI wire types), `domain.go` (domain models), `methods.go` (ARI method constants extracted from api/methods.go — Workspace*, AgentRun*, Agent* only; shim constants deferred to S03)
- `pkg/ari/server/`: `service.go` (interfaces + RPC helpers from api/ari/service.go), `registry.go` (Registry from pkg/ari/registry.go)
- `pkg/ari/client/`: `typed.go` (typed workspace/agentrun/agent clients from api/ari/client.go), `simple.go` (simple JSON-RPC Client from pkg/ari/client.go)

All source packages were changed to their respective sub-package names (`api`, `server`, `client`). Bare type references in service.go and typed.go were qualified with `pkgariapi.`. A deviation was discovered: `registry.go` in T01 temporarily retained the old `api/ari` import because `pkg/store` still used the old type — this was resolved in T02.

**T02 — Migrate consumers, move test files, delete old source files**
Executed 9 consumer migration groups:
- Groups 1–2: `pkg/ari/server/server.go` and `pkg/ari/client/client.go` — updated imports, removed inter-package qualifiers for now-same-package types (RegisterWorkspaceService, WorkspaceClient, etc.)
- Group 3: `pkg/store/` (6 files) — Rule A import swap
- Group 4: `pkg/agentd/` (7 files) — Rule A; `process.go` and `shim_boundary_test.go` intentionally retained bare `api` import for shim constants (S03 scope)
- Group 5: `pkg/workspace/` (2 files) — Rule A
- Group 6: `tests/integration/` (6 files) — Rule A for api/ari; Rule C (pkg/ari → pkg/ari/client) for session/restart/real_cli tests
- Group 7: `cmd/agentdctl/` (9 files) — Rule B consolidation: removed both `api/ari` and bare `api` imports, added single `pkgariapi "pkg/ari/api"`, updated all method constants
- Group 8: `cmd/agentdctl/subcommands/root.go` + `cliutil/cliutil.go` — Rule C
- Group 9: `cmd/agentd/subcommands/server/command.go` — Rule D (`ari.NewRegistry()` → `ariserver.NewRegistry()`)

Three test files were moved into their new sub-packages:
- `pkg/ari/server_test.go` → `pkg/ari/server/server_test.go` (package ari_test → server_test; `ari.Client/NewRegistry` → `ariclient.Client/ariserver.NewRegistry`)
- `pkg/ari/registry_test.go` → `pkg/ari/server/registry_test.go` (package ari → server)
- `pkg/ari/client_test.go` → `pkg/ari/client/simple_test.go` (package ari → client; stdlib-only, no import changes)

All old source files deleted: `api/ari/{types,domain,service,client}.go`, `api/ari/` directory, `pkg/ari/registry.go`, `pkg/ari/client.go`, and the three original test files.

**Key adaptations discovered during execution:**
1. `pkg/ari/client/client.go`: After mechanical `apiari.` → `pkgariapi.` substitution, `WorkspaceClient`/`AgentRunClient`/`AgentClient` were wrongly prefixed — these types live in `typed.go` in the same package and need no qualifier.
2. `pkg/ari/server/server.go`: `RegisterWorkspaceService` etc. are in `service.go` in the same `server` package — no qualifier needed.
3. `pkg/ari/registry.go` (pre-deletion): needed a temporary api/ari → pkgariapi update to compile alongside the updated pkg/store interface before deletion.

All 6 must-have checks verified: zero api/ari imports, zero bare pkg/ari imports, api/ari directory deleted, pkg/ari root files deleted, pkg/ari/api/ has types.go+domain.go+methods.go, make build exits 0, go test ./... all packages pass.

## Verification

1. `rg '"github.com/zoumo/oar/api/ari"' --type go` → exit 1 (0 matches) ✅
2. `rg '"github.com/zoumo/oar/pkg/ari"[^/]' --type go` → exit 1 (0 matches) ✅
3. `make build` → exit 0, both agentd + agentdctl produced ✅
4. `go test ./...` → all packages pass (pkg/ari/client 1.3s, pkg/ari/server 3.3s, pkg/store 5.1s, pkg/agentd 9.5s, pkg/workspace 28s, tests/integration 107.9s) ✅
5. `test ! -d api/ari` → PASS ✅
6. `test ! -f pkg/ari/registry.go && test ! -f pkg/ari/client.go` → PASS ✅
7. `ls pkg/ari/api/` → domain.go methods.go types.go ✅

## Requirements Advanced

None.

## Requirements Validated

None.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

T01: pkg/ari/server/registry.go temporarily retained api/ari import (store interface not yet migrated); resolved in T02. T02: mechanical apiari→pkgariapi substitution required manual correction in client.go (same-package types) and server.go (same-package Register functions). Pre-deletion update to pkg/ari/registry.go (old file) was required but not in plan.

## Known Limitations

pkg/agentd/process.go and shim_boundary_test.go still import bare `api` package for shim method constants (api.MethodShimEvent, api.CategoryRuntime, api.EventTypeStateChange) — these will be migrated in S03 when api/shim and api/methods.go are restructured.

## Follow-ups

S03 must migrate api/methods.go shim constants and delete api/shim/ and the api/ directory root.

## Files Created/Modified

- `pkg/ari/api/types.go` — New: ARI wire types (WorkspaceCreateParams, AgentRunCreateParams, etc.) — moved from api/ari/types.go, package api
- `pkg/ari/api/domain.go` — New: ARI domain models (WorkspaceInfo, AgentRunInfo, AgentInfo, etc.) — moved from api/ari/domain.go, package api
- `pkg/ari/api/methods.go` — New: ARI method constants (MethodWorkspaceCreate et al., MethodAgentRunCreate et al., MethodAgentSet et al.) extracted from api/methods.go
- `pkg/ari/server/service.go` — New: ARI service interfaces (WorkspaceService, AgentRunService, AgentService) + RPC helpers — moved from api/ari/service.go, package server
- `pkg/ari/server/registry.go` — New: Registry + RegisterXService helpers — moved from pkg/ari/registry.go, package server
- `pkg/ari/server/server.go` — Updated: removed ariregistry import (now same package), replaced apiari with pkgariapi
- `pkg/ari/server/server_test.go` — Moved from pkg/ari/server_test.go: package ari_test → server_test; updated all imports to new paths
- `pkg/ari/server/registry_test.go` — Moved from pkg/ari/registry_test.go: package ari → server; updated apiari → pkgariapi
- `pkg/ari/client/typed.go` — New: typed workspace/agentrun/agent clients — moved from api/ari/client.go, package client
- `pkg/ari/client/simple.go` — New: simple JSON-RPC Client — moved from pkg/ari/client.go, package client
- `pkg/ari/client/client.go` — Updated: replaced apiari import with pkgariapi; stripped pkgariapi qualifier from same-package types
- `pkg/ari/client/simple_test.go` — Moved from pkg/ari/client_test.go: package ari → client; stdlib-only, no import changes
- `pkg/store/workspace.go` — Updated: apiari → pkgariapi import alias
- `pkg/store/agent.go` — Updated: apiari → pkgariapi import alias
- `pkg/store/agentrun.go` — Updated: apiari → pkgariapi import alias
- `pkg/agentd/agent.go` — Updated: apiari → pkgariapi
- `pkg/agentd/recovery.go` — Updated: apiari → pkgariapi
- `pkg/agentd/process.go` — Updated: apiari → pkgariapi; bare api import retained for shim constants (S03)
- `pkg/workspace/manager.go` — Updated: apiari → pkgariapi
- `cmd/agentdctl/subcommands/workspace/command.go` — Updated: Rule B consolidation — removed bare api + api/ari, single pkgariapi import
- `cmd/agentdctl/subcommands/up/command.go` — Updated: Rule B consolidation
- `cmd/agentdctl/subcommands/root.go` — Updated: Rule C — ariclient pkg/ari → pkg/ari/client
- `cmd/agentdctl/subcommands/cliutil/cliutil.go` — Updated: Rule C
- `cmd/agentd/subcommands/server/command.go` — Updated: Rule D — pkg/ari → pkg/ari/server for NewRegistry
