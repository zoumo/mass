---
estimated_steps: 91
estimated_files: 38
skills_used: []
---

# T02: Migrate all consumers, move test files, delete old source files

Update every file that imports api/ari or pkg/ari (root) to use the new paths. Move test files to new locations. Delete the old source files. This task completes the restructure.

## Import mapping rules

**Rule A** — `api/ari` → `pkg/ari/api` for pure type users:
- Old: `apiari "github.com/zoumo/oar/api/ari"` (or alias `ari`)
- New: `pkgariapi "github.com/zoumo/oar/pkg/ari/api"` (keep same alias `apiari` or use `pkgariapi`)
- Replace all `apiari.X` → `pkgariapi.X` (or new alias)

**Rule B** — files with BOTH `api/ari` (types) AND bare `api` (ARI method constants only): consolidate both into a single `pkg/ari/api` import, remove the bare `api` import
- Replace `api.MethodWorkspaceX` → `pkgariapi.MethodWorkspaceX` etc.
- Replace `ari.X` (type) → `pkgariapi.X`

**Rule C** — `pkg/ari` root (Client) → `pkg/ari/client`:
- Old: `ariclient "github.com/zoumo/oar/pkg/ari"` (or just `ari`)
- New: `ariclient "github.com/zoumo/oar/pkg/ari/client"`
- `ariclient.NewClient` / `ariclient.Client` are unchanged

**Rule D** — `pkg/ari` root (Registry) → `pkg/ari/server`:
- Old: `ariregistry "github.com/zoumo/oar/pkg/ari"` or `"github.com/zoumo/oar/pkg/ari"` for NewRegistry
- New: registry is now in pkg/ari/server — for external callers use `ariserver "github.com/zoumo/oar/pkg/ari/server"` and `ariserver.NewRegistry()`

## Consumer update groups

**Group 1 — pkg/ari/server/server.go** (Rule A + Rule D in same file):
- Change `apiari` import → `pkgariapi "github.com/zoumo/oar/pkg/ari/api"`
- Remove `ariregistry "github.com/zoumo/oar/pkg/ari"` — registry is now in same package, use `NewRegistry()` / `Registry` directly (no import needed)
- Update all `apiari.X` → `pkgariapi.X`
- Update `ariregistry.NewRegistry()` → `NewRegistry()`, `*ariregistry.Registry` → `*Registry`

**Group 2 — pkg/ari/client/client.go** (Rule A):
- Change `apiari` → `pkgariapi "github.com/zoumo/oar/pkg/ari/api"`
- Update field types: `*apiari.WorkspaceClient` → `*pkgariapi.WorkspaceClient` etc.
- Update constructor body similarly

