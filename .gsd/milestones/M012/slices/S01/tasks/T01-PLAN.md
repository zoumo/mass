---
estimated_steps: 1
estimated_files: 4
skills_used: []
---

# T01: Create pkg/jsonrpc/ core files

Create errors.go, peer.go, server.go, client.go with all types and logic per the final plan.

## Inputs

- `docs/plan/codebase-refactor-20260413.md final plan Phase 1`
- `sourcegraph/jsonrpc2 v0.2.1 API`

## Expected Output

- `pkg/jsonrpc/errors.go`
- `pkg/jsonrpc/peer.go`
- `pkg/jsonrpc/server.go`
- `pkg/jsonrpc/client.go`

## Verification

go build ./pkg/jsonrpc/...
