# Project

## What This Is

Open Agent Runtime (OAR) is a layered runtime for headless coding agents. It manages ACP-speaking agent processes through `agent-shim`, a higher-level daemon in `agentd`, and a future orchestrator layer for multi-agent coordination.

## Core Value

The thing that must stay true is reliable, observable agent execution with truthful lifecycle and recovery semantics. If scope has to shrink, the runtime still needs to launch real ACP agents, manage them cleanly, and tell the truth about their state, ownership boundaries, and restart behavior.

## Current State

### Completed Milestones

**M001 — Core Runtime Implementation.** Built the foundational layers: agent-shim process management, agentd daemon with ARI JSON-RPC server, workspace preparation (Git/EmptyDir/Local), session lifecycle with state machine, metadata persistence in SQLite, and CLI tooling. Integration tests prove the full pipeline `agentd → agent-shim → mockagent`.

**M002 — Contract Convergence.** Converged the design contract across Room, Session, Runtime, Workspace, and shim recovery semantics into one non-conflicting authority map. Replaced legacy PascalCase shim methods with clean-break `session/*` + `runtime/*` surface. Added schema v2 with bootstrap config persistence, `RecoverSessions` startup pass for live shim reconnection, and real CLI integration tests for `gsd-pi` and `claude-code`.

**M003 — Recovery and Safety Hardening.** Hardened daemon recovery with fail-closed posture (RecoveryPhase atomic tracking blocks prompt/cancel during recovery), shim-vs-DB state reconciliation (stopped/created→running/mismatch paths), atomic event resume (SubscribeFromSeq eliminates History→Subscribe gap structurally), damaged-tail tolerant log reads, and DB-backed workspace cleanup safety (ref_count gates, Registry/WorkspaceManager rebuild from DB, recovery-phase guard on cleanup).

**M004 — Realized Room Runtime and Routing.** Turned the Room from a design-only contract into a working runtime. All 3 slices complete:
- **S01:** Room Lifecycle and ARI Surface — room/create, room/status, room/delete handlers. Communication vocabulary converged to mesh/star/isolated. Room-existence validation enforced on session/new. 5 integration tests.
- **S02:** Routing Engine and MCP Tool Injection — room/send ARI handler for orchestrator-driven messaging and room-mcp-server MCP binary for agent-driven messaging. deliverPrompt helper shared between session/prompt and room/send. 12 integration tests.
- **S03:** End-to-End Multi-Agent Integration Proof — TestARIMultiAgentRoundTrip (3-agent bootstrap, bidirectional A↔B + A→C delivery, state transitions, clean teardown) and TestARIRoomTeardownGuards (delete-with-active-members guard, session/remove-on-running guard, correct ordering succeeds). 2 capstone integration tests composing S01+S02 into complete proof.

### What's Implemented

