# S01: Design contract convergence — Research

**Date:** 2026-04-07

## Summary

This slice is **targeted research** with a strong contract-audit component. The repository already contains most of the concepts needed for M002, but they currently tell **different stories depending on which file you read**. The main drift axes are:

1. **Room ownership drift**
   - `docs/design/orchestrator/room-spec.md:21` says agentd only sees independent sessions and does not see rooms.
   - `docs/design/agentd/agentd.md:252-304` defines a `RoomManager` and room message routing.
   - `docs/design/agentd/ari-spec.md:410-460` defines `room/create`, `room/status`, and `room/delete`.
   - The persistence layer already has a `rooms` table and CRUD (`pkg/meta/schema.sql`, `pkg/meta/room.go`), but `pkg/ari/server.go` does **not** implement any `room/*` methods.

2. **Bootstrap / `session/new` drift**
   - `docs/design/agentd/ari-spec.md:168-207` still documents `session/new` with `systemPrompt`, `prompt`, `env`, and `mcpServers`.
   - Actual ARI request types only expose `workspaceId`, `runtimeClass`, `labels`, `room`, and `roomAgent` (`pkg/ari/types.go:73-88`), and the CLI exposes the same limited surface (`cmd/agentdctl/session.go:76-92`).
   - `pkg/ari/server.go:390-438` creates metadata only; the real work enters later through `session/prompt`, which auto-starts the process (`pkg/ari/server.go:453-522`).
   - `pkg/agentd/process.go:206-239` generates a config that omits `systemPrompt`, ACP session config, and per-session permissions entirely.

3. **Runtime / ACP startup semantics drift**
   - `docs/design/runtime/config-spec.md` and `docs/design/runtime/runtime-spec.md` say `systemPrompt` is sent as a silent seed prompt after `session/new`.
   - `docs/design/runtime/design.md` gets closest to the desired separation of “create/config first, work later”, but still blurs `acpAgent.systemPrompt` into ACP `session/new` language in a few places (`docs/design/runtime/design.md:62-73`).
   - Actual runtime behavior is in `pkg/runtime/runtime.go:135-160`: ACP `session/new` uses resolved `cwd` + `mcpServers`, then `systemPrompt` is sent as a **separate first prompt**.

4. **Shim RPC drift**
   - Legacy docs and code still use `Prompt` / `Cancel` / `Subscribe` / `GetState` / `GetHistory` / `Shutdown` plus `$/event` (`docs/design/runtime/shim-rpc-spec.md`, `pkg/rpc/server.go`, `pkg/agentd/shim_client.go`).
   - The clean-break direction is already written down in `docs/plan/shim-rpc-redesign.md`, but S01 needs to make the rest of `docs/design/*` point at the same contract before S02 changes code.

5. **Security / workspace / recovery truth drift**
   - `R038` is only partially expressed. Local workspace attachment, hooks, shared workspace semantics, env injection, and ACP capability posture are split across docs and plans instead of being one explicit boundary contract.
   - `docs/plan/shim-rpc-redesign.md` argues shim should not implement ACP fs/terminal client capabilities, but the runtime currently advertises **filesystem** client capability (`pkg/runtime/runtime.go:126-129`) and implements file + terminal handlers in `pkg/runtime/client.go` / `pkg/runtime/terminal.go`.
   - Recovery docs assume scanning `/run/agentd/shim/*`, but `pkg/agentd/process.go:288-292` currently falls back to `/tmp/agentd-shim/<session>` whenever `cfg.Socket != ""`, which is effectively always.

The best planning stance is: **S01 should freeze one target contract and document implementation lag explicitly in planning/research artifacts, not preserve the current mixed stories inside the design specs.**

## Requirement Targets

### Primary
- **R032** — `docs/design/*` must define one non-conflicting contract for Room, Session, Runtime, Workspace, and recovery semantics.
- **R033** — `agentRoot.path`, resolved `cwd`, `session/new`, `systemPrompt`, and bootstrap behavior must have one authoritative meaning.

### Supporting
- **R038** — local workspace attachment, hook execution, env injection, and shared workspace access need explicit boundary rules now.
- **R036** — durable session config truth is not the main S01 implementation target, but S01 must identify exactly which contract fields are missing from persistence.
- **R044** — keep follow-on hardening work visibly separate so S01 does not try to absorb restart/replay implementation.

## Recommendation

Use the already-recorded planning decisions as the anchor and converge the docs around them:

