---
id: T01
parent: S02
milestone: M013
key_files:
  - pkg/ari/api/types.go
  - pkg/ari/api/domain.go
  - pkg/ari/api/methods.go
  - pkg/ari/server/service.go
  - pkg/ari/server/registry.go
  - pkg/ari/client/typed.go
  - pkg/ari/client/simple.go
key_decisions:
  - Retained api/ari import in pkg/ari/server/registry.go for T01 compile correctness — store interface migration deferred to T02
  - Used pkgariapi alias consistently in server/service.go and client/typed.go for all type qualifications
  - simple.go keeps original Client type name (not renamed) to match source pkg/ari/client.go exactly
duration: 
verification_result: passed
completed_at: 2026-04-14T09:53:14.879Z
blocker_discovered: false
---

# T01: Created 7 new pkg/ari/api/, pkg/ari/server/, pkg/ari/client/ target files; all compile cleanly alongside existing code

**Created 7 new pkg/ari/api/, pkg/ari/server/, pkg/ari/client/ target files; all compile cleanly alongside existing code**

## What Happened

Read all 7 source files (api/ari/types.go, api/ari/domain.go, api/ari/service.go, api/ari/client.go, api/methods.go, pkg/ari/registry.go, pkg/ari/client.go) before writing anything.

Created the three destination packages:

**pkg/ari/api/** (pure types):
- `types.go` — verbatim copy of api/ari/types.go with package renamed to `api`
- `domain.go` — verbatim copy of api/ari/domain.go with package renamed to `api`
- `methods.go` — new file containing only the ARI method constants (Workspace*, AgentRun*, Agent*) extracted from api/methods.go; shim methods excluded

**pkg/ari/server/** (interfaces + registry):
- `service.go` — copy of api/ari/service.go with package `server` and all bare type references qualified with `pkgariapi.`; callRPC/callRPCRaw helpers preserved
- `registry.go` — copy of pkg/ari/registry.go with package `server`; adaptation required (see Deviations)

**pkg/ari/client/** (typed + simple clients):
- `typed.go` — copy of api/ari/client.go with package `client`; `github.com/zoumo/oar/api` replaced by `pkgariapi "github.com/zoumo/oar/pkg/ari/api"` and all method constant references updated to `pkgariapi.MethodX`
- `simple.go` — verbatim copy of pkg/ari/client.go with package `client`; no import changes (stdlib only)

All 7 files were created additively; no existing files were modified or deleted.

## Verification

Ran three targeted build commands and one full build:
- `go build ./pkg/ari/api/...` → exit 0
- `go build ./pkg/ari/server/...` → exit 0
- `go build ./pkg/ari/client/...` → exit 0
- `make build` → exit 0 (agentd + agentdctl both built cleanly)

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go build ./pkg/ari/api/...` | 0 | ✅ pass | 850ms |
| 2 | `go build ./pkg/ari/server/...` | 0 | ✅ pass | 920ms |
| 3 | `go build ./pkg/ari/client/...` | 0 | ✅ pass | 780ms |
| 4 | `make build` | 0 | ✅ pass | 4200ms |

## Deviations

pkg/ari/server/registry.go: the plan called for replacing `apiari "github.com/zoumo/oar/api/ari"` entirely with `pkgariapi "github.com/zoumo/oar/pkg/ari/api"`. However, `pkg/store.ListWorkspaces` still accepts `*api/ari.WorkspaceFilter` (its migration is T02 work). Applying the full replacement caused a type mismatch compile error. Adaptation: `registry.go` retains the old `apiari` import with a comment noting it will be replaced in T02 when the store interface is migrated. No pkgariapi import is needed in registry.go since WorkspaceMeta uses only workspace.WorkspaceSpec and primitive types.

## Known Issues

pkg/ari/server/registry.go still imports api/ari; this is intentional and will be resolved in T02 when pkg/store is updated to use pkg/ari/api types.

## Files Created/Modified

- `pkg/ari/api/types.go`
- `pkg/ari/api/domain.go`
- `pkg/ari/api/methods.go`
- `pkg/ari/server/service.go`
- `pkg/ari/server/registry.go`
- `pkg/ari/client/typed.go`
- `pkg/ari/client/simple.go`
