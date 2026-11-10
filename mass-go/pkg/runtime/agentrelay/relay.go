// Package agentrelay adapts one SDK Session event stream to the MASS envelope.
// It is the session's only SDK Events consumer; ordering, retries and prompt
// admission belong to higher layers.
package agentrelay

import (
	"context"
	"errors"
	"io"
	"maps"
	"sync"

	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

type Sink func(context.Context, events.SessionEvent) error

type Config struct {
	Session agent.Session
	Address runtime.Address
	Source  events.Source
	Sink    Sink
}

type Relay struct {
	cancel context.CancelFunc
	done   chan struct{}

	mu  sync.Mutex
	err error
}

// Start claims the Session's one SDK event stream and forwards validated
// events to sink. It does not create another queue or alter SDK identities.
func Start(ctx context.Context, config Config) (*Relay, error) {
	if config.Session == nil ||
		config.Address.Namespace == "" ||
		config.Address.SessionName == "" ||
		config.Sink == nil {
		return nil, runtime.InvalidArgument("invalid agent relay configuration")
	}
	stream, err := config.Session.Events()
	if err != nil {
		return nil, MapError(err)
	}
	if stream == nil {
		return nil, runtime.FailedPrecondition("agent event stream unavailable")
	}
	relayCtx, cancel := context.WithCancel(ctx)
	relay := &Relay{cancel: cancel, done: make(chan struct{})}
	go relay.consume(relayCtx, config, stream)
	return relay, nil
}

func (r *Relay) consume(ctx context.Context, config Config, stream agent.EventStream) {
	defer close(r.done)
	for {
		event, err := stream.Next(ctx)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
				r.setErr(MapError(err))
			}
			return
		}
		mapped, err := MapEvent(config.Address, config.Source, event)
		if err != nil {
			r.setErr(err)
			return
		}
		if err := config.Sink(ctx, mapped); err != nil {
			r.setErr(err)
			return
		}
	}
}

func (r *Relay) Done() <-chan struct{} { return r.done }

func (r *Relay) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *Relay) Close() {
	r.cancel()
}

func (r *Relay) setErr(err error) {
	r.mu.Lock()
	if r.err == nil {
		r.err = err
	}
	r.mu.Unlock()
}

// MapEvent copies the SDK event identity and payload into the MASS envelope
// without regenerating event ID, time, item ID, type, payload, or metadata.
func MapEvent(address runtime.Address, source events.Source, event agent.Event) (events.SessionEvent, error) {
	if event.SessionID == "" {
		return events.SessionEvent{}, runtime.InvalidArgument("agent event missing session ID")
	}
	var meta map[string]any
	if event.Meta != nil {
		meta = maps.Clone(event.Meta)
	}
	mapped, err := events.NewEvent(events.EventBase{
		EventID:     event.EventID,
		Namespace:   address.Namespace,
		SessionName: address.SessionName,
		SessionID:   event.SessionID,
		Time:        event.Time,
		ItemID:      event.ItemID,
		Meta:        meta,
	}, event.Payload, source)
	if err != nil {
		return events.SessionEvent{}, runtime.InvalidArgument(err.Error())
	}
	return mapped, nil
}

// MapError preserves an SDK stable classification in the MASS public error.
func MapError(err error) error {
	if err == nil {
		return nil
	}
	var native *agent.Error
	if !errors.As(err, &native) || native == nil {
		return &runtime.OperationError{
			Code:    string(agent.ErrorUnavailable),
			Reason:  "agent_operation",
			Message: "Agent operation failed",
		}
	}
	return &runtime.OperationError{
		Code:    string(native.Code),
		Reason:  native.Reason,
		Message: native.Message,
	}
}

func MapCapabilities(capabilities agent.Capabilities) runtime.Capabilities {
	mapped := make(runtime.Capabilities, len(capabilities))
	maps.Copy(mapped, capabilities)
	return mapped
}

func MapEffectiveFeatures(features agent.EffectiveFeatures) runtime.SessionFeatures {
	return runtime.SessionFeatures{AgentQuestions: runtime.Bool(features.AgentQuestions)}
}

func MapAcceptance(acceptance agent.Acceptance) runtime.Acceptance {
	return runtime.Acceptance{
		Sent:           copyBool(acceptance.Sent),
		NativeAccepted: copyBool(acceptance.NativeAccepted),
		NativeRecorded: copyBool(acceptance.NativeRecorded),
	}
}

func MapInputReceipt(receipt agent.InputReceipt) runtime.InputReceipt {
	return runtime.InputReceipt{
		Submission: receipt.Submission,
		Result:     receipt.Result,
		Acceptance: MapAcceptance(receipt.Acceptance),
	}
}

func MapInteractionReceipt(receipt agent.InteractionReceipt) runtime.InteractionReceipt {
	return runtime.InteractionReceipt{
		Interaction: string(receipt.Interaction),
		Evidence:    MapAcceptance(receipt.Acceptance),
	}
}

func MapCancelReceipt(receipt agent.InterruptReceipt) runtime.CancelReceipt {
	result := string(receipt.Result)
	switch receipt.Result {
	case agent.InterruptAcknowledged:
		result = "controlAcknowledged"
	case agent.InterruptNoActiveWork:
		result = "noActiveWork"
	}
	return runtime.CancelReceipt{
		Target: receipt.Target,
		Result: result,
	}
}

func copyBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	return runtime.Bool(*value)
}
