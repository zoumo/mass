package service

import (
	"errors"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

func (s *Service) CreateRuntime(r resource.Runtime) (resource.Runtime, error) {
	if r.Metadata.UID != "" || !r.Metadata.CreatedAt.IsZero() || r.Metadata.Generation != 0 || r.Metadata.ResourceVersion != "" || r.Status.State != "" || r.Status.AppliedGeneration != 0 || r.Status.Capabilities != nil {
		return resource.Runtime{}, runtime.InvalidArgument("system fields are read-only")
	}
	if err := validateMetadata(r.Metadata, false); err != nil {
		return r, err
	}
	if err := s.validateRuntimeSpec(r.Spec); err != nil {
		return r, err
	}
	r.Status = resource.RuntimeStatus{State: "stopped"}
	return s.meta.CreateRuntime(r)
}
func (s *Service) GetRuntime(name string) (resource.Runtime, error) {
	if !validName(name) {
		return resource.Runtime{}, runtime.InvalidArgument("invalid runtime name")
	}
	return s.meta.GetRuntime(name)
}
func (s *Service) UpdateRuntime(name string, spec resource.RuntimeSpec) (resource.Runtime, error) {
	if !validName(name) {
		return resource.Runtime{}, runtime.InvalidArgument("invalid runtime name")
	}
	if err := s.validateRuntimeSpec(spec); err != nil {
		return resource.Runtime{}, err
	}
	for {
		r, err := s.meta.GetRuntime(name)
		if err != nil {
			return r, err
		}
		r.Spec = spec
		r, err = s.meta.UpdateRuntime(r)
		if !errors.Is(err, store.ErrConflict) {
			return r, err
		}
	}
}

func (s *Service) ListRuntimes(q resource.RuntimeQuery) ([]resource.Runtime, error) {
	switch q.State {
	case "", "stopped", "starting", "running", "stopping", "unknown":
	default:
		return nil, runtime.InvalidArgument("invalid runtime state")
	}
	return s.meta.ListRuntimes(q)
}

// Validate the public JSON value before any type-specific factory or Store call.
func (s *Service) validateRuntimeSpec(spec resource.RuntimeSpec) error {
	if spec.Config == nil {
		return runtime.InvalidArgument("config must be a JSON object")
	}
	if err := events.CheckDynamicJSON(map[string]any(spec.Config)); err != nil {
		return runtime.InvalidArgument("config must contain JSON values")
	}
	return s.registry.ValidateConfig(spec.Type, spec.Config)
}
