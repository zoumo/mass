package api

import (
	"encoding/json"
	"fmt"

	acp "github.com/coder/acp-go-sdk"
)

// Event is a sealed interface for all typed events produced by the Translator.
// The unexported discriminator method prevents external implementations.
type Event interface {
	eventType() string
}

// ── Support types ────────────────────────────────────────────────────────────

// Annotations mirrors acp.Annotations.
type Annotations struct {
	Meta         map[string]any `json:"_meta,omitempty"`
	Audience     []string       `json:"audience,omitempty"`
	LastModified *string        `json:"lastModified,omitempty"`
	Priority     *float64       `json:"priority,omitempty"`
}

// ── ContentBlock ─────────────────────────────────────────────────────────────

// ContentBlock mirrors acp.ContentBlock — a discriminated union of 5 content types.
// JSON wire shape is FLAT: {"type":"text","text":"hello","_meta":{...}}
// Go side uses variant pointers with json:"-" + custom MarshalJSON/UnmarshalJSON.
type ContentBlock struct {
	Text         *TextContent         `json:"-"`
	Image        *ImageContent        `json:"-"`
	Audio        *AudioContent        `json:"-"`
	ResourceLink *ResourceLinkContent `json:"-"`
	Resource     *ResourceContent     `json:"-"`
}

func (c ContentBlock) variantCount() int {
	n := 0
	if c.Text != nil {
		n++
	}
	if c.Image != nil {
		n++
	}
	if c.Audio != nil {
		n++
	}
	if c.ResourceLink != nil {
		n++
	}
	if c.Resource != nil {
		n++
	}
	return n
}

func (c ContentBlock) MarshalJSON() ([]byte, error) {
	n := c.variantCount()
	if n == 0 {
		return nil, fmt.Errorf("events: empty ContentBlock: no variant set")
	}
	if n > 1 {
		return nil, fmt.Errorf("events: ContentBlock: multiple variants set (%d)", n)
	}
	switch {
	case c.Text != nil:
		type wrapper struct {
			Type string `json:"type"`
			TextContent
		}
		return json.Marshal(wrapper{Type: "text", TextContent: *c.Text})
	case c.Image != nil:
		type wrapper struct {
			Type string `json:"type"`
			ImageContent
		}
		return json.Marshal(wrapper{Type: "image", ImageContent: *c.Image})
	case c.Audio != nil:
		type wrapper struct {
			Type string `json:"type"`
			AudioContent
		}
		return json.Marshal(wrapper{Type: "audio", AudioContent: *c.Audio})
	case c.ResourceLink != nil:
		type wrapper struct {
			Type string `json:"type"`
			ResourceLinkContent
		}
		return json.Marshal(wrapper{Type: "resource_link", ResourceLinkContent: *c.ResourceLink})
	default: // c.Resource != nil (guaranteed by n==1 check above)
		type wrapper struct {
			Type string `json:"type"`
			ResourceContent
		}
		return json.Marshal(wrapper{Type: "resource", ResourceContent: *c.Resource})
	}
}

func (c *ContentBlock) UnmarshalJSON(data []byte) error {
	var raw struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	switch raw.Type {
	case "text":
		c.Text = &TextContent{}
		return json.Unmarshal(data, c.Text)
	case "image":
		c.Image = &ImageContent{}
		return json.Unmarshal(data, c.Image)
	case "audio":
		c.Audio = &AudioContent{}
		return json.Unmarshal(data, c.Audio)
	case "resource_link":
		c.ResourceLink = &ResourceLinkContent{}
		return json.Unmarshal(data, c.ResourceLink)
	case "resource":
		c.Resource = &ResourceContent{}
		return json.Unmarshal(data, c.Resource)
	default:
		return fmt.Errorf("events: unknown ContentBlock type %q", raw.Type)
	}
}

// TextContent is the text variant of ContentBlock.
// JSON fields match acp.ContentBlockText (minus the "type" discriminator which is handled by ContentBlock).
type TextContent struct {
	Meta        map[string]any `json:"_meta,omitempty"`
	Text        string         `json:"text"`
	Annotations *Annotations   `json:"annotations,omitempty"`
}

