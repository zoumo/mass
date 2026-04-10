---
id: T02
parent: S03
milestone: M007
key_files:
  - pkg/ari/server.go
  - pkg/agentd/process.go
  - pkg/ari/server_test.go
key_decisions:
  - handleAgentPrompt uses CodeRecoveryBlocked (-32001) for bad-state rejection, consistent with workspace/send
  - InjectProcess added as public method on ProcessManager for test injection without Start()
  - miniShimServer defined locally in ari_test package (agentd.mockShimServer is unexported)
  - agentToInfo helper ensures zero agentId fields in all agent/* responses
duration: 
verification_result: passed
completed_at: 2026-04-09T21:32:19.774Z
blocker_discovered: false
---

# T02: Added all agent/* JSON-RPC handlers, InjectProcess test helper, and 18-test handler suite over a real Unix socket — all pass

**Added all agent/* JSON-RPC handlers, InjectProcess test helper, and 18-test handler suite over a real Unix socket — all pass**

## What Happened

Implemented nine agent/* handler functions in server.go (handleAgentCreate, handleAgentPrompt, handleAgentCancel, handleAgentStop, handleAgentDelete, handleAgentRestart, handleAgentList, handleAgentStatus, handleAgentAttach), each with structured slog observability. Added agentToInfo helper and errors.As-based typed error mapping. Added InjectProcess(key, proc) to ProcessManager for test injection without Start(). Replaced stub server_test.go with a full 18-test suite in package ari_test covering workspace/create→status→list→delete→delete-blocked, agent/create→list→status, agent/prompt rejection, agent/delete rejection, workspace/send delivery via injected mock shim, workspace/send error rejection, and recursive agentId absence audit.

## Verification

go build ./pkg/ari/... ./pkg/agentd/... (exit 0); go vet ./pkg/ari/... (exit 0); go test ./pkg/ari/... -count=1 -timeout 60s -v → 18 tests pass in 2007ms. TestProcessManagerStart failure in pkg/agentd confirmed pre-existing via git stash check.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go build ./pkg/ari/... ./pkg/agentd/...` | 0 | ✅ pass | 800ms |
| 2 | `go vet ./pkg/ari/...` | 0 | ✅ pass | 500ms |
| 3 | `go test ./pkg/ari/... -count=1 -timeout 60s -v` | 0 | ✅ pass | 2007ms |

## Deviations

miniShimServer created inline in ari_test (cannot import unexported agentd.mockShimServer). TestAgentCreateReturnsCreating background Start() legitimately fails with 'runtime class not found: default' in test env — test correctly checks only the synchronous reply state='creating'.

## Known Issues

None.

## Files Created/Modified

- `pkg/ari/server.go`
- `pkg/agentd/process.go`
- `pkg/ari/server_test.go`
