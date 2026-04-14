# S07: runtime/status overlay + doc updates

**Goal:** runtime/status overlays Translator's real-time in-memory EventCounts onto the state.json snapshot; design docs reflect the enriched state schema introduced by M014.
**Demo:** After this: test calls Status() with a Translator that has in-memory counts different from state.json; response.State.EventCounts matches Translator memory not file; all acceptance criteria from plan doc pass; make build + go test ./... passes.

## Must-Haves

- `go test ./pkg/shim/server/... -run TestStatus -v` passes — Status() returns Translator memory counts, not state.json counts
- `make build` exits 0
- `go test ./...` exits 0
- shim-rpc-spec.md `runtime/status` response example includes `session`, `eventCounts`, `updatedAt`
- shim-rpc-spec.md `state_change` content example includes `sessionChanged` field
- runtime-spec.md state example includes `session`, `eventCounts`, `updatedAt`

## Proof Level

- This slice proves: contract — Status() overlay proven via unit test with controlled Translator vs state.json divergence

## Integration Closure

- Upstream surfaces consumed: `pkg/shim/server/translator.go` EventCounts() (S04), `pkg/shim/runtime/acp/runtime.go` GetState() with EventCounts flush (S06)
- New wiring introduced: `st.EventCounts = s.trans.EventCounts()` in Service.Status() before returning
- What remains: nothing — this is the final slice in M014

## Verification

- Runtime signals: runtime/status response now carries real-time eventCounts from Translator memory
- Inspection surfaces: `runtime/status` JSON-RPC call returns up-to-date counts even between state writes
- Failure visibility: if Translator is nil or EventCounts() panics, Status() will propagate the panic (existing behavior for nil fields)
- Redaction constraints: none

## Tasks

- [x] **T01: Implement Status() EventCounts overlay and write service_test.go** `est:30m`
  ## Description

The `Service.Status()` method in `pkg/shim/server/service.go` currently reads state from `state.json` via `mgr.GetState()` and returns it as-is. Because EventCounts is flushed to state.json only piggy-backed on state writes (lifecycle transitions, metadata updates), the file value is stale between writes. The Translator holds the authoritative real-time counts in memory. This task adds a single-line overlay before returning: `st.EventCounts = s.trans.EventCounts()`.

It also creates `pkg/shim/server/service_test.go` with a test that proves the overlay works: write a state.json with stale EventCounts, create a Translator with different in-memory counts (by broadcasting events), call Status(), and assert the returned EventCounts matches the Translator memory, not the file.

## Steps

1. Open `pkg/shim/server/service.go` and find the `Status()` method.
2. After `st, err := s.mgr.GetState()` and the error check, add: `st.EventCounts = s.trans.EventCounts()` — this overlays the real-time in-memory counts onto the state read from disk.
3. Create `pkg/shim/server/service_test.go` with:
   - A test `TestStatus_EventCountsOverlay` that:
     a. Creates a temp dir for state storage.
     b. Uses `runtimespec.WriteState()` to write a state.json with stale `EventCounts{"text": 1}` (simulating a previous state write).
     c. Creates a Manager via `acpruntime.New(...)` pointing to that temp dir as stateDir.
     d. Creates a Translator via `NewTranslator("run-1", in, nil)` where `in` is a buffered channel. The Translator does NOT need to be started — we'll use `NotifyStateChange` to broadcast events that increment the in-memory counts. Specifically: call `NotifyStateChange("idle","idle",1,"test",[])` a few times to build up in-memory counts that differ from the file.
     e. Creates a Service via `New(mgr, trans, "", slog.Default())`.
     f. Calls `svc.Status(context.Background())`.
     g. Asserts `result.State.EventCounts` matches `trans.EventCounts()` (Translator memory), NOT the stale `{"text": 1}` from the file.
4. Run `go test ./pkg/shim/server/... -run TestStatus -v` and confirm the test passes.
5. Run `make build` to confirm no compilation errors.
6. Run `go test ./pkg/shim/server/... -count=1` to confirm no regressions.

