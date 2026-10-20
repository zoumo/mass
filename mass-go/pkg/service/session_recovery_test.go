package service_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

// Fault/timing fixture only. Native recovery evidence lives in vertical tests.
type recoveryDriver struct {
	runtime.Driver
	pipe         *events.EventPipe
	closeEntered chan context.Context
	closeRelease chan struct{}
	closeErr     error
	source       events.Source
	slowSource   events.Source
	onResume     func() error
	resumeSource events.Source
	resumeID     string
}

func (d *recoveryDriver) Events() (events.Stream, error) { return d.pipe, nil }
func (d *recoveryDriver) Start(context.Context) (runtime.Capabilities, error) {
	return runtime.Capabilities{}, nil
}
func (d *recoveryDriver) OpenSession(_ context.Context, a runtime.Address, _ runtime.SessionSpec) (runtime.DriverSession, error) {
	d.source = events.NewSource()
	if a.SessionName == "slow" {
		d.slowSource = d.source
	}
	return runtime.DriverSession{SessionID: a.SessionName, Source: d.source}, nil
}
func (d *recoveryDriver) ResumeSession(_ context.Context, id string, _ runtime.Address, _ runtime.SessionSpec) (runtime.DriverSession, error) {
	if d.onResume != nil {
		if err := d.onResume(); err != nil {
			return runtime.DriverSession{}, err
		}
	}
	source := d.resumeSource
	if source == (events.Source{}) {
		source = events.NewSource()
	}
	if d.resumeID != "" {
		id = d.resumeID
	}
	return runtime.DriverSession{SessionID: id, Source: source}, nil
}
func (d *recoveryDriver) CloseSession(ctx context.Context, id string) error {
	if d.closeErr != nil {
		return d.closeErr
	}
	if id == "slow" {
		d.closeEntered <- ctx
		select {
		case <-d.closeRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (d *recoveryDriver) Stop(context.Context) error { d.pipe.End(nil); return nil }

type recoveryFactory struct{ d *recoveryDriver }

func (f recoveryFactory) Validate(runtime.Config) error                      { return nil }
func (f recoveryFactory) New(string, runtime.Config) (runtime.Driver, error) { return f.d, nil }

type recoveryHistory struct {
	store.HistoryStore
	blocked atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *recoveryHistory) Append(id string, records []events.SessionEvent) (*string, error) {
	if h.blocked.Load() {
		h.once.Do(func() { close(h.entered) })
		<-h.release
	}
	return h.HistoryStore.Append(id, records)
}

type recoveryMeta struct {
	store.MetaStore
	failPublication atomic.Bool
}

func (m *recoveryMeta) UpdateSession(r resource.Session) (resource.Session, error) {
	if r.Status.State == events.Unknown && r.Status.AppliedGeneration != 0 && m.failPublication.CompareAndSwap(true, false) {
		return resource.Session{}, errors.New("injected resume status publication failure")
	}
	return m.MetaStore.UpdateSession(r)
}

type RecoverySuite struct {
	meta *recoveryMeta
	suite.Suite
	history *recoveryHistory
	svc     *service.Service
	d       *recoveryDriver
	release func()
}

func (s *RecoverySuite) SetupTest() {
	st, err := file.Open(s.T().TempDir(), false)
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.NoError(st.Close()) })
	s.d = &recoveryDriver{pipe: events.NewEventPipe(32), closeEntered: make(chan context.Context, 16), closeRelease: make(chan struct{})}
	var once sync.Once
	s.release = func() { once.Do(func() { close(s.d.closeRelease) }) }
	s.meta = &recoveryMeta{MetaStore: st}
	s.history = &recoveryHistory{HistoryStore: st, entered: make(chan struct{}), release: make(chan struct{})}
	s.svc = service.New(s.meta, s.history, runtime.NewRegistry(map[string]runtime.Factory{"fixture": recoveryFactory{s.d}}))
	s.T().Cleanup(func() {
		s.release()
		select {
		case <-s.history.release:
		default:
			close(s.history.release)
		}
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.NoError(s.svc.Shutdown(c))
	})
	_, err = s.svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{}}})
	s.Require().NoError(err)
	_, err = s.svc.StartRuntime(context.Background(), "agent")
	s.Require().NoError(err)
	for _, name := range []string{"slow", "fast"} {
		_, err = s.svc.CreateSession(context.Background(), resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: name}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/remote"}}})
		s.Require().NoError(err)
	}
}
func (s *RecoverySuite) TestSlowArchiveDoesNotBlockAnotherSessionsLifecycle() {
	done := make(chan error, 1)
	go func() {
		_, err := s.svc.ArchiveSession(context.Background(), resource.SessionRef{SessionID: "slow"})
		done <- err
	}()
	select {
	case <-s.d.closeEntered:
	case <-time.After(time.Second):
		s.FailNow("archive not dispatched")
	}
	fast := make(chan error, 1)
	go func() {
		_, err := s.svc.ArchiveSession(context.Background(), resource.SessionRef{SessionID: "fast"})
		if err == nil {
			_, err = s.svc.ResumeSession(context.Background(), resource.SessionRef{SessionID: "fast"})
		}
		fast <- err
	}()
	select {
	case err := <-fast:
		s.NoError(err)
	case <-time.After(time.Second):
		s.Fail("unrelated Session lifecycle blocked")
	}
	r, err := s.svc.GetSession(resource.SessionRef{SessionID: "slow"})
	s.Require().NoError(err)
	s.Equal(events.SessionState("closing"), r.Status.State)
	s.release()
	s.NoError(<-done)
}
func TestRecoverySuite(t *testing.T) { suite.Run(t, new(RecoverySuite)) }

