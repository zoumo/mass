# OAR Development Roadmap

## Current State

The project has a working **agent-shim** layer — the OAR Runtime Spec reference
implementation. This covers the bottom of the architecture stack:

```
Implemented:
  pkg/spec       — OAR Runtime Spec types, config parsing, state management
  pkg/runtime    — Manager: agent process lifecycle, ACP handshake, permissions
  pkg/events     — Typed event stream, EventLog (JSONL), ACP→Event translator
  pkg/rpc        — JSON-RPC 2.0 server over Unix socket (shim RPC)
  cmd/agent-shim — CLI entry point
  cmd/agent-shim-cli — Interactive client (state, prompt, chat, shutdown)

Not yet implemented:
  agentd         — High-level daemon (Workspace/Session/Process/Room Manager, ARI, Metadata Store)
  Orchestrator   — Room lifecycle, multi-agent coordination
```

## Phases

### Phase 1: agent-shim Hardening

Solidify the existing agent-shim layer before building on top of it.

#### 1.1 Terminal Operations

**Status**: Stubs returning "not supported" in `pkg/runtime/client.go`.

- [ ] Implement `terminal/execute` — run shell commands in the agent's workspace
- [ ] Implement `terminal/read_output` — stream command output
- [ ] Implement `terminal/set_timeout` — configure execution timeout
- [ ] Add permission policy enforcement for terminal operations
- [ ] Integration tests with mockagent issuing terminal requests

#### 1.2 Session Load (Warm Resume)

**Status**: Not implemented. ACP `session/load` is referenced in specs but not wired.

- [ ] Implement `session/load` support in Manager — restore conversation history
- [ ] Wire `session/load` before `session/prompt` when resuming a stopped agent
- [ ] Persist conversation history (turns) to enable cold restart

#### 1.3 Graceful Shutdown & Error Propagation

- [ ] Propagate agent process exit errors to state.json (add `exitCode`, `error` to State)
- [ ] Surface process exit reason in shim RPC `GetState()` response
- [ ] Handle agent process crash during ACP handshake (cleanup partial state)
- [ ] Add configurable grace period for SIGTERM→SIGKILL escalation (currently hardcoded 5s)

#### 1.4 Event Log Improvements

- [ ] Add event log rotation or size limit (currently unbounded append)
- [ ] Support `GetHistory` with time-range and event-type filters
- [ ] Benchmark event throughput and optimize if needed

---

### Phase 2: agentd Core — Session + Process Management

Build the agentd daemon with the minimum viable feature set:
manage single sessions without rooms.

#### 2.1 Project Scaffolding

- [ ] Create `cmd/agentd/main.go` — daemon entry point with cobra
- [ ] Create `pkg/agentd/` package structure:
  - `pkg/agentd/config/` — daemon config parsing (config.yaml)
  - `pkg/agentd/session/` — Session Manager
  - `pkg/agentd/process/` — Process Manager
  - `pkg/agentd/meta/` — Metadata Store
  - `pkg/agentd/ari/` — ARI JSON-RPC server
- [ ] Define agentd daemon config schema (socket path, workspace root, meta DB path, runtimeClasses)

#### 2.2 RuntimeClass Registry

**Spec**: agentd.md § RuntimeClass

- [ ] Implement `RuntimeClassRegistry` interface (Get, List)
- [ ] Parse runtimeClasses from `config.yaml`
- [ ] Environment variable substitution (`${VAR}` syntax)
- [ ] Validation: required fields, command existence check

#### 2.3 Metadata Store

**Spec**: agentd.md § Metadata Store

- [ ] Choose storage backend (SQLite recommended for simplicity)
- [ ] Define schema: sessions, workspaces, rooms tables
- [ ] Implement CRUD operations with transactions
- [ ] Schema migration support for future upgrades

#### 2.4 Session Manager

**Spec**: agentd.md § Session Manager

