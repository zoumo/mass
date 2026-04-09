---
id: T03
parent: S03
milestone: M005
key_files:
  - cmd/agentdctl/agent.go
  - cmd/agentdctl/helpers.go
  - cmd/agentdctl/main.go
  - cmd/agentdctl/daemon.go
  - cmd/agentd/main.go
key_decisions:
  - Shared CLI helpers extracted from session.go to helpers.go — room.go, workspace.go, and daemon.go all depend on getClient/outputJSON/handleError/parseLabels
  - AgentPromptParams.Prompt field (not .Text) — matches pkg/ari/types.go struct definition
duration: 
verification_result: passed
completed_at: 2026-04-08T18:46:46.468Z
blocker_discovered: false
---

# T03: Migrated agentdctl CLI from session/* to agent/* subcommands, deleted session.go, extracted shared helpers to helpers.go, updated daemon health check to agent/list, and wired AgentManager into cmd/agentd/main.go

**Migrated agentdctl CLI from session/* to agent/* subcommands, deleted session.go, extracted shared helpers to helpers.go, updated daemon health check to agent/list, and wired AgentManager into cmd/agentd/main.go**

## What Happened

Four coordinated changes completed the S03 surface migration at the CLI and daemon wiring layers. Created cmd/agentdctl/agent.go with agentCmd and 8 subcommands (create/list/status/prompt/stop/delete/attach/cancel). Extracted shared CLI helpers (getClient, outputJSON, handleError, parseLabels, string utilities) from session.go into a new helpers.go so room.go/workspace.go/daemon.go continue to compile after session.go deletion. Deleted cmd/agentdctl/session.go entirely. Updated cmd/agentdctl/main.go to register agentCmd instead of sessionCmd. Updated cmd/agentdctl/daemon.go health check to call agent/list instead of session/list. Updated cmd/agentd/main.go to construct AgentManager and pass it as the fourth argument to ari.New().

## Verification

go build ./... passed (exit 0). go build -o /tmp/agentdctl ./cmd/agentdctl passed. /tmp/agentdctl agent --help shows all 8 subcommands. ! /tmp/agentdctl --help | grep -q 'session' passes (no session in root help).

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go build ./...` | 0 | ✅ pass | 6400ms |
| 2 | `go build -o /tmp/agentdctl ./cmd/agentdctl` | 0 | ✅ pass | 200ms |
| 3 | `/tmp/agentdctl agent --help` | 0 | ✅ pass | 10ms |
| 4 | `! /tmp/agentdctl --help 2>&1 | grep -q 'session'` | 0 | ✅ pass | 10ms |

## Deviations

AgentPromptParams field is Prompt not Text — corrected on first build. Helpers moved to helpers.go rather than being deleted with session.go.

## Known Issues

Two pre-existing flaky tests unrelated to T03: TestARIRoomSendToStoppedTarget (shim socket timeout) and TestRuntimeSuite/TestCancel_SendsCancelToAgent (peer disconnect race).

## Files Created/Modified

- `cmd/agentdctl/agent.go`
- `cmd/agentdctl/helpers.go`
- `cmd/agentdctl/main.go`
- `cmd/agentdctl/daemon.go`
- `cmd/agentd/main.go`
