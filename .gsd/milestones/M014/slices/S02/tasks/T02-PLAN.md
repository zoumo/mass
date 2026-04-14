---
estimated_steps: 34
estimated_files: 1
skills_used: []
---

# T02: Write round-trip test covering full State with all union variants

Prove that WriteState → ReadState reproduces identical values for a full State including Session (all sub-fields with union variants populated), EventCounts, and UpdatedAt. This is the slice's demo criterion.

## Steps

1. Open `pkg/runtime-spec/state_test.go` and add a new test method to the existing StateSuite.

2. Create a helper function `fullSessionState()` that returns a `*apiruntime.SessionState` with all fields populated:
   - AgentInfo with Name, Version, Title
   - AgentCapabilities with LoadSession: true, McpCapabilities (Http: true), PromptCapabilities (Image: true, Audio: true, EmbeddedContext: true), SessionCapabilities (Fork: &SessionForkCapabilities{})
   - AvailableCommands — at least 2 commands, one WITH an Input (Unstructured variant with Hint) and one WITHOUT Input (nil)
   - ConfigOptions — at least 2 options: one with Ungrouped ConfigSelectOptions, one with Grouped ConfigSelectOptions
   - SessionInfo with Title and UpdatedAt
   - CurrentMode set to a string value

3. Create `fullState()` helper returning `apiruntime.State` with:
   - All existing fields (OarVersion, ID, Status, PID, Bundle, Annotations, ExitCode)
   - UpdatedAt set to an RFC3339Nano timestamp string
   - Session set to the fullSessionState() result
   - EventCounts: map[string]int{"text": 42, "tool_call": 7, "state_change": 3, "turn_start": 2, "turn_end": 2, "user_message": 2}

4. Add `TestFullStateRoundTrip` — write full state, read it back, deep-compare with testify assert.Equal (or require.Equal). This covers:
   - All basic fields survive
   - Session.AgentInfo survives
   - Session.Capabilities survives (including nested SessionForkCapabilities)
   - Session.AvailableCommands survive (including Unstructured input variant)
   - Session.ConfigOptions survive (both Ungrouped and Grouped variants)
   - Session.SessionInfo survives
   - Session.CurrentMode survives
   - EventCounts survive
   - UpdatedAt survives

5. Add `TestStateRoundTripNilSession` — write State with nil Session, read back, confirm Session is nil (no spurious empty object).

6. Add `TestStateRoundTripEmptyEventCounts` — write State with nil EventCounts map, read back, confirm EventCounts is nil.

7. Run `go test ./pkg/runtime-spec/...` — all tests pass including existing ones.

## Must-Haves

- [ ] Round-trip test exercises ConfigOption Select variant with both Ungrouped and Grouped ConfigSelectOptions
- [ ] Round-trip test exercises AvailableCommandInput Unstructured variant
- [ ] Round-trip test exercises nil Session and nil EventCounts edge cases
- [ ] All existing state_test.go tests continue to pass
- [ ] Test uses testify assertions consistent with existing test style (suite-based)

## Inputs

- `pkg/runtime-spec/api/session.go`
- `pkg/runtime-spec/api/state.go`
- `pkg/runtime-spec/state_test.go`
- `pkg/runtime-spec/state.go`

## Expected Output

- `pkg/runtime-spec/state_test.go`

## Verification

go test ./pkg/runtime-spec/... -v -run 'TestStateSuite'
