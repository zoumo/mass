package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

type turnReconciliationDriver struct {
	runtime.Driver
	read     func(context.Context, string, events.Source) (runtime.TurnReconciliation, error)
	closeErr error
	closes   atomic.Int32
}

func (d *turnReconciliationDriver) ReconcileTurn(ctx context.Context, id string, source events.Source) (runtime.TurnReconciliation, error) {
	return d.read(ctx, id, source)
}

func (d *turnReconciliationDriver) CloseSession(context.Context, string) error {
	d.closes.Add(1)
	return d.closeErr
}

type failedObservation struct{ done chan struct{} }

func (o failedObservation) Done() <-chan struct{} { return o.done }
func (failedObservation) Err() error              { return errors.New("test observation lost") }

func turnReconciliationFixture(t *testing.T, d *turnReconciliationDriver) (*Service, *runBinding, *lane, runtime.DriverSession) {
	t.Helper()
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	saved, err := st.CreateSession(resource.Session{
		Metadata: resource.Metadata{Namespace: "one", Name: "original"},
		Spec:     resource.SessionSpec{RuntimeRef: "agent"},
		Status: resource.SessionStatus{
			SessionID: "thread-original", State: events.Running, AppliedGeneration: 1, RuntimeGeneration: 1,
		},
	})
	require.NoError(t, err)
	source := events.NewSource()
	l := &lane{source: source, session: saved, queue: make(chan delivery, 2), done: make(chan struct{})}
	run := &runBinding{driver: d, started: true, done: make(chan struct{}), lanes: map[string]*lane{saved.Status.SessionID: l}}
	s := &Service{meta: st, runs: map[string]*runBinding{"agent": run}, sessionOps: map[string]*sessionOperation{}}
	go s.process(run, l)
	t.Cleanup(func() {
		close(run.done)
		close(l.queue)
		<-l.done
	})
	return s, run, l, runtime.DriverSession{SessionID: saved.Status.SessionID, Source: source}
}

func TestObservationReconcilesTerminalTurnBeforeRelease(t *testing.T) {
	for _, test := range []struct {
		name, status, state, reason string
		latest                      bool
		closeErr                    error
	}{
		{"released", "completed", "offline", resource.SessionReasonOwnerReleased, true, nil},
		{"release rejected", "failed", "closing", sessionCloseFailed, true, errors.New("test close failed")},
		{"newer native turn", "interrupted", "unknown", sessionObservationLost, false, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := &turnReconciliationDriver{closeErr: test.closeErr}
			s, run, l, native := turnReconciliationFixture(t, d)
			result := runtime.TurnReconciliation{TurnID: "turn-original", Status: test.status, Latest: test.latest}
			ref := resource.SessionRef{SessionID: native.SessionID}
			reached := make(chan struct{}, 1)
			d.read = func(_ context.Context, id string, source events.Source) (runtime.TurnReconciliation, error) {
				if id != native.SessionID || !source.SameRun(native.Source) {
					t.Error("native query retargeted")
				}
				reached <- struct{}{}
				return result, nil
			}
			observation := failedObservation{done: make(chan struct{})}
			native.Observation = observation
			s.watchObservation(run, l, native)
			close(observation.done)
			select {
			case <-reached:
			case <-time.After(time.Second):
				t.Fatal("observation loss did not query the original Turn")
			}
			require.EventuallyWithT(t, func(t *assert.CollectT) {
				current, err := s.GetSession(ref)
				require.NoError(t, err)
				require.Equal(t, events.SessionState(test.state), current.Status.State)
				require.Equal(t, test.reason, current.Status.Reason)
				require.Equal(t, &resource.ReconciledTurn{TurnID: result.TurnID, Status: test.status}, current.Status.ReconciledTurn)
			}, time.Second, time.Millisecond)
			if test.latest {
				require.EqualValues(t, 1, d.closes.Load())
			} else {
				require.Zero(t, d.closes.Load())
			}
		})
	}
}

