package agentrelay

import (
	"context"
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
