package agentrelay

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestMapEventPreservesSDKIdentityAndMeta(t *testing.T) {
	eventID := "01234567-89ab-4cde-8fab-0123456789ab"
	at := time.Date(2026, 11, 10, 1, 2, 3, 4, time.UTC)
	input := agent.AgentMessage{
		MessageID: "message-1",
		Content:   []agent.ContentBlock{agent.TextBlock{Text: "answer"}},
	}
	source := events.NewSource().WithWork("work-1")
	mapped, err := MapEvent(
		runtime.Address{Namespace: "one", SessionName: "demo"},
		source,
		agent.Event{
			EventID:   eventID,
			SessionID: "native-1",
			Time:      at,
			ItemID:    "item-1",
			Payload:   input,
			Meta:      map[string]any{"native": map[string]any{"id": "raw-1"}},
		},
	)
	require.NoError(t, err)
	require.Equal(t, "one", mapped.Namespace)
	require.Equal(t, "demo", mapped.SessionName)
	require.Equal(t, eventID, mapped.EventID)
	require.Equal(t, "native-1", mapped.SessionID)
	require.Equal(t, "item-1", mapped.ItemID)
	require.Equal(t, at, mapped.Time)
	require.Equal(t, string(agent.EventAgentMessage), mapped.Type())
	require.Equal(t, input, mapped.Payload())
	require.Equal(t, map[string]any{"native": map[string]any{"id": "raw-1"}}, mapped.Meta)
	require.True(t, source.SameRun(mapped.Source()))
	require.Equal(t, "work-1", mapped.Source().Work())
}

