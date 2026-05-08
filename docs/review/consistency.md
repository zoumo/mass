# Code-Documentation Consistency Review Report

Date: 2026-04-30

## Summary

This report cross-references the MASS codebase implementation against design documents, skill files, and architectural documentation. The review covers API contracts, state machines, event flow, CLI commands, architecture layout, decisions, and development rules.

**Overall Assessment**: The codebase and documentation are largely well-aligned. The major refactoring from SQLite to bbolt, from Room/Session to Workspace/AgentRun, and the ARI client redesign (D084-D127) have been successfully reflected in both code and docs. Most inconsistencies found are minor documentation gaps or stale references in peripheral docs.

**Statistics**:
- Critical issues: 0
- High issues: 2
- Medium issues: 6
- Low issues: 5

---

## API Contract Mismatches

### 1. `system/info` method not in contract-convergence.md ARI boundary table

- **Design doc**: `docs/design/contract-convergence.md` (ARI Boundary section)
- **Code**: `pkg/ari/api/methods.go:5` defines `MethodSystemInfo = "system/info"`; `pkg/ari/server/service.go:200-206` registers `system/info`
- **What doc says**: ARI Boundary table lists only `workspace/*`, `agent/*`, `agentrun/*` method groups
- **What code does**: Also implements `system/info` which returns daemon version info
- **Severity**: Low
- **Recommended resolution**: Update `docs/design/contract-convergence.md` and `docs/design/mass/ari-spec.md` to list `system/info` in the ARI boundary table

### 2. `agentrun/prompt` returns `{accepted: true}` (fire-and-forget) vs state-machine.md describes synchronous blocking call

- **Design doc**: `docs/develop/state-machine.md` (Prompt Complete Lifecycle section, lines 146-183)
- **Code**: `pkg/ari/server/server.go:484-518` — `Prompt` transitions idle->running, dispatches prompt via `client.SendPrompt`, and returns `{Accepted: true}` immediately
- **What doc says**: The lifecycle diagram (lines 146-183) shows `session/prompt` RPC blocking until `StopReason` is returned, which is the agent-run layer behavior
- **What code does**: At the ARI layer, `agentrun/prompt` is fire-and-forget (returns immediately with `accepted: true`); the blocking prompt is only at the agent-run internal layer
- **Severity**: Low (diagram correctly shows agent-run internal behavior, but the ARI-level fire-and-forget semantics could be more explicitly called out)
- **Recommended resolution**: Add a note in `state-machine.md` clarifying that the ARI `agentrun/prompt` returns immediately; the blocking behavior described is internal to agent-run

### 3. `ari-spec.md` AgentRun domain shape lists `spec.permissions` as `string?` — code uses `PermissionPolicy` type

- **Design doc**: `docs/design/mass/ari-spec.md:566`
- **Code**: `pkg/ari/api/domain.go:143` — `Permissions apiruntime.PermissionPolicy`
- **What doc says**: `spec.permissions` is `string?`
- **What code does**: Uses `apiruntime.PermissionPolicy` which is a string type alias with defined constants
- **Severity**: Low (functionally equivalent — PermissionPolicy is a string enum)
- **Recommended resolution**: No action needed, types are compatible on the wire

---

## State Machine Inconsistencies

### 4. `restarting` phase: state-machine.md describes `idle/running → restarting → creating`; ari-spec.md shows `restarting → stopped → creating`

- **Design doc**: `docs/develop/state-machine.md:49` says "`idle`/`running` + `agentrun/restart` → `restarting` → `creating`"
- **Design doc**: `docs/design/mass/ari-spec.md:611` says "`restarting → stopped → creating`: existing process stopped and restart continues"
- **Code**: `pkg/agentd/process.go:169` guards on `PhaseRestarting` to drop non-stopped events; `pkg/ari/server/server.go:557-563` restart handler
- **What docs say**: Two different stories about the transition path through `restarting`
- **Severity**: Medium
- **Recommended resolution**: Unify documentation. The ari-spec.md description (`restarting → stopped → creating`) is more detailed and matches the actual process of stopping the old shim before starting new one. state-machine.md should be updated to match.

