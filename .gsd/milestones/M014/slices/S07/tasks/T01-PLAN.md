---
estimated_steps: 24
estimated_files: 2
skills_used: []
---

# T01: Implement Status() EventCounts overlay and write service_test.go

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

## Inputs

- `pkg/shim/server/service.go`
- `pkg/shim/server/translator.go`
- `pkg/shim/runtime/acp/runtime.go`
- `pkg/runtime-spec/api/state.go`
- `pkg/runtime-spec/state.go`

## Expected Output

- `pkg/shim/server/service.go`
- `pkg/shim/server/service_test.go`

## Verification

go test ./pkg/shim/server/... -run TestStatus -v -count=1 && make build && go test ./pkg/shim/server/... -count=1