func TestMapEventNormalizesRawJSONWithoutLosingValues(t *testing.T) {
	eventID := "01234567-89ab-4cde-8fab-0123456789ab"
	at := time.Date(2026, 11, 10, 1, 2, 3, 4, time.UTC)
	raw := json.RawMessage(`{"big":9007199254740993,"decimal":0.1000,"nested":{"array":[null,true,"kept"]}}`)
	input := agent.AgentMessage{
		MessageID: "message-1",
		Content:   []agent.ContentBlock{agent.TextBlock{Text: "answer"}},
	}
	mapped, err := MapEvent(
		runtime.Address{Namespace: "one", SessionName: "demo"},
		events.NewSource(),
		agent.Event{
			EventID:   eventID,
			SessionID: "native-1",
			Time:      at,
			ItemID:    "item-1",
			Payload:   input,
			Meta: map[string]any{
				"raw": raw,
				"codex": map[string]any{
					"raw":       raw,
					"requestId": json.RawMessage(`42`),
					"empty":     json.RawMessage(nil),
				},
			},
		},
	)
	require.NoError(t, err)
	require.Equal(t, eventID, mapped.EventID)
	require.Equal(t, "native-1", mapped.SessionID)
	require.Equal(t, at, mapped.Time)
	require.Equal(t, "item-1", mapped.ItemID)
	require.Equal(t, string(agent.EventAgentMessage), mapped.Type())
	require.Equal(t, input, mapped.Payload())

	decoded := map[string]any{
		"big":     json.Number("9007199254740993"),
		"decimal": json.Number("0.1000"),
		"nested": map[string]any{
			"array": []any{nil, true, "kept"},
		},
	}
	require.Equal(t, map[string]any{
		"raw": decoded,
		"codex": map[string]any{
			"raw":       decoded,
			"requestId": json.Number("42"),
			"empty":     nil,
		},
	}, mapped.Meta)

	encoded, err := json.Marshal(mapped)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"big":9007199254740993`)
	require.Contains(t, string(encoded), `"decimal":0.1000`)
	require.Contains(t, string(encoded), `"requestId":42`)
	require.Contains(t, string(encoded), `"nested":{"array":[null,true,"kept"]}`)
}

func TestNormalizeJSONCoversRawContainersAndRejectsInvalidValues(t *testing.T) {
	for name, tc := range map[string]struct {
		input any
		want  any
	}{
		"empty raw": {
			input: json.RawMessage(nil),
			want:  nil,
		},
		"raw null": {
			input: json.RawMessage(`null`),
			want:  nil,
		},
		"raw array": {
			input: json.RawMessage(`[null,true,{"number":9007199254740993}]`),
			want:  []any{nil, true, map[string]any{"number": json.Number("9007199254740993")}},
		},
		"nested raw": {
			input: map[string]any{
				"value": json.RawMessage(`{"decimal":0.1000}`),
			},
			want: map[string]any{
				"value": map[string]any{"decimal": json.Number("0.1000")},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := normalizeJSON(tc.input)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}

	for name, input := range map[string]json.RawMessage{
		"invalid":  json.RawMessage(`{"unterminated":`),
		"trailing": json.RawMessage(`{"one":1} {"two":2}`),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := normalizeJSON(map[string]any{"raw": input})
			require.Error(t, err)
		})
	}
}

func TestRelayClaimsSingleStreamAndForwards(t *testing.T) {
	stream := &fakeStream{events: []agent.Event{testEvent(t)}}
	session := &fakeSession{stream: stream}
	received := make(chan events.SessionEvent, 1)
	relay, err := Start(context.Background(), Config{
		Session: session,
		Address: runtime.Address{Namespace: "one", SessionName: "demo"},
		Source:  events.NewSource(),
		Sink: func(_ context.Context, event events.SessionEvent) error {
			received <- event
			return nil
		},
	})
	require.NoError(t, err)
	select {
	case event := <-received:
		require.Equal(t, testEvent(t).EventID, event.EventID)
	case <-time.After(time.Second):
		t.Fatal("relay did not forward event")
	}
	<-relay.Done()
	require.Equal(t, int32(1), session.calls.Load())
	require.NoError(t, relay.Err())
}

func TestRelayStartsOnceAndMapsStreamErrors(t *testing.T) {
	stream := &fakeStream{err: &agent.Error{
		Code:    agent.ErrorOutcomeUnknown,
		Reason:  "transport_lost",
		Message: "native outcome unknown",
	}}
	session := &fakeSession{stream: stream}
	relay, err := Start(context.Background(), Config{
		Session: session,
		Address: runtime.Address{Namespace: "one", SessionName: "demo"},
		Source:  events.NewSource(),
		Sink:    func(context.Context, events.SessionEvent) error { return nil },
	})
	require.NoError(t, err)
	<-relay.Done()
	var operation *runtime.OperationError
	require.ErrorAs(t, relay.Err(), &operation)
	require.Equal(t, "outcome_unknown", operation.Code)
	require.Equal(t, "transport_lost", operation.Reason)
	_, secondErr := Start(context.Background(), Config{
		Session: session,
		Address: runtime.Address{Namespace: "one", SessionName: "demo"},
		Source:  events.NewSource(),
		Sink:    func(context.Context, events.SessionEvent) error { return nil },
	})
	require.Error(t, secondErr)
	require.Equal(t, int32(2), session.calls.Load())
}

func TestRelayStreamFailureDoesNotPublishSyntheticEvents(t *testing.T) {
	terminal := &agent.Error{
		Code:    agent.ErrorOutcomeUnknown,
		Reason:  "transport_lost",
		Message: "native outcome unknown",
	}
	received := make(chan events.SessionEvent, 1)
	relay, err := Start(context.Background(), Config{
		Session: &fakeSession{stream: &fakeStream{err: terminal}},
		Address: runtime.Address{Namespace: "one", SessionName: "demo"},
		Source:  events.NewSource(),
		Sink: func(_ context.Context, event events.SessionEvent) error {
			received <- event
			return nil
		},
	})
	require.NoError(t, err)
	select {
	case <-relay.Done():
	case <-time.After(time.Second):
		t.Fatal("relay did not stop after terminal failure")
	}
	select {
	case event := <-received:
		t.Fatalf("unexpected synthetic event: %s", event.Type())
	default:
	}

	var operation *runtime.OperationError
	require.ErrorAs(t, relay.Err(), &operation)
	require.Equal(t, "outcome_unknown", operation.Code)
}

func TestRelayUnexpectedEOFIsTerminal(t *testing.T) {
	received := make(chan events.SessionEvent, 2)
	relay, err := Start(context.Background(), Config{
		Session:     &fakeSession{stream: &fakeStream{}},
		Address:     runtime.Address{Namespace: "one", SessionName: "demo"},
		Source:      events.NewSource(),
		ExpectedEOF: func() bool { return false },
		Sink: func(_ context.Context, event events.SessionEvent) error {
			received <- event
			return nil
		},
	})
	require.NoError(t, err)
	select {
	case <-relay.Done():
	case <-time.After(time.Second):
		t.Fatal("relay did not stop after unexpected EOF")
	}
	select {
	case event := <-received:
		t.Fatalf("unexpected synthetic event: %s", event.Type())
	default:
	}
	var operation *runtime.OperationError
	require.ErrorAs(t, relay.Err(), &operation)
	require.Equal(t, "outcome_unknown", operation.Code)
	require.Equal(t, "session_observation_eof", operation.Reason)
}

func TestRelayObservationFailureForwardsOnlyGenuineEvent(t *testing.T) {
	problem := agent.Event{
		EventID:   "01234567-89ab-4cde-8fab-0123456789ab",
		SessionID: "native-1",
		Time:      time.Date(2026, 11, 10, 1, 2, 3, 0, time.UTC),
		Payload:   agent.SessionErrorPayload{Message: "unsupported item", Reason: "unsupported_native_item"},
	}
	received := make(chan events.SessionEvent, 2)
	relay, err := Start(context.Background(), Config{
		Session:     &fakeSession{stream: &fakeStream{events: []agent.Event{problem}}},
		Address:     runtime.Address{Namespace: "one", SessionName: "demo"},
		Source:      events.NewSource(),
		ExpectedEOF: func() bool { return true },
		ObservationFailure: func(event agent.Event) error {
			payload := event.Payload.(agent.SessionErrorPayload)
			if payload.Reason != "unsupported_native_item" {
				return nil
			}
			return &runtime.OperationError{
				Code:    "unavailable",
				Reason:  payload.Reason,
				Message: payload.Message,
			}
		},
		Sink: func(_ context.Context, event events.SessionEvent) error {
			received <- event
			return nil
		},
	})
	require.NoError(t, err)
	select {
	case <-relay.Done():
	case <-time.After(time.Second):
		t.Fatal("relay did not stop after observation failure")
	}

	first := <-received
	require.Equal(t, "unsupported_native_item", failureReason(t, first.Payload()))
	select {
	case event := <-received:
		t.Fatalf("unexpected duplicate terminal event: %s", event.Type())
	default:
	}
	require.Error(t, relay.Err())
}

func TestRelayCloseUnblocksSinkWithRelayContext(t *testing.T) {
	entered := make(chan struct{})
	relay, err := Start(context.Background(), Config{
		Session: &fakeSession{stream: &fakeStream{events: []agent.Event{testEvent(t)}}},
		Address: runtime.Address{Namespace: "one", SessionName: "demo"},
		Source:  events.NewSource(),
		Sink: func(ctx context.Context, _ events.SessionEvent) error {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		},
	})
	require.NoError(t, err)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("relay never entered sink")
	}
	relay.Close()
	select {
	case <-relay.Done():
	case <-time.After(time.Second):
		t.Fatal("relay close did not unblock sink")
	}
	require.NoError(t, relay.Err())
}

func TestRelayCloseStopsWithoutReportingAbnormalTermination(t *testing.T) {
	session := &fakeSession{stream: &contextStream{}}
	relay, err := Start(context.Background(), Config{
		Session: session,
		Address: runtime.Address{Namespace: "one", SessionName: "demo"},
		Source:  events.NewSource(),
		Sink:    func(context.Context, events.SessionEvent) error { return nil },
	})
	require.NoError(t, err)
	relay.Close()
	select {
	case <-relay.Done():
	case <-time.After(time.Second):
		t.Fatal("relay did not stop after close")
	}
	require.NoError(t, relay.Err())
	require.Equal(t, int32(1), session.calls.Load())
}

func TestRelayReportsProviderCancellationAfterRelayStaysLive(t *testing.T) {
	session := &fakeSession{stream: &fakeStream{err: context.Canceled}}
	relay, err := Start(context.Background(), Config{
		Session: session,
		Address: runtime.Address{Namespace: "one", SessionName: "demo"},
		Source:  events.NewSource(),
		Sink:    func(context.Context, events.SessionEvent) error { return nil },
	})
	require.NoError(t, err)
	select {
	case <-relay.Done():
	case <-time.After(time.Second):
		t.Fatal("relay did not stop after provider cancellation")
	}
	require.Error(t, relay.Err())
}

func TestRelayDoesNotSynthesizeOnNormalEOF(t *testing.T) {
	received := make(chan events.SessionEvent, 1)
	relay, err := Start(context.Background(), Config{
		Session: &fakeSession{stream: &fakeStream{}},
		Address: runtime.Address{Namespace: "one", SessionName: "demo"},
		Source:  events.NewSource(),
		Sink: func(_ context.Context, event events.SessionEvent) error {
			received <- event
			return nil
		},
	})
	require.NoError(t, err)
	select {
	case <-relay.Done():
	case <-time.After(time.Second):
		t.Fatal("relay did not stop after EOF")
	}
	require.NoError(t, relay.Err())
	select {
	case event := <-received:
		t.Fatalf("unexpected event on normal EOF: %s", event.Type())
	default:
	}
}

func failureReason(t *testing.T, payload events.Payload) string {
	t.Helper()
	failure, ok := payload.(events.SessionError)
	require.True(t, ok)
	return failure.Reason
}

func TestReceiptCapabilityAndErrorMapping(t *testing.T) {
	yes := true
	receipt := MapInputReceipt(agent.InputReceipt{
		Submission: "submission-1",
		Result:     agent.InputAppliedCurrentWork,
		Acceptance: agent.Acceptance{Sent: &yes},
	})
	require.Equal(t, agent.InputAppliedCurrentWork, receipt.Result)
	require.NotSame(t, &yes, receipt.Acceptance.Sent)
	require.True(t, *receipt.Acceptance.Sent)
	require.Equal(t, runtime.Capabilities{"textInput": true}, MapCapabilities(agent.Capabilities{"textInput": true}))
	require.True(t, MapEffectiveFeatures(agent.EffectiveFeatures{AgentQuestions: true}).AgentQuestionsEnabled())

	native := &agent.Error{Code: agent.ErrorNativeRejected, Reason: "denied", Message: "native denied"}
	mapped := MapError(native)
	var operation *runtime.OperationError
	require.ErrorAs(t, mapped, &operation)
	require.Equal(t, "native_rejected", operation.Code)
	require.NotErrorIs(t, MapError(io.EOF), operation)
}

func TestMapErrorKeepsCauseForMatchingWithoutExposingIt(t *testing.T) {
	cause := errors.New("credential=do-not-expose")
	mapped := MapError(&agent.Error{
		Code:    agent.ErrorOutcomeUnknown,
		Reason:  "interrupt_outcome_unknown",
		Message: "interrupt result is unknown",
		Cause:   cause,
	})

	require.ErrorIs(t, mapped, cause)
	require.NotContains(t, mapped.Error(), "do-not-expose")
	var operation *runtime.OperationError
	require.ErrorAs(t, mapped, &operation)
	require.Equal(t, "outcome_unknown", operation.Code)
	require.Equal(t, "interrupt_outcome_unknown", operation.Reason)
}

func testEvent(t *testing.T) agent.Event {
	t.Helper()
	return agent.Event{
		EventID:   "01234567-89ab-4cde-8fab-0123456789ab",
		SessionID: "native-1",
		Time:      time.Date(2026, 11, 10, 1, 2, 3, 0, time.UTC),
		ItemID:    "message-1",
		Payload: agent.AgentMessage{
			MessageID: "message-1",
			Content:   []agent.ContentBlock{agent.TextBlock{Text: "answer"}},
		},
		Meta: map[string]any{"native": "raw"},
	}
}

type fakeStream struct {
	events []agent.Event
	err    error
}

func (s *fakeStream) Next(context.Context) (agent.Event, error) {
	if len(s.events) > 0 {
		event := s.events[0]
		s.events = s.events[1:]
		return event, nil
	}
	if s.err != nil {
		return agent.Event{}, s.err
	}
	return agent.Event{}, io.EOF
}

type contextStream struct{}

func (*contextStream) Next(ctx context.Context) (agent.Event, error) {
	<-ctx.Done()
	return agent.Event{}, ctx.Err()
}

type fakeSession struct {
	calls  atomic.Int32
	stream agent.EventStream
}

func (s *fakeSession) ID() string                                 { return "native-1" }
func (s *fakeSession) Capabilities() agent.Capabilities           { return nil }
func (s *fakeSession) EffectiveFeatures() agent.EffectiveFeatures { return agent.EffectiveFeatures{} }
func (s *fakeSession) Events() (agent.EventStream, error) {
	if s.calls.Add(1) > 1 {
		return nil, errors.New("stream already claimed")
	}
	return s.stream, nil
}

func (s *fakeSession) StartOrSteer(context.Context, agent.Input) (agent.InputReceipt, error) {
	return agent.InputReceipt{}, nil
}

func (s *fakeSession) Answer(context.Context, agent.InteractionRef, agent.Answer) (agent.InteractionReceipt, error) {
	return agent.InteractionReceipt{}, nil
}

func (s *fakeSession) Interrupt(context.Context, agent.InterruptOptions) (agent.InterruptReceipt, error) {
	return agent.InterruptReceipt{}, nil
}
func (s *fakeSession) Close(context.Context) error { return nil }
func (s *fakeSession) Done() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}
func (s *fakeSession) Err() error { return nil }
