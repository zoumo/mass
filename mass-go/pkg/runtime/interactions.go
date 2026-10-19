package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
)

type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// InteractionRequest contains the implemented permission/form branches.
type InteractionRequest struct {
	Kind            string             `json:"kind"`
	ToolCallID      string             `json:"toolCallId,omitempty"`
	Title           string             `json:"title,omitempty"`
	Options         []PermissionOption `json:"options,omitempty"`
	Message         string             `json:"message,omitempty"`
	RequestedSchema map[string]any     `json:"requestedSchema,omitempty"`
}
type InteractionRequested struct {
	Interaction string             `json:"interaction"`
	Request     InteractionRequest `json:"request"`
	Blocking    bool               `json:"blocking"`
}

func (InteractionRequested) eventType() string { return "session.interaction_requested" }

type InteractionClosed struct {
	Interaction string `json:"interaction"`
	Outcome     string `json:"outcome"`
}

func (InteractionClosed) eventType() string { return "session.interaction_closed" }

type SessionError struct {
	Message string `json:"message"`
	Reason  string `json:"reason,omitempty"`
}

func (SessionError) eventType() string { return "session.error" }

// PendingInteractions retains native closures, never serializes handles, and
// consumes each reference before dispatch. Unknown writes cannot be retried.
type PendingInteractions struct {
	mu      sync.Mutex
	entries map[string]pendingInteraction
}
type pendingInteraction struct {
	session string
	source  Source
	answer  func(context.Context, map[string]any) error
}

func (p *PendingInteractions) Add(session string, source Source, answer func(context.Context, map[string]any) error) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.entries == nil {
		p.entries = map[string]pendingInteraction{}
	}
	if len(p.entries) >= 128 {
		return "", errors.New("pending interaction limit")
	}
	ref := UUID()
	p.entries[ref] = pendingInteraction{session, source, answer}
	return ref, nil
}
func (p *PendingInteractions) Answer(ctx context.Context, session string, source Source, ref string, answer map[string]any) error {
	p.mu.Lock()
	entry, ok := p.entries[ref]
	if !ok || entry.session != session || !entry.source.SameRun(source) || entry.source.Work() != source.Work() {
		p.mu.Unlock()
		return errors.New("interaction is not pending for this session")
	}
	delete(p.entries, ref)
	p.mu.Unlock()
	return entry.answer(ctx, answer)
}
func (p *PendingInteractions) RemoveSession(session string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for ref, entry := range p.entries {
		if entry.session == session {
			delete(p.entries, ref)
		}
	}
}

func PermissionRequest(tool, title string) InteractionRequest {
	return InteractionRequest{Kind: "permission", ToolCallID: tool, Title: title, Options: []PermissionOption{{"allow-once", "Allow once", "allow_once"}, {"reject-once", "Reject once", "reject_once"}}}
}
func PermissionAllowed(answer map[string]any) (bool, error) {
	if len(answer) != 2 || answer["outcome"] != "selected" {
		return false, errors.New("expected selected permission option")
	}
	switch answer["optionId"] {
	case "allow-once":
		return true, nil
	case "reject-once":
		return false, nil
	}
	return false, errors.New("invalid permission option")
}

func (p InteractionRequest) MarshalJSON() ([]byte, error) {
	switch p.Kind {
	case "permission":
		return json.Marshal(struct {
			Kind       string             `json:"kind"`
			ToolCallID string             `json:"toolCallId"`
			Title      string             `json:"title,omitempty"`
			Options    []PermissionOption `json:"options"`
		}{p.Kind, p.ToolCallID, p.Title, p.Options})
	case "form":
		return json.Marshal(struct {
			Kind            string         `json:"kind"`
			Message         string         `json:"message"`
			RequestedSchema map[string]any `json:"requestedSchema"`
		}{p.Kind, p.Message, p.RequestedSchema})
	}
	return nil, errors.New("unsupported interaction kind")
}
