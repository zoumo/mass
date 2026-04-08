# M004 Research: Room Runtime Landscape

## 1. What Exists Today

### 1.1 Design Docs (Converged in M002)

The M002 contract convergence established a **desired-vs-realized** split for Room:

| Layer | Authority Doc | Owns |
|---|---|---|
| Orchestrator (desired) | `docs/design/orchestrator/room-spec.md` | Room intent: membership, workspace intent, communication topology, completion policy |
| agentd / ARI (realized) | `docs/design/agentd/ari-spec.md` | Runtime projection: realized membership, realized workspace attachment, communication mode, routing metadata |
| Shim / Runtime | `docs/design/runtime/shim-rpc-spec.md` | Per-session process truth. No Room awareness. |

This split is clean and recorded as decisions D015 and D019.

**room-spec.md** defines:
- Top-level `Room` kind with `oarVersion`, `metadata`, `spec`
- `spec.workspace` — shared workspace intent (Source object)
- `spec.agents[]` — desired members with `name`, `runtimeClass`, `systemPrompt`
- `spec.communication.mode` — `mesh` / `star` / `isolated`
- Full projection flow: workspace/prepare → room/create → session/new per member → session/prompt

**ari-spec.md** defines:
- `room/create` — register realized Room (name, labels, communication mode)
- `room/status` — inspect realized membership (agent→session mapping, workspace, communication)
- `room/delete` — remove realized Room after sessions stopped
- Session fields: `room` and `roomAgent` on `session/new`

**agentd.md** defines:
- "Realized Room Manager" subsystem concept
- Room metadata persistence, member tracking, routing state
- Room does NOT decide membership or completion policy

### 1.2 Implementation (Code)

**What's built:**

| Component | Status | Notes |
|---|---|---|
| `rooms` DB table | ✅ Schema + triggers | name PK, labels JSON, communication_mode, timestamps |
| `sessions.room` FK | ✅ Schema | References rooms.name, ON DELETE SET NULL |
| `sessions.room_agent` | ✅ Schema | Agent name within room |
| `pkg/meta/room.go` | ✅ 178 lines | CreateRoom, GetRoom, ListRooms, DeleteRoom — full CRUD |
| `pkg/meta/room_test.go` | ✅ 333 lines | CRUD tests, uniqueness, filtering, FK cascade |
| `session/new` Room fields | ✅ Wired | `room` and `roomAgent` passed through ARI → meta store |
| `session/list` Room fields | ✅ Wired | Room/RoomAgent returned in SessionInfo |
| ARI `room/*` handlers | ❌ Not implemented | No room/create, room/status, room/delete in server.go |
| Room Manager package | ❌ Not implemented | No pkg/agentd/room/ exists |
| Routing (room_send/broadcast) | ❌ Not implemented | Design only in roadmap Phase 4.2 |
| MCP tool injection | ❌ Not implemented | Mentioned in Phase 4.2 but no design detail |

**Summary:** The persistence layer (DB schema, CRUD operations, session FK) is complete. The ARI surface and runtime behavior are entirely unbuilt.

### 1.3 Communication Mode Discrepancy

**Design doc** (`room-spec.md`): `mesh`, `star`, `isolated`
**Code** (`models.go`): `broadcast`, `direct`, `hub`
**DB default** (`schema.sql`): `broadcast`

These are different vocabulary for overlapping concepts but they don't map 1:1. This needs resolution before M004 implementation.

| Design | Code | Semantically |
|---|---|---|
| mesh | broadcast? | Any→any |
| star | hub? | Leader coordinates |
| isolated | (no equivalent) | No messaging |
| (no equivalent) | direct | Point-to-point only? |

## 2. Open Design Questions

### Q1: room/create vs implicit Room from session metadata

Two possible paths:
- **Explicit:** Orchestrator calls `room/create` first, then `session/new` with `room` field → Room must exist
- **Implicit:** `session/new` with `room` field auto-creates Room record if it doesn't exist

Current design doc says explicit (`room/create` in projection flow). But the code already allows session creation with room/roomAgent fields without requiring Room record to exist (FK is nullable — `room TEXT DEFAULT ''` not `NOT NULL`).

**Impact:** Explicit is safer and matches the desired-vs-realized split. Implicit is more ergonomic for simple cases.

### Q2: Routing mechanism — MCP tool injection vs ARI relay

Two architectural approaches for inter-agent messaging:

**A. MCP Tool Injection** (from roadmap Phase 4.2):
- Inject `room_send`, `room_broadcast`, `room_status` as MCP tools into each agent's bootstrap
- Agent calls tool → shim intercepts → agentd routes → target session/prompt
- Pro: Agent-initiated, uses existing MCP infrastructure
- Con: Requires MCP server injection into agent bootstrap, agent must be MCP-aware

