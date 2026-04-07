# S02: shim-rpc clean break — Research

**Date:** 2026-04-07

## Summary

This slice is **targeted research**. The design contract for the clean break is already settled in `docs/design/runtime/shim-rpc-spec.md`; the real work is pulling code, tests, and local tooling across that seam without reintroducing protocol drift.

The good news is that the current implementation is still a fairly tight legacy pair:

- `pkg/rpc/server.go` exposes the old PascalCase methods and `$/event` notifications.
- `pkg/agentd/shim_client.go` is the matching old client.
- `cmd/agent-shim-cli/main.go` is a thin direct consumer of the same surface.
- `pkg/agentd/process.go` and `pkg/ari/server.go` mostly depend on that client, not on raw socket details.

That means S02 can stay mostly localized if it migrates the server, client, CLI, and event-log shape together.

The harder seam is **history/recovery shape**, not the method rename itself:

- `pkg/events/log.go` already gives the shim a durable monotonic `seq` space.
- But history is stored and returned as raw `LogEntry{seq, ts, type, payload}` rows, not as the **same notification envelope** that live subscribers receive.
- `pkg/events/translator.go` has unused `NotifyTurnStart` / `NotifyTurnEnd` helpers, but there is currently **no emitted runtime lifecycle notification** matching the converged `runtime/stateChange` contract.
- `pkg/agentd/shim_client.go` does not implement any history/status recovery methods yet; it only knows Prompt / Cancel / Subscribe / GetState / Shutdown.

A second important finding: **older plan docs are stale for this slice**. `docs/plan/shim-rpc-redesign.md` still argues for an ACP-superset / passthrough direction and `runtime/get_state`-style naming. S01 explicitly converged away from that. For S02, the authority is:

- `docs/design/runtime/shim-rpc-spec.md`
- `docs/design/runtime/agent-shim.md`
- `docs/design/runtime/runtime-spec.md`
- `docs/design/contract-convergence.md`

Anything in `docs/plan/*` that conflicts with those is historical context only.

Baseline verification is mostly stable on the relevant surfaces:

- `go test ./pkg/rpc ./pkg/agentd ./pkg/events -count=1` passes
- `go test ./pkg/ari -run 'TestARISession|TestARIWorkspacePrepareEmptyDir|TestARIWorkspaceLifecycleRoundTrip' -count=1` passes
- `go test ./pkg/runtime -run 'TestRuntimeSuite' -count=1` passes
- `go test ./pkg/runtime -count=1` **does not** currently work as a slice gate because `TestTerminalManager_Create_Success` is already red for unrelated terminal behavior

Planner implication: use focused shim/runtime-path tests as the verification contract for S02, not the full `pkg/runtime` suite.

## Requirement Targets

### Primary
- **R034** — the shim surface must stop carrying the legacy PascalCase / `$/event` contract and expose one clean-break protocol aligned with the converged design.

### Supporting
- **R036** — S02 should expose the clean-break protocol surfaces (`runtime/status`, `runtime/history`, `session/subscribe(afterSeq)`) in a way that leaves restart/reconnect truth implementable later, without pretending that durable recovery is complete now.
- **R044** — S02 should keep scope tight: convert the protocol surface and downstream callers, but do **not** absorb the remaining restart/replay hardening backlog that belongs to S03 and later.

### Guardrail / do-not-absorb
- **R035** is not this slice’s closure target. S02 can add the protocol hooks that S03 will need, but it should not claim the replay race is retired unless live/history/resume behavior is actually proven end-to-end.

## Recommendation

Treat S02 as a **single clean-break migration across one stack**, not as a compatibility layer exercise.

1. **Anchor on the converged shim spec, not the older redesign plan.**
   - Keep the typed notification model from `docs/design/runtime/shim-rpc-spec.md`.
   - Do **not** pivot back to raw ACP passthrough or ACP-superset naming.
   - The target surface is:
     - `session/prompt`
     - `session/cancel`
     - `session/subscribe`
     - `runtime/status`
     - `runtime/history`
     - `runtime/stop`
     - notifications: `session/update`, `runtime/stateChange`

2. **Change the event/history shape first.**
   The slice’s real irreversible seam is not the method rename; it is making live notifications and history replay share one truthful envelope. That should be implemented before downstream client/process/ARI rewiring.

