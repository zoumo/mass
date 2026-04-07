# S02: shim-rpc clean break — UAT

**Milestone:** M002
**Written:** 2026-04-07T17:10:00.000Z

## Preconditions

- Go toolchain available (`go build`, `go test`)
- Repository at HEAD of S02 work (commit `e34efc8` or later)
- `scripts/verify-m002-s01-contract.sh` present and executable

---

## TC-01 — Contract verifier passes

**What it proves:** The design-contract cross-doc authority checks established in S01 still pass after S02's implementation changes.

**Steps:**
1. Run `bash scripts/verify-m002-s01-contract.sh`

**Expected outcome:** Script exits 0 and prints `contract verification passed`.

---

## TC-02 — events package: Envelope shape and seq assignment

**What it proves:** `events.Translator` assigns monotonic seq values; `events.Log` stores and retrieves `Envelope` slices correctly; live and replay shapes are identical.

**Steps:**
1. Run `go test ./pkg/events -count=1 -v`

**Expected outcome:** All tests pass. Specifically:
- Envelope seq values are assigned in order (1, 2, 3, …)
- `Log.Get(afterSeq=0)` returns all stored envelopes
- `Log.Get(afterSeq=N)` returns only envelopes with seq > N
- Envelope `Method` field matches the RPC method name (e.g., `runtime/stateChange`)

---

## TC-03 — pkg/rpc: Clean-break shim server surface

**What it proves:** The shim server exposes `session/prompt`, `session/cancel`, `session/subscribe`, `runtime/status`, `runtime/history`, `runtime/shutdown` and no legacy names.

**Steps:**
1. Run `go test ./pkg/rpc -count=1 -v`

**Expected outcome:** All tests pass. No test references `Prompt`, `Cancel`, `Subscribe` (capitalized), `GetState`, `GetHistory`, `Shutdown` (capitalized), or `$/event`.

---

## TC-04 — pkg/agentd: ShimClient NotificationHandler and RuntimeStatus

**What it proves:** `ShimClient` connects with the renamed protocol; `NotificationHandler(method, rawParams)` dispatches correctly; `RuntimeStatus()` returns `ShimStateInfo` with `recovery.lastSeq`.

**Steps:**
1. Run `go test ./pkg/agentd -count=1 -v`

**Expected outcome:** All tests pass. Specifically:
- `ShimClient` subscribes via `session/subscribe` method
- Notification dispatch uses method name string, not type-switch on old PascalCase names
- `RuntimeStatus()` returns a struct with `Recovery.LastSeq` field accessible

---

## TC-05 — pkg/ari: ARI session flows stable

**What it proves:** ARI session handlers (`session/new`, `session/prompt`, `session/cancel`, etc.) continue to work correctly after the shim protocol rename.

**Steps:**
1. Run `go test ./pkg/ari -count=1 -v`

**Expected outcome:** All tests pass. ARI test coverage includes session lifecycle, error cases, and state transitions.

---

## TC-06 — pkg/runtime: Runtime suite

**What it proves:** The runtime layer (which drives the shim server) works end-to-end with the new envelope and method surface.

**Steps:**
1. Run `go test ./pkg/runtime -run TestRuntimeSuite -count=1 -v`

**Expected outcome:** All TestRuntimeSuite test cases pass.

---

## TC-07 — No legacy names in non-test source (slice gate)

**What it proves:** The complete clean-break: zero occurrences of legacy PascalCase method names or `$/event` notifications remain in production source code.

**Steps:**
1. Run: `rg -n --glob '!**/*_test.go' '"Prompt"|"Cancel"|"Subscribe"|"GetState"|"GetHistory"|"Shutdown"|"$/event"' pkg/rpc pkg/agentd pkg/ari cmd/agent-shim-cli`

**Expected outcome:** Command finds **zero matches** (exit code 1 from rg with no results, meaning the negated form `!` exits 0). No legacy method names exist in non-test source.

---

## TC-08 — agent-shim-cli compiles with new surface

**What it proves:** The debug CLI tool compiles against the renamed shim client API.

**Steps:**
1. Run `go build ./cmd/agent-shim-cli`

**Expected outcome:** Build succeeds with exit 0, binary produced.

---

## TC-09 — History replay and live subscribe produce identical envelope shape

**What it proves:** The S01 design requirement that `runtime/history` replay envelopes are byte-for-byte identical to live `session/subscribe` notifications.

**Steps:**
1. Run `go test ./pkg/events -run TestTranslator -count=1 -v` (or equivalent test name)
2. Run `go test ./pkg/rpc -run TestHistory -count=1 -v` (or equivalent test name)

**Expected outcome:** Tests confirm that replayed history envelopes have the same `Method`, `Seq`, and `Params` shape as what was delivered live. No extra or missing fields in replay.

---

## TC-10 — Bootstrap noise stays internal (post-Create hook)

**What it proves:** State-change notifications only attach after `mgr.Create()` completes, so bootstrap-time events are not visible to subscribers.

**Steps:**
1. Run `go test ./pkg/rpc -run TestBootstrap -count=1 -v` or `go test ./pkg/runtime -run TestRuntimeSuite -count=1 -v`

**Expected outcome:** Tests confirm that a subscriber joining after session creation does not receive bootstrap-phase state transitions. Only post-Create state changes appear in history/live streams.

---

## Edge Cases

**EC-01 — Subscriber joins after some events have fired:**
`session/subscribe` with `afterSeq=N` returns only envelopes with seq > N. A fresh subscriber (afterSeq=0) gets the full history. Verified by events package tests.

**EC-02 — RuntimeStatus when no events have fired:**
`RuntimeStatus()` returns `ShimStateInfo` with `Recovery.LastSeq = 0` (or equivalent zero value). Does not panic on empty event log.

**EC-03 — Legacy name used in a test file (allowed):**
The no-legacy-name grep uses `--glob '!**/*_test.go'` to exclude test files. Tests may reference old names in negative test assertions (e.g., asserting a server rejects unknown methods). This is intentional.
