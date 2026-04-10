---
estimated_steps: 17
estimated_files: 6
skills_used: []
---

# T01: Run golangci-lint --fix to eliminate all unconvert, copyloopvar, and ineffassign issues

Run `golangci-lint run --fix ./...` to auto-fix all 24 lint findings across 6 files. The command is idempotent — safe to run whether or not fixes are pre-applied.

Background on each issue category:
- **unconvert (21 issues)**: Unnecessary type-conversions like `json.RawMessage(json.RawMessage(x))` or `int32(someInt32Var)`. golangci-lint --fix strips the redundant outer conversion.
- **copyloopvar (1 issue)**: `pkg/spec/example_bundles_test.go:33` — the loop variable `bundleDir` is copied inside the loop but Go 1.22+ makes this unnecessary. golangci-lint --fix removes the shadow variable.
- **ineffassign (1 issue)**: `pkg/runtime/terminal.go:119` — `outputWriter` is assigned `output` then immediately reassigned to `limitedWriter`; the first assignment is never read. golangci-lint --fix restructures so only one assignment remains.

Affected files:
- `pkg/ari/server_test.go` — 14 unconvert findings
- `pkg/rpc/server_test.go` — 2 unconvert findings
- `pkg/agentd/shim_client.go` — 1 unconvert finding
- `pkg/agentd/shim_client_test.go` — 1 unconvert finding
- `pkg/spec/example_bundles_test.go` — 1 copyloopvar finding
- `pkg/runtime/terminal.go` — 1 ineffassign finding

Steps:
1. Run `golangci-lint run --fix ./...` from the repo root. Ignore unrelated lint output (gocritic, misspell, etc.) — only the three target linters matter for this slice.
2. Run `go build ./...` to confirm the fixes did not break compilation.
3. Run `go test ./pkg/ari/... ./pkg/rpc/... ./pkg/agentd/... ./pkg/spec/... ./pkg/runtime/... 2>&1 | tail -20` to verify affected packages still pass.
4. Run the verification command to confirm 0 remaining findings.

## Inputs

- ``pkg/ari/server_test.go` — 14 unconvert findings to fix`
- ``pkg/rpc/server_test.go` — 2 unconvert findings to fix`
- ``pkg/agentd/shim_client.go` — 1 unconvert finding to fix`
- ``pkg/agentd/shim_client_test.go` — 1 unconvert finding to fix`
- ``pkg/spec/example_bundles_test.go` — 1 copyloopvar finding to fix`
- ``pkg/runtime/terminal.go` — 1 ineffassign finding to fix`

## Expected Output

- ``pkg/ari/server_test.go` — unnecessary type conversions removed`
- ``pkg/rpc/server_test.go` — unnecessary type conversions removed`
- ``pkg/agentd/shim_client.go` — unnecessary type conversion removed`
- ``pkg/agentd/shim_client_test.go` — unnecessary type conversion removed`
- ``pkg/spec/example_bundles_test.go` — redundant loop-variable copy removed`
- ``pkg/runtime/terminal.go` — ineffectual first assignment to outputWriter removed`

## Verification

golangci-lint run ./... 2>&1 | grep -E '\(unconvert\)|\(copyloopvar\)|\(ineffassign\)'; [ $? -eq 1 ] && echo 'PASS: zero findings' || echo 'FAIL: findings remain'