func TestObservationReconciliationFencesAmbiguousWork(t *testing.T) {
	for _, test := range []struct {
		name string
		read func() (runtime.TurnReconciliation, error)
	}{
		{"active", func() (runtime.TurnReconciliation, error) {
			return runtime.TurnReconciliation{TurnID: "turn-original", Status: "inProgress"}, nil
		}},
		{"missing", func() (runtime.TurnReconciliation, error) { return runtime.TurnReconciliation{}, nil }},
		{"query failure", func() (runtime.TurnReconciliation, error) {
			return runtime.TurnReconciliation{}, errors.New("test query failed")
		}},
		{"unknown input", nil},
		{"input in flight", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := &turnReconciliationDriver{}
			s, run, l, native := turnReconciliationFixture(t, d)
			s.mu.Lock()
			l.err = errors.New("test observation lost")
			if test.name == "unknown input" {
				l.unknownInputSource = l.source
			}
			l.inputInFlight = test.name == "input in flight"
			s.mu.Unlock()
			require.True(t, s.observationLost(run, l, native))
			calls := 0
			budget := 2 * time.Second
			if test.read == nil {
				budget = 100 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(t.Context(), budget)
			defer cancel()
			d.read = func(context.Context, string, events.Source) (runtime.TurnReconciliation, error) {
				calls++
				require.NotNil(t, test.read)
				result, err := test.read()
				if calls == 2 {
					cancel()
				}
				return result, err
			}
			s.reconcileObservation(ctx, run, l, native, d)
			current, err := s.GetSession(resource.SessionRef{SessionID: native.SessionID})
			require.NoError(t, err)
			require.Equal(t, events.Unknown, current.Status.State)
			require.Equal(t, sessionObservationLost, current.Status.Reason)
			require.Nil(t, current.Status.ReconciledTurn)
			require.Zero(t, d.closes.Load())
			if test.read == nil {
				require.Zero(t, calls)
			} else {
				require.Equal(t, 2, calls)
			}
		})
	}
}

func TestObservationReconciliationRetriesContestedRelease(t *testing.T) {
	d := &turnReconciliationDriver{}
	s, run, l, native := turnReconciliationFixture(t, d)
	l.err = errors.New("test observation lost")
	require.True(t, s.observationLost(run, l, native))
	calls := 0
	d.read = func(context.Context, string, events.Source) (runtime.TurnReconciliation, error) {
		calls++
		s.mu.Lock()
		defer s.mu.Unlock()
		switch calls {
		case 1:
			// A contender wins close admission's exclusive operation check
			// between the native read and automatic close.
			s.sessionOps[native.SessionID] = &sessionOperation{users: 1}
		case 2:
			delete(s.sessionOps, native.SessionID)
		}
		return runtime.TurnReconciliation{TurnID: "turn-original", Status: "completed", Latest: true}, nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	s.reconcileObservation(ctx, run, l, native, d)
	current, err := s.GetSession(resource.SessionRef{SessionID: native.SessionID})
	require.NoError(t, err)
	require.Equal(t, events.SessionState("offline"), current.Status.State)
	require.Equal(t, &resource.ReconciledTurn{TurnID: "turn-original", Status: "completed"}, current.Status.ReconciledTurn)
	require.Equal(t, 2, calls)
	require.EqualValues(t, 1, d.closes.Load())
}

func TestObservationReconciliationDiscardsLateTurnAfterOwnerChange(t *testing.T) {
	d := &turnReconciliationDriver{}
	s, run, l, native := turnReconciliationFixture(t, d)
	s.mu.Lock()
	l.err = errors.New("test observation lost")
	s.mu.Unlock()
	require.True(t, s.observationLost(run, l, native))
	ref := resource.SessionRef{SessionID: native.SessionID}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	d.read = func(context.Context, string, events.Source) (runtime.TurnReconciliation, error) {
		s.mu.Lock()
		run.lanes[native.SessionID] = &lane{session: l.session, source: events.NewSource()}
		s.mu.Unlock()
		_, err := s.sessionStatus(ref, func(r *resource.Session) {
			r.Status.State = events.Running
			r.Status.Reason = ""
		})
		require.NoError(t, err)
		return runtime.TurnReconciliation{TurnID: "turn-original", Status: "completed", Latest: true}, nil
	}
	s.reconcileObservation(ctx, run, l, native, d)
	current, err := s.GetSession(ref)
	require.NoError(t, err)
	require.Equal(t, events.Running, current.Status.State)
	require.Empty(t, current.Status.Reason)
	require.Nil(t, current.Status.ReconciledTurn)
	require.Zero(t, d.closes.Load())
}