**Group 3 — pkg/store/** (Rule A, 6 files):
- `pkg/store/workspace.go`, `pkg/store/agent.go`, `pkg/store/agentrun.go`
- `pkg/store/workspace_test.go`, `pkg/store/agentrun_test.go`, `pkg/store/agent_test.go`
- All: change `apiari` import to `pkgariapi "github.com/zoumo/oar/pkg/ari/api"`, update all `apiari.X` → `pkgariapi.X`

**Group 4 — pkg/agentd/** (Rules A and B-partial, 7 files):
- `pkg/agentd/agent.go`, `pkg/agentd/recovery.go` — Rule A only (no bare api import)
- `pkg/agentd/process.go` — Rule A for api/ari; the bare `api` import STAYS (it uses `api.MethodShimEvent`, `api.CategoryRuntime`, `api.EventTypeStateChange` which are shim constants not migrated until S03)
- `pkg/agentd/agent_test.go`, `pkg/agentd/recovery_test.go`, `pkg/agentd/process_test.go` — Rule A only
- `pkg/agentd/shim_boundary_test.go` — Rule A for api/ari; bare `api` import STAYS (uses `api.MethodShimEvent` — shim constant, S03)

**Group 5 — pkg/workspace/** (Rule A, 2 files):
- `pkg/workspace/manager.go`, `pkg/workspace/manager_test.go` — Rule A

**Group 6 — tests/integration/** (Rules A and C, 6 files):
- `tests/integration/runtime_test.go`, `tests/integration/e2e_test.go`, `tests/integration/concurrent_test.go` — Rule A only
- `tests/integration/session_test.go`, `tests/integration/restart_test.go`, `tests/integration/real_cli_test.go` — Rule A (api/ari) + Rule C (pkg/ari Client → pkg/ari/client)

**Group 7 — cmd/agentdctl/** (Rule B, ~9 files — consolidate api + api/ari into single pkg/ari/api import):
- `cmd/agentdctl/subcommands/workspace/command.go`
- `cmd/agentdctl/subcommands/workspace/create/file.go`, `git.go`, `empty.go`, `local.go`
- `cmd/agentdctl/subcommands/agent/command.go`
- `cmd/agentdctl/subcommands/agentrun/command.go`
- `cmd/agentdctl/subcommands/daemon/command.go`
- `cmd/agentdctl/subcommands/up/command.go`
- All these files use both `api/ari` for types AND bare `api` for ARI method constants (Workspace*, AgentRun*, Agent* only — no shim constants). Consolidate: remove both old imports, add single `pkgariapi "github.com/zoumo/oar/pkg/ari/api"`, update all `api.MethodX` → `pkgariapi.MethodX` and `ari.X` → `pkgariapi.X`.

**Group 8 — cmd/agentdctl root + cliutil** (Rule C):
- `cmd/agentdctl/subcommands/root.go` — Rule C: `ariclient "pkg/ari"` → `ariclient "pkg/ari/client"`
- `cmd/agentdctl/subcommands/cliutil/cliutil.go` — Rule C same

**Group 9 — cmd/agentd/** (Rules A and D):
- `cmd/agentd/subcommands/server/command.go` — Rule D: `ari "github.com/zoumo/oar/pkg/ari"` → `ariserver "github.com/zoumo/oar/pkg/ari/server"`, update `ari.NewRegistry()` → `ariserver.NewRegistry()`

## Test file moves (write new + delete old)

**Move pkg/ari/server_test.go → pkg/ari/server/server_test.go** (809 lines):
- Change `package ari_test` → `package server_test`
- `apiari "github.com/zoumo/oar/api/ari"` → `pkgariapi "github.com/zoumo/oar/pkg/ari/api"` (Rule A)
- `"github.com/zoumo/oar/pkg/ari"` (used as `ari.Client` and `ari.NewRegistry()`):
  - Add `ariclient "github.com/zoumo/oar/pkg/ari/client"` for Client
  - The existing `ariserver "github.com/zoumo/oar/pkg/ari/server"` already present covers Registry
  - Remove the bare `"github.com/zoumo/oar/pkg/ari"` import
  - Replace `ari.NewClient(sockPath)` → `ariclient.NewClient(sockPath)`
  - Replace `ari.NewRegistry()` → `ariserver.NewRegistry()`
  - Replace `*ari.Client` → `*ariclient.Client`
  - Replace all `apiari.X` → `pkgariapi.X`
- Delete the original `pkg/ari/server_test.go` after creating the new file

**Move pkg/ari/registry_test.go → pkg/ari/server/registry_test.go** (167 lines):
- Change `package ari` → `package server`
- `apiari "github.com/zoumo/oar/api/ari"` → `pkgariapi "github.com/zoumo/oar/pkg/ari/api"` (Rule A)
- All `apiari.X` → `pkgariapi.X`
- Remove bare `"github.com/zoumo/oar/pkg/ari"` import if present (registry types are now same package)
- Delete `pkg/ari/registry_test.go` after creating new file

**Move pkg/ari/client_test.go → pkg/ari/client/simple_test.go** (278 lines):
- Change `package ari` → `package client`
- No import path changes needed (file only imports stdlib)
- References to `NewClient`, `Client` are now in same package — no prefix needed
- Delete `pkg/ari/client_test.go` after creating new file

## Source file deletions

After all consumers are updated and test files moved:
```
rm api/ari/types.go api/ari/domain.go api/ari/service.go api/ari/client.go
rmdir api/ari/
rm pkg/ari/registry.go pkg/ari/client.go
```
(The test files at pkg/ari/*.go root were deleted after their moves above)

## Verification sequence

1. `rg '"github.com/zoumo/oar/api/ari"' --type go` — must return exit 1 (0 matches)
2. `rg '"github.com/zoumo/oar/pkg/ari"[^/]' --type go` — must return exit 1 (0 matches for bare pkg/ari root)
3. `make build` — must exit 0, both binaries produced
4. `go test ./...` — all packages pass

If step 1-2 finds remaining consumers, update them. Compile errors from `make build` reveal missed consumer updates — follow the cascade.

## Inputs

- `pkg/ari/api/types.go`
- `pkg/ari/api/domain.go`
- `pkg/ari/api/methods.go`
- `pkg/ari/server/service.go`
- `pkg/ari/server/registry.go`
- `pkg/ari/client/typed.go`
- `pkg/ari/client/simple.go`
- `pkg/ari/server_test.go`
- `pkg/ari/registry_test.go`
- `pkg/ari/client_test.go`

## Expected Output

- `pkg/ari/server/server_test.go`
- `pkg/ari/server/registry_test.go`
- `pkg/ari/client/simple_test.go`

## Verification

rg '"github.com/zoumo/oar/api/ari"' --type go; make build; go test ./...
