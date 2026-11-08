package service

import (
	"reflect"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func validateSessionConfig(spec runtime.SessionSpec) error {
	if spec.MCPServers == nil {
		return nil
	}
	if err := runtime.ValidateMCPServers(*spec.MCPServers); err != nil {
		return runtime.InvalidArgument("invalid session mcpServers: " + err.Error())
	}
	return nil
}

func (s *Service) resolveSessionSpec(run *runBinding, spec runtime.SessionSpec) (runtime.SessionSpec, error) {
	r, err := s.meta.GetRuntime(run.runtimeName)
	if err != nil {
		return spec, err
	}
	defaults := runtime.SessionDefaults{Features: r.Spec.Features, MCPServers: r.Spec.MCPServers}
	if err := defaults.Validate(); err != nil {
		return spec, runtime.InvalidArgument("invalid runtime session defaults: " + err.Error())
	}
	return defaults.ResolveSessionSpec(spec), nil
}

func sameSessionOverrides(a, b runtime.SessionSpec) bool {
	return reflect.DeepEqual(a.Features, b.Features) && reflect.DeepEqual(a.MCPServers, b.MCPServers)
}
