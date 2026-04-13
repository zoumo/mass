# S03: Phase 2b: ARI Clean-Break Contract Convergence — UAT

**Milestone:** M012
**Written:** 2026-04-13T17:32:34.140Z

## Phase 2b UAT\n\n- [x] docs/design/agentd/ari-spec.md uses domain shapes (no AgentInfo/AgentRunInfo/WorkspaceInfo)\n- [x] api/ari/domain.go created with Agent/AgentRun/Workspace + ARIView() helpers\n- [x] api/ari/types.go has no AgentInfo/AgentRunInfo/WorkspaceInfo types\n- [x] api/meta/ directory deleted\n- [x] pkg/ari/server.go has no agentRunToInfo/agentToInfo functions\n- [x] ARIView() strips sensitive fields from ARI responses\n- [x] Store still persists all fields (bbolt JSON not broken)\n- [x] make build passes\n- [x] go test ./... all 18 packages pass
