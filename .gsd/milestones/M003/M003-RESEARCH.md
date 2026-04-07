# M003 — Research

**Date:** 2026-04-07

## Summary

The codebase already has the raw ingredients for recovery work: `agent-shim` persists `state.json` atomically, appends `events.jsonl`, SQLite persists session/workspace metadata, and there is an integration test named `TestAgentdRestartRecovery`. What it does **not** have is truthful restart behavior. Runtime truth is still mostly held in `ProcessManager.processes` (in-memory), `session/status` only exposes live shim state when that in-memory map is populated, and there is no explicit degraded/blocked recovery surface anywhere in the current types or ARI responses.

The biggest structural issue is split authority. Workspace safety is spread across three refcount systems (`pkg/workspace/manager.go`, `pkg/ari/registry.go`, and SQLite `workspace_refs/ref_count`), but the live session path does not actually call `AcquireWorkspace` / `ReleaseWorkspace` at all. Event recovery is similarly split: history exists, but damaged-tail tolerance does not, and the current `GetHistory(fromSeq)` then `Subscribe()` flow has an explicit gap. Restart recovery should therefore be sliced as truth-model work first, reconnect second, history hardening third, cleanup safety fourth.

The final Codex proof should be a late slice, not an early one. The repository ships real bundles only for `claude-code` and `gsd-pi`; Codex is present in docs and research (`codex-acp`) but not as a checked-in bundle or integration path. Today there is no hardened runtime path to prove against.

## Recommendation

Plan this milestone as five slices:

1. **Recovery contract + fail-closed status surface + stable discovery root**
   - Add explicit recovery posture to status responses (healthy/degraded/blocked or equivalent).
   - Make read-only inspection available even when recovery certainty is incomplete.
   - Refuse operational methods (`prompt`, `stop`, `cleanup`, resume-like actions) when certainty is not established.
   - Normalize the actual shim state-dir/socket discovery contract before implementing reconnect.

2. **Live shim rediscovery/reconnect + truthful session rebuild**
   - On `agentd` startup, scan the shim socket root, reconnect to live shims, call shim state APIs, resubscribe to events, and repopulate daemon runtime state from the live shim set.
   - Reconcile live shim truth with persisted SQLite metadata and mark mismatches as degraded/blocked instead of guessing.

3. **Event history hardening**
   - Make `events.jsonl` tolerant of damaged tail writes.
   - Replace or wrap `GetHistory(fromSeq)` + `Subscribe()` with an atomic catch-up-to-live mechanism (`subscribe(fromSeq)` or equivalent).

4. **Workspace truth + cleanup safety after restart**
   - Move cleanup gating onto persisted/reconciled workspace reference truth.
   - Wire session lifecycle to SQLite `workspace_refs` instead of relying on volatile registry/manager counters.
   - Block cleanup when reconciliation is incomplete.

5. **Codex hardened-path proof**
   - Add a real Codex runtime path (bundle/config/test harness) and prove one prompt round-trip on the hardened runtime, after the recovery semantics are trustworthy.

## Implementation Landscape

### Key Files

- `cmd/agentd/main.go` — daemon entrypoint. It starts fresh managers and the ARI server but does no startup reconnect scan. It also still uses `Stat -> Remove -> Serve` on the ARI socket, which leaves the documented socket race in place.
- `pkg/agentd/process.go` — core shim lifecycle logic. Important current constraints:
  - `Connect(ctx, sessionID)` is **not** restart reconnect; it only returns an existing client from the in-memory `processes` map.
  - `createBundle` places bundles under `WorkspaceRoot`, not a separate bundle root.
  - `createBundle` also switches to `/tmp/agentd-shim/<session>` whenever `cfg.Socket != ""`; since `socket` is a required config field, this means the code path always uses `/tmp/agentd-shim`, not the documented `/run/agentd/shim` root.
