---
estimated_steps: 3
estimated_files: 3
skills_used: []
---

# T03: Keep ARI session flows stable on the renamed shim protocol and prove the slice gate

**Slice:** S02 — shim-rpc clean break
**Milestone:** M002

## Description

Finish the slice at the external runtime boundary this milestone already owns: ARI keeps its existing `session/*` contract, but its shim-facing internals must now use the clean-break client surface. This task should prove the migration without absorbing S03’s restart-truth work or widening the verification scope to unrelated runtime failures.

## Failure Modes

| Dependency | On error | On timeout | On malformed response |
|------------|----------|-----------|----------------------|
| `ProcessManager` plus `ShimClient` | Keep InvalidParams versus InternalError separation intact so callers know whether session state or runtime behavior failed. | Preserve the existing prompt, start, and connect timeouts with the renamed RPC calls. | Fail the request with contextual protocol-mismatch detail instead of mapping bad shim data into a misleading `session/status` response. |

## Load Profile

- **Shared resources**: session registry entries, process-manager client handles, and the same per-session shim transport used by prompt, status, and cancel.
- **Per-operation cost**: one process lookup plus one downstream shim RPC per ARI lifecycle call.
- **10x breakpoint**: concurrent prompt and status traffic would expose serialized shim-client usage or process lookup contention before any new storage bottleneck appears.

## Negative Tests

- **Malformed inputs**: nonexistent `sessionId`, empty prompt text, and invalid lifecycle calls against stopped sessions.
- **Error paths**: auto-start failure, connect failure, prompt failure, and runtime-status mapping failure after the shim surface changes.
- **Boundary conditions**: `created` to `running` auto-start, already-stopped sessions, and `session/status` before any replayable history exists.

## Steps

1. Update `pkg/ari/server.go` to call the renamed shim-client methods, map `runtime/status` back into the existing `session/status` response, and preserve the current InvalidParams and InternalError split when sessions are not running.
2. Refresh `pkg/ari/server_test.go` — and `pkg/ari/types.go` if the wire shape needs a helper adjustment — so prompt, cancel, stop, attach, and status assertions still pass on the renamed shim protocol.
3. Run the focused slice gate and remove any remaining live source-path references to PascalCase shim methods or `$/event` in `pkg/rpc`, `pkg/agentd`, `pkg/ari`, and `cmd/agent-shim-cli`, while keeping legacy-name references only in negative tests where they prove rejection.

## Must-Haves

- [ ] ARI caller-visible `session/*` behavior stays stable while the shim-facing internals switch to the clean-break surface.
- [ ] The focused slice gate proves the renamed protocol, replay and status hooks, and runtime-path stability without claiming restart durability that S03 still owns.
- [ ] Legacy shim names remain only in negative tests or historical docs, not in live source paths.

## Verification

- `bash scripts/verify-m002-s01-contract.sh`
- `go test ./pkg/ari -count=1`
- `go test ./pkg/runtime -run 'TestRuntimeSuite' -count=1`
- `! rg -n --glob '!**/*_test.go' '"Prompt"|"Cancel"|"Subscribe"|"GetState"|"GetHistory"|"Shutdown"|"\$/event"' pkg/rpc pkg/agentd pkg/ari cmd/agent-shim-cli`

## Observability Impact

- Signals added and changed: ARI status and prompt and cancel paths now surface the clean-break shim status and result shape while preserving InvalidParams versus InternalError semantics
- How a future agent inspects this: go test ./pkg/ari -count=1, the focused runtime test suite, and the legacy-source grep in the slice gate
- Failure state exposed: caller-visible errors keep session and running-state context rather than masking protocol mismatches

## Inputs

- `scripts/verify-m002-s01-contract.sh` — doc-level contract regression check that should stay green through the code migration
- `pkg/agentd/shim_client.go` — renamed shim client surface from T02
- `pkg/agentd/process.go` — runtime and session lifecycle semantics ARI already depends on
- `pkg/ari/server.go` — current ARI session lifecycle mapping
- `pkg/ari/server_test.go` — existing ARI lifecycle proof
- `pkg/runtime/runtime_test.go` — focused runtime gate already known to be stable for this slice

## Expected Output

- `pkg/ari/server.go` — ARI shim-facing internals updated to the clean-break client surface
- `pkg/ari/types.go` — helper type adjustments if the status mapping needs them
- `pkg/ari/server_test.go` — caller-visible ARI lifecycle proof on the renamed shim protocol
