package vertical_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/prototype"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/claude"
	"github.com/zoumo/mass/mass-go/pkg/runtime/codex"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type failureSuite struct {
	suite.Suite
	st      *file.Store
	meta    store.MetaStore
	svc     *service.Service
	reg     *runtime.Registry
	unknown bool
	work    string
}

func TestFailureSuite(t *testing.T) { suite.Run(t, new(failureSuite)) }
func (s *failureSuite) SetupTest() {
	dir := s.T().TempDir()
	var err error
	s.st, err = file.Open(dir, false)
	s.Require().NoError(err)
	s.reg = runtime.NewRegistry(map[string]runtime.Factory{"codex": codex.Factory{RecoveryDir: filepath.Join(dir, "private")}, "claude": claude.Factory{RecoveryDir: filepath.Join(dir, "private")}})
	s.meta = s.st
	s.svc = nil
	s.unknown = false
}

func (s *failureSuite) TearDownTest() {
	if s.svc != nil {
		err := s.svc.Shutdown(ctx(s.T()))
		if s.unknown {
			s.Require().Error(err)
		} else {
			s.Require().NoError(err)
		}
	}
	s.Require().NoError(s.st.Close())
}

func (s *failureSuite) start(kind, name string, history store.HistoryStore) {
	path, work := nativeScenario(s.T(), kind, name)
	s.work = work
	s.svc = service.NewWithPolicy(s.meta, history, s.reg, prototype.AutoApprovePermissions)
	bin := mock
	if kind == "claude" {
		bin = claudeMock
	}
	_, err := s.svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: kind, Config: runtime.Config{"executable": bin, "scenario": path}}})
	s.Require().NoError(err)
	_, err = s.svc.StartRuntime(ctx(s.T()), "agent")
	s.Require().NoError(err)
}

func (s *failureSuite) spec(name string) resource.Session {
	return resource.Session{Metadata: resource.Metadata{Namespace: "default", Name: name}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: s.work}}}
}

func (s *failureSuite) open(name string) resource.SessionRef {
	r, err := s.svc.CreateSession(ctx(s.T()), s.spec(name))
	s.Require().NoError(err)
	return resource.SessionRef{SessionID: r.Status.SessionID}
}

func (s *failureSuite) input(ref resource.SessionRef) runtime.InputReceipt {
	r, err := s.svc.SendPrompt(ctx(s.T()), ref, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}})
	s.Require().NoError(err)
	return r
}

func (s *failureSuite) until(sub *service.Subscription, state events.SessionState) {
	for {
		e, err := sub.Next(ctx(s.T()))
		s.Require().NoError(err)
		if p, ok := e.Payload().(events.StateUpdate); ok && ((p.State == nil && state == events.Unknown) || (p.State != nil && *p.State == state)) {
			return
		}
	}
}