// ImageContent is the image variant of ContentBlock.
type ImageContent struct {
	Meta        map[string]any `json:"_meta,omitempty"`
	Data        string         `json:"data"`
	MimeType    string         `json:"mimeType"`
	URI         *string        `json:"uri,omitempty"`
	Annotations *Annotations   `json:"annotations,omitempty"`
}

// AudioContent is the audio variant of ContentBlock.
type AudioContent struct {
	Meta        map[string]any `json:"_meta,omitempty"`
	Data        string         `json:"data"`
	MimeType    string         `json:"mimeType"`
	Annotations *Annotations   `json:"annotations,omitempty"`
}

// ResourceLinkContent is the resource_link variant of ContentBlock.
type ResourceLinkContent struct {
	Meta        map[string]any `json:"_meta,omitempty"`
	URI         string         `json:"uri"`
	Name        string         `json:"name"`
	Description *string        `json:"description,omitempty"`
	MimeType    *string        `json:"mimeType,omitempty"`
	Title       *string        `json:"title,omitempty"`
	Size        *int           `json:"size,omitempty"`
	Annotations *Annotations   `json:"annotations,omitempty"`
}

// ResourceContent is the resource variant of ContentBlock.
type ResourceContent struct {
	Meta        map[string]any   `json:"_meta,omitempty"`
	Resource    EmbeddedResource `json:"resource"`
	Annotations *Annotations     `json:"annotations,omitempty"`
}

// ── EmbeddedResource ─────────────────────────────────────────────────────────

// EmbeddedResource mirrors acp.EmbeddedResourceResource — union of text/blob variants.
// JSON wire shape has NO "type" discriminator — discriminated by text/blob field presence.
type EmbeddedResource struct {
	TextResource *TextResourceContents `json:"-"`
	BlobResource *BlobResourceContents `json:"-"`
}

func (e EmbeddedResource) MarshalJSON() ([]byte, error) {
	n := 0
	if e.TextResource != nil {
		n++
	}
	if e.BlobResource != nil {
		n++
	}
	if n == 0 {
		return nil, fmt.Errorf("events: empty EmbeddedResource: no variant set")
	}
	if n > 1 {
		return nil, fmt.Errorf("events: EmbeddedResource: multiple variants set (%d)", n)
	}
	if e.TextResource != nil {
		return json.Marshal(e.TextResource)
	}
	return json.Marshal(e.BlobResource)
}

func (e *EmbeddedResource) UnmarshalJSON(data []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	_, hasText := m["text"]
	_, hasBlob := m["blob"]
	_, hasURI := m["uri"]
	switch {
	case hasText && hasURI:
		e.TextResource = &TextResourceContents{}
		return json.Unmarshal(data, e.TextResource)
	case hasBlob && hasURI:
		e.BlobResource = &BlobResourceContents{}
		return json.Unmarshal(data, e.BlobResource)
	default:
		return fmt.Errorf("events: unknown EmbeddedResource shape (has text=%v, blob=%v, uri=%v)", hasText, hasBlob, hasURI)
	}
}

// TextResourceContents mirrors acp.TextResourceContents.
type TextResourceContents struct {
	Meta     map[string]any `json:"_meta,omitempty"`
	URI      string         `json:"uri"`
	MimeType *string        `json:"mimeType,omitempty"`
	Text     string         `json:"text"`
}

// BlobResourceContents mirrors acp.BlobResourceContents.
type BlobResourceContents struct {
	Meta     map[string]any `json:"_meta,omitempty"`
	URI      string         `json:"uri"`
	MimeType *string        `json:"mimeType,omitempty"`
	Blob     string         `json:"blob"`
}

// ── ToolCallContent ──────────────────────────────────────────────────────────

