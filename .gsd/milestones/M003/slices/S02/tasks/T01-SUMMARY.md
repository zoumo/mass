---
id: T01
parent: S02
milestone: M003
key_files:
  - pkg/agentd/recovery.go
  - cmd/agentd/main.go
key_decisions:
  - ErrInvalidTransition from Transition() logged at Warn and recovery proceeds — transition edge case should not block reconnecting to a live shim
  - Mismatch detection compares string representations since spec.Status and meta.SessionState don't share an enum
duration: 
verification_result: passed
completed_at: 2026-04-07T17:53:16.256Z
blocker_discovered: false
---

# T01: Added shim-vs-DB state reconciliation to recoverSession and replaced Stat→Remove TOCTOU with unconditional os.Remove for socket cleanup

**Added shim-vs-DB state reconciliation to recoverSession and replaced Stat→Remove TOCTOU with unconditional os.Remove for socket cleanup**

## What Happened

Inserted a reconciliation switch block in recoverSession (pkg/agentd/recovery.go) between the existing client.Status() and client.History() calls. Three cases: (1) shim reports stopped → close client, return error to trigger fail-closed path; (2) shim running but DB says created → Transition to running, logging ErrInvalidTransition at Warn if it fails; (3) any other mismatch → log at Warn and proceed. Also fixed the ARI socket startup TOCTOU race in cmd/agentd/main.go by replacing the Stat→Remove sequence with a single unconditional os.Remove that ignores os.ErrNotExist.

## Verification

go build ./cmd/agentd/... ./pkg/agentd/... — zero errors. go vet ./cmd/agentd/... ./pkg/agentd/... — no issues. go test ./pkg/agentd/... -count=1 -timeout 60s — all existing recovery tests pass.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go build ./cmd/agentd/... ./pkg/agentd/...` | 0 | ✅ pass | 2500ms |
| 2 | `go vet ./cmd/agentd/... ./pkg/agentd/...` | 0 | ✅ pass | 2500ms |
| 3 | `go test ./pkg/agentd/... -count=1 -timeout 60s` | 0 | ✅ pass | 8800ms |

## Deviations

None.

## Known Issues

None.

## Files Created/Modified

- `pkg/agentd/recovery.go`
- `cmd/agentd/main.go`