- `agent-shim` starts ACP agent processes, performs the ACP handshake, and exposes the clean-break `session/*` + `runtime/*` shim RPC surface
- `agentd` manages sessions, runtime classes, workspaces, metadata, and ARI session/workspace methods with durable recovery
- Workspace preparation for Git / EmptyDir / Local sources, with hooks and reference tracking
- Integration tests prove the assembled path `agentd → agent-shim → mockagent` including restart recovery
- Real bundle examples exist under `bin/bundles/claude-code` and `bin/bundles/gsd-pi`
- Design contract fully converged via `docs/design/contract-convergence.md` authority map
- Clean-break shim RPC surface: all legacy PascalCase / `$/event` names replaced with `session/*` + `runtime/*`
- `events.Envelope{Method, Seq, Params}` is the canonical notification shape for both live and replay paths
- Schema v2 with bootstrap_config/socket/PID persistence enables truthful restart recovery
- `RecoverSessions` startup pass reconnects live shims, marks dead shims stopped (fail-closed)
- Fail-closed recovery posture: `RecoveryPhase` atomic tracking blocks operational ARI actions during recovery
- DB-backed workspace ref_count tracking through session lifecycle
- Room ARI surface: `room/create`, `room/status`, `room/delete` JSON-RPC handlers
- Room membership realized from session queries — room/status shows agentName/sessionId/state per member
- Communication vocabulary: mesh/star/isolated (converged from legacy broadcast/direct/hub per D054)
- Room-existence validation in session/new: fail-fast with actionable error suggesting room/create
- Active-member guard: room/delete checks for non-stopped sessions before allowing deletion
- **room/send ARI handler** — point-to-point message routing: resolves targetAgent→session within a room, formats attributed messages `[room:X from:Y]`, delivers via shared `deliverPrompt` helper
- **deliverPrompt helper** — shared auto-start→connect→prompt flow used by both session/prompt and room/send
- **room-mcp-server binary** — MCP stdio server exposing room_send and room_status tools, injected into room sessions at bootstrap via generateConfig
- **stdio MCP transport** — spec.McpServer extended with Name/Command/Args/Env fields; convertMcpServers handles stdio→acp mapping
- **End-to-end multi-agent proof** — 3-agent bidirectional messaging, state transitions (created→running on first delivery), teardown ordering guards, clean room lifecycle — all via ARI JSON-RPC
- 47 ARI integration tests covering session lifecycle, workspace management, room lifecycle, routing, and multi-agent integration

### Current Gaps

- Only point-to-point routing — broadcast/star/isolated mode enforcement deferred
- Per-session prompt mutex needed for busy-target detection when broadcast is implemented
- Attribution is text-prefix only — no structured metadata for programmatic parsing
- room-mcp-server creates short-lived ARI connections per tool call (acceptable for L2 scale)
- Recovery only proven with mockagent; real CLI restart recovery untested
- `runtime/history` RPC and `ShimClient.History` are no longer used by recovery — consider deprecating
- Registry rebuild does not verify on-disk workspace path existence (stale workspace detection)
- Cross-client hardening (multiple ARI clients interacting with same sessions) untested
- Terminal capability deferred (`M001-terminal` removed from near-term plan)
- Codex runtime class validation deferred

## Architecture / Key Patterns

Layered architecture:
- orchestrator / Room desired state (future)
- ARI in `agentd` for realized runtime state and control
- shim RPC in `agent-shim` (`session/*` for turn control, `runtime/*` for process/replay control)
- ACP toward real agent CLIs (`gsd-pi`, `claude-code`, later `codex`)

Established patterns:
- `session/new` is configuration/bootstrap only; work enters through later `session/prompt`
- `agentRoot.path` is the bundle input; resolved `cwd` is derived at runtime
- OAR `sessionId` and ACP `sessionId` are separate identities
- Fail-closed recovery: shim truth wins over DB state; uncertain sessions are blocked, not guessed
- Two-level recovery state: atomic daemon-wide phase for fast guards + per-session RecoveryInfo for inspection
- Always transition out of blocking states on every exit path (no permanent traps)
- DB-as-truth for cleanup gating: volatile in-memory state not trusted for destructive operations
- Room ARI handler pattern: validate params → call store → build result with realized member list
- Active-member guard: room/delete checks for non-stopped sessions before allowing deletion
- Room-existence validation in session/new: fail-fast with actionable error suggesting room/create
- `deliverPrompt(ctx, sessionID, text)` as canonical prompt delivery helper — all delivery paths (session/prompt, room/send, future broadcast) share this
- Room MCP injection: generateConfig checks session.Room and injects stdio MCP server with env vars for agentd connection
- Attributed message format: `[room:<name> from:<sender>] <message>`
- Binary resolution 3-tier pattern: env var → ./bin relative → PATH lookup (used for both shim and room-mcp-server)
- Multi-step integration test pattern: sequential ARI calls building up state, with roomStatus verification after each mutation, and full teardown with post-delete error check
- Teardown guard test pattern: attempt operations in wrong order, assert specific error codes/messages, then demonstrate correct ordering succeeds
