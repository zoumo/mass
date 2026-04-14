# S03: Shim package restructure + api/ deletion — UAT

**Milestone:** M013
**Written:** 2026-04-14T10:52:54.573Z

## UAT: S03 — Shim package restructure + api/ deletion

### Preconditions
- Working directory: `/Users/jim/code/zoumo/open-agent-runtime`
- Go toolchain available (`go build`, `go test`)
- `make` available

---

### TC-01: api/ directory is gone

**Goal:** Verify the legacy `api/` directory was deleted and no longer exists as an import target.

**Steps:**
1. Run: `test ! -d api && echo "PASS: api/ deleted" || echo "FAIL: api/ still exists"`

**Expected:** Prints `PASS: api/ deleted`. Exit code 0.

---

### TC-02: No legacy bare `api` imports remain

**Goal:** Verify no Go source file still imports `github.com/zoumo/oar/api` (the bare package).

**Steps:**
1. Run: `rg 'zoumo/oar/api"' --type go; echo "rg exit: $?"`

**Expected:** No output lines printed. `rg exit: 1` (exit 1 = no matches found — this is the success signal).

---

### TC-03: No legacy `api/shim` imports remain

**Goal:** Verify no Go source file still imports `github.com/zoumo/oar/api/shim`.

**Steps:**
1. Run: `rg '"github.com/zoumo/oar/api/shim"' --type go; echo "rg exit: $?"`

**Expected:** No output lines printed. `rg exit: 1`.

---

### TC-04: pkg/shim/api/ contains all required files

**Goal:** Verify the canonical shim package has all four required files.

**Steps:**
1. Run: `ls pkg/shim/api/`

**Expected:** Output lists exactly: `client.go  methods.go  service.go  types.go`.

---

### TC-05: pkg/shim/api/methods.go contains only shim-scoped constants

**Goal:** Verify method constants are correctly scoped (no workspace/agentrun/agent constants mixed in).

**Steps:**
1. Run: `grep 'MethodSession\|MethodRuntime\|MethodShimEvent\|MethodWorkspace\|MethodAgent' pkg/shim/api/methods.go`

**Expected:** Only `MethodSession*`, `MethodRuntime*`, and `MethodShimEvent` constants appear. No `MethodWorkspace*` or `MethodAgent*` constants.

---

### TC-06: pkg/events/constants.go contains EventType*/Category* constants

**Goal:** Verify event and category constants are present in pkg/events.

**Steps:**
1. Run: `grep 'EventType\|Category' pkg/events/constants.go | head -20`

**Expected:** Lines showing `EventTypeText`, `EventTypeStateChange`, `CategorySession`, `CategoryRuntime` and other EventType* constants. All defined in package `events`.

---

### TC-07: make build produces both binaries

**Goal:** Verify the build still produces both agentd and agentdctl binaries.

**Steps:**
1. Run: `make build && ls -la bin/agentd bin/agentdctl`

**Expected:** Exit 0. Both files listed with non-zero sizes.

---

### TC-08: Full test suite passes

**Goal:** Verify all packages including integration tests pass.

**Steps:**
1. Run: `go test ./... 2>&1 | tail -20`

**Expected:** All lines show `ok` or `[no test files]`. No `FAIL` lines. Exit 0.

---

### TC-09: pkg/events package builds and tests independently

**Goal:** Verify pkg/events is self-contained with the new constants.go.

**Steps:**
1. Run: `go test ./pkg/events/...`

**Expected:** `ok  github.com/zoumo/oar/pkg/events` or `(cached)`. Exit 0.

---

### TC-10: pkg/shim/api package builds independently

**Goal:** Verify pkg/shim/api compiles cleanly as a standalone package.

**Steps:**
1. Run: `go build ./pkg/shim/api/...`

**Expected:** No output. Exit 0.

---

### TC-11: Consumer packages that used api/shim build cleanly

**Goal:** Verify the six migration groups (pkg/agentd, pkg/shim/client, pkg/shim/server, pkg/ari/server, cmd/agentd, cmd/agentdctl) all build without errors.

**Steps:**
1. Run: `go build ./pkg/agentd/... ./pkg/shim/... ./pkg/ari/server/... ./cmd/...`

**Expected:** No output. Exit 0.

