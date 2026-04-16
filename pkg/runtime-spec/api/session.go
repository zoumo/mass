// Package api — session metadata types for state.json.
// These types mirror the ACP session notification payloads so that state.json
// can capture agent-reported metadata (agent info, capabilities, commands,
// config options, session info) self-contained — no cross-package imports.
package api

import (
	"encoding/json"
	"fmt"
)

// ── SessionState ─────────────────────────────────────────────────────────────

// SessionState is the top-level session metadata container stored in state.json.
// Fields are populated progressively as the agent reports notifications.
type SessionState struct {
	AgentInfo         *AgentInfo         `json:"agentInfo,omitempty"`
	Capabilities      *AgentCapabilities `json:"capabilities,omitempty"`
	AvailableCommands []AvailableCommand `json:"availableCommands,omitempty"`
	ConfigOptions     []ConfigOption     `json:"configOptions,omitempty"`
	SessionInfo       *SessionInfo       `json:"sessionInfo,omitempty"`
	CurrentMode       *string            `json:"currentMode,omitempty"`
	Models            *SessionModelState `json:"models,omitempty"`
}

// ── Simple session types (no custom marshal) ─────────────────────────────────

// AgentInfo mirrors acp.Implementation — agent identity metadata.
type AgentInfo struct {
	Meta    map[string]any `json:"_meta,omitempty"`
	Name    string         `json:"name"`
	Version string         `json:"version"`
	Title   *string        `json:"title,omitempty"`
}

// AgentCapabilities mirrors acp.AgentCapabilities.
type AgentCapabilities struct {
	Meta                map[string]any      `json:"_meta,omitempty"`
	LoadSession         bool                `json:"loadSession,omitempty"`
	McpCapabilities     McpCapabilities     `json:"mcpCapabilities,omitempty"`
	PromptCapabilities  PromptCapabilities  `json:"promptCapabilities,omitempty"`
	SessionCapabilities SessionCapabilities `json:"sessionCapabilities,omitempty"`
}

// McpCapabilities mirrors acp.McpCapabilities.
type McpCapabilities struct {
	Meta map[string]any `json:"_meta,omitempty"`
	Http bool           `json:"http,omitempty"`
	Sse  bool           `json:"sse,omitempty"`
}

// PromptCapabilities mirrors acp.PromptCapabilities.
type PromptCapabilities struct {
	Meta            map[string]any `json:"_meta,omitempty"`
	Audio           bool           `json:"audio,omitempty"`
	EmbeddedContext bool           `json:"embeddedContext,omitempty"`
	Image           bool           `json:"image,omitempty"`
}

// SessionCapabilities mirrors acp.SessionCapabilities.
type SessionCapabilities struct {
	Meta map[string]any           `json:"_meta,omitempty"`
	Fork *SessionForkCapabilities `json:"fork,omitempty"`
}

// SessionForkCapabilities mirrors acp.SessionForkCapabilities.
type SessionForkCapabilities struct {
	Meta map[string]any `json:"_meta,omitempty"`
}

// SessionInfo carries session metadata updates.
type SessionInfo struct {
	Meta      map[string]any `json:"_meta,omitempty"`
	Title     *string        `json:"title,omitempty"`
	UpdatedAt *string        `json:"updatedAt,omitempty"`
}

// ── AvailableCommand — union types with custom marshal ───────────────────────

// AvailableCommand mirrors acp.AvailableCommand.
type AvailableCommand struct {
	Meta        map[string]any         `json:"_meta,omitempty"`
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Input       *AvailableCommandInput `json:"input,omitempty"`
}

// AvailableCommandInput mirrors acp.AvailableCommandInput — union with no type
// discriminator, matched by field presence (hint field => Unstructured variant).
type AvailableCommandInput struct {
	Unstructured *UnstructuredCommandInput `json:"-"`
}

func (a AvailableCommandInput) MarshalJSON() ([]byte, error) {
	switch {
	case a.Unstructured != nil:
		return json.Marshal(a.Unstructured)
	default:
		return nil, fmt.Errorf("state: empty AvailableCommandInput: no variant set")
	}
}

func (a *AvailableCommandInput) UnmarshalJSON(data []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	if _, ok := m["hint"]; ok {
		a.Unstructured = &UnstructuredCommandInput{}
		return json.Unmarshal(data, a.Unstructured)
	}
	return fmt.Errorf("state: unknown AvailableCommandInput shape (no matching variant)")
}

// UnstructuredCommandInput mirrors acp.UnstructuredCommandInput.
type UnstructuredCommandInput struct {
	Meta map[string]any `json:"_meta,omitempty"`
	Hint string         `json:"hint"`
}

