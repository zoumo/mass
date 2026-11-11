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
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

type eofObservationSession struct{ agent.Session }

func (eofObservationSession) Events() (agent.EventStream, error)        { return eofObservationSession{}, nil }
func (eofObservationSession) Next(context.Context) (agent.Event, error) { return agent.Event{}, io.EOF }

func TestUnexpectedRelayEOFIsolatesOriginalSession(t *testing.T) {
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	saved, err := st.CreateSession(resource.Session{
		Metadata: resource.Metadata{Namespace: "one", Name: "failed"},
		Spec:     resource.SessionSpec{RuntimeRef: "agent"},
		Status: resource.SessionStatus{
			SessionID: "failed", State: events.Idle, AppliedGeneration: 1, RuntimeGeneration: 1,
		},
	})
	require.NoError(t, err)
	source := events.NewSource()
	relay, err := agentrelay.Start(t.Context(), agentrelay.Config{
		Session: eofObservationSession{}, Address: runtime.Address{Namespace: "one", SessionName: "failed"}, Source: source,
		ExpectedEOF: func() bool { return false }, Sink: func(context.Context, events.SessionEvent) error {
			t.Error("EOF must not synthesize an event")
			return nil
		},
	})
	require.NoError(t, err)
	l := &lane{source: source, session: saved, done: make(chan struct{})}
	sibling := &lane{}
	run := &runBinding{done: make(chan struct{}), lanes: map[string]*lane{"failed": l, "healthy": sibling}}
	s := &Service{meta: st, runs: map[string]*runBinding{"agent": run}, sessionOps: map[string]*sessionOperation{}}
	s.watchObservation(run, l, runtime.DriverSession{SessionID: "failed", Source: source, Observation: relay})
	require.Eventually(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return l.err != nil }, time.Second, time.Millisecond)
	require.Eventually(t, func() bool {
		current, readErr := s.GetSession(resource.SessionRef{SessionID: "failed"})
		return readErr == nil && current.Status.State == events.Unknown && current.Status.Reason == sessionObservationLost
	}, time.Second, time.Millisecond)
	handler := newStateHandler(st, saved, 8, time.Second)
	defer func() { require.NoError(t, handler.Close()) }()
	idle, err := events.NewEvent(events.EventBase{
		EventID: "01234567-89ab-4cde-8fab-0123456789ab", Namespace: "one", SessionName: "failed",
		SessionID: "failed", Time: time.Now(),
	}, events.StateUpdate{State: new(events.Idle)}, source)
	require.NoError(t, err)
	require.NoError(t, handler.update(idle))
	current, err := s.GetSession(resource.SessionRef{SessionID: "failed"})
	require.NoError(t, err)
	require.Equal(t, events.Unknown, current.Status.State)
	s.mu.Lock()
	defer s.mu.Unlock()
	var operation *runtime.OperationError
	require.ErrorAs(t, l.err, &operation)
	require.Equal(t, "outcome_unknown", operation.Code)
	require.Equal(t, "session_observation_eof", operation.Reason)
	require.NoError(t, run.err)
	require.NoError(t, sibling.err)
}

func TestObservationLossCannotOverwriteCloseOrReplacement(t *testing.T) {
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	saved, err := st.CreateSession(resource.Session{
		Metadata: resource.Metadata{Namespace: "one", Name: "original"},
		Spec:     resource.SessionSpec{RuntimeRef: "agent"},
		Status:   resource.SessionStatus{SessionID: "original", State: events.Idle, AppliedGeneration: 1},
	})
	require.NoError(t, err)
	source := events.NewSource()
	original := &lane{source: source, session: saved}
	run := &runBinding{lanes: map[string]*lane{"original": original}}
	svc := &Service{meta: st, runs: map[string]*runBinding{"agent": run}, sessionOps: map[string]*sessionOperation{}}
	for _, state := range []events.SessionState{"closing", "offline", events.Idle} {
		current, err := st.GetSession(resource.SessionRef{SessionID: "original"})
		require.NoError(t, err)
		current.Status.State = state
		_, err = st.UpdateSession(current)
		require.NoError(t, err)
		if state == events.Idle {
			run.lanes["original"] = &lane{source: events.NewSource(), session: saved}
		}
		svc.observationLost(run, original, runtime.DriverSession{SessionID: "original", Source: source})
		current, err = st.GetSession(resource.SessionRef{SessionID: "original"})
		require.NoError(t, err)
		require.Equal(t, state, current.Status.State)
		require.Empty(t, current.Status.Reason)
	}
}
