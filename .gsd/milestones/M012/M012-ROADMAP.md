# M012: Codebase Refactor: Service Interface + Unified RPC + Directory Restructure

## Vision
Replace three duplicated JSON-RPC implementations with a single transport-agnostic pkg/jsonrpc/ framework, establish typed Service Interfaces for ARI and Shim, perform clean-break ARI wire contract convergence, and restructure API packages for clarity and consistency.

## Slice Overview
| ID | Slice | Risk | Depends | Done | After this |
|----|-------|------|---------|------|------------|
| S01 | S01 | medium | — | ✅ | make build passes; go test ./pkg/jsonrpc/... passes all 18 protocol tests |
| S02 | S02 | low | — | ✅ | make build + go test ./... pass; JSON output identical to before |
| S03 | S03 | high | — | ✅ | make build + go test ./... pass; ARI JSON shape matches updated ari-spec.md |
| S04 | S04 | medium | — | ✅ | make build passes; interfaces compile cleanly |
| S05 | S05 | high | — | ⬜ | make build + go test ./... pass; integration tests pass |
| S06 | Phase 5: Cleanup | low | S05 | ⬜ | make build + go test ./... pass; no references to deleted packages |
