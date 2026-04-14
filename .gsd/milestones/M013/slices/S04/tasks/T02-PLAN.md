---
estimated_steps: 17
estimated_files: 12
skills_used: []
---

# T02: Move translator+log to pkg/shim/server/, runtime to pkg/shim/runtime/acp/, delete pkg/events/ and pkg/runtime/, final verification

This task completes the migration by moving the implementation files and deleting the source packages.

**Steps**:
1. Create `pkg/shim/server/translator.go` — copy content from `pkg/events/translator.go`, change `package events` → `package server`. All internal references to ShimEvent, EventType*, Category*, ContentBlock etc. now resolve from `apishim "github.com/zoumo/oar/pkg/shim/api"` — update all unqualified refs (previously same-package) to use the `apishim.` qualifier. Example: `ShimEvent` → `apishim.ShimEvent`, `CategoryRuntime` → `apishim.CategoryRuntime`, `EventTypeTurnStart` → `apishim.EventTypeTurnStart`, `TurnStartEvent{}` → `apishim.TurnStartEvent{}`, etc. Keep existing imports (log/slog, sync, time, acp, uuid). Add `apishim "github.com/zoumo/oar/pkg/shim/api"` import.
2. Create `pkg/shim/server/log.go` — copy content from `pkg/events/log.go`, change `package events` → `package server`. Update `ShimEvent` → `apishim.ShimEvent`, add `apishim` import. Keep `pkg/ndjson` import.
3. Edit `pkg/shim/server/service.go` — remove both the `"github.com/zoumo/oar/pkg/events"` and `"github.com/zoumo/oar/pkg/runtime"` imports. `events.Translator`, `events.ReadEventLog`, `events.ShimEvent` are now same-package (just `Translator`, `ReadEventLog`, `ShimEvent`). `runtime.Manager` is now `acp.Manager` — add import `acpruntime "github.com/zoumo/oar/pkg/shim/runtime/acp"` and replace `*runtime.Manager` with `*acpruntime.Manager`.
4. Move tests from `pkg/events/` to `pkg/shim/server/`: copy `pkg/events/translator_test.go` → `pkg/shim/server/translator_test.go` (change package to `server`; all pkg/events refs become unqualified since they're same-package now). Copy `pkg/events/log_test.go` → `pkg/shim/server/log_test.go` (same). Copy `pkg/events/wire_shape_test.go` → `pkg/shim/server/wire_shape_test.go` (same). Copy `pkg/events/translate_rich_test.go` → `pkg/shim/server/translate_rich_test.go` (same).
5. Create `pkg/shim/runtime/acp/` directory (mkdir). Create `pkg/shim/runtime/acp/runtime.go` — copy content from `pkg/runtime/runtime.go`, change `package runtime` → `package acp`. Imports stay the same (apiruntime, spec, etc.).
6. Create `pkg/shim/runtime/acp/client.go` — copy content from `pkg/runtime/client.go`, change `package runtime` → `package acp`.
7. Move runtime tests: copy `pkg/runtime/runtime_test.go` → `pkg/shim/runtime/acp/runtime_test.go` (change package `runtime_test` → `acp_test`; update import `pkgruntime "github.com/zoumo/oar/pkg/runtime"` → `acpruntime "github.com/zoumo/oar/pkg/shim/runtime/acp"`; replace `pkgruntime.` → `acpruntime.`). Copy `pkg/runtime/client_test.go` → `pkg/shim/runtime/acp/client_test.go` (change package `runtime` → `acp`; any self-referential symbols remain unqualified).
8. Edit `cmd/agentd/subcommands/shim/command.go` — change `"github.com/zoumo/oar/pkg/runtime"` → `acpruntime "github.com/zoumo/oar/pkg/shim/runtime/acp"`; change `"github.com/zoumo/oar/pkg/events"` → `shimsvr "github.com/zoumo/oar/pkg/shim/server"`; update `runtime.New` → `acpruntime.New`, `runtime.StateChange` → `acpruntime.StateChange`, `events.OpenEventLog` → `shimsvr.OpenEventLog`, `events.NewTranslator` → `shimsvr.NewTranslator`. NOTE: pkg/events/constants.go was already consumed by this file only via `events.OpenEventLog` and `events.NewTranslator` calls — no EventType* constants are used here.
9. Delete `pkg/events/` directory: `rm -rf pkg/events/`.
10. Delete `pkg/runtime/` directory: `rm -rf pkg/runtime/`.
11. Run `rg 'zoumo/oar/pkg/events' --type go` → must return exit 1 (zero matches).
12. Run `rg 'zoumo/oar/pkg/runtime"' --type go` → must return exit 1 (zero matches, note the trailing quote to avoid matching pkg/runtime-spec).
13. Run `make build` → exit 0.
14. Run `go test ./...` → all pass.
15. Run `go vet ./...` → exit 0.

## Inputs

- ``pkg/events/translator.go` — source for Translator; refs to ShimEvent/EventType* become apishim-qualified`
- ``pkg/events/log.go` — source for EventLog; ShimEvent refs become apishim-qualified`
- ``pkg/events/translator_test.go` — source for translator tests; move to pkg/shim/server/`
- ``pkg/events/log_test.go` — source for log tests; move to pkg/shim/server/`
- ``pkg/events/wire_shape_test.go` — source for wire shape tests; move to pkg/shim/server/`
- ``pkg/events/translate_rich_test.go` — source for rich translation tests; move to pkg/shim/server/`
- ``pkg/runtime/runtime.go` — source for ACP Manager; move to pkg/shim/runtime/acp/`
- ``pkg/runtime/client.go` — source for acpClient; move to pkg/shim/runtime/acp/`
- ``pkg/runtime/runtime_test.go` — source for Manager tests; move to pkg/shim/runtime/acp/`
- ``pkg/runtime/client_test.go` — source for acpClient tests; move to pkg/shim/runtime/acp/`
- ``pkg/shim/server/service.go` — update: drop pkg/events + pkg/runtime imports; both are now same-package or acpruntime`
- ``cmd/agentd/subcommands/shim/command.go` — update: pkg/runtime → pkg/shim/runtime/acp; pkg/events → pkg/shim/server`

## Expected Output

- ``pkg/shim/server/translator.go` — new file, package server`
- ``pkg/shim/server/log.go` — new file, package server`
- ``pkg/shim/server/translator_test.go` — new file, package server`
- ``pkg/shim/server/log_test.go` — new file, package server`
- ``pkg/shim/server/wire_shape_test.go` — new file, package server`
- ``pkg/shim/server/translate_rich_test.go` — new file, package server`
- ``pkg/shim/server/service.go` — updated; no pkg/events or pkg/runtime imports`
- ``pkg/shim/runtime/acp/runtime.go` — new file, package acp`
- ``pkg/shim/runtime/acp/client.go` — new file, package acp`
- ``pkg/shim/runtime/acp/runtime_test.go` — new file, package acp_test`
- ``pkg/shim/runtime/acp/client_test.go` — new file, package acp`
- ``cmd/agentd/subcommands/shim/command.go` — updated; no pkg/events or pkg/runtime imports`

## Verification

rg 'zoumo/oar/pkg/events' --type go; rg 'zoumo/oar/pkg/runtime"' --type go; make build; go test ./...; go vet ./...
