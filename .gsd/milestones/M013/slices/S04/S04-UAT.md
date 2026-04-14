# S04: Events impl + ACP runtime migration + final verification — UAT

**Milestone:** M013
**Written:** 2026-04-14T11:43:29.693Z

## UAT: S04 — Events impl + ACP runtime migration + final verification

### Preconditions
- Go toolchain available (`go version` ≥ 1.22)
- Working directory: `/Users/jim/code/zoumo/open-agent-runtime`
- Repo is in the post-S04 state (S01–S04 all merged)

---

### Test 1: Old package import paths are gone from the entire codebase

**Purpose:** Confirm pkg/events and pkg/runtime (with trailing quote) have no remaining importers.

Steps:
1. Run `rg 'zoumo/oar/pkg/events' --type go`
2. Run `rg 'zoumo/oar/pkg/runtime"' --type go` (trailing quote avoids matching pkg/runtime-spec)

Expected outcome:
- Both commands exit with code 1 (ripgrep exit 1 = zero matches)
- No file paths printed to stdout

---

### Test 2: Source directories no longer exist

**Purpose:** Confirm the source packages were deleted, not just emptied.

Steps:
1. Run `ls pkg/events`
2. Run `ls pkg/runtime`

Expected outcome:
- Both commands print "No such file or directory" and exit non-zero

---

### Test 3: Target directories exist and contain the expected files

**Purpose:** Confirm translator+log landed in pkg/shim/server/ and runtime landed in pkg/shim/runtime/acp/.

Steps:
1. Run `ls pkg/shim/server/`
2. Run `ls pkg/shim/runtime/acp/`

Expected outcome for pkg/shim/server/:
- translator.go, log.go, service.go (implementation)
- translator_test.go, log_test.go, wire_shape_test.go, translate_rich_test.go (tests)

Expected outcome for pkg/shim/runtime/acp/:
- runtime.go, client.go (implementation)
- runtime_test.go, client_test.go (tests)

---

### Test 4: Event wire types are in pkg/shim/api

**Purpose:** Confirm ShimEvent, typed event structs, and EventType*/Category* constants are in pkg/shim/api.

Steps:
1. Run `ls pkg/shim/api/`
2. Run `grep 'ShimEvent' pkg/shim/api/shim_event.go`
3. Run `grep 'EventTypeTurnStart\|EventTypeText\|CategoryRuntime' pkg/shim/api/event_constants.go`
4. Run `grep 'TextEvent\|ToolCallEvent\|StateChangeEvent' pkg/shim/api/event_types.go`

Expected outcome:
- shim_event.go, event_types.go, event_constants.go all present
- All grep commands return matching lines and exit 0

---

### Test 5: make build produces both binaries cleanly

**Purpose:** End-to-end build verification.

Steps:
1. Run `make build`

Expected outcome:
- Exit code 0
- `bin/agentd` and `bin/agentdctl` produced (or updated)
- No compile errors

---

### Test 6: All Go tests pass including new package locations

**Purpose:** Confirm migrated tests pass under their new package paths.

Steps:
1. Run `go test ./pkg/shim/server/... -v -count=1` and observe translator, log, wire_shape, translate_rich test functions
2. Run `go test ./pkg/shim/runtime/acp/... -v -count=1` and observe runtime and client test functions
3. Run `go test ./pkg/agentd/... -count=1`
4. Run `go test ./pkg/ari/server/... -count=1`

Expected outcome:
- All commands exit 0
- No test failures
- pkg/shim/server shows TestTranslator*, TestLog*, TestWireShape*, TestTranslateRich* passing
- pkg/shim/runtime/acp shows TestRuntime*, TestClient* passing

---

### Test 7: go vet clean for all first-party packages

**Purpose:** Confirm no vet issues were introduced by the migration.

Steps:
1. Run `go vet ./pkg/... ./cmd/...`

Expected outcome:
- Exit code 0
- No output (zero issues)

Note: `go vet ./...` will show a pre-existing lock-copy issue in `third_party/charmbracelet/crush/csync/maps.go` — this is expected, unrelated to S04, and present before this milestone.

---

### Test 8: Integration tests pass

**Purpose:** Confirm the full agentd stack works end-to-end after the structural migration.

Steps:
1. Run `go test ./tests/integration/... -count=1 -timeout 180s`

Expected outcome:
- Exit code 0
- All integration tests pass (TestEndToEndPipeline, TestAgentdRestartRecovery, etc.)

---

### Test 9: EventTypeOf exported accessor exists in pkg/shim/api

**Purpose:** Confirm the sealed-interface bridge function is present (needed by pkg/shim/server/translator.go).

Steps:
1. Run `grep 'func EventTypeOf' pkg/shim/api/event_types.go`

Expected outcome:
- Prints `func EventTypeOf(ev Event) string {` (or similar)
- Exit code 0

---

### Edge Case: pkg/shim/api has no import of pkg/events

**Purpose:** Confirm pkg/shim/api is self-contained and does not re-import the deleted package.

Steps:
1. Run `grep 'pkg/events' pkg/shim/api/*.go`

Expected outcome:
- No matches (exit code 1 or no output)
- pkg/shim/api is fully self-contained
