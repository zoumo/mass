package codex

import (
	"context"

	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/agentrelay"
)

var _ runtime.SessionConfigDriver = (*Driver)(nil)

func (d *Driver) SetSessionConfig(ctx context.Context, id string, change runtime.ConfigChange) (runtime.ConfigReceipt, error) {
	receipt := runtime.ConfigReceipt{Change: change, Acceptance: agent.Acceptance{Sent: runtime.Bool(false)}}
	if err := change.Validate(); err != nil {
		return receipt, agentrelay.MapConfigError(receipt, err)
	}
	s, err := d.ordinarySession(id)
	if err != nil {
		return receipt, err
	}
	controller, ok := s.native.(agent.ConfigController)
	if !ok {
		return receipt, runtime.UnsupportedOption("session_config_unsupported", "SDK Session does not support configuration")
	}
	// Capture the SDK Session once. A later resume must not receive this call.
	receipt, err = controller.SetConfig(ctx, change)
	return receipt, agentrelay.MapConfigError(receipt, err)
}