**B. ARI Relay** (orchestrator-mediated):
- Orchestrator reads output from session A, decides to route to session B, calls `session/prompt` on B
- Pro: No agent-side changes, orchestrator has full control
- Con: Orchestrator becomes bottleneck, no direct agent-to-agent path

**C. Hybrid:**
- MCP tools for agent-initiated messaging
- ARI for orchestrator-mediated routing
- Both enter target session through `session/prompt` (already designed)

The room-spec.md already says "member work still enters through per-session `session/prompt`" — this constrains routing to use session/prompt as the delivery mechanism regardless of who initiates.

### Q3: Delivery semantics for busy targets

Current design notes (from unified-modification-plan.md DES-007):
- Target idle → forward prompt ✓
- Target busy → return `agent busy` ✓
- No queuing, no interruption by default

Unresolved:
- Partial success for broadcast (some targets busy, some idle)
- Delivery ordering guarantees
- Timeout semantics
- Correlation/causation tracking (which message caused which response)
- Sender/receiver result structure

### Q4: Shared workspace safety proof level

Room amplifies host impact — multiple agents can mutate the same files concurrently. Current design explicitly states "no per-session filesystem isolation."

What level of proof is needed:
- **Minimum:** Reference counting works correctly, cleanup is safe (already proven in M003)
- **Medium:** Concurrent file access doesn't corrupt workspace state
- **Maximum:** File-level locking or merge protocol for concurrent access

The design docs intentionally leave this as "shared write risk is the orchestrator's problem" — agentd doesn't try to solve concurrent write safety.

### Q5: Recovery semantics for Room state

M003 proved session recovery (shim reconnect, state reconciliation). Room recovery adds:
- Room record survives restart (already in DB)
- Member-to-session mapping reconstructible from session records (room/roomAgent fields survive)
- In-flight routing state during restart — messages in transit may be lost

Since routing doesn't exist yet, recovery for routing is a design-time concern, not implementation debt.

## 3. Dependency Map

```
                    ┌─────────────────────┐
                    │ Room DB (DONE)       │
                    │ rooms table, CRUD    │
                    │ session FK fields    │
                    └─────────┬───────────┘
                              │
              ┌───────────────┼───────────────┐
              ▼               ▼               ▼
    ┌─────────────────┐ ┌──────────────┐ ┌──────────────────┐
    │ ARI room/*      │ │ Room Manager │ │ Mode Resolution  │
    │ create/status/  │ │ pkg/agentd/  │ │ mesh/star/       │
    │ delete handlers │ │ room/        │ │ isolated vocab   │
    └────────┬────────┘ └──────┬───────┘ └────────┬─────────┘
             │                 │                   │
             └────────┬────────┘                   │
                      ▼                            │
            ┌─────────────────┐                    │
            │ Routing Engine  │◄───────────────────┘
            │ room_send       │
            │ room_broadcast  │
            │ mode enforcement│
            └────────┬────────┘
                     │
           ┌─────────┼──────────┐
           ▼                    ▼
  ┌─────────────────┐  ┌───────────────┐
  │ MCP Injection   │  │ Delivery      │
  │ tool bootstrap  │  │ Semantics     │
  │ per-session     │  │ busy/timeout/ │
  └─────────────────┘  │ correlation   │
                       └───────────────┘
```

## 4. Risk Assessment

| Risk | Severity | Notes |
|---|---|---|
| Communication mode vocabulary split | Low | Fixable in one task, but must happen before Room Manager |
| MCP injection mechanism undefined | Medium | No design detail on how tools reach agent bootstrap |
| Delivery semantics complexity | High | Partial success, ordering, correlation is significant design surface |
| Shared workspace concurrent safety | Medium | Design says "orchestrator's problem" but users will expect agentd to be safe |
| Room recovery during active routing | Low | Can be deferred since routing is new |

## 5. Suggested M004 Scope

Based on the gap analysis, a reasonable M004 could be:

**Layer 1 (Foundation):** Communication mode convergence + ARI room/* handlers + Room Manager core — proves the realized Room lifecycle works end-to-end (create Room → create member sessions → inspect → cleanup).

**Layer 2 (Routing):** Point-to-point `room_send` via session/prompt relay — proves one agent can deliver work to another through agentd.

**Layer 3 (Broadcast + Modes):** `room_broadcast` with partial-success semantics + communication mode enforcement — proves the topology contract is enforced at runtime.

**Deferred:** MCP tool injection (depends on agent bootstrap story), advanced delivery semantics (correlation, ordering), shared workspace isolation.

This layers risk correctly: foundation → point-to-point → broadcast, each demoable.
