---
id: S06
parent: M006
milestone: M006
provides:
  - Zero gocritic findings in golangci-lint run ./... — S07 (testifylint) can proceed against a clean gocritic baseline.
requires:
  []
affects:
  []
key_files:
  - ["pkg/agentd/process_test.go", "pkg/agentd/shim_client_test.go", "pkg/ari/registry.go", "pkg/ari/server.go", "pkg/ari/server_test.go", "pkg/events/translator_test.go", "pkg/rpc/server_test.go", "pkg/runtime/runtime_test.go", "pkg/runtime/terminal.go", "pkg/workspace/hook.go", "pkg/workspace/hook_test.go"]
key_decisions:
  - ["Used os.TempDir() for both filepathJoin fixes — the plan's three-arg split approach for process_test.go was insufficient because gocritic flags the leading '/' in '/tmp' as a path separator within filepath.Join arguments; os.TempDir() is the correct fix for both files."]
patterns_established:
  - ["exitAfterDefer pattern: in TestMain functions that call os.Exit(m.Run()), capture the code first — `code := m.Run(); os.RemoveAll(tmpDir); os.Exit(code)` — because os.Exit bypasses all deferred calls.", "filepathJoin gotcha: filepath.Join treats any arg containing a leading '/' as an absolute path, so filepath.Join(\"/tmp\", ...) still triggers gocritic; use os.TempDir() instead of a literal \"/tmp\" string.", "builtinShadowDecl: Go 1.21+ provides built-in min/max/clear — any file-level definitions shadow them and should be removed; callers transparently use the built-in after deletion."]
observability_surfaces:
  - none
drill_down_paths:
  []
duration: ""
verification_result: passed
completed_at: 2026-04-09T15:49:09.314Z
blocker_discovered: false
---

# S06: Manual: gocritic (45 issues)

**Fixed all 13 active gocritic findings across 11 files — golangci-lint run ./... reports zero gocritic issues.**

## What Happened

S06 had one task (T01) which fixed all 13 active gocritic findings across 11 files and 7 check categories.

**filepathJoin (2 fixes):** Both agentd test files now use `os.TempDir()` instead of a literal "/tmp" string inside `filepath.Join`. The plan originally proposed splitting "/tmp" into three separate arguments for process_test.go, but gocritic treats the leading "/" as a path separator within `filepath.Join` arguments — so the only reliable fix was `os.TempDir()`. shim_client_test.go was already planned to use `os.TempDir()`.

**importShadow (5 fixes):** Local variables shadowing imported package names were renamed: `meta` → `wsMeta` in registry.go (line 80) and server.go (line 374) and server_test.go (line 432); `workspace` → `ws` in server.go (line 291) and server_test.go (line 281). All use sites within each function scope were updated consistently.

**appendAssign (1 fix):** In translator_test.go:620, `all := append(turn1, turn2...)` was replaced with an explicit pre-allocation pattern — `make([]Envelope, 0, len(turn1)+len(turn2))` followed by two separate appends — eliminating the risk of accidentally mutating `turn1`'s underlying array.

**exitAfterDefer (2 fixes):** Both TestMain functions in rpc/server_test.go and runtime/runtime_test.go had `defer os.RemoveAll(tmpDir)` + `os.Exit(m.Run())` which is a no-op defer pattern (os.Exit bypasses deferred calls). Fixed by capturing the exit code into a variable, calling `os.RemoveAll(tmpDir)` explicitly, then passing the code to `os.Exit`.

**builtinShadowDecl (1 fix):** Removed the custom `min(a, b int) int` function from runtime/terminal.go along with its comment block. Go 1.21+ provides a built-in `min`, so the existing call at line 115 now resolves to the built-in automatically — no callers needed updating.

**appendCombine (1 fix):** Two consecutive `append(parts, ...)` calls in workspace/hook.go:33–34 were merged into a single multi-arg append call.

**elseif (1 fix):** Flattened `else { if err != nil { ... } }` to `else if err != nil { ... }` in workspace/hook_test.go:594–600.

Final verification: `golangci-lint run ./... 2>&1 | grep gocritic` produces no output (grep exits 1), confirming zero gocritic issues. The only remaining lint findings are gci (1, pre-existing) and testifylint (5, scoped to S07). Build is clean (`go build ./...` exits 0).

## Verification

Ran `golangci-lint run ./... 2>&1 | grep gocritic; [ $? -eq 1 ] && echo PASS || echo FAIL` — output: PASS. grep found zero gocritic lines and exited 1, satisfying the condition. `go build ./...` also exits 0 — no compilation regressions from any of the 11 file changes.

## Requirements Advanced

None.

## Requirements Validated

None.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

filepathJoin fix for process_test.go used os.TempDir() instead of the plan's filepath.Join("/tmp", "agentd-shim", sessionID) because gocritic flags the leading "/" in "/tmp" as a path separator within filepath.Join — the three-arg split was insufficient.

## Known Limitations

None. All 13 gocritic findings are resolved and build is clean.

## Follow-ups

S07 (testifylint, 5 remaining findings) is the next and final slice.

## Files Created/Modified

- `pkg/agentd/process_test.go` — filepathJoin: replaced filepath.Join("/tmp/agentd-shim", sessionID) with filepath.Join(os.TempDir(), "agentd-shim", sessionID)
- `pkg/agentd/shim_client_test.go` — filepathJoin: replaced literal /tmp string with os.TempDir() in filepath.Join call
- `pkg/ari/registry.go` — importShadow: renamed local var meta→wsMeta and updated all uses in function scope
- `pkg/ari/server.go` — importShadow: renamed workspace→ws (line 291) and meta→wsMeta (line 374), updated all uses
- `pkg/ari/server_test.go` — importShadow: renamed workspace→ws (line 281) and meta→wsMeta (line 432), updated all uses
- `pkg/events/translator_test.go` — appendAssign: replaced all := append(turn1, turn2...) with pre-allocated make + two explicit appends
- `pkg/rpc/server_test.go` — exitAfterDefer: removed defer os.RemoveAll(tmpDir), added explicit cleanup before os.Exit
- `pkg/runtime/runtime_test.go` — exitAfterDefer: removed defer os.RemoveAll(tmpDir), added explicit cleanup before os.Exit
- `pkg/runtime/terminal.go` — builtinShadowDecl: removed custom min(a, b int) int function and its comment — Go 1.21+ built-in takes over
- `pkg/workspace/hook.go` — appendCombine: merged two consecutive append(parts, ...) calls into a single multi-arg append
- `pkg/workspace/hook_test.go` — elseif: flattened else { if err != nil { ... } } to else if err != nil { ... }