- [ ] Implement `SessionManager` interface (Create, Get, List, Update, Delete)
- [ ] Session state machine: Created → Running → Paused:Warm → Paused:Cold → Stopped
- [ ] Label-based filtering in List
- [ ] Prevent Delete on running sessions

#### 2.5 Process Manager

**Spec**: agentd.md § Process Manager

- [ ] Implement `ProcessManager` interface (Start, Stop, State, Connect)
- [ ] `Start` workflow:
  1. Resolve runtimeClass → process config
  2. Generate OAR config.json
  3. Create bundle directory + workspace symlink
  4. Fork/exec agent-shim with bundle path
  5. Connect to shim socket
  6. Subscribe to typed event stream
- [ ] `Connect` for agentd restart recovery (scan `/run/agentd/shim/*/agent-shim.sock`)
- [ ] `Stop` with configurable timeout
- [ ] Monitor shim process health

#### 2.6 ARI Service — Session Methods

**Spec**: ari.md § Session Methods

- [ ] Implement ARI JSON-RPC 2.0 server on Unix socket (`/run/agentd/agentd.sock`)
- [ ] `session/new` — create session + start agent
- [ ] `session/prompt` — forward to shim RPC
- [ ] `session/cancel` — forward to shim RPC
- [ ] `session/stop` — stop agent process
- [ ] `session/remove` — delete session metadata
- [ ] `session/list` — query sessions with filters
- [ ] `session/status` — return session details + process state
- [ ] `session/attach` / `session/detach` — typed event fan-out to ARI clients
- [ ] Event notifications: `session/update`, `session/stateChange`

#### 2.7 agentd CLI

- [ ] Extend `cmd/agent-shim-cli` or create `cmd/agentdctl` for ARI operations
- [ ] Commands: session new/list/status/prompt/stop/remove, daemon status

#### 2.8 Integration Tests

- [ ] End-to-end: agentd → agent-shim → mockagent pipeline
- [ ] Session lifecycle: create → prompt → stop → remove
- [ ] agentd restart recovery: verify shim reconnection
- [ ] Multiple concurrent sessions

---

### Phase 3: Workspace Manager

#### 3.1 Workspace Spec Types

**Spec**: workspace-spec.md

- [ ] Create `pkg/agentd/workspace/` package
- [ ] Define Go types: WorkspaceSpec, Source (Git/EmptyDir/Local), Hook
- [ ] Workspace spec parsing and validation

#### 3.2 Source Handlers

- [ ] Git source: clone with ref, depth support
- [ ] EmptyDir source: create managed directory
- [ ] Local source: validate path exists, use as-is

#### 3.3 Hook Execution

- [ ] Execute setup hooks sequentially in workspace directory
- [ ] Execute teardown hooks on cleanup
- [ ] Hook failure handling: abort prepare, cleanup partial state
- [ ] Capture hook stdout/stderr for debugging

#### 3.4 Workspace Lifecycle

- [ ] Reference counting: track which sessions use each workspace
- [ ] Prevent cleanup when sessions still reference workspace
- [ ] Managed directory cleanup (git/emptyDir only, not local)

#### 3.5 ARI Workspace Methods

**Spec**: ari.md § Workspace Methods

- [ ] `workspace/prepare` — prepare workspace from spec
- [ ] `workspace/list` — list workspaces with status and refs
- [ ] `workspace/cleanup` — teardown hooks + delete managed directory

---

### Phase 4: Room Manager

#### 4.1 Room Manager Core

**Spec**: agentd.md § Room Manager, room-spec.md

- [ ] Create `pkg/agentd/room/` package
- [ ] Implement `RoomManager` interface (CreateRoom, GetRoom, ListRooms, DeleteRoom, RouteMessage, BroadcastMessage)
- [ ] Room metadata persistence in Metadata Store
- [ ] Member tracking: populate from session metadata

#### 4.2 MCP Tool Injection