3. **Then change the matched consumers together.**
   Migrate these in the same wave:
   - `pkg/agentd/shim_client.go`
   - `pkg/agentd/process.go`
   - `pkg/ari/server.go`
   - `cmd/agent-shim-cli/main.go`
   - related tests

4. **Keep S03 work visibly deferred.**
   S02 should expose recovery-compatible methods and sequence semantics, but it should not fake daemon-restart truth, persisted session config truth, or cross-connection replay correctness beyond what is actually implemented and tested.

## Implementation Landscape

### Contract anchors

These are the files the planner should treat as authoritative for S02:

- `docs/design/runtime/shim-rpc-spec.md` — final clean-break protocol contract
- `docs/design/runtime/agent-shim.md` — component boundary and ownership split
- `docs/design/runtime/runtime-spec.md` — runtime state and socket/state-dir layout
- `docs/design/contract-convergence.md` — authority map from S01
- `scripts/verify-m002-s01-contract.sh` — doc-level contract regression check

These files are useful context but **not** authority when they conflict:

- `docs/plan/shim-rpc-redesign.md`
- `docs/plan/unified-modification-plan.md`

The biggest stale point is that the redesign plan still describes an ACP-superset / raw-ACP-ish direction, while S01 locked the target contract to **typed notifications plus `session/*` + `runtime/*` names**.

### Key code seams

#### 1. Shim server surface
- `pkg/rpc/server.go`
- `pkg/rpc/server_test.go`
- `pkg/rpc/server_internal_test.go`

Current behavior:
- dispatches `Prompt`, `Cancel`, `Subscribe`, `GetState`, `GetHistory`, `Shutdown`
- streams `$/event` notifications using `{type, payload}`
- `GetHistory` returns `[]events.LogEntry`
- `GetState` returns a flattened subset of `spec.State`

Target pressure:
- method names change
- notification method name changes
- history result shape changes from raw log rows to replayable notification envelopes
- status result shape grows from plain state to `state + recovery.lastSeq`

#### 2. Shim client surface
- `pkg/agentd/shim_client.go`
- `pkg/agentd/shim_client_test.go`

Current behavior:
- mirrors the legacy server methods exactly
- parses `$/event` + `{type,payload}` into typed `events.Event`
- has no client method for history retrieval
- has no client method for runtime status with recovery metadata
- `SetEventHandler` is effectively a dead-end API; real handlers must be provided at dial time

Planner implication:
- The client/server pair should move together.
- It is safe to break the old names outright because the only in-repo consumers are under direct control in this slice.

#### 3. Event translation and durable log
- `pkg/events/translator.go`
- `pkg/events/types.go`
- `pkg/events/log.go`
- `pkg/events/log_test.go`
- `pkg/events/translator_test.go`

Current behavior:
- ACP `SessionNotification` → typed `events.Event`
- broadcast to subscribers
- append log rows as `seq + timestamp + type + payload`
- `seq` is already monotonic and survives reopen
- `NotifyTurnStart` / `NotifyTurnEnd` exist but are only exercised in tests
- `FileReadEvent`, `FileWriteEvent`, and `CommandEvent` still exist in the typed event set, but there is no active producer for them in the shim event path

Planner implication:
- This package is the natural place to normalize the **one-sequence-space** contract.
- S02 probably needs a canonical notification-envelope representation here or adjacent to it, because both live streaming and `runtime/history` need the same shape.
- Avoid inventing a broad new abstraction layer if a small shared envelope type is enough.

#### 4. Shim bootstrap and event-start ordering
- `cmd/agent-shim/main.go`
- `pkg/runtime/runtime.go`

Important current behavior:
- `mgr.Create()` runs **before** the event log and translator are started.
- That means any internal bootstrap exchange during create is intentionally outside the live/history stream seen by subscribers.

Planner implication:
- If S02 refactors event emission, do not accidentally start logging/subscription before `Create()` unless you intentionally want bootstrap traffic to become externally visible. The current design contract still treats bootstrap as non-external work.

#### 5. Downstream long-lived consumer path
- `pkg/agentd/process.go`
- `pkg/agentd/process_test.go`

Current behavior:
- `ProcessManager.Start` forks shim, waits for socket, dials one client, subscribes immediately, and stores that connected client in memory
- `Stop`, `State`, and `Connect` all depend on the current client method set
- running-session event delivery is a channel fed by the client handler