// ── ConfigOption — union types with custom marshal ───────────────────────────

// ConfigOption mirrors acp.SessionConfigOption — union with "type" discriminator.
// Currently single variant: select.
type ConfigOption struct {
	Select *ConfigOptionSelect `json:"-"`
}

func (c ConfigOption) MarshalJSON() ([]byte, error) {
	if c.Select == nil {
		return nil, fmt.Errorf("state: empty ConfigOption: no variant set")
	}
	type wrapper struct {
		Type string `json:"type"`
		ConfigOptionSelect
	}
	return json.Marshal(wrapper{Type: "select", ConfigOptionSelect: *c.Select})
}

func (c *ConfigOption) UnmarshalJSON(data []byte) error {
	var raw struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	switch raw.Type {
	case "select":
		c.Select = &ConfigOptionSelect{}
		return json.Unmarshal(data, c.Select)
	default:
		return fmt.Errorf("state: unknown ConfigOption type %q", raw.Type)
	}
}

// ConfigOptionSelect mirrors acp.SessionConfigOptionSelect.
type ConfigOptionSelect struct {
	Meta         map[string]any      `json:"_meta,omitempty"`
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	CurrentValue string              `json:"currentValue"`
	Description  *string             `json:"description,omitempty"`
	Category     *string             `json:"category,omitempty"`
	Options      ConfigSelectOptions `json:"options"`
}

// ConfigSelectOptions mirrors acp.SessionConfigSelectOptions — union of
// ungrouped/grouped. JSON wire shape is a bare array; discriminated by element
// structure.
type ConfigSelectOptions struct {
	Ungrouped []ConfigSelectOption `json:"-"`
	Grouped   []ConfigSelectGroup  `json:"-"`
}

func (c ConfigSelectOptions) MarshalJSON() ([]byte, error) {
	n := 0
	if c.Ungrouped != nil {
		n++
	}
	if c.Grouped != nil {
		n++
	}
	if n == 0 {
		return nil, fmt.Errorf("state: empty ConfigSelectOptions: no variant set")
	}
	if n > 1 {
		return nil, fmt.Errorf("state: ConfigSelectOptions: multiple variants set (%d)", n)
	}
	if c.Grouped != nil {
		return json.Marshal(c.Grouped)
	}
	return json.Marshal(c.Ungrouped)
}

func (c *ConfigSelectOptions) UnmarshalJSON(data []byte) error {
	// Parse as raw array to inspect element structure (field presence).
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("state: ConfigSelectOptions: %w", err)
	}
	if len(raw) == 0 {
		return fmt.Errorf("state: empty ConfigSelectOptions: cannot determine variant from empty array")
	}
	// Inspect first element's fields to discriminate:
	// grouped elements have "group" + "name" + "options"
	// ungrouped elements have "name" + "value"
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw[0], &fields); err != nil {
		return fmt.Errorf("state: ConfigSelectOptions: cannot inspect element: %w", err)
	}
	_, hasGroup := fields["group"]
	_, hasOptions := fields["options"]
	_, hasValue := fields["value"]
	switch {
	case hasGroup && hasOptions: // grouped
		c.Grouped = make([]ConfigSelectGroup, 0, len(raw))
		return json.Unmarshal(data, &c.Grouped)
	case hasValue: // ungrouped
		c.Ungrouped = make([]ConfigSelectOption, 0, len(raw))
		return json.Unmarshal(data, &c.Ungrouped)
	default:
		return fmt.Errorf("state: unknown ConfigSelectOptions element shape (has group=%v, options=%v, value=%v)", hasGroup, hasOptions, hasValue)
	}
}

// ConfigSelectOption mirrors acp.SessionConfigSelectOption.
type ConfigSelectOption struct {
	Meta        map[string]any `json:"_meta,omitempty"`
	Name        string         `json:"name"`
	Value       string         `json:"value"`
	Description *string        `json:"description,omitempty"`
}

// ConfigSelectGroup mirrors acp.SessionConfigSelectGroup.
type ConfigSelectGroup struct {
	Meta    map[string]any       `json:"_meta,omitempty"`
	Group   string               `json:"group"`
	Name    string               `json:"name"`
	Options []ConfigSelectOption `json:"options"`
}

// ── Model types ─────────────────────────────────────────────────────────────

// SessionModelState mirrors acp.SessionModelState — model availability and
// current selection reported by the agent.
type SessionModelState struct {
	AvailableModels []ModelInfo `json:"availableModels"`
	CurrentModelId  string     `json:"currentModelId"`
}

// ModelInfo mirrors acp.ModelInfo — identity of a single model.
type ModelInfo struct {
	ModelId     string  `json:"modelId"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}
