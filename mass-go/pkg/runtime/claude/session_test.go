package claude

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

type receiptSession struct {
	agent.Session
	receipt    agent.InputReceipt
	interrupts int
}

func (s *receiptSession) StartOrSteer(context.Context, agent.Input) (agent.InputReceipt, error) {
	return s.receipt, nil
}

func (s *receiptSession) Interrupt(context.Context, agent.InterruptOptions) (agent.InterruptReceipt, error) {
	s.interrupts++
	return agent.InterruptReceipt{Target: "original-work", Result: agent.InterruptAcknowledged}, nil
}

func TestNotAppliedInputDoesNotRedispatchInterrupt(t *testing.T) {
	native := &receiptSession{receipt: agent.InputReceipt{Result: agent.InputNotApplied}}
	driver := &Driver{sessions: map[string]*session{"native": {native: native}}}
	first, err := driver.Cancel(t.Context(), "native")
	require.NoError(t, err)
	require.Equal(t, "controlAcknowledged", first.Result)
	require.Equal(t, "original-work", first.Target)
	_, err = driver.SendPrompt(t.Context(), "native", runtime.Input{Content: []events.ContentBlock{events.Text("prompt")}})
	require.NoError(t, err)
	second, err := driver.Cancel(t.Context(), "native")
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, 1, native.interrupts)
}

func TestDriverPreservesEverySDKInputResult(t *testing.T) {
	for _, result := range []agent.InputResult{agent.InputAppliedCurrentWork, agent.InputAppliedNewWork, agent.InputAppliedBestEffort, agent.InputNotApplied} {
		t.Run(string(result), func(t *testing.T) {
			sent := result != agent.InputNotApplied
			native := &receiptSession{receipt: agent.InputReceipt{Result: result, Acceptance: agent.Acceptance{Sent: &sent}}}
			driver := &Driver{sessions: map[string]*session{"native": {native: native}}}
			receipt, err := driver.SendPrompt(t.Context(), "native", runtime.Input{Content: []events.ContentBlock{events.Text("prompt")}})
			require.NoError(t, err)
			require.Equal(t, result, receipt.Result)
			require.Equal(t, &sent, receipt.Acceptance.Sent)
			require.Nil(t, receipt.Acceptance.NativeAccepted)
		})
	}
}

type endedSession struct {
	agent.Session
	cause error
}

func (s *endedSession) Events() (agent.EventStream, error)        { return s, nil }
func (s *endedSession) Next(context.Context) (agent.Event, error) { return agent.Event{}, io.EOF }
func (s *endedSession) Err() error                                { return s.cause }

type relaySession struct {
	agent.Session
	events chan agent.Event
}

func (*relaySession) Err() error { return nil }

func (s *relaySession) Events() (agent.EventStream, error) { return s, nil }
func (s *relaySession) Next(ctx context.Context) (agent.Event, error) {
	select {
	case e, ok := <-s.events:
		if !ok {
			return agent.Event{}, io.EOF
		}
		return e, nil
	case <-ctx.Done():
		return agent.Event{}, ctx.Err()
	}
}

func TestDriverRelayFailureIsIsolatedToSession(t *testing.T) {
	driver := &Driver{life: t.Context(), pipe: events.NewEventPipe(128)}
	failed := &session{native: &endedSession{cause: errors.New("synthetic transport failure")}, address: runtime.Address{Namespace: "one", SessionName: "failed"}, source: events.NewSource()}
	healthyNative := &relaySession{events: make(chan agent.Event, 1)}
	healthy := &session{native: healthyNative, address: runtime.Address{Namespace: "one", SessionName: "healthy"}, source: events.NewSource()}
	require.NoError(t, driver.startRelay(healthy))
	t.Cleanup(func() {
		healthy.relay.Close()
		select {
		case <-healthy.relay.Done():
		case <-time.After(time.Second):
			t.Error("healthy relay cleanup did not finish")
		}
	})
	require.NoError(t, driver.startRelay(failed))
	t.Cleanup(func() {
		failed.relay.Close()
		select {
		case <-failed.relay.Done():
		case <-time.After(time.Second):
			t.Error("failed relay cleanup did not finish")
		}
	})
	select {
	case <-failed.relay.Done():
	case <-time.After(time.Second):
		t.Fatal("failed relay did not stop")
	}
	require.Error(t, failed.relay.Err())
	require.Eventually(t, func() bool { failed.mu.Lock(); defer failed.mu.Unlock(); return failed.err != nil }, time.Second, time.Millisecond)
	healthyNative.events <- agent.Event{EventID: "01234567-89ab-4cde-8fab-0123456789ab", SessionID: "healthy", Time: time.Now(), Payload: agent.StateUpdatePayload{State: new(agent.StateIdle)}}
	read, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	event, err := driver.pipe.Next(read)
	require.NoError(t, err, "one relay failure must not end the Driver pipe")
	require.Equal(t, "healthy", event.SessionID)
	select {
	case <-healthy.relay.Done():
		t.Fatal("healthy sibling relay terminated")
	default:
	}
}

func TestOpenFailureDetectsUnknownCleanup(t *testing.T) {
	openErr := &agent.Error{Code: agent.ErrorUnavailable, Reason: "initialize_wait_failed"}
	require.False(t, failedNativeCleanup(openErr))
	cleanup := &agent.Error{Code: agent.ErrorUnavailable, Reason: "close_failed"}
	require.True(t, failedNativeCleanup(errors.Join(openErr, cleanup)))
}
