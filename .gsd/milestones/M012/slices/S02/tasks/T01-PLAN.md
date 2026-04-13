---
estimated_steps: 6
estimated_files: 3
skills_used: []
---

# T01: Rename api/spec→api/runtime, move pkg/shimapi→api/shim, update all imports

1. Create api/runtime/ directory with config.go (from api/spec/types.go) and state.go (from api/spec/state.go), package name = runtime
2. Create api/shim/ directory with types.go (from pkg/shimapi/types.go), package name = shim
3. Update all ~17 files importing api/spec -> api/runtime
4. Update all ~6 files importing pkg/shimapi -> api/shim
5. Delete api/spec/ and pkg/shimapi/
6. Verify: make build + go test ./...

## Inputs

- `api/spec/`
- `pkg/shimapi/`

## Expected Output

- `api/runtime/config.go`
- `api/runtime/state.go`
- `api/shim/types.go`

## Verification

make build && go test ./...