**Spec**: room-spec.md § Agent-to-Agent Communication

- [ ] Design injection mechanism (how to pass room MCP tools to agent)
- [ ] Implement `room_send(agent_name, message)` — route to target session's prompt
- [ ] Implement `room_broadcast(message)` — fan-out to all room members
- [ ] Implement `room_status()` — return member states
- [ ] Busy session handling: return error when target is processing

#### 4.3 Communication Modes

- [ ] `mesh` — any agent can message any other
- [ ] `star` — only leader can initiate; others can only reply
- [ ] `isolated` — no inter-agent communication
- [ ] Enforce mode in RouteMessage/BroadcastMessage

#### 4.4 ARI Room Methods

**Spec**: ari.md § Room Methods

- [ ] `room/create`
- [ ] `room/status`
- [ ] `room/delete` (require all member sessions stopped)

---

### Phase 5: Session Lifecycle — Warm/Cold Pause

#### 5.1 Warm Pause

**Spec**: agentd.md § Session Lifecycle

- [ ] Detect turn completion → transition to Paused:Warm
- [ ] Configurable warm idle timeout (`sessionPolicy.warmIdleTimeout`)
- [ ] Max warm sessions limit (`sessionPolicy.maxWarmSessions`)
- [ ] Resume from Paused:Warm: send next prompt directly

#### 5.2 Cold Pause

- [ ] Warm idle timeout → kill agent process → Paused:Cold
- [ ] Persist conversation history for cold restart
- [ ] Cold retention timeout (`sessionPolicy.coldRetentionTimeout`)
- [ ] Resume from Paused:Cold: restart process → `session/load` → prompt

---

### Phase 6: Production Readiness

#### 6.1 Observability

- [ ] Structured logging (slog) throughout agentd and agent-shim
- [ ] Metrics: session count by state, prompt latency, agent process uptime
- [ ] Health endpoint in agentd (`daemon/status` already spec'd)

#### 6.2 Security

- [ ] Unix socket permissions (0600) for agentd.sock
- [ ] Environment variable sanitization in config.json generation
- [ ] Validate workspace paths (prevent directory traversal)
- [ ] Rate limiting on ARI methods

#### 6.3 Error Handling & Recovery

- [ ] agentd crash recovery: rebuild in-memory state from Metadata Store + shim sockets
- [ ] Orphan shim detection and cleanup
- [ ] Workspace leak detection (unreferenced managed directories)

#### 6.4 Documentation

- [ ] API reference: all ARI methods with examples
- [ ] Operator guide: installation, configuration, troubleshooting
- [ ] Developer guide: extending runtimeClass, writing ACP wrappers

---

## Dependency Graph

```
Phase 1 (agent-shim hardening)
    │
    ▼
Phase 2 (agentd core)
    │
    ├──────────────┐
    ▼              ▼
Phase 3         Phase 4
(workspace)     (room) ← depends on Phase 3 for shared workspace
    │              │
    └──────┬───────┘
           ▼
       Phase 5 (warm/cold pause)
           │
           ▼
       Phase 6 (production readiness)
```

Phase 1 can proceed independently. Phase 3 and Phase 4 can be developed in
parallel once Phase 2 is complete, though Room depends on Workspace for shared
workspace support. Phase 5 builds on the session infrastructure from Phase 2.
Phase 6 is ongoing and can overlap with any phase.

## Priority Rationale

1. **Phase 1** first because agent-shim is the foundation — bugs here propagate everywhere.
2. **Phase 2** is the highest-value work: agentd enables managing multiple agents,
   which is the core use case beyond direct agent-shim usage.
3. **Phase 3** before Phase 4 because Room requires shared workspace.
4. **Phase 4** unlocks multi-agent collaboration — the differentiating feature.
5. **Phase 5** is optimization — useful but not blocking core functionality.
6. **Phase 6** is continuous, but critical items (logging, recovery) should start early.