Planner implication:
- S02 does not need to solve daemon-restart reconnect here, but it must keep this long-lived in-memory path working on the new methods.
- `ProcessManager.State` and any callers that expect plain `GetState` semantics will need adapting when `runtime/status` becomes nested.

#### 6. ARI downstream mapping
- `pkg/ari/server.go`
- `pkg/ari/types.go`
- `pkg/ari/server_test.go`

Current behavior:
- `session/prompt` → `processes.Connect()` → `client.Prompt()`
- `session/cancel` → `client.Cancel()`
- `session/status` calls `processes.State()` and expects a plain `spec.State`
- `session/attach` exposes the raw shim socket path, so direct consumers can still talk to the shim boundary

Planner implication:
- The ARI method names do not need to change, but their internals do.
- `session/status` is the main downstream place where the new `runtime/status` shape will need careful mapping.

#### 7. Local debug/proof tool
- `cmd/agent-shim-cli/main.go`

Current behavior:
- speaks raw shim RPC over Unix socket
- hardcodes `Subscribe`, `Prompt`, `GetState`, `Shutdown`, and `$/event`
- is currently the simplest human-facing proof harness for the shim boundary

Planner implication:
- Keep this tool updated in the same slice. It is small, and leaving it stale would make debugging harder for every later slice.

### What is already true vs. still missing

#### Already true
- The shim socket/state-dir layout is established and documented.
- The shim has a durable JSONL event log with monotonically increasing sequence numbers.
- The server/client/tests are already organized around a narrow protocol seam.
- The repo has focused tests at the right boundaries.

#### Still missing for S02 target
- clean-break method names in code
- typed notification method names (`session/update`, `runtime/stateChange`) in code
- one canonical live/history envelope
- `runtime/status` response with recovery metadata
- `runtime/history` response returning replayable notification envelopes instead of raw log rows
- `session/subscribe(afterSeq)` semantics
- actual runtime-state-change emission path matching the spec

### Natural task seams

#### Task seam A — Protocol types + server + log shape
Scope:
- `pkg/rpc/server.go`
- `pkg/events/log.go`
- `pkg/events/translator.go`
- related tests

Goal:
- land the new method names
- define the canonical envelope for live + history
- make `runtime/status` and `runtime/history` match the spec shape
- add runtime-state-change emission path or the minimum structure needed for it

Why first:
- Every downstream client depends on this seam.

#### Task seam B — Shim client + process manager + CLI
Scope:
- `pkg/agentd/shim_client.go`
- `pkg/agentd/process.go`
- `cmd/agent-shim-cli/main.go`
- related tests

Goal:
- update direct consumers to the new method names and result shapes
- keep the in-memory subscribed event path working
- preserve the CLI as a direct proof/debug surface

Why second:
- once the server surface exists, the rest is a contained adaptation wave

#### Task seam C — ARI downstream adaptation
Scope:
- `pkg/ari/server.go`
- `pkg/ari/types.go` only if needed for mapped fields
- related tests

Goal:
- keep ARI `session/*` behavior stable while adapting its shim-facing internals
- map `runtime/status` back into existing `session/status` result expectations

Why third:
- ARI is downstream of the shim client change, not the protocol-defining surface itself

#### Task seam D — Focused proof additions
Scope:
- test files only

Goal:
- add explicit proof for:
  - renamed methods
  - `session/subscribe(afterSeq)` behavior
  - history/live shared envelope
  - `runtime/status.recovery.lastSeq`
  - `runtime/stop` reply-then-disconnect behavior

Why separate:
- S02 is easy to "mostly rename" without actually proving the replay/status shape. The new tests are what stop that from happening.

## Risks and Gotchas

### 1. Stale-plan trap
`docs/plan/shim-rpc-redesign.md` is useful history, but if the planner follows it literally it will rebuild the wrong protocol:
- wrong naming variants (`runtime/get_state` vs `runtime/status`)
- wrong passthrough posture
- wrong notification model

Use `docs/design/runtime/shim-rpc-spec.md` instead.

### 2. Live/history truth is the real breaking change
Renaming `Prompt` → `session/prompt` is easy. Making `runtime/history` replay the **same shape** that subscribers receive is the real contract change, and it touches storage, server, client parsing, and tests together.

