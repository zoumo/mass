package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// Event is a data-only discriminated union. Exactly one payload is present.
// SourcePosition is an adapter receive cursor, not native wire order or Seq.
type Event struct {
	SchemaVersion   int               `json:"schemaVersion"`
	Kind            EventKind         `json:"type"`
	Scope           string            `json:"scope"`
	RuntimeName     string            `json:"runtimeName"`
	SessionID       string            `json:"sessionId,omitempty"`
	ConnectionEpoch string            `json:"connectionEpoch,omitempty"`
	Seq             uint64            `json:"seq"`
	ObservedAt      time.Time         `json:"observedAt"`
	SourcePosition  uint64            `json:"sourcePosition,omitempty"`
	Origin          EventOrigin       `json:"origin"`
	FrameRef        string            `json:"frameRef,omitempty"`
	WorkRef         string            `json:"workRef,omitempty"`
	Message         *MessagePayload   `json:"-"`
	Reasoning       *ReasoningPayload `json:"-"`
	Plan            *PlanPayload      `json:"-"`
	Metadata        *MetadataPayload  `json:"-"`
	Tool            *ToolPayload      `json:"-"`
	Work            *WorkPayload      `json:"-"`
	Session         *SessionSnapshot  `json:"-"`
	Interaction     *Interaction      `json:"-"`
	Error           *SafeError        `json:"-"`
	Diagnostic      Diagnostic        `json:"-"`
}
type EventOrigin struct {
	Kind         string `json:"kind"`
	Attribution  string `json:"attribution"`
	Completeness string `json:"observationCompleteness"`
	Replay       *bool  `json:"replay,omitempty"`
	Synthetic    *bool  `json:"synthetic,omitempty"`
	SourceKind   string `json:"sourceKind,omitempty"`
}
type ContentSet struct {
	Availability       string         `json:"availability"`
	Completeness       string         `json:"completeness"`
	TruncationEvidence string         `json:"truncationEvidence,omitempty"`
	Blocks             []ContentBlock `json:"blocks,omitempty"`
}
type ContentBlock struct {
	BlockRef     string          `json:"blockRef,omitempty"`
	Index        *int            `json:"index,omitempty"`
	Namespace    string          `json:"namespace,omitempty"`
	Kind         string          `json:"kind"`
	Text         *string         `json:"text,omitempty"`
	JSON         json.RawMessage `json:"json,omitempty"`
	Descriptor   string          `json:"descriptor,omitempty"`
	Stage        string          `json:"stage"`
	Completeness string          `json:"completeness"`
}
type MessagePayload struct {
	// BlockIndex and BlockStage carry boundary-only observations without text.
	BlockIndex *int       `json:"blockIndex,omitempty"`
	BlockStage string     `json:"blockStage,omitempty"`
	MessageRef string     `json:"messageRef,omitempty"`
	Role       string     `json:"role"`
	Stage      string     `json:"stage"`
	Content    ContentSet `json:"content"`
}
type ReasoningPayload struct {
	BlockIndex *int   `json:"blockIndex,omitempty"`
	MessageRef string `json:"messageRef,omitempty"`
	Stage      string `json:"stage"`
	// Channel separates native summary and reasoning indexes.
	Channel string     `json:"channel,omitempty"`
	Content ContentSet `json:"content"`
}
type PlanEntry struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}
type PlanPayload struct {
	PlanRef      string      `json:"planRef,omitempty"`
	Explanation  *string     `json:"explanation,omitempty"`
	UpdateMode   string      `json:"updateMode"`
	Entries      []PlanEntry `json:"entries"`
	Completeness string      `json:"completeness"`
	// Text is observed prose when the source supplies no structured checklist.
	Text  *string `json:"text,omitempty"`
	Stage string  `json:"stage,omitempty"`
}

// Each present metadata field replaces its value. Nil means no update;
// a non-nil pointer to an empty slice explicitly clears that collection.
type MetadataPayload struct {
	Capabilities     *[]string `json:"capabilities,omitempty"`
	Commands         *[]string `json:"commands,omitempty"`
	Tools            *[]string `json:"tools,omitempty"`
	Model            *string   `json:"model,omitempty"`
	WorkingDirectory *string   `json:"workingDirectory,omitempty"`
	Title            *string   `json:"title,omitempty"`
}