// ToolCallContent mirrors acp.ToolCallContent — union of content/diff/terminal variants.
// JSON wire shape is FLAT with "type" discriminator.
type ToolCallContent struct {
	Content  *ToolCallContentContent  `json:"-"`
	Diff     *ToolCallContentDiff     `json:"-"`
	Terminal *ToolCallContentTerminal `json:"-"`
}

func (t ToolCallContent) MarshalJSON() ([]byte, error) {
	n := 0
	if t.Content != nil {
		n++
	}
	if t.Diff != nil {
		n++
	}
	if t.Terminal != nil {
		n++
	}
	if n == 0 {
		return nil, fmt.Errorf("events: empty ToolCallContent: no variant set")
	}
	if n > 1 {
		return nil, fmt.Errorf("events: ToolCallContent: multiple variants set (%d)", n)
	}
	switch {
	case t.Content != nil:
		type wrapper struct {
			Type string `json:"type"`
			ToolCallContentContent
		}
		return json.Marshal(wrapper{Type: "content", ToolCallContentContent: *t.Content})
	case t.Diff != nil:
		type wrapper struct {
			Type string `json:"type"`
			ToolCallContentDiff
		}
		return json.Marshal(wrapper{Type: "diff", ToolCallContentDiff: *t.Diff})
	default: // t.Terminal != nil
		type wrapper struct {
			Type string `json:"type"`
			ToolCallContentTerminal
		}
		return json.Marshal(wrapper{Type: "terminal", ToolCallContentTerminal: *t.Terminal})
	}
}

func (t *ToolCallContent) UnmarshalJSON(data []byte) error {
	var raw struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	switch raw.Type {
	case "content":
		t.Content = &ToolCallContentContent{}
		return json.Unmarshal(data, t.Content)
	case "diff":
		t.Diff = &ToolCallContentDiff{}
		return json.Unmarshal(data, t.Diff)
	case "terminal":
		t.Terminal = &ToolCallContentTerminal{}
		return json.Unmarshal(data, t.Terminal)
	default:
		return fmt.Errorf("events: unknown ToolCallContent type %q", raw.Type)
	}
}

// ToolCallContentContent is the content variant of ToolCallContent.
type ToolCallContentContent struct {
	Meta    map[string]any `json:"_meta,omitempty"`
	Content ContentBlock   `json:"content"`
}

// ToolCallContentDiff is the diff variant of ToolCallContent.
type ToolCallContentDiff struct {
	Meta    map[string]any `json:"_meta,omitempty"`
	Path    string         `json:"path"`
	OldText *string        `json:"oldText,omitempty"`
	NewText string         `json:"newText"`
}

// ToolCallContentTerminal is the terminal variant of ToolCallContent.
// Mirrors acp.ToolCallContentTerminal — includes Meta per SDK definition.
type ToolCallContentTerminal struct {
	Meta       map[string]any `json:"_meta,omitempty"`
	TerminalID string         `json:"terminalId"`
}

// ToolCallLocation mirrors acp.ToolCallLocation.
type ToolCallLocation struct {
	Meta map[string]any `json:"_meta,omitempty"`
	Path string         `json:"path"`
	Line *int           `json:"line,omitempty"`
}

// ── AvailableCommand support types ───────────────────────────────────────────

// AvailableCommand mirrors acp.AvailableCommand.
type AvailableCommand struct {
	Meta        map[string]any         `json:"_meta,omitempty"`
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Input       *AvailableCommandInput `json:"input,omitempty"`
}

// AvailableCommandInput mirrors acp.AvailableCommandInput — union with no type discriminator,
// matched by field presence (hint field => Unstructured variant).
type AvailableCommandInput struct {
	Unstructured *UnstructuredCommandInput `json:"-"`
}

