package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/metastore"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

type pendingCloseDriver struct {
	pipe     *events.EventPipe
	closeErr error
	closes   atomic.Int32
}

func (d *pendingCloseDriver) Events() (events.Stream, error) { return d.pipe, nil }
func (*pendingCloseDriver) Start(context.Context) (runtime.Capabilities, error) {
	return runtime.Capabilities{}, nil
}

func (*pendingCloseDriver) OpenSession(context.Context, runtime.Address, runtime.SessionSpec) (runtime.DriverSession, error) {
	return runtime.DriverSession{}, errors.New("unexpected open")
}

func (*pendingCloseDriver) ResumeSession(context.Context, string, runtime.Address, runtime.SessionSpec) (runtime.DriverSession, error) {
	return runtime.DriverSession{}, errors.New("unexpected resume")
}

func (*pendingCloseDriver) LoadSession(context.Context, string, runtime.Address, string, string) error {
	return errors.New("unexpected load")
}
func (*pendingCloseDriver) CancelHistoryLoad(context.Context, string, string) error { return nil }
func (*pendingCloseDriver) SendPrompt(context.Context, string, runtime.Input) (runtime.InputReceipt, error) {
	return runtime.InputReceipt{}, errors.New("unexpected prompt")
}

func (*pendingCloseDriver) Cancel(context.Context, string) (runtime.CancelReceipt, error) {
	return runtime.CancelReceipt{}, errors.New("unexpected cancel")
}

func (*pendingCloseDriver) AnswerInteraction(context.Context, string, string, any) (runtime.Acceptance, error) {
	return runtime.Acceptance{}, errors.New("unexpected answer")
}

func (d *pendingCloseDriver) CloseSession(context.Context, string) error {
	d.closes.Add(1)
	return d.closeErr
}

func (d *pendingCloseDriver) Stop(context.Context) error {
	d.pipe.End(nil)
	return nil
}

type pendingCloseFactory struct{ driver *pendingCloseDriver }

func (f pendingCloseFactory) Validate(runtime.Config) error { return nil }
func (f pendingCloseFactory) New(string, runtime.Config) (runtime.Driver, error) {
	return f.driver, nil
}

type failOfflineOnce struct {
	metastore.MetaStore
	failed atomic.Bool
}

func (m *failOfflineOnce) UpdateSession(r resource.Session) (resource.Session, error) {
	if r.Status.State == "offline" && m.failed.CompareAndSwap(false, true) {
		return resource.Session{}, errors.New("injected offline status failure")
	}
	return m.MetaStore.UpdateSession(r)
}

func TestCloseSettlesPendingWithoutHandleUsingReleaseProof(t *testing.T) {
	for _, test := range []struct {
		name      string
		released  bool
		closeErr  error
		offline   bool
		closeCall int32
	}{
		{name: "already-released", released: true, offline: true},
		{name: "close-succeeds", closeErr: nil, offline: true, closeCall: 1},
		{name: "close-unresolved", closeErr: errors.New("private provider failure"), offline: false, closeCall: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, st, driver, ref := newPendingCloseFixture(t)
			driver.closeErr = test.closeErr
			op := &opening{
				address:  runtime.Address{Namespace: "one", SessionName: "pending"},
				done:     make(chan struct{}),
				native:   runtime.DriverSession{SessionID: "native-pending"},
				released: test.released,
				err:      errors.New("private pending failure"),
			}
			svc.mu.Lock()
			svc.runs["agent"].pending[addressKey(op.address)] = op
			close(op.done)
			svc.mu.Unlock()

			closed, err := svc.CloseSession(context.Background(), ref)
			current, readErr := st.GetSession(ref)
			require.NoError(t, readErr)
			if test.offline {
				require.NoError(t, err)
				require.Equal(t, events.SessionState("offline"), closed.Status.State)
				require.Equal(t, events.SessionState("offline"), current.Status.State)
				svc.mu.Lock()
				_, retained := svc.runs["agent"].pending[addressKey(op.address)]
				svc.mu.Unlock()
				require.False(t, retained, "settled reservation must not remain owned")
			} else {
				require.Error(t, err)
				require.Equal(t, events.SessionState("closing"), current.Status.State)
				require.Equal(t, "session_close_failed", current.Status.Reason)
				svc.mu.Lock()
				retained := svc.runs["agent"].pending[addressKey(op.address)]
				svc.mu.Unlock()
				require.Same(t, op, retained, "unresolved reservation must retain its original owner")

				driver.closeErr = nil
				closed, err = svc.CloseSession(context.Background(), ref)
				require.NoError(t, err, "explicit retry must settle the retained SDK target")
				require.Equal(t, events.SessionState("offline"), closed.Status.State)
			}
			require.Equal(t, test.closeCall, driver.closes.Load())

			svc.mu.Lock()
			delete(svc.runs["agent"].pending, addressKey(op.address))
			svc.mu.Unlock()
		})
	}
}

