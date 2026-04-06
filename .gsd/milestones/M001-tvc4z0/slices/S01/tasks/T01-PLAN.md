---
estimated_steps: 10
estimated_files: 3
skills_used: []
---

# T01: Add exitCode to shim State and GetStateResult

### Steps

1. Add `ExitCode *int` field to `State` struct in `pkg/spec/state_types.go`. The field is optional (pointer) because it's only populated after process exits.
2. Modify `pkg/runtime/runtime.go`: In the background goroutine that calls `cmd.Wait()`, capture exit code using `cmd.ProcessState.ExitCode()` and include it in the `WriteState` call for stopped state.
3. Add `ExitCode *int` field to `GetStateResult` struct in `pkg/rpc/server.go`.
4. In `handleGetState` function in `pkg/rpc/server.go`, populate `ExitCode` from `st.ExitCode`.

### Must-Haves

- [ ] `State` struct has `ExitCode *int` field with JSON tag `exitCode,omitempty`
- [ ] `GetStateResult` struct has `ExitCode *int` field with JSON tag `exitCode,omitempty`
- [ ] Background goroutine in runtime.go captures exit code via `cmd.ProcessState.ExitCode()`
- [ ] `handleGetState` populates ExitCode from state

## Inputs

- `pkg/spec/state_types.go`
- `pkg/runtime/runtime.go`
- `pkg/rpc/server.go`

## Expected Output

- `pkg/spec/state_types.go`
- `pkg/runtime/runtime.go`
- `pkg/rpc/server.go`

## Verification

go test ./pkg/spec/... ./pkg/runtime/... ./pkg/rpc/... -v
