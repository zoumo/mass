---
id: T01
parent: S07
milestone: M001-tvc4z0
provides: []
requires: []
affects: []
key_files: ["pkg/ari/client.go", "pkg/ari/client_test.go"]
key_decisions: ["Simplified client without event handling (single-shot RPC calls only)", "Used blocking decoder.Decode instead of async readLoop for simplicity", "Added response ID mismatch validation for robustness"]
patterns_established: []
drill_down_paths: []
observability_surfaces: []
duration: ""
verification_result: "Verified that:
- go build ./pkg/ari/... passes (compilation succeeds)
- go test ./pkg/ari/... passes (all 6 new client tests + existing server tests pass)
- All Must-Haves met: NewClient connects, Call sends/unmarshals, no event handling, build passes
- All Negative Tests covered: socket missing, daemon unavailable, malformed response, RPC error"
completed_at: 2026-04-06T16:16:22.682Z
blocker_discovered: false
---

# T01: Created simplified ARI JSON-RPC client package with NewClient/Call/Close methods for single-shot management commands.

> Created simplified ARI JSON-RPC client package with NewClient/Call/Close methods for single-shot management commands.

## What Happened
---
id: T01
parent: S07
milestone: M001-tvc4z0
key_files:
  - pkg/ari/client.go
  - pkg/ari/client_test.go
key_decisions:
  - Simplified client without event handling (single-shot RPC calls only)
  - Used blocking decoder.Decode instead of async readLoop for simplicity
  - Added response ID mismatch validation for robustness
duration: ""
verification_result: passed
completed_at: 2026-04-06T16:16:22.683Z
blocker_discovered: false
---

# T01: Created simplified ARI JSON-RPC client package with NewClient/Call/Close methods for single-shot management commands.

**Created simplified ARI JSON-RPC client package with NewClient/Call/Close methods for single-shot management commands.**

## What Happened

Read cmd/agent-shim-cli/main.go to understand the existing JSON-RPC client pattern, then created a simplified version in pkg/ari/client.go. The original client had full event handling with readLoop, pending map, and events channel - but for agentdctl management commands, we only need single-shot RPC calls without streaming events.

Key simplifications from agent-shim-cli:
1. Removed readLoop goroutine - Call uses blocking decoder.Decode directly
2. Removed pending map and events channel - no async response routing needed
3. Removed notify() function - only Call() needed for request-response pattern
4. Added response ID mismatch validation - ensures correct request/response pairing

Created comprehensive test suite in pkg/ari/client_test.go covering:
- Socket file missing (NewClient returns error)
- Daemon unavailable (connection refused)
- Malformed JSON response (Call returns parse error)
- RPC error response (Call returns error with code/message)
- Response ID mismatch (Call returns error)
- Happy path success case

All tests use mock Unix socket servers to verify error handling without requiring a real ARI daemon.

## Verification

Verified that:
- go build ./pkg/ari/... passes (compilation succeeds)
- go test ./pkg/ari/... passes (all 6 new client tests + existing server tests pass)
- All Must-Haves met: NewClient connects, Call sends/unmarshals, no event handling, build passes
- All Negative Tests covered: socket missing, daemon unavailable, malformed response, RPC error

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go build ./pkg/ari/...` | 0 | ✅ pass | 1000ms |
| 2 | `go test ./pkg/ari/... -v -run "TestNewClient|TestCall"` | 0 | ✅ pass | 1312ms |
| 3 | `go test ./pkg/ari/...` | 0 | ✅ pass | 6457ms |


## Deviations

None. Implementation matched the task plan exactly.

## Known Issues

None.

## Files Created/Modified

- `pkg/ari/client.go`
- `pkg/ari/client_test.go`


## Deviations
None. Implementation matched the task plan exactly.

## Known Issues
None.
