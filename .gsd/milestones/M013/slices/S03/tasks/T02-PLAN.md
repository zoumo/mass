---
estimated_steps: 68
estimated_files: 19
skills_used: []
---

# T02: Migrate all consumers + delete api/ directory

Migrate every file that imports api/shim or bare api to the new paths, then delete api/. Apply changes in groups — each group should compile cleanly before moving to the next. Finish with deletion and full suite verification.

Module path: `github.com/zoumo/oar`

## Import alias strategy
- Files using `apishim "github.com/zoumo/oar/api/shim"` → change path to `apishim "github.com/zoumo/oar/pkg/shim/api"` (keep alias, zero call-site changes)
- Files using bare `"github.com/zoumo/oar/api/shim"` (3 test files in pkg/agentd) → add alias `shim "github.com/zoumo/oar/pkg/shim/api"` to preserve `shim.X` usage
- Files using bare `api` for EventType*/Category* outside pkg/events → add/use `"github.com/zoumo/oar/pkg/events"` and change `api.EventType*` → `events.EventType*`, `api.Category*` → `events.Category*`
- Files using bare `api` for MethodShimEvent/MethodSession*/MethodRuntime* → add `apishim` (or `shimapi`) import of `pkg/shim/api` and use `apishim.MethodX`
- Files using bare `api` for ARI workspace methods → add `pkgariapi "github.com/zoumo/oar/pkg/ari/api"` and use `pkgariapi.MethodWorkspaceX`

## Migration groups

### Group 1 — pkg/events/* (5 files): drop bare api import, same-package constants
All five files use api.EventType* and api.Category* constants that now live in pkg/events/constants.go (same package). Steps for each:
1. `pkg/events/types.go` — remove `"github.com/zoumo/oar/api"` import; strip `api.` prefix from all EventType* calls (e.g. `api.EventTypeText` → `EventTypeText`)
2. `pkg/events/shim_event.go` — same; also `api.EventTypeStateChange` → `EventTypeStateChange`; `api.CategoryRuntime` → `CategoryRuntime`; `api.CategorySession` → `CategorySession`
3. `pkg/events/translator.go` — same treatment for all `api.EventType*` and `api.Category*` references
4. `pkg/events/log_test.go` — remove api import; `api.CategorySession` → `CategorySession`; `api.CategoryRuntime` → `CategoryRuntime`
5. `pkg/events/translator_test.go` — same as log_test.go
Verify: `go test ./pkg/events/...`

### Group 2 — pkg/shim/* (2 files): swap apishim import path
6. `pkg/shim/client/client.go` — change `apishim "github.com/zoumo/oar/api/shim"` → `apishim "github.com/zoumo/oar/pkg/shim/api"`; no other changes
7. `pkg/shim/server/service.go` — change `apishim "github.com/zoumo/oar/api/shim"` → `apishim "github.com/zoumo/oar/pkg/shim/api"`; drop bare `"github.com/zoumo/oar/api"` import; replace `api.MethodShimEvent` with `apishim.MethodShimEvent` (2 occurrences)
Verify: `go build ./pkg/shim/...`

### Group 3 — pkg/agentd/* (7 files)
8. `pkg/agentd/recovery.go` — change `apishim "github.com/zoumo/oar/api/shim"` → `apishim "github.com/zoumo/oar/pkg/shim/api"`
9. `pkg/agentd/process.go` — change apishim import path; drop bare `api` import; `api.MethodShimEvent` → `apishim.MethodShimEvent`; `api.CategoryRuntime` → `events.CategoryRuntime`; `api.EventTypeStateChange` → `events.EventTypeStateChange` (pkg/events already imported)
10. `pkg/agentd/mock_shim_server_test.go` — change `apishim "github.com/zoumo/oar/api/shim"` → `apishim "github.com/zoumo/oar/pkg/shim/api"`; no other changes
11. `pkg/agentd/shim_boundary_test.go` — change apishim import path; drop bare `api` import; `api.MethodShimEvent` → `apishim.MethodShimEvent`; `api.CategoryRuntime` → `events.CategoryRuntime`; `api.EventTypeStateChange` → `events.EventTypeStateChange` (pkg/events already imported)
12. `pkg/agentd/recovery_test.go` — change bare `"github.com/zoumo/oar/api/shim"` → `shim "github.com/zoumo/oar/pkg/shim/api"` (add explicit alias `shim`); all `shim.X` call sites unchanged
13. `pkg/agentd/recovery_posture_test.go` — same treatment as recovery_test.go
14. `pkg/agentd/process_test.go` — same treatment as recovery_test.go (`shim.SessionPromptParams` call site preserved)
Verify: `go test ./pkg/agentd/...`

### Group 4 — pkg/ari/server (1 file)
15. `pkg/ari/server/server.go` — change `apishim "github.com/zoumo/oar/api/shim"` → `apishim "github.com/zoumo/oar/pkg/shim/api"`; no other changes needed (apishim.SessionPromptParams call sites preserved)
Verify: `go test ./pkg/ari/server/...`

### Group 5 — cmd/agentd/* (2 files)
16. `cmd/agentd/subcommands/shim/command.go` — change `apishim "github.com/zoumo/oar/api/shim"` → `apishim "github.com/zoumo/oar/pkg/shim/api"`; no other changes (only uses apishim.RegisterShimService)
17. `cmd/agentd/subcommands/workspacemcp/command.go` — drop bare `"github.com/zoumo/oar/api"` import; add `pkgariapi "github.com/zoumo/oar/pkg/ari/api"`; replace `api.MethodWorkspaceSend` → `pkgariapi.MethodWorkspaceSend`; replace `api.MethodWorkspaceStatus` → `pkgariapi.MethodWorkspaceStatus`

