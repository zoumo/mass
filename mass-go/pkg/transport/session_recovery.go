package transport

import (
	"context"

	"github.com/danielgtaylor/huma/v2"
)

// Lifecycle actions accept no business input. A pointer makes the empty JSON
// object optional while strict decoding rejects null and undeclared fields.
type lifecycleByID struct {
	SessionIDPath
	Body *struct{}
}
type lifecycleByNameOrID struct {
	SessionNameOrIDPath
	Body *struct{}
}

func (s *Server) registerSessionRecovery(api huma.API) {
	register(api, huma.Operation{Method: "POST", Path: "/apis/v1/sessions/{sessionId}/archive", OperationID: "archiveSessionByID"}, func(ctx context.Context, in *lifecycleByID) (*sessionOutput, error) {
		ref, err := in.resolve(s)
		if err != nil {
			return nil, err
		}
		r, err := s.api.ArchiveSession(ctx, ref)
		return &sessionOutput{Body: r}, err
	})
	register(api, huma.Operation{Method: "POST", Path: "/apis/v1/namespaces/{namespace}/sessions/{nameOrID}/archive", OperationID: "archiveSessionByNameOrID"}, func(ctx context.Context, in *lifecycleByNameOrID) (*sessionOutput, error) {
		ref, err := in.resolve(s)
		if err != nil {
			return nil, err
		}
		r, err := s.api.ArchiveSession(ctx, ref)
		return &sessionOutput{Body: r}, err
	})
	register(api, huma.Operation{Method: "POST", Path: "/apis/v1/sessions/{sessionId}/resume", OperationID: "resumeSessionByID"}, func(ctx context.Context, in *lifecycleByID) (*sessionOutput, error) {
		ref, err := in.resolve(s)
		if err != nil {
			return nil, err
		}
		r, err := s.api.ResumeSession(ctx, ref)
		return &sessionOutput{Body: r}, err
	})
	register(api, huma.Operation{Method: "POST", Path: "/apis/v1/namespaces/{namespace}/sessions/{nameOrID}/resume", OperationID: "resumeSessionByNameOrID"}, func(ctx context.Context, in *lifecycleByNameOrID) (*sessionOutput, error) {
		ref, err := in.resolve(s)
		if err != nil {
			return nil, err
		}
		r, err := s.api.ResumeSession(ctx, ref)
		return &sessionOutput{Body: r}, err
	})
}