func (s *RecoverySuite) TestArchiveCallerCancellationOnlyEndsWait() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.svc.ArchiveSession(ctx, resource.SessionRef{SessionID: "slow"}); done <- err }()
	var owner context.Context
	select {
	case owner = <-s.d.closeEntered:
	case <-time.After(time.Second):
		s.FailNow("archive not dispatched")
	}
	cancel()
	select {
	case err := <-done:
		var public *runtime.OperationError
		s.Require().ErrorAs(err, &public)
		s.Equal("outcome_unknown", public.Code)
	case <-time.After(time.Second):
		s.FailNow("caller wait did not end")
	}
	s.NoError(owner.Err(), "original release must outlive HTTP/caller cancellation")
	r, err := s.svc.GetSession(resource.SessionRef{SessionID: "slow"})
	s.Require().NoError(err)
	s.Equal(events.SessionState("closing"), r.Status.State)
	s.release()
	s.Eventually(func() bool {
		r, err := s.svc.GetSession(resource.SessionRef{SessionID: "slow"})
		return err == nil && r.Status.State == "archived"
	}, time.Second, time.Millisecond)
}

func (s *RecoverySuite) TestResumePublishesOpeningAndRestoresReleasedFailure() {
	ref := resource.SessionRef{SessionID: "fast"}
	original, err := s.svc.ArchiveSession(context.Background(), ref)
	s.Require().NoError(err)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	s.d.onResume = func() error {
		close(entered)
		<-release
		return errors.Join(runtime.ErrResourcesReleased, errors.New("invalid private evidence"))
	}
	done := make(chan error, 1)
	go func() { _, err := s.svc.ResumeSession(context.Background(), ref); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		s.FailNow("resume not dispatched")
	}
	r, err := s.svc.GetSession(ref)
	s.Require().NoError(err)
	s.Equal(events.SessionState("opening"), r.Status.State)
	unblock()
	s.Error(<-done)
	r, err = s.svc.GetSession(ref)
	s.Require().NoError(err)
	s.Equal(original.Status, r.Status)
	s.Equal(original.Metadata.UID, r.Metadata.UID)
}