### Group 6 — cmd/agentdctl/subcommands/shim/* (2 files)
Both files use bare `api` for a mix of shim method constants AND event type constants:
18. `cmd/agentdctl/subcommands/shim/command.go` — drop bare `api` import; add `shimapi "github.com/zoumo/oar/pkg/shim/api"` and `"github.com/zoumo/oar/pkg/events"`; replace:
    - `api.MethodShimEvent` → `shimapi.MethodShimEvent`
    - `api.MethodSessionSubscribe` → `shimapi.MethodSessionSubscribe`
    - `api.MethodSessionPrompt` → `shimapi.MethodSessionPrompt`
    - `api.MethodSessionCancel` → `shimapi.MethodSessionCancel`
    - `api.MethodRuntimeStatus` → `shimapi.MethodRuntimeStatus`
    - `api.MethodRuntimeHistory` → `shimapi.MethodRuntimeHistory`
    - `api.MethodRuntimeStop` → `shimapi.MethodRuntimeStop`
    - `api.EventTypeStateChange` → `events.EventTypeStateChange`
    - `api.EventTypeTurnEnd` → `events.EventTypeTurnEnd`
    - `api.EventTypeText` → `events.EventTypeText`
    - `api.EventTypeThinking` → `events.EventTypeThinking`
    - `api.EventTypeToolCall` → `events.EventTypeToolCall`
    - `api.EventTypeToolResult` → `events.EventTypeToolResult`
19. `cmd/agentdctl/subcommands/shim/chat.go` — drop bare `api` import; add `shimapi "github.com/zoumo/oar/pkg/shim/api"`, `"github.com/zoumo/oar/pkg/events"`, and `pkgariapi "github.com/zoumo/oar/pkg/ari/api"`; apply the same `api.Method*` → `shimapi.Method*` and `api.EventType*` → `events.EventType*` replacements; also `api.MethodWorkspaceSend` → `pkgariapi.MethodWorkspaceSend`; `api.MethodWorkspaceStatus` → `pkgariapi.MethodWorkspaceStatus`
Verify: `go build ./cmd/...`

## Deletion
20. Delete source files and directories:
    ```bash
    rm api/shim/types.go api/shim/service.go api/shim/client.go
    rmdir api/shim
    rm api/events.go api/methods.go
    rmdir api
    ```

## Final verification
21. `make build` — exits 0, both binaries produced
22. `go test ./...` — all packages pass
23. `rg 'zoumo/oar/api"' --type go` — exits 1 (zero matches)
24. `rg '"github.com/zoumo/oar/api/shim"' --type go` — exits 1 (zero matches)
25. `test ! -d api` — passes

## Inputs

- `pkg/shim/api/types.go`
- `pkg/shim/api/service.go`
- `pkg/shim/api/client.go`
- `pkg/shim/api/methods.go`
- `pkg/events/constants.go`
- `pkg/events/types.go`
- `pkg/events/shim_event.go`
- `pkg/events/translator.go`
- `pkg/events/log_test.go`
- `pkg/events/translator_test.go`
- `pkg/shim/client/client.go`
- `pkg/shim/server/service.go`
- `pkg/agentd/recovery.go`
- `pkg/agentd/process.go`
- `pkg/agentd/mock_shim_server_test.go`
- `pkg/agentd/shim_boundary_test.go`
- `pkg/agentd/recovery_test.go`
- `pkg/agentd/recovery_posture_test.go`
- `pkg/agentd/process_test.go`
- `pkg/ari/server/server.go`
- `cmd/agentd/subcommands/shim/command.go`
- `cmd/agentd/subcommands/workspacemcp/command.go`
- `cmd/agentdctl/subcommands/shim/command.go`
- `cmd/agentdctl/subcommands/shim/chat.go`
- `api/shim/types.go`
- `api/shim/service.go`
- `api/shim/client.go`
- `api/events.go`
- `api/methods.go`

## Expected Output

- `pkg/events/types.go`
- `pkg/events/shim_event.go`
- `pkg/events/translator.go`
- `pkg/events/log_test.go`
- `pkg/events/translator_test.go`
- `pkg/shim/client/client.go`
- `pkg/shim/server/service.go`
- `pkg/agentd/recovery.go`
- `pkg/agentd/process.go`
- `pkg/agentd/mock_shim_server_test.go`
- `pkg/agentd/shim_boundary_test.go`
- `pkg/agentd/recovery_test.go`
- `pkg/agentd/recovery_posture_test.go`
- `pkg/agentd/process_test.go`
- `pkg/ari/server/server.go`
- `cmd/agentd/subcommands/shim/command.go`
- `cmd/agentd/subcommands/workspacemcp/command.go`
- `cmd/agentdctl/subcommands/shim/command.go`
- `cmd/agentdctl/subcommands/shim/chat.go`

## Verification

rg 'zoumo/oar/api"' --type go; echo $? # expect 1 (no matches)
rg '"github.com/zoumo/oar/api/shim"' --type go; echo $? # expect 1
test ! -d api && echo 'api/ deleted'
make build && go test ./...
