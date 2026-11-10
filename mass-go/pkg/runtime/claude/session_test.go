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

func TestDriverReportsUnexpectedRelayTermination(t *testing.T) {
	cause := errors.New("synthetic transport failure")
	driver := &Driver{life: t.Context(), pipe: events.NewEventPipe(128)}
	session := &session{native: &endedSession{cause: cause}, address: runtime.Address{Namespace: "one", SessionName: "demo"}, source: events.NewSource()}
	require.NoError(t, driver.startRelay(session))
	read, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err := driver.pipe.Next(read)
	require.ErrorIs(t, err, cause)
}

func TestOpenFailureDetectsUnknownCleanup(t *testing.T) {
	openErr := &agent.Error{Code: agent.ErrorUnavailable, Reason: "initialize_wait_failed"}
	require.False(t, failedNativeCleanup(openErr))
	cleanup := &agent.Error{Code: agent.ErrorUnavailable, Reason: "close_failed"}
	require.True(t, failedNativeCleanup(errors.Join(openErr, cleanup)))
}