- `pkg/agentd/shim_client.go` — current shim RPC client. It still uses the older `Prompt` / `Subscribe` / `GetState` / `GetHistory` / `Shutdown` surface and event parsing still includes file/command event types.
- `pkg/ari/server.go` — current truth surface for operators. `session/status` only asks the shim for runtime state when metadata says the session is `running` and the in-memory process map still has a client. After daemon restart, even a live shim will not appear here until reconnect is implemented. No degraded/blocked fields exist.
- `pkg/events/log.go` — durable event log. Tail corruption currently breaks both `ReadEventLog` and `OpenEventLog`, because `countLines()` also fails on decode errors.
- `pkg/runtime/runtime.go` — shim-side runtime truth. It writes atomic `state.json`, records `lastTurn`, and drives ACP prompt/cancel. This is the best existing truth source for “what the live shim believes,” but it currently has no explicit recovery-certainty state.
- `pkg/spec/state_types.go` / `pkg/spec/state.go` — durable runtime state contract and path helpers. Current runtime state has `creating/created/running/stopped` only; no degraded/blocked or recovery provenance fields.
- `pkg/meta/schema.sql` / `pkg/meta/session.go` / `pkg/meta/workspace.go` — persisted metadata. SQLite already has `workspace_refs` and trigger-maintained `ref_count`, but the live ARI session path does not call those APIs. Session persistence also lacks durable config needed for truthful rebuild (no env, MCP servers, permissions, system prompt, bootstrap policy).
- `pkg/workspace/manager.go` — first in-memory workspace refcount system. Useful as early implementation scaffolding, but not restart-safe.
- `pkg/ari/registry.go` — second in-memory workspace refcount system. `workspace/cleanup` currently gates on this, not on persisted refs.
- `tests/integration/restart_test.go` — existing restart surface. Today it proves mainly that metadata survives daemon restart; it explicitly allows the post-restart session to be merely inspectable rather than truly reconnected.
- `bin/bundles/README.md` and `bin/bundles/{claude-code,gsd-pi}` — real-client proof surfaces. There is no checked-in Codex bundle today.
- `docs/plan/unified-modification-plan.md` — already identifies the right backlog items: atomic event recovery, refcount hardening, socket race cleanup, durable session config gaps, and Codex proof.

### Build Order

**Prove fail-closed truthfulness first.** Before implementing reconnect, add a status surface that can truthfully say “inspectable but blocked” when recovery is incomplete. That retires the main risk of this milestone: silent guesswork.

**Normalize discovery inputs next.** Reconnect work is not credible until `agentd` and `agent-shim` agree on the actual state-dir/socket root and socket ownership semantics. The current `/tmp/agentd-shim` vs `/run/agentd/shim` drift should be resolved early.

**Then implement live rediscovery/reconnect.** At daemon startup, scan the shim socket root, dial live shims, query their runtime state, and rebuild in-memory process/session runtime state from live shim truth plus persisted metadata.

**After reconnect works, harden history replay.** The current `GetHistory(fromSeq)` then `Subscribe()` model has a gap, and damaged-tail logs can break both replay and reopen. That needs an explicit recovery contract, not a best-effort helper.

**Then unify workspace cleanup safety.** Today cleanup is guarded by volatile counts, while the persisted refcount system is not actually wired into session lifecycle. Once reconnect and reconciliation exist, cleanup must move onto persisted/reconciled truth.

**Finish with Codex proof.** Codex should validate the hardened path, not distract from building it.

### Verification Approach

- Unit verification:
  - `go test ./pkg/events/... ./pkg/agentd/... ./pkg/ari/... ./pkg/meta/...`
- Restart integration verification:
  - Extend `tests/integration/restart_test.go` so restart must reconnect to a still-running shim and surface truthful state, not just preserved metadata.
- Recovery fault injection:
  - Truncate `events.jsonl` tail and verify status remains readable, history replay skips only the damaged tail, and operations block if certainty cannot be established.
  - Start daemon, create a running session, kill only `agentd`, restart daemon, verify `session/status` reflects live shim truth and operational gating follows recovery certainty.
  - Force metadata/live mismatch (orphan shim socket, stale session row, stale workspace ref) and verify read-only inspection remains available while operations fail closed.
- Cleanup safety verification:
  - Prove a workspace with active/recovered session references cannot be cleaned up after restart.
- Real-client verification:
  - Keep existing Claude/pi manual proof paths working.
  - Add one real Codex prompt round-trip on the hardened runtime path.

