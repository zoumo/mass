package service

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/agentrelay"
)

type eofObservationSession struct{ agent.Session }

func (eofObservationSession) Events() (agent.EventStream, error)        { return eofObservationSession{}, nil }
func (eofObservationSession) Next(context.Context) (agent.Event, error) { return agent.Event{}, io.EOF }

func TestUnexpectedRelayEOFIsolatesOriginalSession(t *testing.T) {
	source := events.NewSource()
	relay, err := agentrelay.Start(t.Context(), agentrelay.Config{
		Session: eofObservationSession{}, Address: runtime.Address{Namespace: "one", SessionName: "failed"}, Source: source,
		ExpectedEOF: func() bool { return false }, Sink: func(context.Context, events.SessionEvent) error {
			t.Error("EOF must not synthesize an event")
			return nil
		},
	})
	require.NoError(t, err)
	l := &lane{source: source, session: resource.Session{Spec: resource.SessionSpec{RuntimeRef: "agent"}}, done: make(chan struct{})}
	sibling := &lane{}
	run := &runBinding{done: make(chan struct{}), lanes: map[string]*lane{"failed": l, "healthy": sibling}}
	s := &Service{runs: map[string]*runBinding{"agent": run}}
	s.watchObservation(run, l, runtime.DriverSession{SessionID: "failed", Source: source, Observation: relay})
	require.Eventually(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return l.err != nil }, time.Second, time.Millisecond)
	s.mu.Lock()
	defer s.mu.Unlock()
	var operation *runtime.OperationError
	require.ErrorAs(t, l.err, &operation)
	require.Equal(t, "outcome_unknown", operation.Code)
	require.Equal(t, "session_observation_eof", operation.Reason)
	require.NoError(t, run.err)
	require.NoError(t, sibling.err)
}