func (m *MetadataPayload) Apply(p MetadataPayload) {
	if p.Capabilities != nil {
		v := append([]string{}, (*p.Capabilities)...)
		m.Capabilities = &v
	}
	if p.Commands != nil {
		v := append([]string{}, (*p.Commands)...)
		m.Commands = &v
	}
	if p.Tools != nil {
		v := append([]string{}, (*p.Tools)...)
		m.Tools = &v
	}
	if p.Model != nil {
		v := *p.Model
		m.Model = &v
	}
	if p.WorkingDirectory != nil {
		v := *p.WorkingDirectory
		m.WorkingDirectory = &v
	}
	if p.Title != nil {
		v := *p.Title
		m.Title = &v
	}
}

// ToolPayload keeps semantic identity, presentation, data and execution evidence separate.
// Input/Output are snapshots of this observation; missing is not an empty value.
type ToolPayload struct {
	MessageRef   string     `json:"messageRef,omitempty"`
	BlockIndex   *int       `json:"blockIndex,omitempty"`
	CallRef      string     `json:"callRef"`
	Name         string     `json:"name,omitempty"`
	Category     string     `json:"category"`
	Namespace    *string    `json:"namespace,omitempty"`
	DisplayLabel *string    `json:"displayLabel,omitempty"`
	Input        ContentSet `json:"input"`
	// InputJSONDelta is an observed JSON fragment, never a complete JSON value.
	InputJSONDelta  *string        `json:"inputJsonDelta,omitempty"`
	Output          ContentSet     `json:"output"`
	Content         *ContentSet    `json:"content,omitempty"`
	Locations       []ToolLocation `json:"locations,omitempty"`
	State           string         `json:"state"`
	ExitCode        *int32         `json:"exitCode,omitempty"`
	DurationMS      *int64         `json:"durationMs,omitempty"`
	OutcomeEvidence string         `json:"outcomeEvidence"`
}
type ToolLocation struct {
	Path string `json:"path"`
}

// JSONContent preserves JSON numbers and nested nulls without string conversion.
// A null field is unavailable; an explicit JSON null block uses JSONValueContent.
func JSONContent(raw json.RawMessage, stage, completeness string) ContentSet {
	if len(raw) == 0 {
		return ContentSet{Availability: "missing", Completeness: completeness}
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ContentSet{Availability: "null", Completeness: completeness}
	}
	return JSONValueContent(raw, stage, completeness)
}
func JSONValueContent(raw json.RawMessage, stage, completeness string) ContentSet {
	return ContentSet{Availability: "provided", Completeness: completeness, Blocks: []ContentBlock{{Kind: "json", JSON: append(json.RawMessage(nil), raw...), Stage: stage, Completeness: completeness}}}
}

type WorkPayload struct {
	Status string `json:"status"`
}
type SafeError struct {
	Category string `json:"category"`
	Scope    string `json:"scope"`
}

func TextContent(text, stage, completeness string, index *int) ContentSet {
	availability := "provided"
	if text == "" {
		availability = "provided_empty"
	}
	return ContentSet{Availability: availability, Completeness: completeness, Blocks: []ContentBlock{{Kind: "text", Text: &text, Index: index, Stage: stage, Completeness: completeness}}}
}

