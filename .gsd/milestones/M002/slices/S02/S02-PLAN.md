# S02: shim-rpc clean break

**Goal:** Implement the clean-break shim RPC contract in code so the server, durable event and history surface, shim client, process manager, CLI, and ARI all converge on session/* plus runtime/* without reopening the restart and replay scope that belongs to S03.
**Demo:** After this: After this: the shim server, agentd, CLI, and ARI all speak the clean-break `session/*` + `runtime/*` surface, and focused tests prove replayable history and status hooks without claiming restart truth.

## Tasks
- [x] **T01: Land replayable notification envelopes and the clean-break shim server surface** — Replace the legacy shim server methods and raw history rows with one replayable notification envelope so live subscribe, history replay, runtime status, and runtime state-change delivery all speak the converged contract without exposing bootstrap noise.
  - Estimate: 1h 30m
  - Files: pkg/events/log.go, pkg/events/translator.go, pkg/events/log_test.go, pkg/events/translator_test.go, pkg/rpc/server.go, pkg/rpc/server_test.go, pkg/runtime/runtime.go, cmd/agent-shim/main.go
  - Verify: go test ./pkg/events ./pkg/rpc -count=1
- [x] **T02: Migrated shim_client.go, process.go, and agent-shim-cli to clean-break session/* + runtime/* protocol; all pkg/agentd tests pass** — Update the matched downstream consumers together so the long-lived in-memory shim connection, event channel, and local debug CLI all consume the renamed protocol and envelope shape without widening scope into restart reconciliation.
  - Estimate: 1h 15m
  - Files: pkg/agentd/shim_client.go, pkg/agentd/shim_client_test.go, pkg/agentd/process.go, pkg/agentd/process_test.go, cmd/agent-shim-cli/main.go
  - Verify: go test ./pkg/agentd -count=1
- [x] **T03: Keep ARI session flows stable on the renamed shim protocol and prove the slice gate** — Adapt ARI to the renamed shim client surface, preserve the existing session/* contract for callers, and close the slice with focused tests plus a no-legacy-source-path check rather than pulling S03 restart scope forward.
  - Estimate: 1h
  - Files: pkg/ari/server.go, pkg/ari/types.go, pkg/ari/server_test.go
  - Verify: bash scripts/verify-m002-s01-contract.sh && go test ./pkg/ari -count=1 && go test ./pkg/runtime -run 'TestRuntimeSuite' -count=1 && ! rg -n --glob '!**/*_test.go' '"Prompt"|"Cancel"|"Subscribe"|"GetState"|"GetHistory"|"Shutdown"|"\$/event"' pkg/rpc pkg/agentd pkg/ari cmd/agent-shim-cli
