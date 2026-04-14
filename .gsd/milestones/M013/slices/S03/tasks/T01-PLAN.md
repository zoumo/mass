---
estimated_steps: 37
estimated_files: 5
skills_used: []
---

# T01: Create pkg/shim/api/ and pkg/events/constants.go — additive only

Create the destination packages that consumers will migrate to in T02. This task is purely additive — api/ still exists and nothing is changed, so the build stays green throughout.

## Steps

1. Create `pkg/shim/api/methods.go` — shim-only method constants (package `api`):
   ```go
   package api
   // Shim RPC methods (agent-shim ↔ agentd).
   const (
     MethodSessionPrompt    = "session/prompt"
     MethodSessionCancel    = "session/cancel"
     MethodSessionLoad      = "session/load"
     MethodSessionSubscribe = "session/subscribe"
     MethodRuntimeStatus    = "runtime/status"
     MethodRuntimeHistory   = "runtime/history"
     MethodRuntimeStop      = "runtime/stop"
   )
   const (
     MethodShimEvent = "shim/event"
   )
   ```

2. Create `pkg/shim/api/types.go` — copy verbatim from `api/shim/types.go`, change `package shim` → `package api`. Keep all imports as-is (pkg/runtime-spec/api, pkg/events). No other changes needed.

3. Create `pkg/shim/api/service.go` — copy verbatim from `api/shim/service.go`, change `package shim` → `package api`. All types referenced (SessionPromptParams, ShimService, etc.) are now same-package — remove any `shim.` qualifier if present. Keep `pkg/jsonrpc` import.

4. Create `pkg/shim/api/client.go` — copy from `api/shim/client.go`; change `package shim` → `package api`; drop the bare `"github.com/zoumo/oar/api"` import; replace every `api.MethodSession*` / `api.MethodRuntime*` reference with the unqualified constant name (e.g. `api.MethodSessionPrompt` → `MethodSessionPrompt`) because the constants are now in the same package. Types (SessionPromptParams etc.) are also same-package — no qualifier needed.

5. Create `pkg/events/constants.go` — package `events`, copy all constants from `api/events.go` verbatim:
   ```go
   package events
   // EventType* and Category* constants — moved from github.com/zoumo/oar/api.
   const (
     EventTypeText        = "text"
     // ... all EventType* constants ...
     EventTypeStateChange = "state_change"
   )
   const (
     CategorySession = "session"
     CategoryRuntime = "runtime"
   )
   ```

6. Verify: `go build ./pkg/shim/api/...` exits 0; `go build ./pkg/events/...` exits 0; `make build` exits 0 (api/ still exists, no consumers changed yet).

## Inputs

- `api/shim/types.go`
- `api/shim/service.go`
- `api/shim/client.go`
- `api/methods.go`
- `api/events.go`

## Expected Output

- `pkg/shim/api/types.go`
- `pkg/shim/api/service.go`
- `pkg/shim/api/client.go`
- `pkg/shim/api/methods.go`
- `pkg/events/constants.go`

## Verification

go build ./pkg/shim/api/... && go build ./pkg/events/... && make build
