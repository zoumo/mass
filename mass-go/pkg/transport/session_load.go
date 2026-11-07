package transport

import (
	"context"
	"net/url"

	"github.com/danielgtaylor/huma/v2"

	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

type sessionLoad struct {
	Metadata   sessionCreateMetadata    `json:"metadata"`
	RuntimeRef string                   `json:"runtimeRef" minLength:"1"`
	Cwd        string                   `json:"cwd" minLength:"1"`
	SessionID  string                   `json:"sessionId" minLength:"1"`
	Features   *runtime.SessionFeatures `json:"features,omitempty"`
	MCPServers *[]runtime.MCPServer     `json:"mcpServers,omitempty"`
}

type sessionLoadInput struct{ Body sessionLoad }

type sessionLoadAccepted struct {
	Location string `header:"Location"`
	Body     resource.SessionLoadAccepted
}

type historyLoadCancel struct {
	LoadID string `json:"loadId" minLength:"1"`
}

type cancelHistoryLoadByID struct {
	SessionIDPath
	Body historyLoadCancel
}

type cancelHistoryLoadByNameOrID struct {
	SessionNameOrIDPath
	Body historyLoadCancel
}

type historyLoadOutput struct{ Body resource.HistoryLoadStatus }

func (s *Server) registerSessionLoad(api huma.API) {
	register(api, huma.Operation{Method: "POST", Path: "/apis/v1/sessions/load", OperationID: "loadSession", DefaultStatus: 202}, func(ctx context.Context, in *sessionLoadInput) (*sessionLoadAccepted, error) {
		if err := resource.ValidateSessionID(in.Body.SessionID); err != nil {
			return nil, err
		}
		r, err := s.api.LoadSession(ctx, resource.SessionLoad{
			Metadata:   resource.Metadata{Namespace: in.Body.Metadata.Namespace, Name: in.Body.Metadata.Name},
			RuntimeRef: in.Body.RuntimeRef, Cwd: in.Body.Cwd, SessionID: in.Body.SessionID,
			Features: in.Body.Features, MCPServers: in.Body.MCPServers,
		})
		return &sessionLoadAccepted{Location: "/apis/v1/sessions/" + url.PathEscape(r.Session.Status.SessionID), Body: r}, err
	})
	register(api, huma.Operation{Method: "POST", Path: "/apis/v1/sessions/{sessionId}/history-load/cancel", OperationID: "cancelHistoryLoadByID"}, func(ctx context.Context, in *cancelHistoryLoadByID) (*historyLoadOutput, error) {
		ref, err := in.resolve(s)
		if err != nil {
			return nil, err
		}
		r, err := s.api.CancelHistoryLoad(ctx, ref, in.Body.LoadID)
		return &historyLoadOutput{Body: r}, err
	})
	register(api, huma.Operation{Method: "POST", Path: "/apis/v1/namespaces/{namespace}/sessions/{nameOrID}/history-load/cancel", OperationID: "cancelHistoryLoadByNameOrID"}, func(ctx context.Context, in *cancelHistoryLoadByNameOrID) (*historyLoadOutput, error) {
		ref, err := in.resolve(s)
		if err != nil {
			return nil, err
		}
		r, err := s.api.CancelHistoryLoad(ctx, ref, in.Body.LoadID)
		return &historyLoadOutput{Body: r}, err
	})
}