## Constraints

- `pkg/agentd/config.go` requires `socket`, so `pkg/agentd/process.go` currently always takes the `/tmp/agentd-shim` branch. Any reconnect design that assumes `/run/agentd/shim` is wrong until this contract is fixed.
- `bundleRoot` appears in tests and docs, but not in the actual `Config` struct. Bundles currently derive from `WorkspaceRoot`, so recovery work cannot assume bundle location is already a separate, explicit contract.
- There is no persisted SessionConfig beyond `runtimeClass`, `workspaceId`, room fields, labels, and state. Recovering env, permissions, MCP servers, system prompt, or bootstrap behavior requires new durable fields or an explicit non-goal.
- No explicit degraded/blocked recovery state exists in `meta.SessionState`, `spec.Status`, or ARI response types. The operator-visible recovery posture must be added; it cannot be inferred from current enums.
- `ProcessManager.Connect` is already named like restart recovery, but it is only an in-memory lookup. The planner should treat true reconnect as new work.
- The repo ships real bundles only for `claude-code` and `gsd-pi`. Codex proof requires new bundle/config/test setup.

## Common Pitfalls

- **Treating metadata as runtime truth** — `session/status` currently trusts stored session state first and only asks the shim when the in-memory process map still exists. After daemon restart that map is empty, so status lies by omission unless reconnect fills it or the surface says it is degraded/blocked.
- **Assuming workspace refcount safety already exists** — SQLite `workspace_refs` exists, but the live session path never calls `AcquireWorkspace` / `ReleaseWorkspace`. Cleanup today is gated by in-memory registry counts only.
- **Fixing damaged logs only in `ReadEventLog`** — `OpenEventLog` also calls `countLines()`, so tail corruption must not break sequence initialization on reopen.
- **Proving reconnect with mockagent only** — the milestone explicitly requires one real Codex round-trip. Existing automated coverage is still mockagent-centric.
- **Treating socket race as later polish** — restart discovery depends on deterministic socket ownership and cleanup. The daemon still uses `Stat -> Remove -> Serve` on its own socket.

## Open Risks

- Live shim state and persisted session rows may disagree on whether a session is active, and the current schema has nowhere to record “inspectable but blocked pending reconciliation.”
- Durable session-config work may overlap with M002 contract-convergence decisions if ownership boundaries are not fixed first.
- Codex ACP adapter behavior may differ from Claude/pi around persistence or capability shape, so the Codex slice should stay proof-oriented and late.

## Candidate Requirements

- **Candidate:** `session/status` must expose an explicit recovery posture (`healthy` / `degraded` / `blocked`, or equivalent) and keep read-only inspection available when certainty is incomplete.
- **Candidate:** after `agentd` restart, live shim discovery and reconnect must rebuild session runtime truth from discovered shim sockets plus persisted metadata, not from metadata alone.
- **Candidate:** event recovery must tolerate damaged-tail `events.jsonl` writes and provide an atomic catch-up-to-live subscription boundary.
- **Candidate:** workspace cleanup must use persisted or reconciled reference truth, not volatile in-memory counters.
- **Candidate:** one real Codex ACP prompt round-trip on the hardened runtime path is required before milestone completion.

## Skills Discovered

| Technology | Skill | Status |
|------------|-------|--------|
| Go | none relevant found | not installed |
| SQLite | `martinholovsky/claude-skills-generator@sqlite database expert` | found but not installed — too DB-generic for this milestone’s core recovery work |
| Codex | `supercent-io/skills-template@oh-my-codex`, `softaworks/agent-toolkit@codex` | found but not installed — generic Codex usage skills, not clearly useful for ACP/runtime hardening |

## Sources

- `docs/plan/unified-modification-plan.md` — already captures the right backlog shape: atomic event recovery, refcount hardening, socket race cleanup, durable session config gaps, and Codex proof.
- `docs/research/acpx.md` — Codex is already documented in project research as `codex-acp` via `npx`, but there is no checked-in bundle or runtime proof path yet.
- `tests/integration/restart_test.go` — current restart coverage proves the recovery surface exists but does not yet enforce truthful live reconnect.