func (s *RecoverySuite) TestUnknownCloseRetainsOwnershipAndPreventsRestore() {
	ref := resource.SessionRef{SessionID: "fast"}
	s.d.closeErr = errors.New("release unconfirmed")
	_, err := s.svc.ArchiveSession(context.Background(), ref)
	s.Require().Error(err)
	r, err := s.svc.GetSession(ref)
	s.Require().NoError(err)
	s.Equal(events.SessionState("closing"), r.Status.State)
	s.NotZero(r.Status.AppliedGeneration)
	_, err = s.svc.ResumeSession(context.Background(), ref)
	s.ErrorIs(err, runtime.ErrFailedPrecondition)
	s.d.closeErr = nil
	r, err = s.svc.ArchiveSession(context.Background(), ref)
	s.Require().NoError(err)
	s.Equal(events.SessionState("archived"), r.Status.State)
	r, err = s.svc.ResumeSession(context.Background(), ref)
	s.Require().NoError(err)
	s.Equal("fast", r.Status.SessionID)
}

func (s *RecoverySuite) TestLateArchivedEventDoesNotPoisonRuntime() {
	old := s.d.source
	fast := resource.SessionRef{SessionID: "fast"}
	_, err := s.svc.ArchiveSession(context.Background(), fast)
	s.Require().NoError(err)
	sub, err := s.svc.SubscribeEvents(resource.SessionRef{SessionID: "slow"})
	s.Require().NoError(err)
	defer sub.Close()
	state := events.Running
	makeEvent := func(name string, source events.Source) events.SessionEvent {
		e, err := events.NewEvent(events.EventBase{EventID: "01234567-89ab-4cde-8fab-0123456789ab", Namespace: "one", SessionName: name, SessionID: name, Time: time.Now().UTC()}, events.StateUpdate{State: &state}, source)
		s.Require().NoError(err)
		return e
	}
	s.Require().NoError(s.d.pipe.Send(context.Background(), makeEvent("fast", old)))
	s.Require().NoError(s.d.pipe.Send(context.Background(), makeEvent("slow", s.d.slowSource)))
	c, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = sub.Next(c)
	s.Require().NoError(err, "old released source must not stop shared delivery")
	r, err := s.svc.GetSession(fast)
	s.Require().NoError(err)
	s.Equal(events.SessionState("archived"), r.Status.State)
	_, err = s.svc.ResumeSession(context.Background(), fast)
	s.NoError(err)
}

func (s *RecoverySuite) TestResumePublicationFailureSettlesWithoutReplacingIdentity() {
	ref := resource.SessionRef{SessionID: "fast"}
	original, err := s.svc.ArchiveSession(context.Background(), ref)
	s.Require().NoError(err)
	s.meta.failPublication.Store(true)
	_, err = s.svc.ResumeSession(context.Background(), ref)
	s.Require().Error(err)
	_, err = s.svc.ResumeSession(context.Background(), ref)
	s.ErrorIs(err, runtime.ErrFailedPrecondition)
	s.Require().NoError(s.svc.ReconcileOpening(context.Background(), "agent", runtime.Address{Namespace: "one", SessionName: "fast"}))
	saved, err := s.svc.GetSession(ref)
	s.Require().NoError(err)
	s.Equal(original.Status, saved.Status)
	s.Equal(original.Spec, saved.Spec)
	s.Equal(original.Metadata.UID, saved.Metadata.UID)
	_, err = s.svc.ResumeSession(context.Background(), ref)
	s.NoError(err)
}

func (s *RecoverySuite) TestSameSessionArchiveAndResumeHaveOneController() {
	ref := resource.SessionRef{SessionID: "slow"}
	archived := make(chan error, 1)
	restored := make(chan error, 1)
	go func() { _, err := s.svc.ArchiveSession(context.Background(), ref); archived <- err }()
	select {
	case <-s.d.closeEntered:
	case <-time.After(time.Second):
		s.FailNow("close not dispatched")
	}
	go func() { _, err := s.svc.ResumeSession(context.Background(), ref); restored <- err }()
	select {
	case err := <-restored:
		s.Fail("restore escaped original close", "%v", err)
		return
	case <-time.After(50 * time.Millisecond):
	}
	s.release()
	s.NoError(<-archived)
	s.NoError(<-restored)
	_, err := s.svc.ResumeSession(context.Background(), ref)
	s.ErrorIs(err, runtime.ErrFailedPrecondition)
	r, err := s.svc.GetSession(ref)
	s.Require().NoError(err)
	s.Equal("slow", r.Status.SessionID)
	s.NotZero(r.Status.AppliedGeneration)
}

