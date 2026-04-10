# S02: Auto-fix: unconvert + copyloopvar + ineffassign (24 issues) — UAT

**Milestone:** M006
**Written:** 2026-04-09T14:24:31.087Z

## UAT: S02 — Auto-fix: unconvert + copyloopvar + ineffassign

### Preconditions
- Go toolchain installed and `go build ./...` passes from repo root
- `golangci-lint` v2 installed and accessible on PATH
- Working directory is repo root (`/Users/jim/code/zoumo/open-agent-runtime`)
- S01 (gci/gofumpt formatting fixes) is complete — no gci/gofumpt noise in lint output

---

### TC-S02-01: Zero unconvert findings

**What it tests:** All redundant type-conversion wrappings have been removed.

**Steps:**
1. Run: `golangci-lint run ./... 2>&1 | grep '(unconvert)'`
2. Observe the exit code: `echo $?`

**Expected outcome:**
- Step 1 produces no output (empty stdout)
- Step 2 prints `1` (grep found no matches = exit 1)

**Edge case:** If any new Go files are added that introduce redundant casts, the grep will output matching lines and exit 0 — this test would then correctly flag the regression.

---

### TC-S02-02: Zero copyloopvar findings

**What it tests:** No loop variables are being unnecessarily copied inside loop bodies.

**Steps:**
1. Run: `golangci-lint run ./... 2>&1 | grep '(copyloopvar)'`
2. Observe the exit code: `echo $?`

**Expected outcome:**
- Step 1 produces no output
- Step 2 prints `1`

**Note:** `pkg/spec/example_bundles_test.go` was the only affected file. The `bundleDir := bundleDir` shadow was removed by `golangci-lint run --fix`.

---

### TC-S02-03: Zero ineffassign findings

**What it tests:** No variables are assigned a value that is immediately overwritten without being read.

**Steps:**
1. Run: `golangci-lint run ./... 2>&1 | grep '(ineffassign)'`
2. Observe the exit code: `echo $?`

**Expected outcome:**
- Step 1 produces no output
- Step 2 prints `1`

**Note:** `pkg/runtime/terminal.go` had the only ineffassign finding (`var outputWriter io.Writer = output` immediately overwritten by `outputWriter = limitedWriter`). The fix removed the intermediate variable entirely.

---

### TC-S02-04: Combined one-shot verification

**What it tests:** All three linter categories are clean in a single command (the canonical slice verification gate).

**Steps:**
1. Run:
   ```
   golangci-lint run ./... 2>&1 | grep -E '\(unconvert\)|\(copyloopvar\)|\(ineffassign\)'; [ $? -eq 1 ] && echo 'PASS: zero findings' || echo 'FAIL: findings remain'
   ```

**Expected outcome:**
- Output: `PASS: zero findings`
- No lint finding lines printed before the PASS line

---

### TC-S02-05: Build integrity after lint fixes

**What it tests:** The lint fixes did not break compilation (no missing imports, no type errors).

**Steps:**
1. Run: `go build ./...`
2. Observe exit code

**Expected outcome:**
- Exit code 0, no output (clean build)

**Why this matters:** The gocritic auto-fix pass introduced `errors.As()` rewrites without the `"errors"` import in 5 files. This test confirms all such repairs were applied correctly.

---

### TC-S02-06: go vet clean after lint fixes

**What it tests:** No vet-detectable issues were introduced by the mechanical lint fixes.

**Steps:**
1. Run: `go vet ./...`
2. Observe exit code

**Expected outcome:**
- Exit code 0, no output

---

### TC-S02-07: Affected package test suites pass

**What it tests:** The 6 originally targeted packages (plus workspace, which had gocritic side-effects) have no regressions.

**Steps:**
1. Run:
   ```
   go test ./pkg/ari/... ./pkg/rpc/... ./pkg/agentd/... ./pkg/spec/... ./pkg/runtime/... ./pkg/workspace/...
   ```

**Expected outcome:**
- All 6 lines show `ok  github.com/open-agent-d/open-agent-d/pkg/<name>`
- No `FAIL` lines
- No panic output

---

### TC-S02-08: Previously fixed files no longer contain redundant casts

**What it tests:** Spot-check that the specific patterns that triggered unconvert are gone from source.

**Steps:**
1. Run: `grep -n 'int64(rpcErr\.Code)' pkg/ari/server_test.go pkg/rpc/server_test.go`
2. Run: `grep -n 'json\.RawMessage(json\.RawMessage\|json\.RawMessage(\*req\.Params)' pkg/agentd/shim_client.go pkg/agentd/shim_client_test.go`
3. Run: `grep -n 'var outputWriter io\.Writer = output' pkg/runtime/terminal.go`
4. Run: `grep -n 'bundleDir := bundleDir' pkg/spec/example_bundles_test.go`

**Expected outcome:**
- All four greps produce no output (patterns are absent)

---

### TC-S02-09: gocritic import repairs are present in all 5 affected files

**What it tests:** The `"errors"` import was correctly added to the 5 files that had gocritic rewrites.

**Steps:**
1. Run:
   ```
   for f in pkg/ari/server.go pkg/workspace/git.go pkg/workspace/hook_test.go pkg/agentd/session_test.go pkg/runtime/terminal.go; do
     grep -l '"errors"' "$f" && echo "$f: OK" || echo "$f: MISSING errors import"
   done
   ```

**Expected outcome:**
- Each file prints `<path>: OK`
- No `MISSING errors import` lines