- **D015**: `Room Spec` is orchestrator-owned desired state; ARI `room/*` (if kept) is agentd-owned realized runtime state.
- **D016**: `session/new` is **configuration-only**. It should not carry bootstrap task input. Work should enter later via `session/prompt`.
- **D008**: shim RPC moves to the clean-break `session/*` + `runtime/*` story; no backward-compatibility preservation.

That leads to one coherent target contract:

1. **Room**
   - `docs/design/orchestrator/room-spec.md` describes desired state only.
   - `docs/design/agentd/agentd.md` and `docs/design/agentd/ari-spec.md` describe realized runtime room state only.
   - Do **not** keep the current triple-story where agentd both “sees” and “does not see” rooms.

2. **Bootstrap**
   - `session/new` creates durable session configuration and identity only.
   - `agentRoot.path` is the only source of runtime working directory resolution; resolved `cwd` is derived at runtime.
   - `systemPrompt` is part of session configuration, not overlapping task input.
   - Initial work enters via later `session/prompt`, not via a `prompt` field on `session/new`.

3. **State / recovery mapping**
   - The docs need one mapping table for runtime state (`creating/created/running/stopped`), session state (`created/running/paused:warm/paused:cold/stopped`), and process/recovery status.
   - The contract must explicitly distinguish OAR session/agent ID from ACP `sessionId`.

4. **Shim protocol**
   - S01 should rewrite the design docs toward the clean-break target surface.
   - The legacy PascalCase / `$/event` implementation belongs in implementation-gap notes, not as the normative spec.

5. **Security boundaries**
   - S01 should make the boundary rules explicit even if enforcement lands in S03: local path canonicalization/ownership, hooks as host command execution, env injection precedence, shared workspace implications, and ACP fs/terminal posture.

**Small but useful truth patch:** fix `bin/bundles/claude-code/config.json:2` (`oaiVersion` → `oarVersion`) during the slice or as a tiny prerequisite task. Later real-client validation should not be blocked by a broken checked-in fixture.

## Implementation Landscape

### Contract anchors

These are the least-wrong anchors to build from:

- `docs/plan/unified-modification-plan.md` — best existing inventory of the design conflicts and their intended convergence direction.
- `docs/plan/shim-rpc-redesign.md` — authoritative clean-break direction for the shim boundary.
- `docs/design/runtime/design.md` — closest current description of “config/create first, prompt later”, though it still needs cleanup around how `systemPrompt` relates to ACP `session/new`.
- Decisions **D015** and **D016** — already settle the hardest S01 planning questions.

### Key files by seam

#### 1. Room ownership seam
- `docs/design/orchestrator/room-spec.md` — desired-state room contract; currently says agentd does not see rooms.
- `docs/design/agentd/agentd.md` — realized room manager and message routing story.
- `docs/design/agentd/ari-spec.md` — room API surface, plus attach/event story.
- `pkg/meta/schema.sql`, `pkg/meta/models.go`, `pkg/meta/room.go` — proof that realized room persistence already exists.
- `pkg/ari/server.go` — proof that realized room RPC does **not** exist yet.

#### 2. Bootstrap / session-config seam
- `docs/design/agentd/ari-spec.md` — stale `session/new` request shape.
- `pkg/ari/types.go` — actual `session/new` surface today.
- `cmd/agentdctl/session.go` — user-facing CLI surface today.
- `pkg/ari/server.go` — actual creation path (`session/new` metadata only, `session/prompt` auto-start later).
- `pkg/agentd/process.go` — actual config.json generation path, currently missing session-level config fields.
- `pkg/meta/models.go` — durable session model today, which lacks `systemPrompt`, env, permissions, MCP servers, bootstrap policy, ACP `sessionId`.

#### 3. Runtime / state / ID seam
- `docs/design/runtime/runtime-spec.md` — runtime lifecycle and state contract.
- `docs/design/runtime/config-spec.md` — config contract.
- `pkg/runtime/runtime.go` — actual handshake order and seed-prompt behavior.
- `pkg/spec/state_types.go` — actual persisted runtime state shape; note the absence of ACP `sessionId`.
- `docs/plan/shim-rpc-redesign.md` — already calls for `sessionId` to become first-class in runtime state.

#### 4. Shim protocol seam
- `docs/design/runtime/shim-rpc-spec.md` — old normative spec.
- `docs/design/runtime/agent-shim.md` — old “ACP hidden behind typed events” story.
- `pkg/rpc/server.go` — current legacy server methods and `$/event` notifications.
- `pkg/agentd/shim_client.go` — current legacy client.
- `pkg/events/translator.go` — current typed-event translation layer; important because S02 will likely reduce or reshape this.

