---
id: S04
parent: M012
milestone: M012
provides:
  - (none)
requires:
  []
affects:
  []
key_files:
  - ["api/ari/service.go", "api/ari/client.go", "api/shim/service.go", "api/shim/client.go"]
key_decisions:
  - (none)
patterns_established:
  - (none)
observability_surfaces:
  - none
drill_down_paths:
  []
duration: ""
verification_result: passed
completed_at: 2026-04-13T17:37:05.238Z
blocker_discovered: false
---

# S04: Phase 3: Service Interface + Register + Typed Clients

**api/ari and api/shim now have full Service Interfaces, Register functions, and typed clients**

## What Happened

Defined WorkspaceService/AgentRunService/AgentService interfaces with Register functions that build jsonrpc.ServiceDesc. Typed clients (WorkspaceClient, AgentRunClient, AgentClient, ShimClient) wrap jsonrpc.Client with method-specific typed wrappers. ShimService documents 5 Subscribe implementation constraints using Peer abstraction.

## Verification

go build ./api/... and make build pass

## Requirements Advanced

None.

## Requirements Validated

None.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

None.

## Known Limitations

None.

## Follow-ups

None.

## Files Created/Modified

None.
