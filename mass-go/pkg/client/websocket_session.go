package client

import (
	"context"
	"unicode/utf8"

	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

type sessionCreateParams struct {
	Input sessionCreateInput `json:"input"`
}
type sessionCreateInput struct {
	Metadata     sessionMetadata      `json:"metadata"`
	Spec         resource.SessionSpec `json:"spec"`
	InitialInput *runtime.Input       `json:"initialInput,omitempty"`
}
type sessionMetadata struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}
type sessionRefParams struct {
	Session resource.SessionRef `json:"session"`
}
type sessionListParams struct {
	Query *sessionQuery `json:"query,omitempty"`
}
type sessionQuery struct {
	Namespace  string       `json:"namespace,omitempty"`
	RuntimeRef string       `json:"runtimeRef,omitempty"`
	State      string       `json:"state,omitempty"`
	Page       *sessionPage `json:"page,omitempty"`
}
type sessionPage struct {
	After string `json:"after,omitempty"`
	Limit int    `json:"limit,omitempty"`
}
type sessionResumeParams struct {
	Session resource.SessionRef `json:"session"`
	Input   *sessionResumeInput `json:"input,omitempty"`
}
type sessionResumeInput struct {
	InitialInput *runtime.Input `json:"initialInput,omitempty"`
}
type sessionLoadParams struct {
	Input sessionLoadInput `json:"input"`
}
type sessionLoadInput struct {
	Metadata   sessionMetadata          `json:"metadata"`
	RuntimeRef string                   `json:"runtimeRef"`
	Cwd        string                   `json:"cwd"`
	SessionID  string                   `json:"sessionId"`
	Features   *runtime.SessionFeatures `json:"features,omitempty"`
	MCPServers *[]runtime.MCPServer     `json:"mcpServers,omitempty"`
}
type sessionCancelLoadParams struct {
	Session resource.SessionRef `json:"session"`
	LoadID  string              `json:"loadId"`
}
type sessionHistoryParams struct {
	Session resource.SessionRef `json:"session"`
	Query   *historyQuery       `json:"query,omitempty"`
}
type historyQuery struct {
	Types []string `json:"types,omitempty"`
	After string   `json:"after,omitempty"`
	Limit int      `json:"limit,omitempty"`
}

// CreateSession creates a Session without sending an initial input.
func (c *WebSocketClient) CreateSession(ctx context.Context, v resource.Session, out any) error {
	return c.call(ctx, "session.create", sessionCreateParams{Input: sessionCreateInput{
		Metadata: sessionMetadata{Namespace: v.Metadata.Namespace, Name: v.Metadata.Name},
		Spec:     v.Spec,
	}}, out)
}

// CreateSessionWithInput sends create-only input and preserves its receipt.
func (c *WebSocketClient) CreateSessionWithInput(ctx context.Context, v resource.SessionCreateInput, out any) error {
	return c.call(ctx, "session.create", sessionCreateParams{Input: sessionCreateInput{
		Metadata:     sessionMetadata{Namespace: v.Metadata.Namespace, Name: v.Metadata.Name},
		Spec:         v.Spec,
		InitialInput: v.InitialInput,
	}}, out)
}

// GetSession resolves one explicit SessionRef without fallback or escaping.
func (c *WebSocketClient) GetSession(ctx context.Context, ref resource.SessionRef, out any) error {
	if err := validateSessionRef(ref); err != nil {
		return err
	}
	return c.call(ctx, "session.get", sessionRefParams{Session: ref}, out)
}

// ListSessions uses the nested page object and returns the same page shape as Service.
func (c *WebSocketClient) ListSessions(ctx context.Context, q resource.SessionQuery, out any) error {
	if q.Limit < 0 || q.SessionID != "" {
		return invalid()
	}
	params := sessionListParams{}
	query := sessionQuery{Namespace: q.Namespace, RuntimeRef: q.RuntimeRef, State: string(q.State)}
	if q.After != "" || q.Limit != 0 {
		query.Page = &sessionPage{After: q.After, Limit: q.Limit}
	}
	if query != (sessionQuery{}) {
		params.Query = &query
	}
	return c.call(ctx, "session.list", params, out)
}

// ResumeSession resumes a Session without sending a new input.
func (c *WebSocketClient) ResumeSession(ctx context.Context, ref resource.SessionRef, out any) error {
	if err := validateSessionRef(ref); err != nil {
		return err
	}
	return c.call(ctx, "session.resume", sessionResumeParams{Session: ref}, out)
}

// ResumeSessionWithInput resumes a Session and preserves the one-use receipt.
func (c *WebSocketClient) ResumeSessionWithInput(ctx context.Context, ref resource.SessionRef, input runtime.Input, out any) error {
	if err := validateSessionRef(ref); err != nil {
		return err
	}
	copied, err := runtime.CopyInput(input)
	if err != nil {
		return err
	}
	return c.call(ctx, "session.resume", sessionResumeParams{
		Session: ref,
		Input:   &sessionResumeInput{InitialInput: &copied},
	}, out)
}

// CloseSession waits for the existing Service close settlement.
func (c *WebSocketClient) CloseSession(ctx context.Context, ref resource.SessionRef, out any) error {
	if err := validateSessionRef(ref); err != nil {
		return err
	}
	return c.call(ctx, "session.close", sessionRefParams{Session: ref}, out)
}

// LoadSession returns only the Service acceptance result.
func (c *WebSocketClient) LoadSession(ctx context.Context, input resource.SessionLoad, out any) error {
	if err := resource.ValidateSessionID(input.SessionID); err != nil {
		return err
	}
	return c.call(ctx, "session.load", sessionLoadParams{Input: sessionLoadInput{
		Metadata:   sessionMetadata{Namespace: input.Metadata.Namespace, Name: input.Metadata.Name},
		RuntimeRef: input.RuntimeRef,
		Cwd:        input.Cwd,
		SessionID:  input.SessionID,
		Features:   input.Features,
		MCPServers: input.MCPServers,
	}}, out)
}

// CancelHistoryLoad cancels the original load identity, never a newer input.
func (c *WebSocketClient) CancelHistoryLoad(ctx context.Context, ref resource.SessionRef, loadID string, out any) error {
	if err := validateSessionRef(ref); err != nil {
		return err
	}
	if loadID == "" || !utf8.ValidString(loadID) {
		return invalid()
	}
	return c.call(ctx, "session.cancelLoad", sessionCancelLoadParams{Session: ref, LoadID: loadID}, out)
}

// History reads committed events only.
func (c *WebSocketClient) History(ctx context.Context, ref resource.SessionRef, q store.HistoryQuery, out any) error {
	if err := validateSessionRef(ref); err != nil {
		return err
	}
	if q.Limit < 0 || q.Types != nil && len(q.Types) == 0 {
		return invalid()
	}
	params := sessionHistoryParams{Session: ref}
	if len(q.Types) != 0 || q.After != "" || q.Limit != 0 {
		params.Query = &historyQuery{Types: q.Types, After: q.After, Limit: q.Limit}
	}
	return c.call(ctx, "session.history", params, out)
}

func validateSessionRef(ref resource.SessionRef) error {
	if ref.SessionID != "" {
		if ref.Namespace != "" || ref.Name != "" || !utf8.ValidString(ref.SessionID) {
			return invalid()
		}
		return nil
	}
	if !validSegment(ref.Namespace) || !validSegment(ref.Name) {
		return invalid()
	}
	return nil
}
