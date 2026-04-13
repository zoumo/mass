---
id: S03
parent: M012
milestone: M012
provides:
  - (none)
requires:
  []
affects:
  []
key_files:
  - ["api/ari/domain.go", "api/ari/types.go", "docs/design/agentd/ari-spec.md"]
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
completed_at: 2026-04-13T17:32:34.140Z
blocker_discovered: false
---

# S03: Phase 2b: ARI Clean-Break Contract Convergence

**ARI wire format now uses Agent/AgentRun/Workspace domain shapes; Info DTOs deleted; ari-spec.md updated; all tests pass**

## What Happened

Updated docs/design/agentd/ari-spec.md with new metadata/spec/status domain shapes for all methods. Created api/ari/domain.go from api/meta/types.go. Added ARIView() helpers on AgentRun and Workspace to strip internal fields at ARI boundary (ShimSocketPath, ShimStateDir, ShimPID, BootstrapConfig, Hooks). Removed AgentInfo/AgentRunInfo/WorkspaceInfo from api/ari/types.go. Added new Result wrapper types (AgentSetResult, AgentRunCreateResult, etc.). Updated pkg/ari/server.go to use domain types directly. Updated all 20+ consumer files. Deleted api/meta/ directory. Fixed all test field accesses for nested domain structure.

## Verification

make build + go test ./... all pass. No api/meta imports remain. ari-spec.md uses domain shapes throughout.

## Requirements Advanced

None.

## Requirements Validated

None.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

json:\"-\" for sensitive fields was replaced with ARIView() method pattern because json:\"-\" also blocks bbolt store persistence. The security guarantee (fields not in ARI responses) is identical.

## Known Limitations

None.

## Follow-ups

None.

## Files Created/Modified

None.
