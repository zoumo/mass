# Quick Task: fix bug according to @docs/plan/async-prompt-design.md

**Date:** 2026-04-09
**Branch:** gsd/quick/1-fix-bug-according-to-docs-plan-async-pro

## What Changed

- **`deliverPromptAsync` helper** added to `pkg/ari/server.go`: performs session/connect synchronously (so dispatch errors surface to the caller immediately), then launches the actual `client.Prompt()` call in a background goroutine with a bounded 120s timeout. The goroutine handles `running → created/error` state transitions when the turn completes.

- **`handleAgentPrompt` rewritten** as async: adds running-state guard (`"agent is already processing a prompt; cancel it first via agent/cancel"`), calls `deliverPromptAsync`, returns `{accepted: true}` immediately on successful dispatch.

- **`handleRoomSend` rewritten** as async: adds running-state guard (`"target agent is busy processing another prompt; cancel its current turn or try again later"`), calls `deliverPromptAsync`, returns `{delivered: true}` immediately on successful dispatch.

- **`AgentPromptResult`** in `pkg/ari/types.go`: replaced `StopReason string` with `Accepted bool`. Async model has no synchronous stop reason.

- **`RoomSendResult`** in `pkg/ari/types.go`: removed `StopReason string` (not available asynchronously).

- **`cmd/room-mcp-server/main.go`**: removed `StopReason` from `ariRoomSendResult`; updated success message to `"Message delivered to {target}. The target agent will process it asynchronously."`; surfaces busy rejection as a human-readable message.

- **`cmd/agentdctl/agent.go`**: added `--wait` flag to the `agent prompt` command — polls `agent/status` until state transitions out of `running`.

- **`pkg/ari/server_test.go`**: updated all `agent/prompt` assertions from `StopReason` to `Accepted`; added `pollAgentUntilIdle` helper (polls until state ≠ `running`); added two new tests: `TestARIAgentPromptRejectWhenRunning` and `TestARIRoomSendRejectWhenRunning` verifying the new concurrency guard.

## Files Modified

- `pkg/ari/server.go` — `deliverPromptAsync`, updated `handleAgentPrompt`, updated `handleRoomSend`
- `pkg/ari/types.go` — `AgentPromptResult` (Accepted), `RoomSendResult` (no StopReason)
- `pkg/ari/server_test.go` — async assertions, `pollAgentUntilIdle`, two new guard tests
- `cmd/room-mcp-server/main.go` — `ariRoomSendResult`, response text, busy error handling
- `cmd/agentdctl/agent.go` — `--wait` flag for `agent prompt`

## Verification

- `go build ./...` — clean build, no errors
- `go vet ./pkg/ari/... ./cmd/room-mcp-server/... ./cmd/agentdctl/...` — clean
- `go test ./pkg/ari/... -run "TestARIAgentLifecycle|TestARIAgentPromptAutoStart|TestARIAgentPromptOnStopped|TestARIAgentPromptOnError|TestARIAgentPromptRejectWhenRunning|TestARIRoomSendRejectWhenRunning|TestARISessionRemoveProtected"` — all 7 PASS (6.0s)
- `go test ./pkg/ari/... -timeout 300s` — full suite PASS (15.9s)
- Commit `367a7f2` on branch `main`
