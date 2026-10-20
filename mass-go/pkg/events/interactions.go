package events

import (
	"encoding/json"
	"errors"
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

func PermissionRequest(tool, title string) InteractionRequest {
	return InteractionRequest{Kind: "permission", ToolCallID: tool, Title: title, Options: []PermissionOption{{"allow-once", "Allow once", "allow_once"}, {"reject-once", "Reject once", "reject_once"}}}
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
