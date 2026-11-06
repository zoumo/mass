package transport

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/zoumo/mass/mass-go/pkg/resource"
)

// Close and archive accept no business input. A pointer makes the empty JSON
// object optional while strict decoding rejects null and undeclared fields.
type lifecycleByID struct {
	SessionIDPath
	Body *struct{}
}
type lifecycleByNameOrID struct {
	SessionNameOrIDPath
	Body *struct{}
}

type resumeByID struct {
	SessionIDPath
	Body *resource.SessionResume
}
type resumeByNameOrID struct {
	SessionNameOrIDPath
	Body *resource.SessionResume
}
type sessionResumed struct{ Body resource.SessionResumed }

func (s *Server) resume(ctx context.Context, ref resource.SessionRef, input *resource.SessionResume) (*sessionResumed, error) {
	var result resource.SessionResumed
	var err error
	if input == nil || input.InitialInput == nil {
		result.Session, err = s.api.ResumeSession(ctx, ref)
	} else {
		result, err = s.api.ResumeSessionWithInput(ctx, ref, *input.InitialInput)
	}
	return &sessionResumed{Body: result}, err
}

func (s *Server) registerSessionRecovery(api huma.API) {
	register(api, huma.Operation{Method: "POST", Path: "/apis/v1/sessions/{sessionId}/close", OperationID: "closeSessionByID"}, func(ctx context.Context, in *lifecycleByID) (*sessionOutput, error) {
		ref, err := in.resolve(s)
		if err != nil {
			return nil, err
		}
		r, err := s.api.CloseSession(ctx, ref)
		return &sessionOutput{Body: r}, err
	})
	register(api, huma.Operation{Method: "POST", Path: "/apis/v1/namespaces/{namespace}/sessions/{nameOrID}/close", OperationID: "closeSessionByNameOrID"}, func(ctx context.Context, in *lifecycleByNameOrID) (*sessionOutput, error) {
		ref, err := in.resolve(s)
		if err != nil {
			return nil, err
		}
		r, err := s.api.CloseSession(ctx, ref)
		return &sessionOutput{Body: r}, err
	})
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
	register(api, huma.Operation{Method: "POST", Path: "/apis/v1/sessions/{sessionId}/resume", OperationID: "resumeSessionByID"}, func(ctx context.Context, in *resumeByID) (*sessionResumed, error) {
		ref, err := in.resolve(s)
		if err != nil {
			return nil, err
		}
		return s.resume(ctx, ref, in.Body)
	})
	register(api, huma.Operation{Method: "POST", Path: "/apis/v1/namespaces/{namespace}/sessions/{nameOrID}/resume", OperationID: "resumeSessionByNameOrID"}, func(ctx context.Context, in *resumeByNameOrID) (*sessionResumed, error) {
		ref, err := in.resolve(s)
		if err != nil {
			return nil, err
		}
		return s.resume(ctx, ref, in.Body)
	})
}