### 5. contract-convergence.md state machine diagram omits `restarting` state

- **Design doc**: `docs/design/contract-convergence.md:62-67` — state machine diagram
- **Code**: `pkg/runtime-spec/api/types.go:23` defines `PhaseRestarting Phase = "restarting"`
- **What doc says**: Simple diagram shows `creating`, `idle`, `running`, `stopped`, `error` only
- **What code does**: Implements a `restarting` phase with guards in `routeEvent`
- **Severity**: Medium
- **Recommended resolution**: Add `restarting` to the contract-convergence.md state machine diagram, or add a note that the simple diagram omits `restarting` for clarity (ari-spec.md has the full diagram)

### 6. `streamSeq` field designed in D067/D077/D078/D079 but never implemented in code

- **Design doc**: `.gsd/DECISIONS.md` — D067, D077, D078, D079 all reference `streamSeq` as a turn-aware ordering field
- **Code**: `pkg/agentrun/api/event.go` — `AgentRunEvent` struct has no `StreamSeq` field
- **What doc says**: "streamSeq resets to 0 per turn and increments within it"
- **What code does**: Only `seq` (global) and `turnId` are implemented; no per-turn sub-sequence
- **Severity**: Medium (design decision documented but not yet implemented)
- **Recommended resolution**: Add a note in DECISIONS.md marking D067/D077-D079 as "designed but implementation deferred", or implement the field

---

## Architecture Divergences

### 7. ARCHITECTURE.md references legacy `session/subscribe` terminology

- **Design doc**: `docs/ARCHITECTURE.md:134` — "session/subscribe(afterSeq=lastSeq)"
- **Code**: `pkg/agentrun/api/methods.go:8` — only `MethodRuntimeWatchEvent = "runtime/watch_event"` exists
- **What doc says**: Restart recovery uses "session/subscribe(afterSeq=lastSeq)"
- **What code does**: Uses `runtime/watch_event` with `fromSeq` parameter
- **Severity**: High
- **Recommended resolution**: Update ARCHITECTURE.md "Restart recovery" section to use `runtime/watch_event(fromSeq=0)` instead of the legacy `session/subscribe(afterSeq=lastSeq)` terminology

### 8. ARCHITECTURE.md mentions `session/subscribe(fromSeq=0)` in Session metadata pipeline

- **Design doc**: `docs/ARCHITECTURE.md:155` references `runtime/status` overlay approach
- **Code**: The actual method is `runtime/watch_event` for event subscription, `runtime/status` for state query
- **What doc says**: "runtime/status → Status() → read state.json from disk → overlay real-time EventCounts from Translator memory → return enriched State"
- **What code does**: This matches actual implementation in `pkg/agentrun/server/service.go`
- **Severity**: (Confirmed aligned — no issue here)

### 9. ARCHITECTURE.md default socket path vs code

- **Design doc**: `docs/design/mass/ari-spec.md:25` says default path is `/run/mass/mass.sock`
- **Code**: `pkg/agentd/options.go` derives socket from `--root` flag (default `$HOME/.mass`), so default is `$HOME/.mass/mass.sock`
- **SKILL.md**: `skills/mass-guide/SKILL.md:17` says "defaults to `$HOME/.mass/mass.sock`"
- **Severity**: Medium
- **Recommended resolution**: Update `docs/design/mass/ari-spec.md` to state `$HOME/.mass/mass.sock` (or clarify both production and user paths). The SKILL.md is correct.

### 10. ARCHITECTURE.md binary list says `mass mesh-mcp` but package name is `workspacemcp`

- **Design doc**: `docs/ARCHITECTURE.md:89` — "bin/mass" with "mass run" + "mass mesh-mcp"
- **Code**: `cmd/mass/commands/workspacemcp/command.go:167` — uses `Use: "mesh-mcp"`
- **What doc says**: `mass mesh-mcp` subcommand
- **What code does**: Command registered as `mesh-mcp` under the `mass` binary. Package is named `workspacemcp` internally.
- **Severity**: Low (package name vs cobra Use name — CLI surface is correct)
- **Recommended resolution**: No action needed. Internal package name is an implementation detail.