func (s *failureSuite) cancel(kind string) {
	s.start(kind, "cancel", s.st)
	ref := s.open("demo")
	sub, err := s.svc.SubscribeEvents(ref)
	s.Require().NoError(err)
	defer sub.Close()
	receipt := s.input(ref)
	s.Equal(runtime.Yes(), receipt.Acceptance.Sent)
	s.until(sub, events.Running)
	ack, err := s.svc.Cancel(ctx(s.T()), ref)
	s.Require().NoError(err)
	s.Equal("controlAcknowledged", ack.Result)
	current, err := s.svc.GetSession(ref)
	s.Require().NoError(err)
	s.Equal(events.Running, current.Status.State)
	_, err = s.svc.SendPrompt(ctx(s.T()), ref, runtime.Input{Content: []events.ContentBlock{events.Text("must not resend")}})
	s.Require().Error(err)
	s.until(sub, events.Idle)
	if kind == "claude" {
		_, err = s.svc.SendPrompt(ctx(s.T()), ref, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}})
		s.Require().ErrorContains(err, "close/resume required")
	}
	s.T().Log("PASS matched interrupt receipt does not terminate work or admit another input")
}
func (s *failureSuite) TestCodexCancelReceipt()  { s.cancel("codex") }
func (s *failureSuite) TestClaudeCancelReceipt() { s.cancel("claude") }
func (s *failureSuite) disconnect(kind string) {
	s.start(kind, "disconnect", s.st)
	s.unknown = true
	ref := s.open("demo")
	sub, err := s.svc.SubscribeEvents(ref)
	s.Require().NoError(err)
	defer sub.Close()
	receipt := s.input(ref)
	s.Equal(runtime.Yes(), receipt.Acceptance.Sent)
	s.until(sub, events.Running)
	s.until(sub, events.Unknown)
	// The returned ACK/write evidence is independent of subsequent observation loss.
	s.Equal(runtime.Yes(), receipt.Acceptance.Sent)
	if kind == "codex" {
		s.Equal(runtime.Yes(), receipt.Acceptance.NativeAccepted)
	}
	_, err = s.svc.SendPrompt(ctx(s.T()), ref, runtime.Input{Content: []events.ContentBlock{events.Text("must not resend")}})
	s.Require().Error(err)
	_, err = s.svc.StopRuntime(ctx(s.T()), "agent")
	s.Require().Error(err)
	r, err := s.svc.GetRuntime("agent")
	s.Require().NoError(err)
	s.Equal("stopping", r.Status.State)
	_, err = s.svc.StartRuntime(ctx(s.T()), "agent")
	s.Require().Error(err)
	cold := service.New(s.st, s.st, runtime.NewRegistry(map[string]runtime.Factory{"codex": codex.Factory{RecoveryDir: s.T().TempDir()}, "claude": claude.Factory{RecoveryDir: s.T().TempDir()}}))
	_, err = cold.StartRuntime(ctx(s.T()), "agent")
	s.Require().ErrorContains(err, "previous runtime release")
	s.T().Log("PASS EOF preserves acceptance, unknown work and stopping ownership; no automatic resend")
}
func (s *failureSuite) TestCodexDisconnect()  { s.disconnect("codex") }
func (s *failureSuite) TestClaudeDisconnect() { s.disconnect("claude") }
func (s *failureSuite) TestUnknownInputIsNotResent() {
	s.start("codex", "unknown-input", s.st)
	s.unknown = true
	ref := s.open("demo")
	_, err := s.svc.SendPrompt(ctx(s.T()), ref, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}})
	s.Require().ErrorContains(err, "outcome_unknown")
	_, err = s.svc.SendPrompt(ctx(s.T()), ref, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}})
	s.Require().Error(err)
}

// The fault injects a successful authoritative file append followed by an unknown
// return. It does not synthesize Agent observations or replace the SDK Driver.
type unknownAppend struct {
	store.HistoryStore
	once    sync.Once
	entered chan struct{}
}

func (g *unknownAppend) Append(id string, records []events.SessionEvent) (*string, error) {
	fail := false
	g.once.Do(func() { fail = true })
	result, err := g.HistoryStore.Append(id, records)
	if fail && err == nil {
		close(g.entered)
		return result, errors.New("injected outcome_unknown after durable append")
	}
	return result, err
}

func (s *failureSuite) TestUnknownAppendExplicitSettlement() {
	fault := &unknownAppend{HistoryStore: s.st, entered: make(chan struct{})}
	s.start("codex", "basic", fault)
	ref := s.open("demo")
	sub, err := s.svc.SubscribeEvents(ref)
	s.Require().NoError(err)
	defer sub.Close()
	s.input(ref)
	select {
	case <-fault.entered:
	case <-ctx(s.T()).Done():
		s.FailNow("missing append fault")
	}
	s.Require().Eventually(func() bool { _, err := sub.Next(ctx(s.T())); return err != nil }, time.Second, 5*time.Millisecond)
	page, err := s.st.ListHistory(ref.SessionID, store.HistoryQuery{})
	s.Require().NoError(err)
	s.Require().Len(page.Events, 1)
	original := page.Events[0].EventID
	s.Require().Eventually(func() bool { return s.svc.ReconcileHistory(ctx(s.T()), ref) == nil }, time.Second, 5*time.Millisecond)
	s.Require().Eventually(func() bool {
		p, e := s.st.ListHistory(ref.SessionID, store.HistoryQuery{})
		return e == nil && len(p.Events) == 4
	}, time.Second, 5*time.Millisecond)
	page, err = s.st.ListHistory(ref.SessionID, store.HistoryQuery{})
	s.Require().NoError(err)
	s.Equal(original, page.Events[0].EventID)
	_, err = s.svc.SendPrompt(ctx(s.T()), ref, runtime.Input{Content: []events.ContentBlock{events.Text("sealed")}})
	s.Require().Error(err)
	s.T().Log("PASS written-but-unknown event not published; explicit full-prefix validation and same UUID settlement; admission stays sealed")
}

