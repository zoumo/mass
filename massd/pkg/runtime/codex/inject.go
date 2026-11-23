package codex

import (
	"context"

	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/massd/pkg/runtime"
	"github.com/zoumo/mass/massd/pkg/runtime/agentrelay"
)

var _ runtime.InjectDriver = (*Driver)(nil)

func (d *Driver) Inject(ctx context.Context, id string, input runtime.Input) (runtime.InjectReceipt, error) {
	notSent := runtime.InjectReceipt{Acceptance: runtime.Acceptance{Sent: runtime.Bool(false)}}
	if err := runtime.ValidateInput(input); err != nil {
		return notSent, err
	}
	s, err := d.ordinarySession(id)
	if err != nil {
		return notSent, err
	}
	injector, ok := s.native.(agent.Injector)
	if !ok {
		return notSent, runtime.UnsupportedOption("inject_unsupported", "Session does not support context injection")
	}
	receipt, err := injector.Inject(ctx, input)
	return runtime.InjectReceipt{Acceptance: agentrelay.MapAcceptance(receipt.Acceptance)}, agentrelay.MapError(err)
}