func (s *RecoverySuite) TestLateOriginalFactsKeepIdentityAndDoNotChangeRecoveredState() {
	ref := resource.SessionRef{SessionID: "fast"}
	old := s.d.source
	sub, err := s.svc.SubscribeEvents(ref)
	s.Require().NoError(err)
	defer sub.Close()
	_, err = s.svc.ArchiveSession(context.Background(), ref)
	s.Require().NoError(err)
	current, err := s.svc.ResumeSession(context.Background(), ref)
	s.Require().NoError(err)
	state := events.Running
	message, err := events.NewEvent(events.EventBase{EventID: "01234567-89ab-4cde-8fab-0123456789ab", Namespace: "one", SessionName: "fast", SessionID: "fast", ItemID: "late-original", Time: time.Now().UTC()}, events.AgentMessage{MessageID: "late-original", Content: []events.ContentBlock{events.Text("original fact")}}, old)
	s.Require().NoError(err)
	update, err := events.NewEvent(events.EventBase{EventID: "01234567-89ab-4cde-8fab-0123456789ac", Namespace: "one", SessionName: "fast", SessionID: "fast", Time: time.Now().UTC()}, events.StateUpdate{State: &state}, old)
	s.Require().NoError(err)
	s.Require().NoError(s.d.pipe.Send(context.Background(), message))
	s.Require().NoError(s.d.pipe.Send(context.Background(), update))
	c, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	delivered, err := sub.Next(c)
	s.Require().NoError(err)
	s.Equal(message, delivered)
	_, err = sub.Next(c)
	s.Require().NoError(err)
	saved, err := s.svc.GetSession(ref)
	s.Require().NoError(err)
	s.Equal(current.Status, saved.Status)
	page, err := s.svc.ListHistoryEvents(ref, store.HistoryQuery{})
	s.Require().NoError(err)
	s.Require().Len(page.Events, 1)
	s.Equal(message.EventID, page.Events[0].EventID)
	s.Equal("fast", page.Events[0].SessionID)
}

func (s *RecoverySuite) TestRestoreBacklogDoesNotBlockAnotherSession() {
	ref := resource.SessionRef{SessionID: "fast"}
	old := s.d.source
	_, err := s.svc.ArchiveSession(context.Background(), ref)
	s.Require().NoError(err)
	s.history.blocked.Store(true)
	message, err := events.NewEvent(events.EventBase{EventID: "01234567-89ab-4cde-8fab-0123456789ab", Namespace: "one", SessionName: "fast", SessionID: "fast", ItemID: "backlog", Time: time.Now().UTC()}, events.AgentMessage{MessageID: "backlog", Content: []events.ContentBlock{events.Text("old complete fact")}}, old)
	s.Require().NoError(err)
	s.Require().NoError(s.d.pipe.Send(context.Background(), message))
	select {
	case <-s.history.entered:
	case <-time.After(time.Second):
		s.FailNow("history did not block")
	}
	state := events.Running
	update := func(source events.Source) events.SessionEvent {
		e, err := events.NewEvent(events.EventBase{EventID: "01234567-89ab-4cde-8fab-0123456789ac", Namespace: "one", SessionName: "fast", SessionID: "fast", Time: time.Now().UTC()}, events.StateUpdate{State: &state}, source)
		s.Require().NoError(err)
		return e
	}
	for range 100 {
		s.Require().NoError(s.d.pipe.Send(context.Background(), update(old)))
	}
	s.d.resumeSource = events.NewSource()
	early := update(s.d.resumeSource)
	entered := make(chan struct{})
	s.d.onResume = func() error {
		for range 60 {
			if err := s.d.pipe.Send(context.Background(), early); err != nil {
				return err
			}
		}
		close(entered)
		return nil
	}
	resumed := make(chan error, 1)
	go func() { _, err := s.svc.ResumeSession(context.Background(), ref); resumed <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		s.FailNow("resume notifications not delivered")
	}
	s.release()
	unrelated := make(chan error, 1)
	go func() {
		_, err := s.svc.ArchiveSession(context.Background(), resource.SessionRef{SessionID: "slow"})
		unrelated <- err
	}()
	select {
	case err := <-unrelated:
		s.Require().NoError(err)
	case <-time.After(time.Second):
		s.FailNow("backlog handoff held global Service ownership")
	}
	close(s.history.release)
	select {
	case err := <-resumed:
		s.NoError(err)
	case <-time.After(time.Second):
		s.FailNow("restore did not finish after backlog release")
	}
}

