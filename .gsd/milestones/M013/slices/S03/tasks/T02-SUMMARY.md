---
id: T02
parent: S03
milestone: M013
key_files:
  - pkg/events/types.go
  - pkg/events/shim_event.go
  - pkg/events/translator.go
  - pkg/events/log_test.go
  - pkg/events/translator_test.go
  - pkg/shim/client/client.go
  - pkg/shim/server/service.go
  - pkg/agentd/recovery.go
  - pkg/agentd/process.go
  - pkg/agentd/mock_shim_server_test.go
  - pkg/agentd/shim_boundary_test.go
  - pkg/agentd/recovery_test.go
  - pkg/agentd/recovery_posture_test.go
  - pkg/agentd/process_test.go
  - pkg/ari/server/server.go
  - cmd/agentd/subcommands/shim/command.go
  - cmd/agentd/subcommands/workspacemcp/command.go
  - cmd/agentdctl/subcommands/shim/command.go
  - cmd/agentdctl/subcommands/shim/chat.go
key_decisions:
  - Used unqualified constant names in pkg/events/* since the new constants.go is in the same package — no import needed
  - Kept explicit `shim` alias for recovery_test/recovery_posture_test/process_test to preserve shim.X call-site readability without renaming
  - Did NOT add pkgariapi import to chat.go (planner anticipated workspace method refs that do not exist in that file)
duration: 
verification_result: passed
completed_at: 2026-04-14T10:45:51.489Z
blocker_discovered: false
---

# T02: Migrated all consumers from api/ and api/shim to pkg/shim/api/ and pkg/events/, then deleted the api/ directory; make build + go test ./... pass with zero legacy import targets remaining

**Migrated all consumers from api/ and api/shim to pkg/shim/api/ and pkg/events/, then deleted the api/ directory; make build + go test ./... pass with zero legacy import targets remaining**

## What Happened

This task executed the full consumer migration in 6 ordered groups and then deleted the legacy api/ directory.

**Group 1 — pkg/events/** (5 files): Dropped `"github.com/zoumo/oar/api"` import from types.go, shim_event.go, translator.go, log_test.go, translator_test.go. All `api.EventType*`, `api.CategorySession`, `api.CategoryRuntime` references became unqualified identifiers now resolved from the same-package constants.go (created in T01). `go test ./pkg/events/...` passed.

**Group 2 — pkg/shim/** (2 files): Swapped apishim import path in client/client.go. In server/service.go, dropped bare `"github.com/zoumo/oar/api"` import and replaced 2 occurrences of `api.MethodShimEvent` → `apishim.MethodShimEvent`. `go build ./pkg/shim/...` passed.

**Group 3 — pkg/agentd/** (7 files): recovery.go — simple apishim path swap. process.go — dropped bare api import, replaced `api.MethodShimEvent` → `apishim.MethodShimEvent`, `api.CategoryRuntime` → `events.CategoryRuntime`, `api.EventTypeStateChange` → `events.EventTypeStateChange`. mock_shim_server_test.go — simple apishim path swap. shim_boundary_test.go — same pattern as process.go. recovery_test.go, recovery_posture_test.go, process_test.go — changed bare `"github.com/zoumo/oar/api/shim"` → `shim "github.com/zoumo/oar/pkg/shim/api"` (explicit alias to preserve shim.X call sites). `go test ./pkg/agentd/...` passed (TestProcessManagerStart initial failure was a stale binary artifact; re-run confirmed pass).

**Group 4 — pkg/ari/server/server.go** (1 file): Simple apishim path swap. No call-site changes needed. `go build ./pkg/ari/server/...` passed.

**Group 5 — cmd/agentd/** (2 files): shim/command.go — simple apishim path swap. workspacemcp/command.go — dropped bare api import, added `pkgariapi "github.com/zoumo/oar/pkg/ari/api"`, replaced `api.MethodWorkspaceSend` and `api.MethodWorkspaceStatus`. `go build ./cmd/agentd/...` passed.

**Group 6 — cmd/agentdctl/subcommands/shim/** (2 files): command.go — dropped bare api, added `shimapi "github.com/zoumo/oar/pkg/shim/api"` and `"github.com/zoumo/oar/pkg/events"`, replaced all Method* and EventType* references. chat.go — same approach; the planner anticipated workspace method references in chat.go but they were not present, so the pkgariapi import was not added (would have been unused). `go build ./cmd/...` passed.

**Deletion**: `rm api/shim/{types,service,client}.go && rmdir api/shim && rm api/{events,methods}.go && rmdir api`. Directory confirmed absent.

**Final verification**: make build produced both binaries; `go test ./...` passed all packages including the 108s integration test suite. Both rg checks returned exit 1 (no legacy imports).

## Verification

Four checks from the task plan, all passing:
1. `rg 'zoumo/oar/api"' --type go; echo $?` → exit 1 (zero matches)
2. `rg '"github.com/zoumo/oar/api/shim"' --type go; echo $?` → exit 1 (zero matches)  
3. `test ! -d api && echo 'api/ deleted'` → printed 'api/ deleted'
4. `make build && go test ./...` → make build exit 0 (both binaries produced); go test ./... all packages pass including integration tests

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `rg 'zoumo/oar/api"' --type go; echo $?` | 1 | ✅ pass | 200ms |
| 2 | `rg '"github.com/zoumo/oar/api/shim"' --type go; echo $?` | 1 | ✅ pass | 200ms |
| 3 | `test ! -d api && echo 'api/ deleted'` | 0 | ✅ pass | 50ms |
| 4 | `make build` | 0 | ✅ pass | 4200ms |
| 5 | `go test ./...` | 0 | ✅ pass | 109700ms |

## Deviations

chat.go did not need the pkgariapi import — the planner expected MethodWorkspaceSend/Status references there but they were not present in the file. Import was omitted to avoid a compile error from unused imports.

## Known Issues

None.

## Files Created/Modified

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
