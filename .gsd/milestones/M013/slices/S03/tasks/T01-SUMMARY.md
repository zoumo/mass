---
id: T01
parent: S03
milestone: M013
key_files:
  - pkg/shim/api/methods.go
  - pkg/shim/api/types.go
  - pkg/shim/api/service.go
  - pkg/shim/api/client.go
  - pkg/events/constants.go
key_decisions:
  - pkg/shim/api/methods.go includes only the shim subset of method constants (not workspace/agentrun/agent) to keep the package focused on the shim protocol boundary
duration: 
verification_result: passed
completed_at: 2026-04-14T10:32:52.956Z
blocker_discovered: false
---

# T01: Created pkg/shim/api/ (methods, types, service, client) and pkg/events/constants.go as additive destination packages; all builds pass with api/ still intact

**Created pkg/shim/api/ (methods, types, service, client) and pkg/events/constants.go as additive destination packages; all builds pass with api/ still intact**

## What Happened

This task is purely additive — no existing code was modified. Five new files were created:

1. `pkg/shim/api/methods.go` — shim-only RPC method constants (MethodSession*, MethodRuntime*, MethodShimEvent) in package `api`, scoped to just the shim subset (no workspace/agentrun/agent constants).
2. `pkg/shim/api/types.go` — verbatim copy of `api/shim/types.go` with `package shim` → `package api`; all imports (pkg/runtime-spec/api, pkg/events) kept as-is.
3. `pkg/shim/api/service.go` — verbatim copy of `api/shim/service.go` with `package shim` → `package api`; all type references are same-package so no qualifier changes were needed.
4. `pkg/shim/api/client.go` — copy of `api/shim/client.go` with `package shim` → `package api`; dropped the `"github.com/zoumo/oar/api"` import; replaced every `api.MethodSession*/api.MethodRuntime*` reference with the unqualified constant names (now in the same package).
5. `pkg/events/constants.go` — package `events`; all EventType* and Category* constants copied verbatim from `api/events.go`.

The `api/` directory is untouched; no consumers were changed. Build stays green throughout.

## Verification

Ran three checks from the task plan:
- `go build ./pkg/shim/api/...` → exit 0
- `go build ./pkg/events/...` → exit 0
- `make build` (builds bin/agentd and bin/agentdctl) → exit 0

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go build ./pkg/shim/api/...` | 0 | ✅ pass | 800ms |
| 2 | `go build ./pkg/events/...` | 0 | ✅ pass | 400ms |
| 3 | `make build` | 0 | ✅ pass | 3200ms |

## Deviations

None.

## Known Issues

None.

## Files Created/Modified

- `pkg/shim/api/methods.go`
- `pkg/shim/api/types.go`
- `pkg/shim/api/service.go`
- `pkg/shim/api/client.go`
- `pkg/events/constants.go`
