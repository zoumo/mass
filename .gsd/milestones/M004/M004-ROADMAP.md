# M004: Realized Room Runtime and Routing

## Vision
Turn the Room from a design-only contract into a working runtime: orchestrators can create Rooms, attach member sessions, and agents can exchange point-to-point messages through agentd-mediated routing.

## Slice Overview
| ID | Slice | Risk | Depends | Done | After this |
|----|-------|------|---------|------|------------|
| S01 | Room Lifecycle and ARI Surface | low | — | ✅ | Orchestrator can create a Room, create 2 member sessions pointing at it, query room/status to see both members, stop sessions, and delete the Room. |
| S02 | Routing Engine and MCP Tool Injection | high | — | ✅ | Agent A calls room_send MCP tool → agentd resolves target → target agent receives prompt with sender attribution. |
| S03 | End-to-End Multi-Agent Integration Proof | medium | — | ✅ | Full round-trip: Room create → member bootstrap → bidirectional message exchange → Room teardown. All via ARI. |
