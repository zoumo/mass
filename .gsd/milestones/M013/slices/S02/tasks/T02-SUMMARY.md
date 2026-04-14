---
id: T02
parent: S02
milestone: M013
key_files:
  - pkg/ari/server/server.go
  - pkg/ari/server/registry.go
  - pkg/ari/client/client.go
  - pkg/ari/server/server_test.go
  - pkg/ari/server/registry_test.go
  - pkg/ari/client/simple_test.go
  - pkg/store/workspace.go
  - pkg/store/agent.go
  - pkg/store/agentrun.go
  - pkg/agentd/agent.go
  - pkg/agentd/recovery.go
  - pkg/agentd/process.go
  - pkg/workspace/manager.go
  - cmd/agentdctl/subcommands/workspace/command.go
  - cmd/agentdctl/subcommands/up/command.go
  - cmd/agentdctl/subcommands/root.go
  - cmd/agentd/subcommands/server/command.go
key_decisions:
  - Removed pkgariapi prefix from WorkspaceClient/AgentRunClient/AgentClient in pkg/ari/client/client.go — these types are defined in pkg/ari/client/typed.go (same package) and need no qualifier
  - Unqualified RegisterWorkspaceService/RegisterAgentRunService/RegisterAgentService calls in pkg/ari/server/server.go — these Register functions are defined in service.go in the same server package
duration: 
verification_result: passed
completed_at: 2026-04-14T10:12:05.943Z
blocker_discovered: false
---

# T02: Migrated all 35+ api/ari and 9 pkg/ari-root consumers to new paths, moved 3 test files, deleted all old source files; make build + go test ./... both pass

**Migrated all 35+ api/ari and 9 pkg/ari-root consumers to new paths, moved 3 test files, deleted all old source files; make build + go test ./... both pass**

## What Happened

Executed the full consumer migration for the ARI package restructure. The work proceeded in 9 consumer groups plus test file moves and deletions.

**Consumer updates (Rules A-D):**
- Groups 1–2 (pkg/ari/server/server.go, pkg/ari/client/client.go): Replaced `apiari "api/ari"` → `pkgariapi "pkg/ari/api"`, removed `ariregistry "pkg/ari"` import, unqualified `Register*Service` calls (now same package), changed `*ariregistry.Registry` → `*Registry`.
- pkg/ari/server/registry.go: Replaced the deferred `api/ari` import retained from T01 with `pkgariapi`.
- Group 3 (pkg/store, 6 files): Simple apiari → pkgariapi alias + usage swap.
- Group 4 (pkg/agentd, 7 files): Same swap; process.go and shim_boundary_test.go intentionally retained the bare `api` import for shim constants (S03 work).
- Group 5 (pkg/workspace, 2 files): Same swap.
- Group 6 (tests/integration, 6 files): Changed `ari "api/ari"` alias → `pkgariapi`; changed `ariclient "pkg/ari"` → `ariclient "pkg/ari/client"` in session/restart/real_cli tests.
- Group 7 (cmd/agentdctl 8 files, Rule B): Removed bare `"github.com/zoumo/oar/api"` import, consolidated into single `pkgariapi "pkg/ari/api"`, updated all `api.MethodX` → `pkgariapi.MethodX` and `ari.X` → `pkgariapi.X`.
- Group 8 (root.go + cliutil.go, Rule C): Changed `ariclient "pkg/ari"` → `ariclient "pkg/ari/client"`.
- Group 9 (cmd/agentd/server/command.go, Rule D): Removed bare `pkg/ari` import, changed `ari.NewRegistry()` → `ariserver.NewRegistry()`.

**Test file moves:**
- pkg/ari/registry_test.go → pkg/ari/server/registry_test.go: Changed `package ari` → `package server`, updated `apiari` → `pkgariapi`.
- pkg/ari/client_test.go → pkg/ari/client/simple_test.go: Changed `package ari` → `package client`; stdlib-only file, no import changes needed.
- pkg/ari/server_test.go → pkg/ari/server/server_test.go: Changed `package ari_test` → `package server_test`, updated `apiari` → `pkgariapi`, replaced `ari.Client/NewClient/NewRegistry` → `ariclient.Client/NewClient` + `ariserver.NewRegistry`.

**Adaptation discovered during execution:** pkg/ari/client/client.go erroneously had `pkgariapi.WorkspaceClient` etc. after the blind `apiari.` → `pkgariapi.` substitution. These types (`WorkspaceClient`, `AgentRunClient`, `AgentClient`) are defined in `pkg/ari/client/typed.go` (same package), so the prefix was removed. Same for `NewWorkspaceClient` etc.

**Deletions:** api/ari/{types,domain,service,client}.go + rmdir api/ari/; pkg/ari/registry.go + pkg/ari/client.go + three old test files. The old `pkg/ari/registry.go` needed a temporary import update (api/ari → pkgariapi) to compile cleanly before deletion, because pkg/store now uses pkgariapi types.

## Verification

Four verification checks run in sequence:
1. `rg '"github.com/zoumo/oar/api/ari"' --type go` — exit 1 (PASS: 0 matches)
2. `rg '"github.com/zoumo/oar/pkg/ari"[^/]' --type go` — exit 1 (PASS: 0 matches)
3. `make build` — exit 0 (agentd + agentdctl both produced)
4. `go test ./... -count=1 -timeout=120s` — all packages pass including pkg/ari/server (7.5s), pkg/ari/client (1.3s), tests/integration (106.8s), pkg/store, pkg/agentd, pkg/workspace

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `rg '"github.com/zoumo/oar/api/ari"' --type go` | 1 | ✅ pass | 120ms |
| 2 | `rg '"github.com/zoumo/oar/pkg/ari"[^/]' --type go` | 1 | ✅ pass | 110ms |
| 3 | `make build` | 0 | ✅ pass | 3200ms |
| 4 | `go test ./... -count=1 -timeout=120s` | 0 | ✅ pass | 111300ms |

## Deviations

pkg/ari/client/client.go: after applying the blind `apiari.` → `pkgariapi.` substitution, the ARIClient struct fields were wrongly prefixed with `pkgariapi.` for types that live in the same `client` package (WorkspaceClient, AgentRunClient, AgentClient). Fixed by stripping the qualifier. This was a predictable adaptation since the plan noted typed.go types would be in the same package.

pkg/ari/registry.go (the old source file, pre-deletion): required a temporary import update (api/ari → pkgariapi) to compile cleanly alongside the updated pkg/store interface, before being deleted. The plan did not explicitly list this step but it was logically required.

## Known Issues

None.

## Files Created/Modified

- `pkg/ari/server/server.go`
- `pkg/ari/server/registry.go`
- `pkg/ari/client/client.go`
- `pkg/ari/server/server_test.go`
- `pkg/ari/server/registry_test.go`
- `pkg/ari/client/simple_test.go`
- `pkg/store/workspace.go`
- `pkg/store/agent.go`
- `pkg/store/agentrun.go`
- `pkg/agentd/agent.go`
- `pkg/agentd/recovery.go`
- `pkg/agentd/process.go`
- `pkg/workspace/manager.go`
- `cmd/agentdctl/subcommands/workspace/command.go`
- `cmd/agentdctl/subcommands/up/command.go`
- `cmd/agentdctl/subcommands/root.go`
- `cmd/agentd/subcommands/server/command.go`