---

## CLI Documentation Gaps

### 11. SKILL.md lists `massctl agentrun task wait` command — not documented in ari-spec.md

- **Design doc**: `docs/design/mass/ari-spec.md` — no mention of `task/wait`
- **Code**: `cmd/massctl/commands/agentrun/task_wait.go` — CLI-only polling wrapper (no corresponding ARI method)
- **SKILL.md**: Not listed in SKILL.md either
- **Severity**: Low
- **Recommended resolution**: Add `task wait` to the SKILL.md Task Lifecycle section. This is a CLI convenience (client-side polling), not an ARI method, so ari-spec.md doesn't need it.

### 12. Orchestration guide references missing `docs/design/mass/lifecycle-hooks.md`

- **Design doc**: `docs/develop/orchestration-guide.md:316` links to `./mass/lifecycle-hooks.md`
- **Code/Filesystem**: The file does NOT exist at `docs/design/mass/lifecycle-hooks.md`; only `docs/design/mass/watch-framework.md` exists
- **Severity**: High
- **Recommended resolution**: Either create `lifecycle-hooks.md` as referenced, or update the link to point to `watch-framework.md` (which covers event delivery to callers)

### 13. Orchestration guide uses `agentrun_send` MCP tool name inconsistently

- **Design doc**: `docs/develop/orchestration-guide.md:231,264,273,381` references `agentrun_send`
- **Code**: `pkg/ari/api/workspace_mesh.go:6` defines `WorkspaceMeshToolAgentRunSend = "agentrun_send"`
- **Severity**: Low (names match — this is consistent)
- **Recommended resolution**: No action needed.

### 14. SKILL.md missing `massctl ext pipeline` command group

- **SKILL.md**: `skills/mass-guide/SKILL.md` does not document `massctl ext pipeline` commands
- **Code**: `cmd/massctl/commands/ext/command.go` registers `ext` with `pipeline` subcommand
- **Severity**: Medium
- **Recommended resolution**: Add `ext pipeline` commands to SKILL.md or create a separate reference. The `ext` group is for offline tools (no daemon required), which is a distinct usage category.

---

## Decision Implementation Status

### Fully Implemented Decisions (Spot-Checked)

| Decision | Status | Evidence |
|----------|--------|----------|
| D084 (bbolt backend) | Implemented | `pkg/agentd/store/store.go` uses `go.etcd.io/bbolt` |
| D085 (single Phase enum) | Implemented | `pkg/runtime-spec/api/types.go` defines unified Phase |
| D086 (Workspace replaces Room) | Implemented | No `room/*` methods in code; `workspace/send` exists |
| D087 (workspace+name identity) | Implemented | All methods use `ObjectKey{Workspace, Name}` |
| D088 (Shim write authority) | Implemented | `routeEvent` in `process.go:135-195` handles DB state from notifications |
| D107 (self-fork) | Implemented | `ProcessManager.RunBinary` + `os.Executable()` pattern |
| D109 (agent=template, agentrun=instance) | Implemented | `agent/*` and `agentrun/*` method groups |
| D111 (socket path validation) | Implemented | `pkg/runtime-spec/maxsockpath_darwin.go`, `maxsockpath_linux.go` |
| D112 (adapter pattern for Service) | Implemented | `pkg/ari/server/server.go` uses adapters |
| D119 (writeState closure) | Implemented | Referenced in ARCHITECTURE.md constraint 10 |
| D126 (controller-runtime Client) | Implemented | `pkg/ari/client/interfaces.go` |

### Decisions Designed But Not Yet Implemented

| Decision | Status | Notes |
|----------|--------|-------|
| D067 (streamSeq) | Not implemented | Event envelope has `seq` + `turnId` only |
| D077/D078/D079 (StreamSeq details) | Not implemented | Related to D067 |
| D089 (unconditional session/load) | Partially | `session/load` method exists in `pkg/agentrun/api/methods.go` |

---

## Event Flow Consistency

### Event Consumer Guide vs Code

