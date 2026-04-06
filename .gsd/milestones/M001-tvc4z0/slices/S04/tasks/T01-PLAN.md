---
estimated_steps: 1
estimated_files: 2
skills_used: []
---

# T01: Update pkg/meta SessionState constants

Align SessionState constants in pkg/meta/models.go with design doc specification. Change from running/stopped/paused/error to created/running/paused:warm/paused:cold/stopped. Update all tests in pkg/meta/session_test.go to use new constants. Re-run tests to verify persistence layer works with new state values.

## Inputs

- `pkg/meta/models.go`
- `pkg/meta/session_test.go`

## Expected Output

- `pkg/meta/models.go`
- `pkg/meta/session_test.go`

## Verification

go test ./pkg/meta/... -v

## Observability Impact

none — simple constant update
