---
estimated_steps: 14
estimated_files: 4
skills_used: []
---

# T02: Add spec.StatusIdle + spec.StatusError; update pkg/runtime to write idle

Delete spec.StatusCreated and add spec.StatusIdle (value "idle") and spec.StatusError (value "error") to pkg/spec/state_types.go. Update all callers within pkg/spec and pkg/runtime.

Steps:
1. Edit pkg/spec/state_types.go:
   - Replace `StatusCreated Status = "created"` with `StatusIdle Status = "idle"` (keep comment updated: ACP handshake done, ready for prompt)
   - Add `StatusError Status = "error"` after StatusStopped
   - Update inline docs
2. Edit pkg/runtime/runtime.go:
   - Replace `spec.StatusCreated` → `spec.StatusIdle` (appears in Create() after handshake success, and in Prompt() when resetting to idle after turn ends). Check all occurrences: `grep -n StatusCreated pkg/runtime/runtime.go`
3. Edit pkg/spec/state_test.go: replace StatusCreated → StatusIdle in test assertions.
4. Edit pkg/runtime/runtime_test.go: replace StatusCreated → StatusIdle in test assertions.

Constraints:
- Do NOT add a StatusCreated alias — no compat layer.
- The JSON value written to state.json changes from "created" to "idle" — this is intentional per D085.
- Other files outside pkg/spec and pkg/runtime that use StatusCreated will be fixed in T03/T04 (they're in packages being adapted there).

## Inputs

- `pkg/spec/state_types.go`
- `pkg/runtime/runtime.go`
- `pkg/spec/state_test.go`
- `pkg/runtime/runtime_test.go`

## Expected Output

- `pkg/spec/state_types.go`
- `pkg/runtime/runtime.go`
- `pkg/spec/state_test.go`
- `pkg/runtime/runtime_test.go`

## Verification

cd /Users/jim/code/zoumo/open-agent-runtime && go build ./pkg/spec/... ./pkg/runtime/... && ! rg 'StatusCreated' --type go pkg/spec/ pkg/runtime/ && go test ./pkg/spec/... ./pkg/runtime/... -count=1 -timeout 30s
