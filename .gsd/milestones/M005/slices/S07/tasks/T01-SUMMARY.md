---
id: T01
parent: S07
milestone: M005
key_files:
  - pkg/agentd/process.go
  - pkg/agentd/recovery.go
  - pkg/agentd/recovery_test.go
  - cmd/agentd/main.go
  - pkg/ari/server_test.go
  - pkg/agentd/process_test.go
key_decisions:
  - Changed recoverSession signature to return (spec.Status, error) so caller can use shim status for agent reconciliation without re-querying the processes map
  - Removed the len(candidates)==0 early-return guard so the creating-cleanup pass always runs even when there are no candidate sessions
duration: 
verification_result: passed
completed_at: 2026-04-08T21:46:50.623Z
blocker_discovered: false
---

# T01: Injected AgentManager into ProcessManager and implemented agent state reconciliation in RecoverSessions with three new unit tests proving all error/running/creating-cleanup branches

**Injected AgentManager into ProcessManager and implemented agent state reconciliation in RecoverSessions with three new unit tests proving all error/running/creating-cleanup branches**

## What Happened

Added agents *AgentManager field to ProcessManager struct, updated NewProcessManager signature across all 5 call sites (cmd/agentd/main.go, two server_test.go harnesses, process_test.go, recovery_test.go). Changed recoverSession to return (spec.Status, error) so the caller can use shim status directly for agent reconciliation. Implemented three RecoverSessions branches: failure→agent error, success+running→agent running, and a post-loop creating-cleanup pass for agents bootstrapping when the daemon restarted. Fixed a subtle early-return bug: the existing 'len(candidates)==0 → return nil' guard would short-circuit the creating-cleanup pass; removed it so the cleanup always runs. Added three new unit tests with a createTestAgentForRecovery helper covering all three reconciliation paths.

## Verification

go test ./pkg/agentd/... -count=1 -timeout 120s → ok (6.07s); go test ./pkg/ari/... -count=1 -timeout 120s → ok (12.28s); go build ./... → ok. All 15 TestRecoverSessions_* tests pass including the three new agent reconciliation tests.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go test ./pkg/agentd/... -count=1 -timeout 120s` | 0 | ✅ pass | 6070ms |
| 2 | `go test ./pkg/ari/... -count=1 -timeout 120s` | 0 | ✅ pass | 12280ms |
| 3 | `go build ./...` | 0 | ✅ pass | 7300ms |

## Deviations

Removed the `if len(candidates) == 0 { return nil }` early-return guard. The original plan omitted this detail but the intent was clear: creating-cleanup must always run. This is a local implementation fix, not a plan deviation.

## Known Issues

None.

## Files Created/Modified

- `pkg/agentd/process.go`
- `pkg/agentd/recovery.go`
- `pkg/agentd/recovery_test.go`
- `cmd/agentd/main.go`
- `pkg/ari/server_test.go`
- `pkg/agentd/process_test.go`
