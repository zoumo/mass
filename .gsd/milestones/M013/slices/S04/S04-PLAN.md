# S04: Events impl + ACP runtime migration + final verification

**Goal:** Move pkg/events/ wire types (ShimEvent, typed events, EventType*/Category* constants) into pkg/shim/api/; move translator.go and log.go into pkg/shim/server/; move pkg/runtime/ into pkg/shim/runtime/acp/; update all consumers; delete the now-empty pkg/events/ and pkg/runtime/ directories.
**Demo:** After this: make build + go test ./... + go vet ./... all pass; pkg/events/ and pkg/runtime/ do not exist; pkg/shim/server/ has translator.go and log.go; pkg/shim/runtime/acp/ has the ACP runtime.

## Must-Haves

- make build + go test ./... + go vet ./... all pass; pkg/events/ and pkg/runtime/ do not exist; pkg/shim/server/ has translator.go and log.go; pkg/shim/runtime/acp/ has the ACP runtime; no file in the repo imports github.com/zoumo/oar/pkg/events or github.com/zoumo/oar/pkg/runtime.

## Proof Level

- This slice proves: final-assembly — make build + go test ./... + go vet ./... all pass

## Integration Closure

cmd/agentd/subcommands/shim/command.go is the top-level wiring point — it constructs runtime.Manager and events.Translator and passes them to shimserver.New(). After migration it imports pkg/shim/runtime/acp and pkg/shim/server instead. pkg/shim/server/service.go becomes a pure same-package file (no cross-package imports for events or runtime). All agentd and cmd consumers update their pkg/events → pkg/shim/api import.

## Verification

- No observability surface changes — this is a pure structural migration. Translator, EventLog, and ShimEvent keep all their existing behavior; they just live in different package paths.

## Tasks

- [x] **T01: Add events wire types to pkg/shim/api/ and update all external consumers** `est:2h`
  This task is purely additive + consumer-rewrite for the wire-type layer. It does NOT delete pkg/events/ yet.

**Why this order**: pkg/shim/api/types.go already imports pkg/events for events.ShimEvent. The cleanest fix is to move ShimEvent (and all event-typed structs and constants) into pkg/shim/api itself — making types.go self-contained. External consumers of events.ShimEvent and events.EventType* then point to pkg/shim/api instead. translator.go and log.go are NOT moved here because they depend on pkg/events types being in the same package; once the types are in pkg/shim/api, T02 can move translator+log to pkg/shim/server and change their package declaration safely.

