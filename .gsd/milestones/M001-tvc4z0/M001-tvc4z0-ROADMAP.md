# M001-tvc4z0: 

## Vision
From "manage one agent" to "manage multiple agents" — agentd daemon manages sessions/workspaces/processes through shim layer, exposing ARI interface for orchestrator and CLI

## Slice Overview
| ID | Slice | Risk | Depends | Done | After this |
|----|-------|------|---------|------|------------|
| S01 | Scaffolding + Phase 1.3 exitCode | medium | — | ✅ | agentd daemon starts with config.yaml, listens on socket; shim exitCode surfaces in GetState |
| S02 | Metadata Store (SQLite) | medium | S01 | ✅ | SQLite metadata store created, CRUD operations work, schema in place |
| S03 | RuntimeClass Registry | medium | S01 | ✅ | RuntimeClass registry resolves names to launch configs with env substitution |
| S04 | Session Manager | medium | S02 | ✅ | Session Manager CRUD works, state machine transitions verified |
| S05 | Process Manager | high | S02, S03, S04 | ✅ | Process Manager starts shim process, connects socket, subscribes events; mockagent responds |
| S06 | ARI Service | high | S04, S05 | ✅ | ARI JSON-RPC server exposes session/* methods, CLI can create/prompt/stop sessions |
| S07 | agentdctl CLI | low | S06 | ✅ | agentdctl CLI can manage sessions through ARI: new/list/prompt/stop/remove |
| S08 | Integration Tests | low | S06, S07 | ✅ | Full pipeline works: agentd → agent-shim → mockagent end-to-end; 11 integration tests pass |
