package service

import (
	"context"
	"errors"
	"reflect"

	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/agentrelay"
)

func validateSessionConfig(spec runtime.SessionSpec) error {
	if err := spec.Config.Validate(); err != nil {
		return err
	}
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
	return reflect.DeepEqual(a.Config, b.Config) && reflect.DeepEqual(a.Features, b.Features) && reflect.DeepEqual(a.MCPServers, b.MCPServers)
}

// SetSessionConfig updates only the original live binding, never its durable spec.
func (s *Service) SetSessionConfig(ctx context.Context, ref resource.SessionRef, change runtime.ConfigChange) (runtime.ConfigReceipt, error) {
	notSent := runtime.ConfigReceipt{Change: change, Acceptance: agent.Acceptance{Sent: runtime.Bool(false)}}
	if err := change.Validate(); err != nil {
		return notSent, agentrelay.MapConfigError(notSent, err)
	}
	receipt, err := sessionWork(ctx, s, ref, false, true, func(owner context.Context, run *runBinding, id string) (runtime.ConfigReceipt, error) {
		driver, ok := run.driver.(runtime.SessionConfigDriver)
		if !ok {
			return notSent, runtime.UnsupportedOption("session_config_unsupported", "Driver does not support Session configuration")
		}
		return driver.SetSessionConfig(owner, id, change)
	})
	// Admission and abandoned waits have no SDK receipt. Keep the known request,
	// but never infer sent from local admission. An abandoned wait remains unknown.
	if receipt.Change.ID == "" {
		receipt.Change = change
		var public *runtime.OperationError
		if !errors.As(err, &public) || public.Reason != "work_wait_abandoned" {
			receipt.Acceptance.Sent = runtime.Bool(false)
			if errors.Is(err, context.DeadlineExceeded) {
				err = &runtime.OperationError{Code: "deadline_exceeded", Reason: "config_not_sent", Message: "Configuration deadline elapsed before dispatch"}
			}
		}
	}
	return receipt, err
}