**Steps**:
1. Create `pkg/shim/api/shim_event.go` — copy content from `pkg/events/shim_event.go`, change `package events` → `package api`. All types (ShimEvent, PhaseForEvent) land in the api package.
2. Create `pkg/shim/api/event_types.go` — copy content from `pkg/events/types.go`, change `package events` → `package api`. All typed event structs (TextEvent, ToolCallEvent, StateChangeEvent, ContentBlock and helpers, etc.) land in api package.
3. Create `pkg/shim/api/event_constants.go` — copy content from `pkg/events/constants.go`, change `package events` → `package api`. EventType* and Category* constants land in api package.
4. Edit `pkg/shim/api/types.go` — remove the `"github.com/zoumo/oar/pkg/events"` import; all `events.ShimEvent` references become bare `ShimEvent` (same package).
5. Edit `pkg/shim/client/client.go` — change import from `"github.com/zoumo/oar/pkg/events"` to `apishim "github.com/zoumo/oar/pkg/shim/api"` (it already imports apishim); update `events.ShimEvent` → `apishim.ShimEvent`.
6. Edit `pkg/ari/server/server_test.go` — change `"github.com/zoumo/oar/pkg/events"` to `apishim "github.com/zoumo/oar/pkg/shim/api"` (already has shimapi import); replace `events.ShimEvent` → `apishim.ShimEvent`.
7. Edit `pkg/agentd/process.go` — change `"github.com/zoumo/oar/pkg/events"` import to `apishim "github.com/zoumo/oar/pkg/shim/api"`; replace all `events.ShimEvent`, `events.CategoryRuntime`, `events.EventTypeStateChange`, `events.StateChangeEvent` → `apishim.*`.
8. Edit `pkg/agentd/recovery.go` — same pattern: events import → apishim; replace events.ShimEvent usage.
9. Edit `pkg/agentd/mock_shim_server_test.go` — events import → apishim; replace events.ShimEvent and events.TextEvent.
10. Edit `pkg/agentd/shim_boundary_test.go` — events import → apishim; replace events.ShimEvent.
11. Edit `pkg/agentd/process_test.go` — events import → apishim; replace events.ShimEvent, events.TextEvent, events.TurnEndEvent.
12. Edit `cmd/agentdctl/subcommands/shim/command.go` — events import → apishim; replace all events.EventType* constants.
13. Edit `cmd/agentdctl/subcommands/shim/chat.go` — events import → apishim; replace all events.EventType* constants.
14. Run `go build ./pkg/shim/api/... ./pkg/agentd/... ./pkg/ari/server/... ./cmd/...` to verify zero errors. Then `make build`.
  - Files: `pkg/shim/api/shim_event.go`, `pkg/shim/api/event_types.go`, `pkg/shim/api/event_constants.go`, `pkg/shim/api/types.go`, `pkg/shim/client/client.go`, `pkg/ari/server/server_test.go`, `pkg/agentd/process.go`, `pkg/agentd/recovery.go`, `pkg/agentd/mock_shim_server_test.go`, `pkg/agentd/shim_boundary_test.go`, `pkg/agentd/process_test.go`, `cmd/agentdctl/subcommands/shim/command.go`, `cmd/agentdctl/subcommands/shim/chat.go`
  - Verify: go build ./pkg/shim/api/... && go build ./pkg/agentd/... && go build ./pkg/ari/server/... && go build ./cmd/... && make build

- [x] **T02: Move translator+log to pkg/shim/server/, runtime to pkg/shim/runtime/acp/, delete pkg/events/ and pkg/runtime/, final verification** `est:2h`
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
  - Files: `pkg/shim/server/translator.go`, `pkg/shim/server/log.go`, `pkg/shim/server/translator_test.go`, `pkg/shim/server/log_test.go`, `pkg/shim/server/wire_shape_test.go`, `pkg/shim/server/translate_rich_test.go`, `pkg/shim/server/service.go`, `pkg/shim/runtime/acp/runtime.go`, `pkg/shim/runtime/acp/client.go`, `pkg/shim/runtime/acp/runtime_test.go`, `pkg/shim/runtime/acp/client_test.go`, `cmd/agentd/subcommands/shim/command.go`
  - Verify: rg 'zoumo/oar/pkg/events' --type go; rg 'zoumo/oar/pkg/runtime"' --type go; make build; go test ./...; go vet ./...

## Files Likely Touched

- pkg/shim/api/shim_event.go
- pkg/shim/api/event_types.go
- pkg/shim/api/event_constants.go
- pkg/shim/api/types.go
- pkg/shim/client/client.go
- pkg/ari/server/server_test.go
- pkg/agentd/process.go
- pkg/agentd/recovery.go
- pkg/agentd/mock_shim_server_test.go
- pkg/agentd/shim_boundary_test.go
- pkg/agentd/process_test.go
- cmd/agentdctl/subcommands/shim/command.go
- cmd/agentdctl/subcommands/shim/chat.go
- pkg/shim/server/translator.go
- pkg/shim/server/log.go
- pkg/shim/server/translator_test.go
- pkg/shim/server/log_test.go
- pkg/shim/server/wire_shape_test.go
- pkg/shim/server/translate_rich_test.go
- pkg/shim/server/service.go
- pkg/shim/runtime/acp/runtime.go
- pkg/shim/runtime/acp/client.go
- pkg/shim/runtime/acp/runtime_test.go
- pkg/shim/runtime/acp/client_test.go
- cmd/agentd/subcommands/shim/command.go
