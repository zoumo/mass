# M004: Realized Room Runtime and Routing

## Vision

Turn the Room from a design-only contract into a working runtime: orchestrators can create Rooms, attach member sessions to shared workspaces, and agents can send point-to-point messages to each other through agentd-mediated routing.

## Scope

**In scope (L1 + L2):**

- Converge communication mode vocabulary (mesh/star/isolated) between design docs and code
- Implement ARI `room/create`, `room/status`, `room/delete` handlers
- Build Room Manager in agentd with lifecycle validation (explicit create before session membership)
- Point-to-point `room_send` routing: agent A → agentd → agent B via `session/prompt`
- MCP tool injection mechanism for agent-initiated `room_send`
- Integration tests proving multi-agent message exchange through a Room

**Out of scope (deferred):**

- `room_broadcast` with partial-success semantics
- Communication mode enforcement (mesh/star/isolated routing rules)
- Advanced delivery semantics (ordering, correlation, timeout, causation tracking)
- Shared workspace concurrent write safety beyond existing ref counting
- Warm/cold pause interaction with Room membership

## Key Decisions

- **D051:** Explicit `room/create` required before `session/new` can reference a Room
- **D052:** Hybrid routing — MCP tool injection + orchestrator relay, both via `session/prompt`
- **D053:** M004 scope = L1 (foundation) + L2 (point-to-point routing)
- **D054:** Communication mode vocabulary = mesh/star/isolated (design doc wins over code)

## Dependencies

- **M003 (complete):** Recovery and safety hardening — Room state survives restart via existing DB
- **Existing infrastructure:** rooms table, session room/roomAgent FK, CRUD operations in pkg/meta

## Constraints

- Room is realized runtime state, not orchestrator intent (D015/D019 from M002)
- All work enters through `session/prompt` — routing does not bypass this
- `session/new` remains configuration-only bootstrap
- No per-session filesystem isolation within shared workspaces

## Success Criteria

1. Orchestrator can create a Room, create 2+ member sessions, and inspect membership via `room/status`
2. Agent A can call `room_send("agentB", "message")` and agentB receives it as a `session/prompt`
3. Room cleanup follows reference rules — `room/delete` only succeeds after member sessions are stopped/removed
4. Communication mode vocabulary is unified across design docs, code, and DB schema
5. All existing tests continue to pass (Room changes don't break non-Room session flows)
