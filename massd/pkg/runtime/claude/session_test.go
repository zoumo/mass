package claude

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"
	nativeclaude "github.com/zoumo/agent-sdk-go/claude"

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/runtime"
)

type receiptSession struct {
	agent.Session
	receipt    agent.InputReceipt
	err        error
	interrupts int
}

func (s *receiptSession) StartOrSteer(context.Context, agent.Input) (agent.InputReceipt, error) {
	return s.receipt, s.err
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

func TestPromptFailurePreservesDispatchEvidence(t *testing.T) {
	cases := []struct {
		name     string
		code     agent.ErrorCode
		dispatch *nativeclaude.DispatchError
		sent     *bool
		wantSent *bool
	}{
		{
			name: "outbound frame rejected before write", code: agent.ErrorResourceExhausted,
			dispatch: &nativeclaude.DispatchError{
				Op: "submit", State: nativeclaude.NotSent,
				Err: &nativeclaude.ResourceLimitError{Resource: "outbound frame bytes", Limit: 4 << 20},
			},
			wantSent: runtime.Bool(false),
		},
		{
			name: "uncertain dispatch", code: agent.ErrorOutcomeUnknown,
			dispatch: &nativeclaude.DispatchError{Op: "submit", State: nativeclaude.OutcomeUnknown, Err: io.ErrShortWrite},
		},
		{
			name: "observed write with uncertain outcome", code: agent.ErrorOutcomeUnknown,
			sent: runtime.Yes(), wantSent: runtime.Yes(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sdkErr := &agent.Error{Code: tc.code, Reason: "start_or_steer_failed", Message: "Prompt failed"}
			if tc.dispatch != nil {
				sdkErr.Cause = tc.dispatch
			}
			native := &receiptSession{receipt: agent.InputReceipt{Acceptance: agent.Acceptance{Sent: tc.sent}}, err: sdkErr}
			driver := &Driver{sessions: map[string]*session{"native": {native: native}}}
			receipt, err := driver.SendPrompt(t.Context(), "native", runtime.Input{Content: []events.ContentBlock{events.Text("prompt")}})
			var public *runtime.OperationError
			require.ErrorAs(t, err, &public)
			require.Equal(t, string(tc.code), public.Code)
			require.Equal(t, runtime.InputReceipt{Acceptance: runtime.Acceptance{Sent: tc.wantSent}}, receipt)
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
func (*relaySession) ID() string { return "healthy" }

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