#### 5. Security / workspace seam
- `docs/design/workspace/workspace-spec.md` — current workspace semantics, including local unmanaged workspace and shared usage patterns.
- `pkg/workspace/manager.go` — actual prepare/cleanup + in-memory refcount semantics.
- `pkg/runtime/client.go` / `pkg/runtime/terminal.go` — actual ACP filesystem / terminal handling and permission-policy behavior.
- `pkg/meta/schema.sql` — persisted `workspace_refs` already exists, but is not the active truth source for session lifecycle.

#### 6. Real proof surfaces
- `bin/bundles/gsd-pi/config.json` — appears spec-valid.
- `bin/bundles/claude-code/config.json` — currently invalid due to `oaiVersion` typo.

### Natural seams for task decomposition

1. **Top-level contract rewrite**
   - Update `docs/design/README.md` plus a compact cross-doc terminology pass so Room / Session / Runtime / Workspace / shim terms align.

2. **Room convergence task**
   - Rewrite `room-spec.md`, `agentd.md`, and `ari-spec.md` together.
   - This is one seam because changing only one of them recreates the contradiction immediately.

3. **Bootstrap convergence task**
   - Rewrite `ari-spec.md`, `runtime-spec.md`, `config-spec.md`, and `runtime/design.md` together.
   - This is where `session/new`, `systemPrompt`, resolved `cwd`, and initial work entry must be made single-story.

4. **State / recovery mapping task**
   - Add one explicit mapping table and identifier model (`OAR id` vs `ACP sessionId`) across runtime docs.
   - This is the clean handoff point into S02/S03.

5. **Shim contract rewrite task**
   - Rewrite `shim-rpc-spec.md` and `agent-shim.md` to the clean-break target surface.
   - Keep implementation lag visible in plan/research, not in the normative spec.

6. **Fixture truth task**
   - Fix and validate checked-in bundle configs, especially `claude-code`.
   - Cheap and keeps later validation slices from tripping on a fixture bug.

### What to build or prove first

1. **Freeze room ownership and bootstrap semantics first.** These decisions fan out into nearly every other file.
2. **Then freeze the identifier/state mapping.** Without this, shim and recovery docs keep drifting.
3. **Then rewrite the shim boundary docs.** S02 depends on having the target protocol written down.
4. **Only after that, capture the security boundary rules.** They depend on knowing which protocol/runtime story is actually being claimed.

## Current contradictions worth preserving in planner context

### Room: doc-only split vs partial runtime reality
- `room-spec.md` says agentd does not see rooms.
- `agentd.md` and `ari-spec.md` give agentd a room manager and `room/*` APIs.
- SQLite already persists rooms.
- ARI server does not implement `room/*` today (`pkg/ari/server.go:180-204`).

Planner implication: S01 must either (a) keep realized `room/*` in the design and clearly mark implementation lag outside the normative spec, or (b) temporarily prune room APIs from the normative ARI contract. The current contradiction cannot remain.

### Bootstrap: code flow is closer to D016 than the docs are
- ARI docs still describe `session/new` with bootstrap task input.
- Real code already treats `session/new` as metadata creation and `session/prompt` as work entry.
- The gap is not mostly control flow; it is **durable config shape**. Session persistence and config generation do not yet carry the fields the converged contract needs.

Planner implication: S01 can safely converge docs to config-only `session/new` without fighting the current control flow, but it must leave S03 a precise backlog for persistent session config.

### ACP capability posture is undecided, not just undocumented
- `docs/plan/shim-rpc-redesign.md` argues shim should not implement ACP fs/terminal client capabilities.
- `pkg/runtime/runtime.go` currently advertises filesystem client capability.
- `pkg/runtime/client.go` implements filesystem and terminal handlers.
- `docs/design/agentd/agentd.md` also describes shim handling `fs/*` and `terminal/*` requests.

Planner implication: S01 should explicitly settle the intended capability posture. This affects protocol docs, security docs, and future event-model cleanup.

### Recovery/socket story is already false in the docs
- Design docs assume `/run/agentd/shim/*/agent-shim.sock` is the discovery truth.
- `pkg/agentd/process.go:288-292` currently uses `/tmp/agentd-shim/<session>` in the active code path.

Planner implication: S01 should document the intended discovery root and note that implementation correction is follow-on work. Do not keep both paths implied as true.