type slowAppend struct {
	store.HistoryStore
	once             sync.Once
	entered, release chan struct{}
}

func (g *slowAppend) Append(id string, records []events.SessionEvent) (*string, error) {
	if id == "codex-session" {
		g.once.Do(func() { close(g.entered); <-g.release })
	}
	return g.HistoryStore.Append(id, records)
}

func (s *failureSuite) TestSlowStorageDoesNotBlockOtherSession() {
	gate := &slowAppend{HistoryStore: s.st, entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	defer release()
	s.start("codex", "slow-isolation", gate)
	a := s.open("a")
	b := s.open("b")
	s.input(a)
	select {
	case <-gate.entered:
	case <-ctx(s.T()).Done():
		s.FailNow("A storage gate")
	}
	sub, err := s.svc.SubscribeEvents(b)
	s.Require().NoError(err)
	defer sub.Close()
	s.input(b)
	s.until(sub, events.Running)
	s.until(sub, events.Idle)
	p, err := s.st.ListHistory(b.SessionID, store.HistoryQuery{})
	s.Require().NoError(err)
	s.Len(p.Events, 4)
	p, err = s.st.ListHistory(a.SessionID, store.HistoryQuery{})
	s.Require().NoError(err)
	s.Empty(p.Events)
	release()
	_, err = s.svc.ArchiveSession(ctx(s.T()), a)
	s.Require().NoError(err)
	_, err = s.svc.ArchiveSession(ctx(s.T()), b)
	s.Require().NoError(err)
	s.T().Log("PASS B completes and persists while A append remains blocked in its own lane")
}

func (s *failureSuite) TestLateCreateKeepsOriginalReservation() {
	s.start("codex", "late-create", s.st)
	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := s.svc.CreateSession(short, s.spec("a"))
	s.Require().ErrorContains(err, "outcome_unknown")
	_, err = s.svc.CreateSession(ctx(s.T()), s.spec("a"))
	s.Require().ErrorIs(err, store.ErrConflict)
	b := s.open("b")
	_, err = s.svc.GetSession(b)
	s.Require().NoError(err)
	// Stop seals admission, waits original A settlement/cleanup, then releases B.
	_, err = s.svc.StopRuntime(ctx(s.T()), "agent")
	s.Require().NoError(err)
	_, err = s.svc.GetSession(resource.SessionRef{Namespace: "default", Name: "a"})
	s.Require().ErrorIs(err, store.ErrNotFound)
	s.T().Log("PASS slow A does not block B; late A ID released under original owner before stop")
}

func (s *failureSuite) TestOldWorkCannotControlNewWork() {
	s.start("codex", "old-work", s.st)
	ref := s.open("demo")
	sub, err := s.svc.SubscribeEvents(ref)
	s.Require().NoError(err)
	defer sub.Close()
	s.input(ref)
	s.until(sub, events.Running)
	s.until(sub, events.Idle)
	receipt := s.input(ref)
	s.Equal("turn-2", receipt.Submission)
	s.until(sub, events.Running)
	// The strict script puts turn-1 started/completed before turn-2 interrupt.
	time.Sleep(200 * time.Millisecond)
	ack, err := s.svc.Cancel(ctx(s.T()), ref)
	s.Require().NoError(err)
	s.Equal("controlAcknowledged", ack.Result)
	current, err := s.svc.GetSession(ref)
	s.Require().NoError(err)
	s.Equal(events.Running, current.Status.State)
	s.until(sub, events.Idle)
}

func (s *failureSuite) TestBoundedOverloadRetainsDelivery() {
	gate := &slowAppend{HistoryStore: s.st, entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	defer release()
	s.start("codex", "overload", gate)
	ref := s.open("demo")
	s.input(ref)
	select {
	case <-gate.entered:
	case <-ctx(s.T()).Done():
		s.FailNow("storage gate")
	}
	s.Require().Eventually(func() bool { _, err := s.svc.SubscribeEvents(ref); return err != nil }, time.Second, 5*time.Millisecond)
	_, err := s.svc.StopRuntime(ctx(s.T()), "agent")
	s.Require().ErrorContains(err, "responsibility remains")
	release()
	// Repeated saturation is explicit; the operator settles the same consumer.
	deadline := time.Now().Add(20 * time.Second)
	count := 0
	for time.Now().Before(deadline) {
		_ = s.svc.ReconcileDelivery(ctx(s.T()), "agent")
		p, e := s.st.ListHistory(ref.SessionID, store.HistoryQuery{Limit: 1000})
		s.Require().NoError(e)
		count = len(p.Events)
		if count == 160 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	s.Require().Equal(160, count)
	s.T().Log("PASS bounded overload explicitly fails, retains overflow plus upstream responsibility, and settles all 160 original facts")
}

func (s *failureSuite) TestSlowSubscriberDoesNotChangeHistory() {
	s.start("codex", "slow-subscriber", s.st)
	ref := s.open("demo")
	sub, err := s.svc.SubscribeEvents(ref)
	s.Require().NoError(err)
	defer sub.Close()
	s.input(ref)
	s.Require().Eventually(func() bool {
		p, e := s.st.ListHistory(ref.SessionID, store.HistoryQuery{Limit: 1000})
		return e == nil && len(p.Events) == 140
	}, 6*time.Second, 5*time.Millisecond)
	_, err = sub.Next(ctx(s.T()))
	var public *runtime.OperationError
	s.Require().ErrorAs(err, &public)
	s.Equal("resource_exhausted", public.Code)
	s.Equal("slow_subscriber", public.Reason)
}

func (s *failureSuite) TestStopTimeoutSealsAdmissionUntilRelease() {
	s.start("codex", "stop-permission", s.st)
	ref := s.open("demo")
	sub, err := s.svc.SubscribeEvents(ref)
	s.Require().NoError(err)
	defer sub.Close()
	s.input(ref)
	handle := ""
	for handle == "" {
		e, err := sub.Next(ctx(s.T()))
		s.Require().NoError(err)
		if p, ok := e.Payload().(events.InteractionRequested); ok {
			handle = p.Interaction
		}
	}
	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = s.svc.StopRuntime(short, "agent")
	s.Require().Error(err)
	r, err := s.svc.GetRuntime("agent")
	s.Require().NoError(err)
	s.Equal("stopping", r.Status.State)
	_, err = s.svc.CreateSession(ctx(s.T()), s.spec("other"))
	s.Require().Error(err)
	_, err = s.svc.SendPrompt(ctx(s.T()), ref, runtime.Input{Content: []events.ContentBlock{events.Text("sealed")}})
	s.Require().Error(err)
	_, err = s.svc.ResumeSession(ctx(s.T()), ref)
	s.Require().Error(err)
	_, err = s.svc.AnswerInteraction(ctx(s.T()), ref, handle, map[string]any{"action": "accept", "content": map[string]any{"answers": map[string]any{"format": "json"}}})
	s.Require().Error(err)
	time.Sleep(450 * time.Millisecond)
	session, err := s.svc.GetSession(ref)
	s.Require().NoError(err)
	s.Equal(events.SessionState("closing"), session.Status.State)
	_, err = s.svc.StopRuntime(ctx(s.T()), "agent")
	s.Require().NoError(err)
	s.T().Log("PASS stop timeout preserves stopping/closing and closed admission; late idle does not reopen; original release completes explicit retry")
}

func (s *failureSuite) TestEarlyFactsWaitForIdentityCommit() {
	gate := &identityGate{MetaStore: s.st, entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	defer release()
	s.meta = gate
	s.start("claude", "early", s.st)
	done := make(chan error, 1)
	go func() { _, err := s.svc.CreateSession(ctx(s.T()), s.spec("demo")); done <- err }()
	select {
	case <-gate.entered:
	case <-ctx(s.T()).Done():
		s.FailNow("identity gate")
	}
	_, err := s.st.GetSession(resource.SessionRef{Namespace: "default", Name: "demo"})
	s.Require().ErrorIs(err, store.ErrNotFound)
	_, err = s.st.ListHistory("claude-session", store.HistoryQuery{})
	s.Require().ErrorIs(err, store.ErrNotFound)
	release()
	s.Require().NoError(<-done)
	s.Require().Eventually(func() bool {
		p, e := s.st.ListHistory("claude-session", store.HistoryQuery{})
		return e == nil && len(p.Events) == 2
	}, time.Second, 5*time.Millisecond)
	s.T().Log("PASS SDK-native pre-init complete facts stay bounded and invisible until persistent identity commit")
}

func (s *failureSuite) TestEarlyCapacityFailsExplicitlyAndReleases() {
	s.start("claude", "early-overflow", s.st)
	_, err := s.svc.CreateSession(ctx(s.T()), s.spec("demo"))
	s.Require().ErrorContains(err, "pre-identity buffer exhausted")
	_, err = s.svc.GetSession(resource.SessionRef{Namespace: "default", Name: "demo"})
	s.Require().ErrorIs(err, store.ErrNotFound)
}

func (s *failureSuite) TestTimeoutDuringIdentityCommitSettlesOriginalOwner() {
	gate := &identityGate{MetaStore: s.st, entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	defer release()
	s.meta = gate
	s.start("claude", "early", s.st)
	short, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_, err := s.svc.CreateSession(short, s.spec("demo"))
	s.Require().ErrorContains(err, "outcome_unknown")
	select {
	case <-gate.entered:
	default:
		s.FailNow("native open must precede identity timeout")
	}
	release()
	_, err = s.svc.StopRuntime(ctx(s.T()), "agent")
	s.Require().NoError(err)
	r, err := s.svc.GetSession(resource.SessionRef{Namespace: "default", Name: "demo"})
	s.Require().NoError(err)
	s.Equal(events.SessionState("archived"), r.Status.State)
	p, err := s.st.ListHistory(r.Status.SessionID, store.HistoryQuery{})
	s.Require().NoError(err)
	s.Len(p.Events, 2)
}

func (s *failureSuite) TestLateAnswerCannotControlRecoveredRun() {
	s.start("codex", "late-answer", s.st)
	ref := s.open("demo")
	sub, err := s.svc.SubscribeEvents(ref)
	s.Require().NoError(err)
	defer sub.Close()
	s.input(ref)
	old := ""
	for old == "" {
		e, err := sub.Next(ctx(s.T()))
		s.Require().NoError(err)
		if p, ok := e.Payload().(events.InteractionRequested); ok {
			old = p.Interaction
		}
	}
	s.until(sub, events.Idle)
	_, err = s.svc.ArchiveSession(ctx(s.T()), ref)
	s.Require().NoError(err)
	_, err = s.svc.ResumeSession(ctx(s.T()), ref)
	s.Require().NoError(err)
	_, err = s.svc.SendPrompt(ctx(s.T()), ref, runtime.Input{Content: []events.ContentBlock{events.Text("continue")}})
	s.Require().NoError(err)
	s.until(sub, events.Running)
	_, err = s.svc.AnswerInteraction(ctx(s.T()), ref, old, map[string]any{"action": "accept", "content": map[string]any{"answers": map[string]any{"format": map[string]any{"answers": []string{"json"}}}}})
	s.Require().Error(err)
	s.until(sub, events.Idle)
	s.T().Log("PASS native resolved original handle and old turn notifications cannot answer or terminate the recovered run")
}

type unknownIdentity struct{ store.MetaStore }

func (g unknownIdentity) CreateSession(r resource.Session) (resource.Session, error) {
	value, err := g.MetaStore.CreateSession(r)
	if err != nil {
		return value, err
	}
	return value, errors.New("injected unknown identity commit")
}

func (s *failureSuite) TestUnknownIdentityCommitReconcilesOriginalFacts() {
	s.meta = unknownIdentity{s.st}
	s.start("claude", "early", s.st)
	_, err := s.svc.CreateSession(ctx(s.T()), s.spec("demo"))
	s.Require().ErrorContains(err, "unknown identity commit")
	_, err = s.svc.CreateSession(ctx(s.T()), s.spec("demo"))
	s.Require().ErrorIs(err, store.ErrConflict)
	page, err := s.st.ListHistory("claude-session", store.HistoryQuery{})
	s.Require().NoError(err)
	s.Empty(page.Events)
	err = s.svc.ReconcileOpening(ctx(s.T()), "agent", runtime.Address{Namespace: "default", SessionName: "demo"})
	s.Require().NoError(err)
	r, err := s.svc.GetSession(resource.SessionRef{SessionID: "claude-session"})
	s.Require().NoError(err)
	s.Equal(events.SessionState("archived"), r.Status.State)
	page, err = s.st.ListHistory("claude-session", store.HistoryQuery{})
	s.Require().NoError(err)
	s.Len(page.Events, 2)
	s.T().Log("PASS unknown identity commit preserves reservation and early facts until explicit original-owner readback and release")
}

func (s *failureSuite) TestConcurrentStopSharesOriginalResponsibility() {
	s.start("codex", "cancel", s.st)
	ref := s.open("demo")
	sub, err := s.svc.SubscribeEvents(ref)
	s.Require().NoError(err)
	defer sub.Close()
	s.input(ref)
	s.until(sub, events.Running)
	done := make(chan error, 2)
	for range 2 {
		go func() { _, err := s.svc.StopRuntime(ctx(s.T()), "agent"); done <- err }()
	}
	for range 2 {
		s.Require().NoError(<-done)
	}
	r, err := s.svc.GetRuntime("agent")
	s.Require().NoError(err)
	s.Equal("stopped", r.Status.State)
}

// This gate delays a transport write, not native observations or Store commits.
type pausedSSE struct {
	*httptest.ResponseRecorder
	ready   chan struct{}
	release chan struct{}
	first   bool
}

func (w *pausedSSE) Flush() {
	if !w.first {
		w.first = true
		close(w.ready)
		<-w.release
	}
	w.ResponseRecorder.Flush()
}

var _ http.Flusher = (*pausedSSE)(nil)

func (s *failureSuite) TestSSEOverflowIsExplicit() {
	s.start("codex", "slow-subscriber", s.st)
	ref := s.open("demo")
	w := &pausedSSE{ResponseRecorder: httptest.NewRecorder(), ready: make(chan struct{}), release: make(chan struct{})}
	path, err := client.SessionPath(ref)
	s.Require().NoError(err)
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path+"/events/stream", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	handler, err := transport.New(s.svc, transport.Options{LocalOnly: true})
	s.Require().NoError(err)
	defer handler.Shutdown()
	done := make(chan struct{})
	go func() { defer close(done); handler.ServeHTTP(w, request) }()
	<-w.ready
	// Always release the writer on a failed assertion too.
	defer func() {
		select {
		case <-w.release:
		default:
			close(w.release)
		}
		<-done
	}()
	s.input(ref)
	s.Require().Eventually(func() bool {
		p, e := s.st.ListHistory(ref.SessionID, store.HistoryQuery{Limit: 1000})
		return e == nil && len(p.Events) == 140
	}, 6*time.Second, 5*time.Millisecond)
	close(w.release)
	<-done
	s.Contains(w.Body.String(), "event: subscription.error")
	s.NotContains(w.Body.String(), "event: subscription.closed")
	s.NotContains(w.Body.String(), "slow subscriber")
	s.T().Log("PASS synthetic transport write stall => Service overflow => explicit redacted SSE error, full 140-fact history preserved")
}
