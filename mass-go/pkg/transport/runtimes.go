package transport

import (
	"context"
	"net/url"

	"github.com/danielgtaylor/huma/v2"

	"github.com/zoumo/mass/mass-go/pkg/resource"
)

type runtimePath struct {
	Name string `path:"runtime"`
}
type runtimeList struct {
	Type  string `query:"type"`
	State string `query:"state" enum:"stopped,starting,running,stopping,unknown"`
}
type (
	runtimeCreateInput struct{ Body runtimeCreate }
	runtimeUpdateInput struct {
		Name string `path:"runtime"`
		Body resource.RuntimeSpec
	}
)

type (
	runtimeOutput  struct{ Body resource.Runtime }
	runtimeCreated struct {
		Location string `header:"Location"`
		Body     resource.Runtime
	}
)

type (
	runtimeListOutput struct{ Body []resource.Runtime }
	runtimeAction     struct {
		Name string `path:"runtime"`
		Body *struct{}
	}
)
type emptyOutput struct{}

func (s *Server) registerRuntimes(api huma.API) {
	register(api, huma.Operation{Method: "POST", Path: "/apis/v1/runtimes", OperationID: "createRuntime", DefaultStatus: 201}, func(_ context.Context, in *runtimeCreateInput) (*runtimeCreated, error) {
		r, err := s.api.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: in.Body.Metadata.Name, Namespace: in.Body.Metadata.Namespace}, Spec: in.Body.Spec})
		return &runtimeCreated{Location: "/apis/v1/runtimes/" + url.PathEscape(r.Metadata.Name), Body: r}, err
	})
	register(api, huma.Operation{Method: "GET", Path: "/apis/v1/runtimes", OperationID: "listRuntimes"}, func(_ context.Context, in *runtimeList) (*runtimeListOutput, error) {
		r, err := s.api.ListRuntimes(resource.RuntimeQuery{Type: in.Type, State: in.State})
		return &runtimeListOutput{Body: r}, err
	})
	register(api, huma.Operation{Method: "GET", Path: "/apis/v1/runtimes/{runtime}", OperationID: "getRuntime"}, func(_ context.Context, in *runtimePath) (*runtimeOutput, error) {
		r, err := s.api.GetRuntime(in.Name)
		return &runtimeOutput{Body: r}, err
	})
	register(api, huma.Operation{Method: "PUT", Path: "/apis/v1/runtimes/{runtime}", OperationID: "updateRuntime"}, func(_ context.Context, in *runtimeUpdateInput) (*runtimeOutput, error) {
		r, err := s.api.UpdateRuntime(in.Name, in.Body)
		return &runtimeOutput{Body: r}, err
	})
	register(api, huma.Operation{Method: "POST", Path: "/apis/v1/runtimes/{runtime}/start", OperationID: "startRuntime"}, func(ctx context.Context, in *runtimeAction) (*runtimeOutput, error) {
		r, err := s.api.StartRuntime(context.WithoutCancel(ctx), in.Name)
		return &runtimeOutput{Body: r}, err
	})
	register(api, huma.Operation{Method: "POST", Path: "/apis/v1/runtimes/{runtime}/stop", OperationID: "stopRuntime"}, func(ctx context.Context, in *runtimeAction) (*runtimeOutput, error) {
		r, err := s.api.StopRuntime(ctx, in.Name)
		return &runtimeOutput{Body: r}, err
	})
	register(api, huma.Operation{Method: "DELETE", Path: "/apis/v1/runtimes/{runtime}", OperationID: "deleteRuntime", DefaultStatus: 204}, func(_ context.Context, in *runtimePath) (*emptyOutput, error) {
		return nil, s.api.DeleteRuntime(in.Name)
	})
}
