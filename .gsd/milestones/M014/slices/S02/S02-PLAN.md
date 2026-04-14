# S02: state.json type definitions

**Goal:** pkg/runtime-spec/api defines SessionState, AgentInfo, AgentCapabilities, SessionInfo, AvailableCommand, ConfigOption and all sub-types with correct custom MarshalJSON/UnmarshalJSON; State struct includes UpdatedAt, Session, and EventCounts fields; round-trip test proves WriteState → ReadState fidelity for a full State with all union variants populated.
**Demo:** After this: round-trip test WriteState({Session: full SessionState with ConfigOption Select variant + AvailableCommandInput Unstructured}) → ReadState reproduces identical values; EventCounts and UpdatedAt survive round-trip.

## Must-Haves

- All new types compile without importing pkg/shim/api
- State struct has UpdatedAt (string), Session (*SessionState), EventCounts (map[string]int) fields
- ConfigOption Select variant round-trips correctly (type discriminator preserved)
- AvailableCommandInput Unstructured variant round-trips correctly (field-presence discrimination)
- ConfigSelectOptions both Ungrouped and Grouped variants round-trip correctly
- EventCounts and UpdatedAt survive WriteState → ReadState
- `go test ./pkg/runtime-spec/...` passes
- `go build ./pkg/runtime-spec/...` passes

## Proof Level

- This slice proves: Contract — proves the state.json schema accepts and round-trips all session metadata types. No runtime or integration required.

## Integration Closure

Upstream: none (no dependencies). Downstream: S03 (writeState refactor) and S05 (bootstrap capture) will use the types defined here. The new State fields (Session, EventCounts, UpdatedAt) and the SessionState type are the contract surfaces this slice provides.

## Verification

- None — pure type definitions with no runtime behavior.

## Tasks

- [x] **T01: Define session metadata types and extend State struct** `est:45m`
  Add all session metadata types to pkg/runtime-spec/api so State can represent ACP session data in state.json. This is foundational work for the entire M014 milestone — every downstream slice depends on these type definitions.

**Key constraint (D123):** All types must be self-contained in pkg/runtime-spec/api. Do NOT import pkg/shim/api. The union types (AvailableCommand, ConfigOption, ConfigSelectOptions, AvailableCommandInput) already exist in pkg/shim/api/event_types.go — copy the struct definitions and MarshalJSON/UnmarshalJSON methods, adapting package references.

## Steps

1. Create `pkg/runtime-spec/api/session.go` with package declaration and imports (encoding/json, fmt only).

2. Define simple session types (no custom marshal needed):
   - `SessionState` — top-level session metadata container:
     ```go
     type SessionState struct {
         AgentInfo         *AgentInfo         `json:"agentInfo,omitempty"`
         Capabilities      *AgentCapabilities `json:"capabilities,omitempty"`
         AvailableCommands []AvailableCommand `json:"availableCommands,omitempty"`
         ConfigOptions     []ConfigOption     `json:"configOptions,omitempty"`
         SessionInfo       *SessionInfo       `json:"sessionInfo,omitempty"`
         CurrentMode       *string            `json:"currentMode,omitempty"`
     }
     ```
   - `AgentInfo` — mirrors acp.Implementation:
     ```go
     type AgentInfo struct {
         Meta    map[string]any `json:"_meta,omitempty"`
         Name    string         `json:"name"`
         Version string         `json:"version"`
         Title   *string        `json:"title,omitempty"`
     }
     ```
   - `AgentCapabilities` — mirrors acp.AgentCapabilities:
     ```go
     type AgentCapabilities struct {
         Meta                map[string]any      `json:"_meta,omitempty"`
         LoadSession         bool                `json:"loadSession,omitempty"`
         McpCapabilities     McpCapabilities     `json:"mcpCapabilities,omitempty"`
         PromptCapabilities  PromptCapabilities  `json:"promptCapabilities,omitempty"`
         SessionCapabilities SessionCapabilities `json:"sessionCapabilities,omitempty"`
     }
     ```
   - `McpCapabilities` — mirrors acp.McpCapabilities:
     ```go
     type McpCapabilities struct {
         Meta map[string]any `json:"_meta,omitempty"`
         Http bool           `json:"http,omitempty"`
         Sse  bool           `json:"sse,omitempty"`
     }
     ```
   - `PromptCapabilities` — mirrors acp.PromptCapabilities:
     ```go
     type PromptCapabilities struct {
         Meta            map[string]any `json:"_meta,omitempty"`
         Audio           bool           `json:"audio,omitempty"`
         EmbeddedContext bool           `json:"embeddedContext,omitempty"`
         Image           bool           `json:"image,omitempty"`
     }
     ```
   - `SessionCapabilities` — mirrors acp.SessionCapabilities:
     ```go
     type SessionCapabilities struct {
         Meta map[string]any           `json:"_meta,omitempty"`
         Fork *SessionForkCapabilities `json:"fork,omitempty"`
     }
     ```
   - `SessionForkCapabilities` — mirrors acp.SessionForkCapabilities:
     ```go
     type SessionForkCapabilities struct {
         Meta map[string]any `json:"_meta,omitempty"`
     }
     ```
   - `SessionInfo` — session metadata updates:
     ```go
     type SessionInfo struct {
         Meta      map[string]any `json:"_meta,omitempty"`
         Title     *string        `json:"title,omitempty"`
         UpdatedAt *string        `json:"updatedAt,omitempty"`
     }
     ```

