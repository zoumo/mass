package events

import (
	"encoding/json"
	"errors"
)

type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind" enum:"allow_once,allow_always,reject_once,reject_always"`
}

// InteractionRequest carries the permission, form and URL notification branches.
type InteractionRequest struct {
	Content         []ContentBlock     `json:"content,omitempty"`
	URL             string             `json:"url,omitempty"`
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
	Reason      string `json:"reason,omitempty"`
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
			Content    []ContentBlock     `json:"content,omitempty"`
		}{p.Kind, p.ToolCallID, p.Title, p.Options, p.Content})
	case "url":
		return json.Marshal(struct {
			Kind    string `json:"kind"`
			Message string `json:"message"`
			URL     string `json:"url"`
		}{p.Kind, p.Message, p.URL})
	case "form":
		return json.Marshal(struct {
			Kind            string         `json:"kind"`
			Message         string         `json:"message"`
			RequestedSchema map[string]any `json:"requestedSchema"`
		}{p.Kind, p.Message, p.RequestedSchema})
	}
	return nil, errors.New("unsupported interaction kind")
}

func (p InteractionRequest) Validate() error {
	switch p.Kind {
	case "permission":
		if p.ToolCallID == "" || len(p.Options) == 0 || p.Message != "" || p.RequestedSchema != nil || p.URL != "" {
			return errors.New("invalid permission request")
		}
		for _, o := range p.Options {
			if !oneOf(o.Kind, "allow_once", "allow_always", "reject_once", "reject_always") {
				return errors.New("invalid permission option")
			}
		}
		for _, b := range p.Content {
			if err := b.Validate(); err != nil {
				return err
			}
		}
	case "form":
		if p.Message == "" || p.RequestedSchema == nil || p.ToolCallID != "" || p.Title != "" || p.Options != nil || p.Content != nil || p.URL != "" {
			return errors.New("invalid form request")
		}
		return CheckDynamicJSON(p.RequestedSchema)
	case "url":
		if p.Message == "" || p.URL == "" || p.ToolCallID != "" || p.Title != "" || p.Options != nil || p.Content != nil || p.RequestedSchema != nil {
			return errors.New("invalid URL request")
		}
	default:
		return errors.New("unsupported interaction kind")
	}
	return nil
}
