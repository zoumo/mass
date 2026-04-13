# S02: Phase 2a: Pure Rename/Move (api/spec→api/runtime, pkg/shimapi→api/shim)

**Goal:** Rename api/spec to api/runtime and move pkg/shimapi to api/shim. Update all import paths. No behavior changes.
**Demo:** make build + go test ./... pass; JSON output identical to before

## Must-Haves

- Not provided.

## Proof Level

- This slice proves: Not provided.

## Integration Closure

Not provided.

## Verification

- Not provided.

## Tasks

- [x] **T01: Rename api/spec→api/runtime, move pkg/shimapi→api/shim, update all imports** `est:1h`
  1. Create api/runtime/ directory with config.go (from api/spec/types.go) and state.go (from api/spec/state.go), package name = runtime
2. Create api/shim/ directory with types.go (from pkg/shimapi/types.go), package name = shim
3. Update all ~17 files importing api/spec -> api/runtime
4. Update all ~6 files importing pkg/shimapi -> api/shim
5. Delete api/spec/ and pkg/shimapi/
6. Verify: make build + go test ./...
  - Files: `api/runtime/config.go`, `api/runtime/state.go`, `api/shim/types.go`
  - Verify: make build && go test ./...

## Files Likely Touched

- api/runtime/config.go
- api/runtime/state.go
- api/shim/types.go