### Bundle proof surface already has a fixture bug
- `bin/bundles/claude-code/config.json:2` uses `oaiVersion` instead of `oarVersion`.

Planner implication: include a tiny fixture-validation step somewhere early, or S04 proof work will waste time on a trivial config error.

## Verification Approach

### Contract verification

Use cheap textual checks after the doc rewrite:

```bash
rg -n "agentd 只看到独立的 session|room/create|room/status|room/delete|Prompt\"|\$/event|session/new.*prompt|systemPrompt.*静默" docs/design
```

Expected outcome: only the chosen target-story terms remain in normative docs; contradictions are removed or moved into planning/history docs.

### Code-surface sanity checks

These do **not** prove implementation convergence yet, but they catch accidental drift and fixture breakage:

```bash
go test ./pkg/spec ./pkg/runtime ./pkg/rpc ./pkg/ari ./pkg/agentd ./pkg/meta ./pkg/events ./pkg/workspace
```

Recommended additional focused check for the planner to add if it touches bundle fixtures:
- parse + validate `bin/bundles/*/config.json` through `spec.ParseConfig` / `spec.ValidateConfig` so fixture typos are caught automatically.

### Planner-level proof for this slice

S01 is done when a fresh reader can answer these questions from `docs/design/*` without switching stories:
- Does agentd own realized rooms, or only sessions?
- What exactly does `session/new` do?
- Where does resolved `cwd` come from?
- Is `systemPrompt` configuration or task input?
- What is the target shim protocol surface?
- How do OAR IDs, ACP `sessionId`, runtime state, and session state relate?

## Constraints

- **Do not converge the specs to the current legacy shim surface.** That would directly undermine S02.
- **Do not hide implementation lag inside the normative docs.** Keep legacy/current-gap notes in planning/research artifacts or explicitly marked implementation-status sections.
- **Enduring docs should tell one current contract story.** Avoid mixing “current legacy behavior” and “target contract” in the same normative paragraphs.
- `pkg/meta` and `pkg/ari/types.go` do not yet have the durable fields required by the converged bootstrap contract. S01 should expose that gap precisely, not try to paper over it.

## Common Pitfalls

- Updating only `ari-spec.md` or only `room-spec.md`. These files must move together.
- Keeping `prompt` on `session/new` “for compatibility”. The milestone explicitly rejected that posture.
- Forgetting that actual ARI prompt input is `text` today while the docs still show `prompt` payloads.
- Missing the ACP capability conflict and letting security docs assume a capability posture the code does not currently match.
- Letting the broken `claude-code` bundle fixture survive into later real-client validation work.

## Open Risks

- If S01 writes only a pure target contract and does not leave a precise implementation-gap inventory, S02/S03 will spend time re-discovering what still differs in code.
- If S01 tries to fully resolve persistence/recovery implementation in the docs, it may pull S03 work too early and dilute the slice.
- The fs/terminal posture may force a meaningful protocol/security decision, not just wording cleanup.

## Skills Discovered

| Technology | Skill | Status |
|------------|-------|--------|
| Go | none relevant found | not installed |
| SQLite | `martinholovsky/claude-skills-generator@sqlite database expert` | found but not installed — too DB-generic for this slice |
| JSON-RPC | `azzgo/agent-skills@aria2-json-rpc` | found but not installed — unrelated to OAR’s contract work |

## Sources

- `docs/plan/unified-modification-plan.md`
- `docs/plan/shim-rpc-redesign.md`
- `docs/design/README.md`
- `docs/design/orchestrator/room-spec.md`
- `docs/design/agentd/agentd.md`
- `docs/design/agentd/ari-spec.md`
- `docs/design/runtime/design.md`
- `docs/design/runtime/config-spec.md`
- `docs/design/runtime/runtime-spec.md`
- `docs/design/runtime/shim-rpc-spec.md`
- `docs/design/runtime/agent-shim.md`
- `pkg/ari/types.go`
- `pkg/ari/server.go`
- `pkg/agentd/process.go`
- `pkg/agentd/shim_client.go`
- `pkg/rpc/server.go`
- `pkg/runtime/runtime.go`
- `pkg/runtime/client.go`
- `pkg/runtime/terminal.go`
- `pkg/spec/state_types.go`
- `pkg/meta/models.go`
- `pkg/meta/schema.sql`
- `pkg/meta/room.go`
- `pkg/workspace/manager.go`
- `cmd/agentdctl/session.go`
- `bin/bundles/gsd-pi/config.json`
- `bin/bundles/claude-code/config.json`