func (s *RecoverySuite) assertFailedRestoreKeepsOldFact(mode string) {
	ref := resource.SessionRef{SessionID: "fast"}
	old := s.d.source
	original, err := s.svc.ArchiveSession(context.Background(), ref)
	s.Require().NoError(err)
	// A slow-Session subscriber supplies an ordered shared-stream barrier.
	barrier, err := s.svc.SubscribeEvents(resource.SessionRef{SessionID: "slow"})
	s.Require().NoError(err)
	defer barrier.Close()
	message, err := events.NewEvent(events.EventBase{EventID: "01234567-89ab-4cde-8fab-0123456789ab", Namespace: "one", SessionName: "fast", SessionID: "fast", ItemID: "late-original", Time: time.Now().UTC()}, events.AgentMessage{MessageID: "late-original", Content: []events.ContentBlock{events.Text("original accepted fact")}}, old)
	s.Require().NoError(err)
	state := events.Running
	update, err := events.NewEvent(events.EventBase{EventID: "01234567-89ab-4cde-8fab-0123456789ac", Namespace: "one", SessionName: "slow", SessionID: "slow", Time: time.Now().UTC()}, events.StateUpdate{State: &state}, s.d.slowSource)
	s.Require().NoError(err)
	if mode != "released" {
		s.d.resumeID = "replacement"
		s.d.resumeSource = events.NewSource()
	}
	if mode == "cleanup" {
		s.d.closeErr = errors.New("replacement release unknown")
	}
	s.d.onResume = func() error {
		if err := s.d.pipe.Send(context.Background(), message); err != nil {
			return err
		}
		if err := s.d.pipe.Send(context.Background(), update); err != nil {
			return err
		}
		c, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := barrier.Next(c); err != nil {
			return err
		}
		if mode == "released" {
			return errors.Join(runtime.ErrResourcesReleased, errors.New("private restore rejected"))
		}
		return nil
	}
	_, err = s.svc.ResumeSession(context.Background(), ref)
	s.Require().Error(err)
	if mode == "cleanup" {
		s.d.closeErr = nil
		s.Require().NoError(s.svc.ReconcileOpening(context.Background(), "agent", runtime.Address{Namespace: "one", SessionName: "fast"}))
	}
	s.Eventually(func() bool {
		page, err := s.svc.ListHistoryEvents(ref, store.HistoryQuery{})
		return err == nil && len(page.Events) == 1 && page.Events[0].EventID == message.EventID
	}, time.Second, time.Millisecond, "rejected restore must not discard original complete fact")
	saved, err := s.svc.GetSession(ref)
	s.Require().NoError(err)
	s.Equal(original.Status, saved.Status)
}
func (s *RecoverySuite) TestResourceFreeRestoreFailureKeepsOldFact() {
	s.assertFailedRestoreKeepsOldFact("released")
}
func (s *RecoverySuite) TestReplacementRestoreFailureKeepsOldFact() {
	s.assertFailedRestoreKeepsOldFact("replacement")
}
func (s *RecoverySuite) TestReplacementCleanupSettlementKeepsOldFact() {
	s.assertFailedRestoreKeepsOldFact("cleanup")
}