3. Copy union types with custom marshal from `pkg/shim/api/event_types.go` into `session.go`. These types have identical JSON wire shapes. Copy the following exactly (adjust package references from 'events:' to 'state:' in error messages):
   - `AvailableCommand` struct (no custom marshal)
   - `AvailableCommandInput` struct + `MarshalJSON` + `UnmarshalJSON` (field-presence discriminator: 'hint' → Unstructured)
   - `UnstructuredCommandInput` struct
   - `ConfigOption` struct + `MarshalJSON` + `UnmarshalJSON` ('type' discriminator: 'select' → Select)
   - `ConfigOptionSelect` struct
   - `ConfigSelectOptions` struct + `MarshalJSON` + `UnmarshalJSON` (array element shape discriminator)
   - `ConfigSelectOption` struct
   - `ConfigSelectGroup` struct

4. Extend `pkg/runtime-spec/api/state.go` — add three new fields to the State struct:
   ```go
   // UpdatedAt is the RFC3339Nano timestamp of the last state write.
   UpdatedAt string `json:"updatedAt,omitempty"`
   
   // Session contains ACP session metadata populated progressively
   // as the agent reports notifications.
   Session *SessionState `json:"session,omitempty"`
   
   // EventCounts maps event type strings to their cumulative counts.
   // Derived field — set on every state write, not independently.
   EventCounts map[string]int `json:"eventCounts,omitempty"`
   ```

5. Run `go build ./pkg/runtime-spec/...` to verify compilation.

## Must-Haves

- [ ] pkg/runtime-spec/api does NOT import pkg/shim/api (check go imports)
- [ ] All union types have correct MarshalJSON/UnmarshalJSON methods
- [ ] Error messages in marshal methods use 'state:' prefix (not 'events:') to distinguish from shim/api copies
- [ ] State struct has UpdatedAt, Session, EventCounts fields with correct json tags
- [ ] SessionState has all 6 sub-fields: AgentInfo, Capabilities, AvailableCommands, ConfigOptions, SessionInfo, CurrentMode
  - Files: `pkg/runtime-spec/api/session.go`, `pkg/runtime-spec/api/state.go`
  - Verify: go build ./pkg/runtime-spec/... && ! grep 'shim/api' pkg/runtime-spec/api/session.go

- [x] **T02: Write round-trip test covering full State with all union variants** `est:30m`
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
  - Files: `pkg/runtime-spec/state_test.go`
  - Verify: go test ./pkg/runtime-spec/... -v -run 'TestStateSuite'

## Files Likely Touched

- pkg/runtime-spec/api/session.go
- pkg/runtime-spec/api/state.go
- pkg/runtime-spec/state_test.go