The `docs/develop/event-consumer-guide.md` is **well-aligned** with the actual implementation:

1. **Event types** (Section 3): All 10 event types in the guide match `pkg/agentrun/api/event_constants.go` exactly:
   - `turn_start`, `agent_message`, `agent_thinking`, `user_message`, `tool_call`, `tool_result`, `plan`, `turn_end`, `error`, `runtime_update`

2. **AgentRunEvent envelope** (Section 2): Matches `pkg/agentrun/api/event.go:23-42` exactly:
   - `runId`, `sessionId`, `seq`, `time`, `type`, `turnId`, `payload`

3. **Watch method**: Guide says `runtime/watch_event` — matches `pkg/agentrun/api/methods.go:8` (`MethodRuntimeWatchEvent`)

4. **fromSeq semantics** (Section 1.3): Guide describes K8s List-Watch pattern — matches `SessionWatchEventParams.FromSeq` in `pkg/agentrun/api/types.go:37-40`

5. **watchId demux** (Section 1.2): Matches `AgentRunEvent.WatchID` field in code

**Minor note**: The guide uses `runtime/event_update` as the notification method — matches `pkg/agentrun/api/methods.go:16`

---

## Development Rules Compliance

### code-principle.md Rules

The codebase follows the principles in `docs/develop/rules/code-principle.md`:

- **Clarity**: Descriptive names used throughout (e.g., `reserveIdleAgent`, `routeEvent`, `startEventConsumer`)
- **Simplicity**: Standard patterns (mutex, bbolt, cobra) used consistently
- **Consistency**: Naming conventions are stable (e.g., all ObjectKey patterns, all adapter patterns)
- **DRY**: `reserveIdleAgent` helper shared by `Prompt`, `Send`, `TaskDo`, `TaskRetry`

### api/ subdirectory rule (ARCHITECTURE.md constraint 8)

- **Rule**: "api/ packages contain only struct, const, enum. No interfaces, no functions"
- **Code audit**:
  - `pkg/ari/api/`: Contains structs, consts, ObjectKey functions (helper methods on types like `IsDisabled()`, `ARIView()`, `ApplyListOptions`) — these are type methods, not standalone functions
  - `pkg/agentrun/api/`: Contains `EventTypeOf(ev Event) string` (D118 — necessary cross-package accessor for sealed interface)
  - `pkg/runtime-spec/api/`: Pure types only
- **Assessment**: Minor bending for type methods (`ARIView`, `ApplyListOptions`) but these are on types defined in the same package. The `EventTypeOf` function is documented in D118 as an intentional minimal exception.
- **Severity**: Low (acceptable deviations with documented rationale)

---

## Recommendations

### Priority 1 (High — should fix soon)

1. **Update ARCHITECTURE.md** to replace `session/subscribe(afterSeq=lastSeq)` with `runtime/watch_event(fromSeq=0)` in the Restart Recovery section. This is the most visible documentation error — the legacy terminology could confuse new contributors.

2. **Create or redirect `lifecycle-hooks.md`** — the orchestration guide links to a non-existent file. Either create a placeholder explaining hooks are a design proposal, or redirect to `watch-framework.md`.

### Priority 2 (Medium — schedule for cleanup)

3. **Unify `restarting` documentation** — state-machine.md and ari-spec.md describe slightly different transition paths. Converge on the ari-spec.md description.

4. **Add `restarting` to contract-convergence.md** state machine diagram for completeness.

5. **Mark D067/D077-D079 as deferred** in DECISIONS.md or implement `streamSeq`.

6. **Update ari-spec.md default socket path** from `/run/mass/mass.sock` to `$HOME/.mass/mass.sock`.

7. **Document `massctl ext pipeline`** in SKILL.md.

8. **Add `system/info`** to contract-convergence.md ARI boundary table.

### Priority 3 (Low — nice to have)

9. Add ARI-level fire-and-forget note to state-machine.md prompt lifecycle.

10. Add `task wait` CLI command to SKILL.md.

11. Consider whether `ApplyListOptions` and `ARIView` violate the api/ subdirectory rule strictly enough to warrant moving them.
