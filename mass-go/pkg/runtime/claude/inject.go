package claude

import (
	"context"

	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/agentrelay"
)

var _ runtime.InjectDriver = (*Driver)(nil)

func (d *Driver) Inject(ctx context.Context, id string, input runtime.Input) (runtime.InjectReceipt, error) {
	notSent := runtime.InjectReceipt{Acceptance: runtime.Acceptance{Sent: runtime.Bool(false)}}
	if err := validateClaudeInput(input); err != nil {
		return notSent, err
	}
	s, err := d.get(id)
	if err != nil {
		return notSent, err
	}
	s.mu.Lock()
	unavailable := s.closing || s.err != nil
	s.mu.Unlock()
	if unavailable {
		return notSent, runtime.FailedPrecondition("session unavailable")
	}
	injector, ok := s.native.(agent.Injector)
	if !ok {
		return notSent, runtime.UnsupportedOption("inject_unsupported", "Session does not support context injection")
	}
	receipt, err := injector.Inject(ctx, input)
	return runtime.InjectReceipt{Acceptance: agentrelay.MapAcceptance(receipt.Acceptance)}, mapClaudeError(err)
}
