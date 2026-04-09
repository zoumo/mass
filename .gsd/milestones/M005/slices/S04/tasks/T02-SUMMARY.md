---
id: T02
parent: S04
milestone: M005
key_files:
  - pkg/ari/types.go
  - pkg/ari/server.go
  - pkg/ari/server_test.go
  - cmd/agentdctl/agent.go
key_decisions:
  - Old session deletion happens inside the restart goroutine (not before Reply) to keep Reply latency minimal; the agent is in creating state between Reply and goroutine-start which blocks concurrent prompts via the T01 guard
  - agents.UpdateState has no transition validation so stopped→creating and error→creating work as-is without changes to pkg/agentd/agent.go
duration: 
verification_result: passed
completed_at: 2026-04-08T19:58:55.074Z
blocker_discovered: false
---

# T02: Replaced MethodNotFound stub with real async handleAgentRestart, added AgentRestartResult to types.go, replaced TestARIAgentRestartStub with TestARIAgentRestartAsync, and wired agentdctl restart subcommand — all tests pass

**Replaced MethodNotFound stub with real async handleAgentRestart, added AgentRestartResult to types.go, replaced TestARIAgentRestartStub with TestARIAgentRestartAsync, and wired agentdctl restart subcommand — all tests pass**

## What Happened

Added AgentRestartResult type to types.go. Replaced the handleAgentRestart MethodNotFound stub in server.go with a full async implementation: validates agent is stopped/error, pre-fetches linked session, transitions agent to creating synchronously, replies immediately, then bootstraps in a 90s background goroutine (delete old session, create new session, acquire workspace/registry, start process, transition to created/error with structured slog logging). Replaced TestARIAgentRestartStub with TestARIAgentRestartAsync that exercises the full lifecycle with a real mockagent shim (create→prompt→stop→restart→poll→prompt→stop→delete). Added agentRestartCmd to agentdctl with ExactArgs(1), Long description, and runAgentRestart that outputs JSON result.

## Verification

go test ./pkg/ari/... -run TestARIAgentRestartAsync -v -timeout 90s → PASS; go test ./pkg/ari/... -count=1 -timeout 120s → PASS (full suite); go build ./... → clean; go build -o /tmp/agentdctl ./cmd/agentdctl && /tmp/agentdctl agent restart --help → correct output

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go test ./pkg/ari/... -run TestARIAgentRestartAsync -v -timeout 90s` | 0 | ✅ pass | 1680ms |
| 2 | `go test ./pkg/ari/... -count=1 -timeout 120s` | 0 | ✅ pass | 12028ms |
| 3 | `go build ./...` | 0 | ✅ pass | 800ms |
| 4 | `go build -o /tmp/agentdctl ./cmd/agentdctl && /tmp/agentdctl agent restart --help` | 0 | ✅ pass | 500ms |

## Deviations

None.

## Known Issues

None.

## Files Created/Modified

- `pkg/ari/types.go`
- `pkg/ari/server.go`
- `pkg/ari/server_test.go`
- `cmd/agentdctl/agent.go`