func (a AvailableCommandInput) MarshalJSON() ([]byte, error) {
	switch {
	case a.Unstructured != nil:
		return json.Marshal(a.Unstructured)
	default:
		return nil, fmt.Errorf("events: empty AvailableCommandInput: no variant set")
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
	return fmt.Errorf("events: unknown AvailableCommandInput shape (no matching variant)")
}

// UnstructuredCommandInput mirrors acp.UnstructuredCommandInput.
type UnstructuredCommandInput struct {
	Meta map[string]any `json:"_meta,omitempty"`
	Hint string         `json:"hint"`
}

// ── ConfigOption support types ────────────────────────────────────────────────

// ConfigOption mirrors acp.SessionConfigOption — union with "type" discriminator.
// Currently single variant: select.
type ConfigOption struct {
	Select *ConfigOptionSelect `json:"-"`
}

func (c ConfigOption) MarshalJSON() ([]byte, error) {
	// Currently single-variant union; multi-variant guard for future-proofing.
	if c.Select == nil {
		return nil, fmt.Errorf("events: empty ConfigOption: no variant set")
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
		return fmt.Errorf("events: unknown ConfigOption type %q", raw.Type)
	}
}

// ConfigOptionSelect mirrors acp.SessionConfigOptionSelect.
type ConfigOptionSelect struct {
	Meta         map[string]any     `json:"_meta,omitempty"`
	ID           string             `json:"id"`
	Name         string             `json:"name"`
	CurrentValue string             `json:"currentValue"`
	Description  *string            `json:"description,omitempty"`
	Category     *string            `json:"category,omitempty"`
	Options      ConfigSelectOptions `json:"options"`
}

// ConfigSelectOptions mirrors acp.SessionConfigSelectOptions — union of ungrouped/grouped.
// JSON wire shape is a bare array; discriminated by element structure.
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
		return nil, fmt.Errorf("events: empty ConfigSelectOptions: no variant set")
	}
	if n > 1 {
		return nil, fmt.Errorf("events: ConfigSelectOptions: multiple variants set (%d)", n)
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
		return fmt.Errorf("events: ConfigSelectOptions: %w", err)
	}
	if len(raw) == 0 {
		return fmt.Errorf("events: empty ConfigSelectOptions: cannot determine variant from empty array")
	}
	// Inspect first element's fields to discriminate:
	// grouped elements have "group" + "name" + "options"
	// ungrouped elements have "name" + "value"
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw[0], &fields); err != nil {
		return fmt.Errorf("events: ConfigSelectOptions: cannot inspect element: %w", err)
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
		return fmt.Errorf("events: unknown ConfigSelectOptions element shape (has group=%v, options=%v, value=%v)", hasGroup, hasOptions, hasValue)
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

// ── Cost ─────────────────────────────────────────────────────────────────────

// Cost mirrors acp.Cost.
type Cost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// ── Core event types ──────────────────────────────────────────────────────────

// TextEvent carries a streamed text chunk from the agent.
// Text is the convenience field for backward compatibility; Content carries full data.
type TextEvent struct {
	Text    string        `json:"text"`
	Content *ContentBlock `json:"content,omitempty"`
}

func (TextEvent) eventType() string { return EventTypeText }

// ThinkingEvent carries a streamed thinking/reasoning chunk from the agent.
type ThinkingEvent struct {
	Text    string        `json:"text"`
	Content *ContentBlock `json:"content,omitempty"`
}

func (ThinkingEvent) eventType() string { return EventTypeThinking }

// ToolCallEvent signals that the agent invoked a tool.
type ToolCallEvent struct {
	Meta      map[string]any     `json:"_meta,omitempty"`
	ID        string             `json:"id"`
	Kind      string             `json:"kind"`
	Title     string             `json:"title"`
	Status    string             `json:"status,omitempty"`
	Content   []ToolCallContent  `json:"content,omitempty"`
	Locations []ToolCallLocation `json:"locations,omitempty"`
	RawInput  any                `json:"rawInput,omitempty"`
	RawOutput any                `json:"rawOutput,omitempty"`
}

func (ToolCallEvent) eventType() string { return EventTypeToolCall }

// ToolResultEvent carries the outcome of a tool invocation.
type ToolResultEvent struct {
	Meta      map[string]any     `json:"_meta,omitempty"`
	ID        string             `json:"id"`
	Status    string             `json:"status"`
	Kind      string             `json:"kind,omitempty"`
	Title     string             `json:"title,omitempty"`
	Content   []ToolCallContent  `json:"content,omitempty"`
	Locations []ToolCallLocation `json:"locations,omitempty"`
	RawInput  any                `json:"rawInput,omitempty"`
	RawOutput any                `json:"rawOutput,omitempty"`
}

func (ToolResultEvent) eventType() string { return EventTypeToolResult }

// PlanEvent carries an updated plan from the agent session.
// Meta is included to preserve the top-level _meta from SessionUpdatePlan.
// Entries still uses acp.PlanEntry directly; full mirroring is deferred.
type PlanEvent struct {
	Meta    map[string]any  `json:"_meta,omitempty"`
	Entries []acp.PlanEntry `json:"entries"`
}

func (PlanEvent) eventType() string { return EventTypePlan }

// UserMessageEvent carries a streamed text chunk echoed from the user's prompt.
// ACP agents echo the incoming prompt back as UserMessageChunk notifications.
type UserMessageEvent struct {
	Text    string        `json:"text"`
	Content *ContentBlock `json:"content,omitempty"`
}

func (UserMessageEvent) eventType() string { return EventTypeUserMessage }

// TurnStartEvent signals the start of an agent turn.
type TurnStartEvent struct{}

func (TurnStartEvent) eventType() string { return EventTypeTurnStart }

// TurnEndEvent signals the end of an agent turn with a stop reason.
type TurnEndEvent struct {
	StopReason string `json:"stopReason"`
}

func (TurnEndEvent) eventType() string { return EventTypeTurnEnd }

// ErrorEvent is emitted when an unknown or malformed event variant is encountered.
type ErrorEvent struct {
	Msg string `json:"message"`
}

func (ErrorEvent) eventType() string { return EventTypeError }

// ── New event types (previously silently discarded) ───────────────────────────

// AvailableCommandsEvent carries the current list of available commands/tools.
type AvailableCommandsEvent struct {
	Meta     map[string]any     `json:"_meta,omitempty"`
	Commands []AvailableCommand `json:"commands"`
}

func (AvailableCommandsEvent) eventType() string { return EventTypeAvailableCommands }

// CurrentModeEvent carries mode changes.
type CurrentModeEvent struct {
	Meta   map[string]any `json:"_meta,omitempty"`
	ModeID string         `json:"modeId"`
}

func (CurrentModeEvent) eventType() string { return EventTypeCurrentMode }

// ConfigOptionEvent carries config option changes.
type ConfigOptionEvent struct {
	Meta          map[string]any `json:"_meta,omitempty"`
	ConfigOptions []ConfigOption `json:"configOptions"`
}

func (ConfigOptionEvent) eventType() string { return EventTypeConfigOption }

// SessionInfoEvent carries session metadata updates.
type SessionInfoEvent struct {
	Meta      map[string]any `json:"_meta,omitempty"`
	Title     *string        `json:"title,omitempty"`
	UpdatedAt *string        `json:"updatedAt,omitempty"`
}

func (SessionInfoEvent) eventType() string { return EventTypeSessionInfo }

// UsageEvent carries token/API usage statistics.
type UsageEvent struct {
	Meta map[string]any `json:"_meta,omitempty"`
	Cost *Cost          `json:"cost,omitempty"`
	Size int            `json:"size"`
	Used int            `json:"used"`
}

func (UsageEvent) eventType() string { return EventTypeUsage }


// StateChangeEvent carries runtime process lifecycle transitions.
// This is a runtime category event, not a session event.
type StateChangeEvent struct {
	PreviousStatus string `json:"previousStatus"`
	Status         string `json:"status"`
	PID            int    `json:"pid,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

func (StateChangeEvent) eventType() string { return EventTypeStateChange }

// EventTypeOf returns the event type string for the given Event.
// This exported accessor allows packages outside pkg/shim/api to retrieve the
// event type without requiring access to the unexported eventType() method.
func EventTypeOf(ev Event) string {
	return ev.eventType()
}
