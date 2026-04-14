---
estimated_steps: 102
estimated_files: 2
skills_used: []
---

# T01: Define session metadata types and extend State struct

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

## Inputs

- `pkg/shim/api/event_types.go`
- `pkg/runtime-spec/api/state.go`
- `pkg/runtime-spec/api/types.go`

## Expected Output

- `pkg/runtime-spec/api/session.go`
- `pkg/runtime-spec/api/state.go`

## Verification

go build ./pkg/runtime-spec/... && ! grep 'shim/api' pkg/runtime-spec/api/session.go