func (e Event) payload() (any, error) {
	if e.Kind != Unknown && e.Diagnostic != (Diagnostic{}) {
		return nil, fmt.Errorf("event has an extra diagnostic payload")
	}
	values := []any{e.Message, e.Reasoning, e.Plan, e.Metadata, e.Tool, e.Work, e.Session, e.Interaction, e.Error}
	count := 0
	for _, v := range values {
		if !reflect.ValueOf(v).IsNil() {
			count++
		}
	}
	var p any
	switch e.Kind {
	case MessageCompleted, MessageStarted, MessageDelta:
		p = e.Message
	case ReasoningCompleted, ReasoningDelta:
		p = e.Reasoning
	case Plan:
		p = e.Plan
	case Metadata:
		p = e.Metadata
	case ToolAnnounced, ToolUpdated, ToolCompleted:
		p = e.Tool
	case TurnStarted, TurnCompleted:
		p = e.Work
	case SessionReady:
		p = e.Session
	case InteractionRequested:
		p = e.Interaction
	case Failure:
		p = e.Error
	case Unknown:
		if count != 0 {
			return nil, fmt.Errorf("diagnostic has another payload")
		}
		return e.Diagnostic, nil
	default:
		return nil, fmt.Errorf("unsupported event type")
	}
	if count != 1 || reflect.ValueOf(p).IsNil() {
		return nil, fmt.Errorf("event payload does not match type")
	}
	return p, nil
}
func (e Event) validate() error {
	if e.SchemaVersion != 1 || e.Scope != "session" || e.RuntimeName == "" || e.SessionID == "" || e.ConnectionEpoch == "" || e.Seq == 0 || e.Seq > 9007199254740991 || e.ObservedAt.IsZero() {
		return fmt.Errorf("invalid event envelope")
	}
	if e.Origin.Kind != "native" && e.Origin.Kind != "runtime" && e.Origin.Kind != "sdk_derived" {
		return fmt.Errorf("invalid event origin")
	}
	if !oneOf(e.Origin.Attribution, "root", "child", "unresolved", "runtime") || !oneOf(e.Origin.Completeness, "complete", "partial", "unknown") {
		return fmt.Errorf("invalid event provenance")
	}
	if e.Message != nil {
		if string(e.Kind) != "message."+e.Message.Stage {
			return fmt.Errorf("message type and stage disagree")
		}
		if e.Message.BlockIndex != nil && *e.Message.BlockIndex < 0 {
			return fmt.Errorf("invalid block index")
		}
		if e.Message.BlockStage != "" && !oneOf(e.Message.BlockStage, "started", "delta", "completed") {
			return fmt.Errorf("invalid block stage")
		}
		if !oneOf(e.Message.Role, "user", "assistant", "system", "tool", "unknown") || !oneOf(e.Message.Stage, "started", "delta", "completed") {
			return fmt.Errorf("invalid message")
		}
		return validateContent(e.Message.Content)
	}
	if e.Reasoning != nil {
		if e.Reasoning.BlockIndex != nil && *e.Reasoning.BlockIndex < 0 {
			return fmt.Errorf("invalid reasoning block index")
		}
		if !strings.HasSuffix(string(e.Kind), "."+e.Reasoning.Stage) {
			return fmt.Errorf("reasoning type and stage disagree")
		}
		if !oneOf(e.Reasoning.Stage, "delta", "completed") {
			return fmt.Errorf("invalid reasoning")
		}
		return validateContent(e.Reasoning.Content)
	}
	if e.Tool != nil {
		t := e.Tool
		if t.CallRef == "" || !oneOf(t.Category, "execute", "read", "write", "search", "network", "other", "unknown") || !oneOf(t.State, "announced", "running", "completed", "failed", "cancelled", "unknown") || t.OutcomeEvidence == "" {
			return fmt.Errorf("invalid tool")
		}
		if (e.Kind == ToolUpdated && !oneOf(t.State, "announced", "running", "unknown")) || (e.Kind == ToolAnnounced && t.State != "announced") || (e.Kind == ToolCompleted && !oneOf(t.State, "completed", "failed", "cancelled", "unknown")) {
			return fmt.Errorf("invalid tool lifecycle")
		}
		if t.InputJSONDelta != nil && (e.Kind != ToolUpdated || t.Input.Completeness != "partial" || t.Input.Availability != "missing" || len(t.Input.Blocks) > 0) {
			return fmt.Errorf("invalid tool input fragment")
		}
		if t.BlockIndex != nil && *t.BlockIndex < 0 {
			return fmt.Errorf("invalid tool block index")
		}
		if err := validateContent(t.Input); err != nil {
			return err
		}
		if err := validateContent(t.Output); err != nil {
			return err
		}
		if t.Content != nil {
			return validateContent(*t.Content)
		}
	}
	if e.Plan != nil {
		if !oneOf(e.Plan.UpdateMode, "replace", "patch") || !oneOf(e.Plan.Completeness, "complete", "partial", "unknown") {
			return fmt.Errorf("invalid plan")
		}
		for _, p := range e.Plan.Entries {
			if !oneOf(p.Status, "pending", "in_progress", "completed", "unknown") {
				return fmt.Errorf("invalid plan status")
			}
		}
	}
	return nil
}
func oneOf(v string, choices ...string) bool {
	for _, c := range choices {
		if v == c {
			return true
		}
	}
	return false
}
func validateContent(c ContentSet) error {
	if !oneOf(c.Availability, "missing", "null", "provided_empty", "provided", "redacted", "unsupported") || !oneOf(c.Completeness, "complete", "partial", "truncated", "unknown") {
		return fmt.Errorf("invalid content availability")
	}
	if oneOf(c.Availability, "missing", "null") && len(c.Blocks) > 0 {
		return fmt.Errorf("unavailable content has blocks")
	}
	if c.Completeness == "truncated" && c.TruncationEvidence == "" {
		return fmt.Errorf("missing truncation evidence")
	}
	for _, b := range c.Blocks {
		if b.Index != nil && *b.Index < 0 {
			return fmt.Errorf("invalid block index")
		}
		if b.Kind != "json" && len(b.JSON) != 0 {
			return fmt.Errorf("content mismatch")
		}
		if !oneOf(b.Kind, "text", "json", "file_ref", "media_ref", "redacted", "unsupported") || !oneOf(b.Stage, "started", "delta", "completed") || !oneOf(b.Completeness, "complete", "partial", "truncated", "unknown") {
			return fmt.Errorf("invalid content block")
		}
		if b.Kind == "text" && (b.Text == nil || len(b.JSON) != 0) {
			return fmt.Errorf("invalid text block")
		}
		if b.Kind == "json" && (len(b.JSON) == 0 || !json.Valid(b.JSON) || b.Text != nil) {
			return fmt.Errorf("invalid JSON block")
		}
		if b.Kind != "text" && b.Text != nil {
			return fmt.Errorf("content mismatch")
		}
	}
	return nil
}
func (e Event) MarshalJSON() ([]byte, error) {
	p, err := e.payload()
	if err != nil {
		return nil, err
	}
	if err = e.validate(); err != nil {
		return nil, err
	}
	type envelope Event
	return json.Marshal(struct {
		envelope
		Payload any `json:"payload"`
	}{envelope(e), p})
}
func (e *Event) UnmarshalJSON(data []byte) error {
	type envelope Event
	var wire struct {
		envelope
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	candidate := Event(wire.envelope)
	var p any
	switch candidate.Kind {
	case MessageCompleted, MessageStarted, MessageDelta:
		candidate.Message = &MessagePayload{}
		p = candidate.Message
	case ReasoningCompleted, ReasoningDelta:
		candidate.Reasoning = &ReasoningPayload{}
		p = candidate.Reasoning
	case Plan:
		candidate.Plan = &PlanPayload{}
		p = candidate.Plan
	case Metadata:
		candidate.Metadata = &MetadataPayload{}
		p = candidate.Metadata
	case ToolAnnounced, ToolUpdated, ToolCompleted:
		candidate.Tool = &ToolPayload{}
		p = candidate.Tool
	case TurnStarted, TurnCompleted:
		candidate.Work = &WorkPayload{}
		p = candidate.Work
	case SessionReady:
		candidate.Session = &SessionSnapshot{}
		p = candidate.Session
	case InteractionRequested:
		candidate.Interaction = &Interaction{}
		p = candidate.Interaction
	case Failure:
		candidate.Error = &SafeError{}
		p = candidate.Error
	case Unknown:
		p = &candidate.Diagnostic
	default:
		return fmt.Errorf("unsupported event type")
	}
	if len(wire.Payload) == 0 || bytes.Equal(bytes.TrimSpace(wire.Payload), []byte("null")) {
		return fmt.Errorf("missing payload")
	}
	if candidate.Kind == Metadata {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(wire.Payload, &fields); err != nil {
			return err
		}
		for _, value := range fields {
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return fmt.Errorf("null metadata field")
			}
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(wire.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(p); err != nil {
		return err
	}
	if err := candidate.validate(); err != nil {
		return err
	}
	*e = candidate
	return nil
}
