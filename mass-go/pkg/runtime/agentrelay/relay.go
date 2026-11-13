// Package agentrelay adapts one SDK Session event stream to the MASS envelope.
// It is the session's only SDK Events consumer; ordering, retries and prompt
// admission belong to higher layers.
package agentrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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
	// ExpectedEOF identifies a transport EOF requested by the Driver owner.
	// A nil predicate treats EOF as a normal end of observation.
	ExpectedEOF func() bool
	// ObservationFailure identifies an SDK event that preserves a non-fatal
	// provider session but loses observation coverage required by MASS.
	ObservationFailure func(agent.Event) error
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
	defer r.cancel()
	for {
		event, err := stream.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, io.EOF) {
				if config.ExpectedEOF == nil || config.ExpectedEOF() {
					return
				}
				r.setErr(&runtime.OperationError{
					Code:    string(agent.ErrorOutcomeUnknown),
					Reason:  "session_observation_eof",
					Message: "Agent event stream ended unexpectedly",
				})
				return
			}
			r.setErr(MapError(err))
			return
		}
		if event.SessionID != config.Session.ID() {
			r.setErr(runtime.FailedPrecondition("agent event session identity mismatch"))
			return
		}
		mapped, err := MapEvent(config.Address, config.Source, event)
		if err != nil {
			r.setErr(err)
			return
		}
		var failure error
		if config.ObservationFailure != nil {
			failure = config.ObservationFailure(event)
			if failure != nil {
				// Make the loss visible to concurrent operations before a
				// backpressured sink has accepted the triggering SDK event.
				r.setErr(failure)
			}
		}
		if err := config.Sink(ctx, mapped); err != nil {
			if ctx.Err() != nil {
				return
			}
			r.setErr(err)
			return
		}
		if failure != nil {
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
	if err := event.Validate(); err != nil {
		return events.SessionEvent{}, MapError(err)
	}
	var meta map[string]any
	if event.Meta != nil {
		normalized, err := normalizeJSON(event.Meta)
		if err != nil {
			return events.SessionEvent{}, runtime.InvalidArgument("agent event metadata is not valid JSON")
		}
		meta = normalized.(map[string]any)
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

// normalizeJSON converts the SDK metadata to the MASS dynamic JSON value set.
// Encoding first also validates raw JSON containers; decoding with json.Number
// keeps their original numeric text instead of narrowing it through float64.
func normalizeJSON(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return nil, err
	}
	return normalized, nil
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
	code := string(native.Code)
	switch native.Code {
	case agent.ErrorFailedPrecondition, agent.ErrorClosed:
		// MASS classifies a rejected current-state operation as a public
		// conflict so HTTP keeps its established 409 response.
		code = "conflict"
	}
	mapped := &runtime.OperationError{
		Code:    code,
		Reason:  native.Reason,
		Message: native.Message,
	}
	if native.Cause == nil {
		return mapped
	}
	return &classifiedError{operation: mapped, cause: native.Cause}
}

type classifiedError struct {
	operation *runtime.OperationError
	cause     error
}

func (e *classifiedError) Error() string { return e.operation.Error() }

func (e *classifiedError) Unwrap() []error {
	return []error{e.operation, e.cause}
}

func MapCapabilities(capabilities agent.Capabilities) runtime.Capabilities {
	mapped := make(runtime.Capabilities, len(capabilities))
	for key := range capabilities {
		mapped[key] = capabilities.Supports(key)
	}
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
	case agent.InterruptNoActiveWork:
		result = "noActiveWork"
	case agent.InterruptAcknowledged:
		result = "controlAcknowledged"
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

// StopRejected reports the SDK lifecycle admission failure. Other Stop results
// are terminal and must be retained instead of retrying provider release.
func StopRejected(err error) bool {
	var native *agent.Error
	return errors.As(err, &native) && native.Reason == "provider_has_open_sessions"
}

// MapConfigError preserves the SDK error in the Go chain while keeping unknown
// setter failures uncertain rather than classifying them as retryable.
func MapConfigError(receipt agent.ConfigReceipt, err error) error {
	if err == nil {
		return nil
	}
	var native *agent.Error
	if errors.As(err, &native) && native != nil {
		var public *runtime.OperationError
		if errors.As(MapError(err), &public) {
			// The SDK's explicit NotSent receipt and original deadline cause
			// prove this configuration request did not reach native dispatch.
			// Keep other SDK conclusions, its reason, and the original chain.
			if native.Code == agent.ErrorUnavailable && native.Reason == "set_config_failed" &&
				receipt.Acceptance.Sent != nil && !*receipt.Acceptance.Sent &&
				errors.Is(err, context.DeadlineExceeded) {
				public.Code = "deadline_exceeded"
			}
			return &classifiedError{operation: public, cause: err}
		}
	}
	return &classifiedError{operation: &runtime.OperationError{Code: "outcome_unknown", Reason: "config_operation_unconfirmed", Message: "Configuration result unconfirmed; do not resend"}, cause: err}
}
