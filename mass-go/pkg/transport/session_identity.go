package transport

import (
	"context"
	"github.com/danielgtaylor/huma/v2"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"net/url"
)

type sessionCreateInput struct{ Body sessionCreate }
type sessionOutput struct{ Body resource.Session }
type sessionCreated struct {
	Location string `header:"Location"`
	Body     resource.Session
}
type SessionIDPath struct {
	SessionID string `path:"sessionId"`
}
type SessionNameOrIDPath struct {
	Namespace string `path:"namespace"`
	NameOrID  string `path:"nameOrID"`
}
type sessionListInput struct {
	Namespace  string              `query:"namespace"`
	RuntimeRef string              `query:"runtimeRef"`
	State      events.SessionState `query:"state" enum:"offline,opening,idle,running,requires_action,closing,archived,unknown"`
	After      string              `query:"after"`
	Limit      int                 `query:"limit" minimum:"1" default:"100"`
}
type sessionListOutput struct{ Body resource.SessionPage }

func (s *Server) registerSessionIdentity(api huma.API) {
	register(api, huma.Operation{Method: "POST", Path: "/apis/v1/sessions", OperationID: "createSession", DefaultStatus: 201}, func(ctx context.Context, in *sessionCreateInput) (*sessionCreated, error) {
		r, err := s.api.CreateSession(context.WithoutCancel(ctx), resource.Session{Metadata: resource.Metadata{Namespace: in.Body.Metadata.Namespace, Name: in.Body.Metadata.Name}, Spec: in.Body.Spec})
		return &sessionCreated{Location: "/apis/v1/sessions/" + url.PathEscape(r.Status.SessionID), Body: r}, err
	})
	register(api, huma.Operation{Method: "GET", Path: "/apis/v1/sessions", OperationID: "listSessions"}, func(_ context.Context, in *sessionListInput) (*sessionListOutput, error) {
		r, err := s.api.ListSessions(resource.SessionQuery{Namespace: in.Namespace, RuntimeRef: in.RuntimeRef, State: in.State, After: in.After, Limit: in.Limit})
		return &sessionListOutput{Body: r}, err
	})
	register(api, huma.Operation{Method: "GET", Path: "/apis/v1/sessions/{sessionId}", OperationID: "getSessionByID"}, func(_ context.Context, in *SessionIDPath) (*sessionOutput, error) {
		if err := resource.ValidateSessionID(in.SessionID); err != nil {
			return nil, err
		}
		r, err := s.api.GetSession(resource.SessionRef{SessionID: in.SessionID})
		return &sessionOutput{Body: r}, err
	})
	register(api, huma.Operation{Method: "GET", Path: "/apis/v1/namespaces/{namespace}/sessions/{nameOrID}", OperationID: "getSessionByNameOrID"}, func(_ context.Context, in *SessionNameOrIDPath) (*sessionOutput, error) {
		ref, err := s.api.ResolveSession(in.Namespace, in.NameOrID)
		if err != nil {
			return nil, err
		}
		r, err := s.api.GetSession(ref)
		return &sessionOutput{Body: r}, err
	})
}