## Key constraints
- The Translator.EventCounts() method already exists and returns a copy (S04). Just call it.
- Manager.GetState() reads state.json via `spec.ReadState()`. The test must write state.json to the Manager's stateDir before calling Status().
- The Manager created for the test does NOT need to call Create() — it just needs a valid stateDir with a state.json file so GetState() can read it.
- Import paths: `acpruntime "github.com/zoumo/oar/pkg/shim/runtime/acp"`, `apiruntime "github.com/zoumo/oar/pkg/runtime-spec/api"`, `runtimespec "github.com/zoumo/oar/pkg/runtime-spec"`.
- The config.json is NOT needed in bundleDir for this test — Manager.GetState() only reads stateDir/state.json, it doesn't touch bundleDir.
  - Files: `pkg/shim/server/service.go`, `pkg/shim/server/service_test.go`
  - Verify: go test ./pkg/shim/server/... -run TestStatus -v -count=1 && make build && go test ./pkg/shim/server/... -count=1

- [ ] **T02: Update design docs to reflect M014 enriched state schema** `est:30m`
  ## Description

M014 added `session`, `eventCounts`, and `updatedAt` to state.json and `sessionChanged` to state_change events. The design docs still show the pre-M014 schema. This task updates the normative examples to match the current implementation.

## Steps

1. **Update `docs/design/runtime/shim-rpc-spec.md`:**
   a. Find the `runtime/status` Response JSON example (around line 183). Add `updatedAt`, `session` (with a minimal example showing `agentInfo` and `capabilities`), and `eventCounts` to the `state` object. Add a prose note after the example explaining that `eventCounts` in the `runtime/status` response is overlaid from Translator memory for real-time accuracy, and may differ from the value persisted in state.json.
   b. Find the `state_change` content JSON example (around line 397). Add `"sessionChanged": ["configOptions"]` to demonstrate a metadata-only state_change. Add a prose note that `sessionChanged` is present on metadata-only changes where `previousStatus == status`, listing the possible values: agentInfo, capabilities, availableCommands, configOptions, sessionInfo, currentMode.

2. **Update `docs/design/runtime/runtime-spec.md`:**
   a. Find the State Example JSON (around line 57). Add `updatedAt`, `session` (with a minimal example showing `agentInfo`), and `eventCounts` fields. Keep the example simple — show that these fields exist; full type definitions are in the Go source.
   b. After the `The state MAY include additional properties.` line, add a brief description of the new fields: `updatedAt` (RFC3339Nano timestamp of last state write), `session` (ACP session metadata populated progressively), `eventCounts` (cumulative per-type event counts, derived field).

3. **Do NOT update `docs/design/runtime/agent-shim.md`** — per K029, it is descriptive only and defers to shim-rpc-spec.md and runtime-spec.md for protocol details.

4. Verify: `grep -q 'eventCounts' docs/design/runtime/shim-rpc-spec.md && grep -q 'eventCounts' docs/design/runtime/runtime-spec.md && grep -q 'sessionChanged' docs/design/runtime/shim-rpc-spec.md`

## Key constraints
- Per K029, shim-rpc-spec.md is the authority for method/notification semantics. runtime-spec.md is the authority for state dir layout and state shape. agent-shim.md is descriptive only — do NOT add protocol details there.
- Per K059, when docs explain removed concepts, use affirmative phrasing to avoid tripping grep gates.
- Keep examples minimal — show that the fields exist, don't reproduce the full Go type hierarchy in JSON.
- Write prose in the same language as the surrounding text (Chinese for shim-rpc-spec.md, English for runtime-spec.md).
  - Files: `docs/design/runtime/shim-rpc-spec.md`, `docs/design/runtime/runtime-spec.md`
  - Verify: grep -q 'eventCounts' docs/design/runtime/shim-rpc-spec.md && grep -q 'eventCounts' docs/design/runtime/runtime-spec.md && grep -q 'sessionChanged' docs/design/runtime/shim-rpc-spec.md && grep -q 'updatedAt' docs/design/runtime/runtime-spec.md

## Files Likely Touched

- pkg/shim/server/service.go
- pkg/shim/server/service_test.go
- docs/design/runtime/shim-rpc-spec.md
- docs/design/runtime/runtime-spec.md
