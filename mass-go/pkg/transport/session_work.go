package transport

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

type inputByID struct {
	SessionIDPath
	Body runtime.Input
}
type inputByNameOrID struct {
	SessionNameOrIDPath
	Body runtime.Input
}
type cancelByID struct {
	SessionIDPath
	Body *struct{}
}
type cancelByNameOrID struct {
	SessionNameOrIDPath
	Body *struct{}
}
type answerByID struct {
	SessionIDPath
	Interaction64 string `path:"interaction64" minLength:"1" pattern:"^[A-Za-z0-9_-]+$"`
	Body          any
}
type answerByNameOrID struct {
	SessionNameOrIDPath
	Interaction64 string `path:"interaction64" minLength:"1" pattern:"^[A-Za-z0-9_-]+$"`
	Body          any
}
type (
	inputOutput  struct{ Body runtime.InputReceipt }
	answerOutput struct{ Body runtime.InteractionReceipt }
	cancelOutput struct{ Body runtime.CancelReceipt }
)

func (p SessionIDPath) resolve(_ *Server) (resource.SessionRef, error) {
	if err := resource.ValidateSessionID(p.SessionID); err != nil {
		return resource.SessionRef{}, err
	}
	return resource.SessionRef{SessionID: p.SessionID}, nil
}

func (p SessionNameOrIDPath) resolve(s *Server) (resource.SessionRef, error) {
	return s.api.ResolveSession(p.Namespace, p.NameOrID)
}

func (s *Server) registerSessionWork(api huma.API) {
	register(api, huma.Operation{Method: "POST", Path: "/apis/v1/sessions/{sessionId}/inputs", OperationID: "sendPromptByID"}, func(ctx context.Context, in *inputByID) (*inputOutput, error) {
		ref, err := in.resolve(s)
		if err != nil {
			return nil, err
		}
		v, err := s.api.SendPrompt(ctx, ref, in.Body)
		return &inputOutput{Body: v}, err
	})
	register(api, huma.Operation{Method: "POST", Path: "/apis/v1/namespaces/{namespace}/sessions/{nameOrID}/inputs", OperationID: "sendPromptByNameOrID"}, func(ctx context.Context, in *inputByNameOrID) (*inputOutput, error) {
		ref, err := in.resolve(s)
		if err != nil {
			return nil, err
		}
		v, err := s.api.SendPrompt(ctx, ref, in.Body)
		return &inputOutput{Body: v}, err
	})
	register(api, huma.Operation{Method: "POST", Path: "/apis/v1/sessions/{sessionId}/cancel", OperationID: "cancelByID"}, func(ctx context.Context, in *cancelByID) (*cancelOutput, error) {
		ref, err := in.resolve(s)
		if err != nil {
			return nil, err
		}
		v, err := s.api.Cancel(ctx, ref)
		return &cancelOutput{Body: v}, err
	})
	register(api, huma.Operation{Method: "POST", Path: "/apis/v1/namespaces/{namespace}/sessions/{nameOrID}/cancel", OperationID: "cancelByNameOrID"}, func(ctx context.Context, in *cancelByNameOrID) (*cancelOutput, error) {
		ref, err := in.resolve(s)
		if err != nil {
			return nil, err
		}
		v, err := s.api.Cancel(ctx, ref)
		return &cancelOutput{Body: v}, err
	})
	register(api, huma.Operation{Method: "POST", Path: "/apis/v1/sessions/{sessionId}/interactions/{interaction64}/answer", OperationID: "answerInteractionByID", RequestBody: &huma.RequestBody{Required: true}}, func(ctx context.Context, in *answerByID) (*answerOutput, error) {
		ref, err := in.resolve(s)
		if err != nil {
			return nil, err
		}
		return s.answer(ctx, ref, in.Interaction64, in.Body)
	})
	register(api, huma.Operation{Method: "POST", Path: "/apis/v1/namespaces/{namespace}/sessions/{nameOrID}/interactions/{interaction64}/answer", OperationID: "answerInteractionByNameOrID", RequestBody: &huma.RequestBody{Required: true}}, func(ctx context.Context, in *answerByNameOrID) (*answerOutput, error) {
		ref, err := in.resolve(s)
		if err != nil {
			return nil, err
		}
		return s.answer(ctx, ref, in.Interaction64, in.Body)
	})
}

func (s *Server) answer(ctx context.Context, ref resource.SessionRef, encoded string, body any) (*answerOutput, error) {
	interaction, err := decodeRef(encoded)
	if err != nil {
		return nil, err
	}
	receipt, err := s.api.AnswerInteraction(ctx, ref, interaction, body)
	return &answerOutput{Body: receipt}, err
}