func TestCloseRetainsPendingWithoutHandleOrReleaseProof(t *testing.T) {
	svc, st, driver, ref := newPendingCloseFixture(t)
	op := &opening{
		address: runtime.Address{Namespace: "one", SessionName: "pending"},
		done:    make(chan struct{}),
		err:     errors.New("private pending failure"),
	}
	svc.mu.Lock()
	svc.runs["agent"].pending[addressKey(op.address)] = op
	close(op.done)
	svc.mu.Unlock()

	_, err := svc.CloseSession(context.Background(), ref)
	require.Error(t, err)
	current, readErr := st.GetSession(ref)
	require.NoError(t, readErr)
	require.Equal(t, events.SessionState("closing"), current.Status.State)
	require.Equal(t, "session_close_failed", current.Status.Reason)
	require.Zero(t, driver.closes.Load())
	svc.mu.Lock()
	require.Same(t, op, svc.runs["agent"].pending[addressKey(op.address)])
	delete(svc.runs["agent"].pending, addressKey(op.address))
	svc.mu.Unlock()
}

func TestCloseSettlesPendingBufferedEventsBeforeOffline(t *testing.T) {
	svc, st, driver, ref := newPendingCloseFixture(t)
	source := events.NewSource()
	event, err := events.NewEvent(events.EventBase{
		EventID:   "10000000-0000-4000-8000-000000000322",
		Namespace: "one", SessionName: "pending", SessionID: ref.SessionID,
		ItemID: "pending-final", Time: time.Now(),
	}, events.AgentMessage{MessageID: "pending-final", Content: []events.ContentBlock{events.Text("settled")}}, source)
	require.NoError(t, err)
	op := &opening{
		address: runtime.Address{Namespace: "one", SessionName: "pending"},
		done:    make(chan struct{}),
		native:  runtime.DriverSession{SessionID: ref.SessionID, Source: source},
		events:  []events.SessionEvent{event},
	}
	svc.mu.Lock()
	svc.runs["agent"].pending[addressKey(op.address)] = op
	close(op.done)
	svc.mu.Unlock()

	closed, err := svc.CloseSession(context.Background(), ref)
	require.NoError(t, err)
	require.Equal(t, events.SessionState("offline"), closed.Status.State)
	require.EqualValues(t, 1, driver.closes.Load())
	page, err := st.ListHistory(ref.SessionID, store.HistoryQuery{Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	require.Equal(t, event.EventID, page.Events[0].EventID)
}

func TestCloseWithoutHandleRetainsUnresolvedHistoryLoad(t *testing.T) {
	svc, st, driver, _ := newPendingCloseFixture(t)
	ref := resource.SessionRef{SessionID: "unresolved-load"}
	_, err := st.CreateSession(resource.Session{
		Metadata: resource.Metadata{Namespace: "one", Name: "unresolved-load"},
		Spec:     resource.SessionSpec{RuntimeRef: "agent"},
		Status: resource.SessionStatus{
			SessionID: ref.SessionID,
			State:     events.Unknown,
			HistoryLoad: &resource.HistoryLoadStatus{
				LoadID: "load-pending",
				Phase:  "accepted",
			},
		},
	})
	require.NoError(t, err)

	_, err = svc.CloseSession(context.Background(), ref)
	require.Error(t, err)
	current, readErr := st.GetSession(ref)
	require.NoError(t, readErr)
	require.Equal(t, events.SessionState("closing"), current.Status.State)
	require.Equal(t, "session_close_failed", current.Status.Reason)
	require.Zero(t, driver.closes.Load())
}

func TestCloseRetriesStatusOnlyFailureWithoutHandle(t *testing.T) {
	svc, st, driver, _ := newPendingCloseFixture(t)
	ref := resource.SessionRef{SessionID: "status-only"}
	_, err := st.CreateSession(resource.Session{
		Metadata: resource.Metadata{Namespace: "one", Name: "status-only"},
		Spec:     resource.SessionSpec{RuntimeRef: "agent"},
		Status:   resource.SessionStatus{SessionID: ref.SessionID, State: events.SessionState("closing")},
	})
	require.NoError(t, err)

	closed, err := svc.CloseSession(context.Background(), ref)
	require.NoError(t, err)
	require.Equal(t, events.SessionState("offline"), closed.Status.State)
	require.Zero(t, driver.closes.Load())
}

func TestCloseRetriesPendingReleaseAfterStatusFailure(t *testing.T) {
	svc, st, driver, ref := newPendingCloseFixtureWithMeta(t, func(meta metastore.MetaStore) metastore.MetaStore {
		return &failOfflineOnce{MetaStore: meta}
	})
	op := &opening{
		address: runtime.Address{Namespace: "one", SessionName: "pending"},
		done:    make(chan struct{}),
		native:  runtime.DriverSession{SessionID: ref.SessionID},
	}
	svc.mu.Lock()
	svc.runs["agent"].pending[addressKey(op.address)] = op
	close(op.done)
	svc.mu.Unlock()

	_, err := svc.CloseSession(context.Background(), ref)
	require.Error(t, err)
	current, readErr := st.GetSession(ref)
	require.NoError(t, readErr)
	require.Equal(t, events.SessionState("closing"), current.Status.State)
	require.Equal(t, "session_close_failed", current.Status.Reason)
	svc.mu.Lock()
	require.Same(t, op, svc.runs["agent"].pending[addressKey(op.address)], "release proof must survive a status-only failure")
	svc.mu.Unlock()

	closed, err := svc.CloseSession(context.Background(), ref)
	require.NoError(t, err)
	require.Equal(t, events.SessionState("offline"), closed.Status.State)
	require.EqualValues(t, 1, driver.closes.Load(), "retry must not repeat a confirmed SDK close")
	svc.mu.Lock()
	_, retained := svc.runs["agent"].pending[addressKey(op.address)]
	svc.mu.Unlock()
	require.False(t, retained)
}

func TestCloseFailureReasonOmitsProviderDiagnostics(t *testing.T) {
	require.Equal(t, "unavailable:private_reason", closeFailureReason(&runtime.OperationError{Code: "unavailable", Reason: "private_reason"}))
	require.Equal(t, "context_canceled", closeFailureReason(context.Canceled))
	require.Equal(t, "context_deadline_exceeded", closeFailureReason(context.DeadlineExceeded))
	require.Equal(t, "internal_error", closeFailureReason(errors.New("/private/provider/path and token")))
}

func newPendingCloseFixture(t *testing.T) (*Service, *file.Store, *pendingCloseDriver, resource.SessionRef) {
	return newPendingCloseFixtureWithMeta(t, nil)
}

func newPendingCloseFixtureWithMeta(t *testing.T, wrap func(metastore.MetaStore) metastore.MetaStore) (*Service, *file.Store, *pendingCloseDriver, resource.SessionRef) {
	t.Helper()
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	driver := &pendingCloseDriver{pipe: events.NewEventPipe(8)}
	var meta metastore.MetaStore = st
	if wrap != nil {
		meta = wrap(meta)
	}
	svc := New(meta, st, runtime.NewRegistry(map[string]runtime.Factory{"pending": pendingCloseFactory{driver}}))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
		require.NoError(t, st.Close())
	})
	_, err = svc.CreateRuntime(resource.Runtime{
		Metadata: resource.Metadata{Name: "agent"},
		Spec:     resource.RuntimeSpec{Type: "pending", Config: runtime.Config{}},
	})
	require.NoError(t, err)
	_, err = svc.StartRuntime(context.Background(), "agent")
	require.NoError(t, err)
	_, err = st.CreateSession(resource.Session{
		Metadata: resource.Metadata{Namespace: "one", Name: "pending"},
		Spec:     resource.SessionSpec{RuntimeRef: "agent"},
		Status: resource.SessionStatus{
			SessionID:         "native-pending",
			State:             events.Unknown,
			AppliedGeneration: 1,
		},
	})
	require.NoError(t, err)
	return svc, st, driver, resource.SessionRef{SessionID: "native-pending"}
}
