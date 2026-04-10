---
estimated_steps: 8
estimated_files: 3
skills_used: []
---

# T01: Delete 12 unused symbols across three files

Remove all symbols flagged by the `unused` linter. There are three distinct edits:

**1. `pkg/agentd/shim_client.go`** — remove the `mu sync.Mutex` field from the `ShimClient` struct (line 35). After removal, `sync` is no longer imported; remove the `"sync"` line from the import block as well.

**2. `pkg/ari/server.go`** — delete the entire session handler section. The dispatch `Handle()` method only routes `workspace/*`, `agent/*`, and `room/*` — the `session/*` surface was never registered and these handlers are dead code. The exact blocks to delete:
- Lines 431–566: the `// Session handlers` section comment block + `handleSessionNew` function (lines 435–514) + `deliverPrompt` function (lines 519–565)
- Lines 627–967: `handleSessionPrompt` through `handleSessionDetach` (9 methods)

IMPORTANT — do NOT touch `deliverPromptAsync` (lines 567–626). It is NOT flagged and IS called by `handleAgentPrompt` (line 1255) and `handleRoomSend` (line 1996). Only the sync `deliverPrompt` (line 519) is unused; the async variant is live.

After the deletions, the `sync` import in server.go is still used by other code — leave it.

**3. `pkg/events/translator_test.go`** — remove the `ptrInt` helper (lines 376–377). It is defined but never called in any test. Remove both the comment line and the function body.

## Inputs

- ``pkg/agentd/shim_client.go` — ShimClient struct with unused mu field`
- ``pkg/ari/server.go` — contains session handler section (lines 431–967) and deliverPromptAsync (567–626) to keep`
- ``pkg/events/translator_test.go` — contains unused ptrInt helper at lines 376–377`

## Expected Output

- ``pkg/agentd/shim_client.go` — mu field and sync import removed`
- ``pkg/ari/server.go` — session handler section deleted, deliverPromptAsync preserved`
- ``pkg/events/translator_test.go` — ptrInt function removed`

## Verification

go build ./... && golangci-lint run ./... 2>&1 | grep unused; [ $? -eq 1 ] && echo 'PASS: no unused findings' || echo 'FAIL: unused findings remain'; go test ./...