### 3. `runtime/stateChange` does not currently exist in the live path
`pkg/events/translator.go` has `NotifyTurnStart` / `NotifyTurnEnd`, but nothing calls them outside tests, and the spec wants `runtime/stateChange`, not the old `turn_start` / `turn_end` typed events. S02 must decide whether to replace those helpers or introduce a new runtime-state-change emission path instead of carrying the old event names forward.

### 4. Bootstrap visibility is fragile
Because the translator/log start after `Create()`, bootstrap exchanges are currently invisible to history/live subscribers. That matches the contract’s "bootstrap is not external work" stance. A refactor that changes startup ordering could accidentally expose bootstrap noise and blur the contract again.

### 5. Full `pkg/runtime` is not a clean slice gate today
`go test ./pkg/runtime -count=1` currently fails in `TestTerminalManager_Create_Success`, unrelated to shim RPC. S02 should use focused runtime tests or a narrower command as its verification gate until that terminal issue is handled separately.

## Verification Approach

### Focused slice gate
Use these as the main regression gate for S02:

```bash
bash scripts/verify-m002-s01-contract.sh
go test ./pkg/events ./pkg/rpc ./pkg/agentd ./pkg/ari -count=1
go test ./pkg/runtime -run 'TestRuntimeSuite' -count=1
```

### Tests the slice should add or update explicitly

1. **Server protocol tests** (`pkg/rpc/server_test.go`)
   - new method names respond correctly
   - unknown old method names fail
   - `session/subscribe(afterSeq)` only emits `seq > afterSeq`
   - `runtime/history` returns replayable notification envelopes
   - `runtime/status` returns `state` plus `recovery.lastSeq`
   - `runtime/stop` replies before the socket fully tears down

2. **Client tests** (`pkg/agentd/shim_client_test.go`)
   - parse `session/update` and `runtime/stateChange`
   - exercise new status/history methods if added
   - confirm no remaining dependency on `$/event`

3. **Process-manager integration tests** (`pkg/agentd/process_test.go`)
   - start → subscribe → prompt still works through the renamed surface
   - status fetch still works through the new runtime/status result shape

4. **ARI tests** (`pkg/ari/server_test.go`)
   - session lifecycle still passes when shim internals move to the new surface
   - `session/status` still maps the runtime truth correctly

### Useful non-gate sanity checks

```bash
rg -n '"Prompt"|"Cancel"|"Subscribe"|"GetState"|"GetHistory"|"Shutdown"|"\$/event"' pkg cmd tests
rg -n 'turn_start|turn_end|file_read|file_write|command' pkg/events pkg/rpc pkg/agentd tests
```

Expected end state:
- no normative code-path references to the legacy shim surface remain
- any retained legacy names only appear in historical docs or explicitly transitional comments/tests, if at all

## Skills Discovered

| Technology | Skill | Status |
|------------|-------|--------|
| Go | none relevant found | not installed |
| JSON-RPC | `azzgo/agent-skills@aria2-json-rpc` | found but unrelated; not installed |

No installed skill materially improves this slice. The repo’s own Go/runtime patterns are the right guide.

## Sources

- `docs/design/runtime/shim-rpc-spec.md`
- `docs/design/runtime/agent-shim.md`
- `docs/design/runtime/runtime-spec.md`
- `docs/design/runtime/design.md`
- `docs/plan/shim-rpc-redesign.md`
- `docs/plan/unified-modification-plan.md`
- `scripts/verify-m002-s01-contract.sh`
- `pkg/rpc/server.go`
- `pkg/rpc/server_test.go`
- `pkg/agentd/shim_client.go`
- `pkg/agentd/shim_client_test.go`
- `pkg/agentd/process.go`
- `pkg/agentd/process_test.go`
- `pkg/ari/server.go`
- `pkg/ari/types.go`
- `pkg/ari/server_test.go`
- `pkg/events/translator.go`
- `pkg/events/types.go`
- `pkg/events/log.go`
- `pkg/events/log_test.go`
- `pkg/runtime/runtime.go`
- `pkg/runtime/runtime_test.go`
- `pkg/runtime/client.go`
- `cmd/agent-shim/main.go`
- `cmd/agent-shim-cli/main.go`
- `.gsd/milestones/M002/slices/S01/S01-SUMMARY.md`
- `.gsd/milestones/M002/slices/S01/S01-RESEARCH.md`
