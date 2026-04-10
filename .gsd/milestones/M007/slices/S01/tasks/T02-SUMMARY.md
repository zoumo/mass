---
id: T02
parent: S01
milestone: M007
key_files:
  - pkg/runtime/runtime.go
  - pkg/spec/state_test.go
  - pkg/runtime/runtime_test.go
key_decisions:
  - state.json writes 'idle' (not 'created') after ACP handshake and after each prompt turn — intentional per D085
duration: 
verification_result: passed
completed_at: 2026-04-09T19:28:39.568Z
blocker_discovered: false
---

# T02: Replaced all spec.StatusCreated usages in pkg/spec and pkg/runtime with spec.StatusIdle; both packages build cleanly and all 64 tests pass

**Replaced all spec.StatusCreated usages in pkg/spec and pkg/runtime with spec.StatusIdle; both packages build cleanly and all 64 tests pass**

## What Happened

T01 had already added StatusIdle and StatusError to state_types.go and removed StatusCreated, so the only work was mechanical substitution in three files: runtime.go (two call-sites: bootstrap-complete and prompt-completed state writes), state_test.go (sampleState helper), and runtime_test.go (TestCreate_ReachesCreatedState assertion). No compat alias was introduced. state.json now emits \"idle\" instead of \"created\" after handshake and after each prompt turn, per D085.

## Verification

go build ./pkg/spec/... ./pkg/runtime/... exits 0; grep for StatusCreated in those packages finds zero matches; go test ./pkg/spec/... passes 16 tests; go test ./pkg/runtime/... passes 48 tests including the full TestRuntimeSuite integration suite against the mock ACP agent.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go build ./pkg/spec/... ./pkg/runtime/...` | 0 | ✅ pass | 4300ms |
| 2 | `grep -rn StatusCreated --include=*.go pkg/spec/ pkg/runtime/ (negated)` | 1 | ✅ pass | 100ms |
| 3 | `go test ./pkg/spec/... -count=1 -timeout 30s` | 0 | ✅ pass | 4100ms |
| 4 | `go test ./pkg/runtime/... -count=1 -timeout 60s` | 0 | ✅ pass | 6000ms |

## Deviations

T01 had already removed StatusCreated from state_types.go, so step 1 of the task plan was a no-op. Steps 2–4 executed as planned.

## Known Issues

pkg/rpc and pkg/agentd still reference spec.StatusCreated — covered by T03/T04 per slice plan.

## Files Created/Modified

- `pkg/runtime/runtime.go`
- `pkg/spec/state_test.go`
- `pkg/runtime/runtime_test.go`
