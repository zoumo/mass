---
id: T03
parent: S07
milestone: M005
key_files:
  - tests/integration/session_test.go
  - tests/integration/concurrent_test.go
  - tests/integration/e2e_test.go
  - tests/integration/restart_test.go
key_decisions:
  - All three target files were already fully migrated before T03 executed; task verified correctness by running the full suite
  - real_cli_test.go legitimately retains session/* calls (testing the real CLI runtime, not the agent/* surface)
duration: 
verification_result: passed
completed_at: 2026-04-08T22:04:33.525Z
blocker_discovered: false
---

# T03: Confirmed all integration tests fully migrated to agent/* ARI surface: 7/7 tests pass, zero session/* calls in non-CLI test files

**Confirmed all integration tests fully migrated to agent/* ARI surface: 7/7 tests pass, zero session/* calls in non-CLI test files**

## What Happened

All three target files (session_test.go, concurrent_test.go, e2e_test.go) were already fully rewritten to use the agent/* surface. session_test.go contains 4 rewritten tests plus all shared helpers (waitForAgentState, createAgentAndWait, createRoom, deleteRoom, stopAndDeleteAgent). concurrent_test.go runs 3 agents concurrently with clientMu serialization. e2e_test.go exercises the full 9-step agent/* pipeline. restart_test.go uses helpers from session_test.go without duplication. The only remaining session/* calls are in real_cli_test.go which legitimately tests the real CLI runtime. Full suite ran in 8.5s: 7 tests pass, 2 skip cleanly (ANTHROPIC_API_KEY not set).

## Verification

make build (exit 0); go test ./tests/integration/... -count=1 -timeout 180s (exit 0, PASS 8.481s, 7 pass 2 skip); grep for session/* in tests/integration/ excluding real_cli_test.go returns empty.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `make build` | 0 | ✅ pass | 4200ms |
| 2 | `go test ./tests/integration/... -count=1 -timeout 180s` | 0 | ✅ pass | 9400ms |
| 3 | `grep -rn 'session/new|session/prompt|session/stop|session/status|session/remove' tests/integration/ | grep -v real_cli_test.go` | 1 | ✅ pass (no matches) | 50ms |

## Deviations

None. All files were already fully migrated; this task confirmed correctness by running the full suite.

## Known Issues

None.

## Files Created/Modified

- `tests/integration/session_test.go`
- `tests/integration/concurrent_test.go`
- `tests/integration/e2e_test.go`
- `tests/integration/restart_test.go`
