package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/zoumo/mass/internal/massdtest"
	"github.com/zoumo/mass/internal/testpeer"
	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/eventstore"
	"github.com/zoumo/mass/massd/pkg/metastore"
	"github.com/zoumo/mass/massd/pkg/prototype"
	"github.com/zoumo/mass/massd/pkg/resource"
	"github.com/zoumo/mass/massd/pkg/runtime"
	"github.com/zoumo/mass/massd/pkg/runtime/claude"
	"github.com/zoumo/mass/massd/pkg/runtime/codex"
	"github.com/zoumo/mass/massd/pkg/service"
	"github.com/zoumo/mass/massd/pkg/store"
	"github.com/zoumo/mass/massd/pkg/transport"
)

type failureSuite struct {
	suite.Suite
	st      *massdtest.FileStores
	meta    metastore.MetaStore
	svc     *service.Service
	reg     *runtime.Registry
	unknown bool
	work    string
}

func TestFailureSuite(t *testing.T) { suite.Run(t, new(failureSuite)) }
func (s *failureSuite) SetupTest() {
	dir := s.T().TempDir()
	var err error
	s.st, err = massdtest.OpenFileStores(dir, false)
	s.Require().NoError(err)
	s.reg = runtime.NewRegistry(map[string]runtime.Factory{"codex": codex.Factory{RecoveryDir: filepath.Join(dir, "private")}, "claude": claude.Factory{RecoveryDir: filepath.Join(dir, "private")}})
	s.meta = s.st.Meta
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

func (s *failureSuite) start(kind, name string, history eventstore.EventStore) {
	path, work := nativeScenario(s.T(), kind, name)
	s.work = work
	s.svc = service.NewWithOptions(
		s.meta, history, s.reg,
		service.Options{
			SessionIdleTimeout: service.DefaultSessionIdleTimeout,
			InteractionPolicy:  prototype.AutoApprovePermissions,
		},
	)
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
		e, err := nextSubscription(sub, ctx(s.T()))
		s.Require().NoError(err)
		if p, ok := e.Payload().(events.StateUpdate); ok && ((p.State == nil && state == events.Unknown) || (p.State != nil && *p.State == state)) {
			return
		}
	}
}

func (s *failureSuite) cancel(kind string) {
	s.start(kind, "cancel", s.st.Events)
	ref := s.open("demo")
	sub, err := s.svc.SubscribeEvents(ref)
	s.Require().NoError(err)
	defer sub.Close()
	receipt := s.input(ref)
	s.Equal(runtime.Bool(true), receipt.Acceptance.Sent)
	s.until(sub, events.Running)
	ack, err := s.svc.Cancel(ctx(s.T()), ref)
	s.Require().NoError(err)
	s.Equal("controlAcknowledged", ack.Result)
	current, err := s.svc.GetSession(ref)
	s.Require().NoError(err)
	s.Equal(events.Running, current.Status.State)
	s.until(sub, events.Idle)
	if kind == "claude" {
		settled, err := s.svc.Cancel(ctx(s.T()), ref)
		s.Require().NoError(err)
		s.Equal("noActiveWork", settled.Result)
	}
	_, err = s.svc.CloseSession(ctx(s.T()), ref)
	s.Require().NoError(err)
	s.T().Log("PASS matched interrupt receipt acknowledges control; only native completion settles running work")
}
func (s *failureSuite) TestCodexCancelReceipt()  { s.cancel("codex") }
func (s *failureSuite) TestClaudeCancelReceipt() { s.cancel("claude") }
func requireObservationLost(t *testing.T, svc *service.Service, ref resource.SessionRef) {
	t.Helper()
	require.Eventually(t, func() bool {
		session, err := svc.GetSession(ref)
		return err == nil && session.Status.State == events.Unknown && session.Status.Reason == "session_observation_lost"
	}, 10*time.Second, 10*time.Millisecond)
}

func (s *failureSuite) disconnect(kind string) {
	s.start(kind, "disconnect", s.st.Events)
	s.unknown = true
	ref := s.open("demo")
	sub, err := s.svc.SubscribeEvents(ref)
	s.Require().NoError(err)
	defer sub.Close()
	receipt := s.input(ref)
	s.Equal(runtime.Bool(true), receipt.Acceptance.Sent)
	s.until(sub, events.Running)
	requireObservationLost(s.T(), s.svc, ref)
	// The returned ACK/write evidence is independent of subsequent observation loss.
	s.Equal(runtime.Bool(true), receipt.Acceptance.Sent)
	if kind == "codex" {
		s.Equal(runtime.Bool(true), receipt.Acceptance.NativeAccepted)
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
	cold := service.NewWithOptions(
		s.st.Meta, s.st.Events, runtime.NewRegistry(map[string]runtime.Factory{"codex": codex.Factory{RecoveryDir: s.T().TempDir()}, "claude": claude.Factory{RecoveryDir: s.T().TempDir()}}),
		service.Options{SessionIdleTimeout: service.DefaultSessionIdleTimeout},
	)
	_, err = cold.StartRuntime(ctx(s.T()), "agent")
	s.Require().ErrorContains(err, "previous runtime release")
	if kind == "codex" {
		// Explicit owner settlement can release the disconnected provider after
		// the first attempt reports observation loss. Read back that release.
		_, err = s.svc.StopRuntime(ctx(s.T()), "agent")
		s.Require().NoError(err)
		r, err = s.svc.GetRuntime("agent")
		s.Require().NoError(err)
		s.Equal("stopped", r.Status.State)
		s.unknown = false
	}
	s.T().Log("PASS EOF preserves acceptance, seals original binding and retains stopping ownership; no automatic resend")
}
func (s *failureSuite) TestCodexDisconnect()  { s.disconnect("codex") }
func (s *failureSuite) TestClaudeDisconnect() { s.disconnect("claude") }
func TestDriverSessionObservationAfterDisconnect(t *testing.T) {
	for _, tc := range []struct{ kind, code, reason string }{
		{"claude", "unavailable", "session_terminated"},
		{"codex", "outcome_unknown", "session_observation_eof"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			driver, _, pidFile := lifecycleDriver(t, tc.kind, "acknowledged")
			work, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			opened, err := driver.OpenSession(ctx(t), runtime.Address{Namespace: "default", SessionName: "active"}, runtime.SessionSpec{Cwd: work})
			require.NoError(t, err)
			require.NotNil(t, opened.Observation)
			_, err = driver.SendPrompt(ctx(t), opened.SessionID, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}})
			require.NoError(t, err)
			pid, err := os.ReadFile(pidFile)
			require.NoError(t, err)
			id, err := strconv.Atoi(string(pid))
			require.NoError(t, err)
			peer, err := os.FindProcess(id)
			require.NoError(t, err)
			require.NoError(t, peer.Kill())
			select {
			case <-opened.Observation.Done():
			case <-ctx(t).Done():
				t.Fatal("native disconnect did not end session observation")
			}
			var operation *runtime.OperationError
			require.ErrorAs(t, opened.Observation.Err(), &operation)
			require.Equal(t, tc.code, operation.Code)
			require.Equal(t, tc.reason, operation.Reason)
		})
	}
}

func (s *failureSuite) TestUnknownInputIsNotResent() {
	s.start("codex", "unknown-input", s.st.Events)
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
	eventstore.EventStore
	once    sync.Once
	entered chan struct{}
}

func (g *unknownAppend) Append(id string, records []events.SessionEvent) (*string, error) {
	fail := false
	g.once.Do(func() { fail = true })
	result, err := g.EventStore.Append(id, records)
	if fail && err == nil {
		close(g.entered)
		return result, errors.New("injected outcome_unknown after durable append")
	}
	return result, err
}

func (s *failureSuite) TestUnknownAppendExplicitSettlement() {
	fault := &unknownAppend{EventStore: s.st.Events, entered: make(chan struct{})}
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
	// No uncommitted fact is published; the healthy realtime stream remains open.
	wait, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	for {
		_, err := nextSubscription(sub, wait)
		if err != nil {
			s.Require().ErrorIs(err, context.DeadlineExceeded)
			break
		}
	}
	s.Require().NoError(sub.Err())
	page, err := s.st.Events.ListHistory(ref.SessionID, store.HistoryQuery{})
	s.Require().NoError(err)
	s.Require().Len(page.Events, 1)
	original := page.Events[0].EventID
	s.Require().Eventually(func() bool { return s.svc.ReconcileHistory(ctx(s.T()), ref) == nil }, time.Second, 5*time.Millisecond)
	s.Require().Eventually(func() bool {
		p, e := s.st.Events.ListHistory(ref.SessionID, store.HistoryQuery{})
		return e == nil && len(p.Events) == 4
	}, time.Second, 5*time.Millisecond)
	page, err = s.st.Events.ListHistory(ref.SessionID, store.HistoryQuery{})
	s.Require().NoError(err)
	s.Equal(original, page.Events[0].EventID)
	_, err = s.svc.SendPrompt(ctx(s.T()), ref, runtime.Input{Content: []events.ContentBlock{events.Text("sealed")}})
	s.Require().Error(err)
	s.T().Log("PASS written-but-unknown event not published; explicit full-prefix validation and same UUID settlement; admission stays sealed")
}

type slowAppend struct {
	eventstore.EventStore
	once             sync.Once
	entered, release chan struct{}
}

func (g *slowAppend) Append(id string, records []events.SessionEvent) (*string, error) {
	if id == "codex-session" {
		g.once.Do(func() { close(g.entered); <-g.release })
	}
	return g.EventStore.Append(id, records)
}

func (s *failureSuite) TestSlowStorageDoesNotBlockOtherSession() {
	gate := &slowAppend{EventStore: s.st.Events, entered: make(chan struct{}), release: make(chan struct{})}
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
	p, err := s.st.Events.ListHistory(b.SessionID, store.HistoryQuery{})
	s.Require().NoError(err)
	s.Len(p.Events, 4)
	p, err = s.st.Events.ListHistory(a.SessionID, store.HistoryQuery{})
	s.Require().NoError(err)
	s.Empty(p.Events)
	release()
	_, err = s.svc.CloseSession(ctx(s.T()), a)
	s.Require().NoError(err)
	_, err = s.svc.CloseSession(ctx(s.T()), b)
	s.Require().NoError(err)
	s.T().Log("PASS B completes and persists while A append remains blocked in its own lane")
}

func (s *failureSuite) TestLateCreateKeepsOriginalReservation() {
	s.start("codex", "late-create", s.st.Events)
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
	s.start("codex", "old-work", s.st.Events)
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
	gate := &slowAppend{EventStore: s.st.Events, entered: make(chan struct{}), release: make(chan struct{})}
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
	// Active-session overflow must start owned close, not stop the Runtime reader.
	s.Require().Eventually(func() bool {
		current, err := s.svc.GetSession(ref)
		return err == nil && current.Status.State == "closing"
	}, 2*time.Second, 5*time.Millisecond)
	_, err := s.svc.SendPrompt(ctx(s.T()), ref, runtime.Input{Content: []events.ContentBlock{events.Text("sealed")}})
	requireOverloadOrResumeRequired(s.T(), err)
	release()
	// All original facts remain queryable after the storage gate is released.
	deadline := time.Now().Add(20 * time.Second)
	count := 0
	for time.Now().Before(deadline) {
		p, e := s.st.Events.ListHistory(ref.SessionID, store.HistoryQuery{Limit: 1000})
		s.Require().NoError(e)
		count = len(p.Events)
		if count == 160 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	s.Require().Equal(160, count)
	s.T().Log("PASS native Codex overload fails locally, owns close, and settles all 160 original facts without reader reconciliation")
}

// Each native turn fits in the Service delivery queue. Await its live idle
// event before starting the next turn so only the unread subscriber overflows.
func (s *failureSuite) publishSlowSubscriberTurns(ref resource.SessionRef) {
	s.T().Helper()
	live, err := s.svc.SubscribeEvents(ref)
	s.Require().NoError(err)
	defer live.Close()
	for range 2 {
		s.input(ref)
		s.until(live, events.Running)
		s.until(live, events.Idle)
	}
}

func (s *failureSuite) requireSlowSubscriberHistory(ref resource.SessionRef) {
	s.T().Helper()
	page, err := s.svc.ListHistoryEvents(ref, store.HistoryQuery{Limit: 1000})
	s.Require().NoError(err)
	s.Require().Len(page.Events, 140)
}

func (s *failureSuite) TestSlowSubscriberDoesNotChangeHistory() {
	s.start("codex", "slow-subscriber", s.st.Events)
	ref := s.open("demo")
	sub, err := s.svc.SubscribeEvents(ref)
	s.Require().NoError(err)
	defer sub.Close()
	s.publishSlowSubscriberTurns(ref)
	s.requireSlowSubscriberHistory(ref)

	for range 128 {
		_, err = nextSubscription(sub, ctx(s.T()))
		s.Require().NoError(err)
	}
	_, err = nextSubscription(sub, ctx(s.T()))
	s.Require().ErrorIs(err, events.ErrSlowSubscriber)
}

func (s *failureSuite) TestStopTimeoutSealsAdmissionUntilRelease() {
	s.start("codex", "stop-permission", s.st.Events)
	ref := s.open("demo")
	sub, err := s.svc.SubscribeEvents(ref)
	s.Require().NoError(err)
	defer sub.Close()
	s.input(ref)
	handle := ""
	for handle == "" {
		e, err := nextSubscription(sub, ctx(s.T()))
		s.Require().NoError(err)
		if p, ok := e.Payload().(events.InteractionRequested); ok {
			handle = string(p.Interaction)
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
	_, err = s.svc.AnswerInteraction(ctx(s.T()), ref, handle, map[string]any{"action": "accept", "content": map[string]any{"answers": map[string]any{"Which format?": "json"}}})
	s.Require().Error(err)
	s.Require().Eventually(func() bool {
		session, err := s.svc.GetSession(ref)
		return err == nil && session.Status.State == "offline"
	}, 2*time.Second, 5*time.Millisecond, "original stop owner continues after caller timeout")
	_, err = s.svc.StopRuntime(ctx(s.T()), "agent")
	s.Require().NoError(err)
	s.T().Log("PASS stop timeout preserves stopping/closing and closed admission; late idle does not reopen; original release continues after caller timeout")
}

func (s *failureSuite) TestEarlyFactsWaitForIdentityCommit() {
	gate := &identityGate{MetaStore: s.st.Meta, entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	defer release()
	s.meta = gate
	s.start("claude", "early", s.st.Events)
	done := make(chan struct {
		session resource.Session
		err     error
	}, 1)
	go func() {
		session, err := s.svc.CreateSession(ctx(s.T()), s.spec("demo"))
		done <- struct {
			session resource.Session
			err     error
		}{session, err}
	}()
	select {
	case <-gate.entered:
	case <-ctx(s.T()).Done():
		s.FailNow("identity gate")
	}
	_, err := s.st.Meta.GetSession(resource.SessionRef{Namespace: "default", Name: "demo"})
	s.Require().ErrorIs(err, store.ErrNotFound)
	release()
	created := <-done
	s.Require().NoError(created.err)
	sessionID := created.session.Status.SessionID
	s.Require().Eventually(func() bool {
		p, e := s.st.Events.ListHistory(sessionID, store.HistoryQuery{})
		return e == nil && len(p.Events) == 3
	}, time.Second, 5*time.Millisecond)
	s.T().Log("PASS SDK-native pre-init complete facts stay bounded and invisible until persistent identity commit")
}

func (s *failureSuite) TestResumeEarlyCapacityFailsExplicitlyAndReleases() {
	path, work := nativeScenario(s.T(), "claude", "early")
	s.work = work
	config := s.T().TempDir()
	bin := filepath.Join(root, "resume-overflow-peer")
	out, err := exec.CommandContext(ctx(s.T()), "go", "build", "-o", bin, "./testdata/claude-linked-replay").CombinedOutput()
	s.Require().NoError(err, "%s", out)
	s.svc = service.NewWithOptions(
		s.meta, s.st.Events, s.reg,
		service.Options{SessionIdleTimeout: service.DefaultSessionIdleTimeout},
	)
	_, err = s.svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "claude", Config: runtime.Config{
		"executable": bin, "scenario": path, "env": testpeer.RuntimeEnv(map[string]string{"CLAUDE_CONFIG_DIR": config}),
	}}})
	s.Require().NoError(err)
	_, err = s.svc.StartRuntime(ctx(s.T()), "agent")
	s.Require().NoError(err)
	ref := s.open("demo")
	s.Require().Eventually(func() bool {
		page, err := s.st.Events.ListHistory(ref.SessionID, store.HistoryQuery{})
		return err == nil && len(page.Events) == 3
	}, time.Second, 5*time.Millisecond)
	_, err = s.svc.CloseSession(ctx(s.T()), ref)
	s.Require().NoError(err)
	prepareClaudeSavedSession(s.T(), config, work, ref.SessionID)
	overflow, overflowWork := nativeScenario(s.T(), "claude", "early-overflow")
	data, err := os.ReadFile(overflow)
	s.Require().NoError(err)
	data = bytes.ReplaceAll(data, []byte(overflowWork), []byte(work))
	data = bytes.ReplaceAll(data, []byte(`"claude-session"`), []byte(`"`+ref.SessionID+`"`))
	s.Require().NoError(os.WriteFile(path, data, 0o600))
	_, err = s.svc.ResumeSession(ctx(s.T()), ref)
	var operation *runtime.OperationError
	s.Require().ErrorAs(err, &operation)
	s.Equal("resource_exhausted", operation.Code)
	s.Equal("initialize_buffer_exhausted", operation.Reason)
	s.Require().ErrorIs(err, runtime.ErrResourcesReleased)
	current, err := s.svc.GetSession(ref)
	s.Require().NoError(err)
	s.Equal(events.SessionState("offline"), current.Status.State)
	page, err := s.st.Events.ListHistory(ref.SessionID, store.HistoryQuery{})
	s.Require().NoError(err)
	s.Len(page.Events, 3)
}

func (s *failureSuite) TestCancellationDuringIdentityCommitSettlesOriginalOwner() {
	gate := &identityGate{MetaStore: s.st.Meta, entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	defer release()
	s.meta = gate
	s.start("claude", "early", s.st.Events)
	caller, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.svc.CreateSession(caller, s.spec("demo")); done <- err }()
	// Start caller cancellation only after native open reached the identity
	// commit boundary; process startup time must not select a different scenario.
	select {
	case <-gate.entered:
	case <-ctx(s.T()).Done():
		s.FailNow("native open did not reach identity commit")
	}
	cancel()
	s.Require().ErrorContains(<-done, "outcome_unknown")
	release()
	_, err := s.svc.StopRuntime(ctx(s.T()), "agent")
	s.Require().NoError(err)
	r, err := s.svc.GetSession(resource.SessionRef{Namespace: "default", Name: "demo"})
	s.Require().NoError(err)
	s.Equal(events.SessionState("offline"), r.Status.State)
	p, err := s.st.Events.ListHistory(r.Status.SessionID, store.HistoryQuery{})
	s.Require().NoError(err)
	s.Len(p.Events, 3)
}

func (s *failureSuite) TestLateAnswerCannotControlRecoveredRun() {
	s.start("codex", "late-answer", s.st.Events)
	ref := s.open("demo")
	sub, err := s.svc.SubscribeEvents(ref)
	s.Require().NoError(err)
	defer sub.Close()
	s.input(ref)
	old := ""
	for old == "" {
		e, err := nextSubscription(sub, ctx(s.T()))
		s.Require().NoError(err)
		if p, ok := e.Payload().(events.InteractionRequested); ok {
			old = string(p.Interaction)
		}
	}
	s.until(sub, events.Idle)
	_, err = s.svc.CloseSession(ctx(s.T()), ref)
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

type unknownIdentity struct {
	metastore.MetaStore
	id string
}

func (g *unknownIdentity) CreateSession(r resource.Session) (resource.Session, error) {
	value, err := g.MetaStore.CreateSession(r)
	if err != nil {
		return value, err
	}
	g.id = value.Status.SessionID
	return value, errors.New("injected unknown identity commit")
}

func (s *failureSuite) TestUnknownIdentityCommitReconcilesOriginalFacts() {
	identity := &unknownIdentity{MetaStore: s.st.Meta}
	s.meta = identity
	s.start("claude", "early", s.st.Events)
	_, err := s.svc.CreateSession(ctx(s.T()), s.spec("demo"))
	s.Require().ErrorContains(err, "unknown identity commit")
	_, err = s.svc.CreateSession(ctx(s.T()), s.spec("demo"))
	s.Require().ErrorIs(err, store.ErrConflict)
	page, err := s.st.Events.ListHistory(identity.id, store.HistoryQuery{})
	s.Require().NoError(err)
	s.Empty(page.Events)
	err = s.svc.ReconcileOpening(ctx(s.T()), "agent", runtime.Address{Namespace: "default", SessionName: "demo"})
	s.Require().NoError(err)
	r, err := s.svc.GetSession(resource.SessionRef{Namespace: "default", Name: "demo"})
	s.Require().NoError(err)
	s.Equal(events.SessionState("offline"), r.Status.State)
	page, err = s.st.Events.ListHistory(r.Status.SessionID, store.HistoryQuery{})
	s.Require().NoError(err)
	s.Len(page.Events, 3)
	s.T().Log("PASS unknown identity commit preserves reservation and early facts until explicit original-owner readback and release")
}

func (s *failureSuite) TestConcurrentStopSharesOriginalResponsibility() {
	s.start("codex", "cancel", s.st.Events)
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

type overflowSubscriptionAPI struct {
	*service.Service

	broadcast  *events.EventBroadcast
	prePublish []events.SessionEvent
}

func (a *overflowSubscriptionAPI) SubscribeEvents(resource.SessionRef) (*service.Subscription, error) {
	subscription, err := a.broadcast.Subscribe()
	if err != nil {
		return nil, err
	}
	for _, event := range a.prePublish {
		if err := a.broadcast.Publish(event); err != nil {
			_ = subscription.Close()
			return nil, err
		}
	}
	return subscription, nil
}

type subscriptionOverflowFrame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Result  json.RawMessage `json:"result"`
	Params  json.RawMessage `json:"params"`
	Error   json.RawMessage `json:"error"`
}

func dialSubscriptionOverflow(t *testing.T, server *httptest.Server, ref resource.SessionRef) (*websocket.Conn, string) {
	t.Helper()
	conn, response, err := websocket.DefaultDialer.DialContext(ctx(t), "ws"+strings.TrimPrefix(server.URL, "http")+"/jsonrpc", nil)
	if response != nil {
		_ = response.Body.Close()
	}
	requireNoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	requireNoError(t, conn.WriteJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      "init",
		"method":  "initialize",
		"params":  map[string]string{"protocolVersion": "1"},
	}))
	readSubscriptionOverflowFrame(t, conn)
	requireNoError(t, conn.WriteJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      "subscribe",
		"method":  "session.subscribe",
		"params":  map[string]any{"session": ref},
	}))
	frame := readSubscriptionOverflowFrame(t, conn)
	var result struct {
		SubscriptionID string `json:"subscriptionId"`
	}
	requireNoError(t, json.Unmarshal(frame.Result, &result))
	if result.SubscriptionID == "" {
		t.Fatal("subscription response did not contain an ID")
	}
	return conn, result.SubscriptionID
}

func readSubscriptionOverflowFrame(t *testing.T, conn *websocket.Conn) subscriptionOverflowFrame {
	t.Helper()
	requireNoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	messageType, raw, err := conn.ReadMessage()
	requireNoError(t, err)
	if messageType != websocket.TextMessage {
		t.Fatalf("unexpected WebSocket message type %d", messageType)
	}
	var frame subscriptionOverflowFrame
	requireNoError(t, json.Unmarshal(raw, &frame))
	return frame
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (s *failureSuite) TestSubscriptionOverflowIsExplicit() {
	s.start("codex", "slow-subscriber", s.st.Events)
	ref := s.open("demo")
	s.publishSlowSubscriberTurns(ref)
	s.requireSlowSubscriberHistory(ref)

	session, err := s.svc.GetSession(ref)
	s.Require().NoError(err)
	history, err := s.svc.ListHistoryEvents(ref, store.HistoryQuery{Limit: 2})
	s.Require().NoError(err)
	s.Require().Len(history.Events, 2)
	broadcast, err := events.NewBroadcast(session.Metadata.Namespace, session.Metadata.Name, ref.SessionID, 1)
	s.Require().NoError(err)
	// This injects an already-overflowed EventBroadcast into the transport. The
	// real Service overflow path is covered by TestSlowSubscriberDoesNotChangeHistory.
	api := &overflowSubscriptionAPI{
		Service:    s.svc,
		broadcast:  broadcast,
		prePublish: history.Events,
	}
	handler, err := transport.New(api, transport.Options{WriteTimeout: 30 * time.Second})
	s.Require().NoError(err)
	defer handler.Shutdown()
	server := httptest.NewServer(handler)
	defer server.Close()
	conn, subscriptionID := dialSubscriptionOverflow(s.T(), server, ref)

	first := readSubscriptionOverflowFrame(s.T(), conn)
	s.Equal("session.event", first.Method)
	var firstParams struct {
		SubscriptionID string              `json:"subscriptionId"`
		Event          events.SessionEvent `json:"event"`
	}
	s.Require().NoError(json.Unmarshal(first.Params, &firstParams))
	s.Equal(subscriptionID, firstParams.SubscriptionID)
	s.Equal(history.Events[0].EventID, firstParams.Event.EventID)

	closed := readSubscriptionOverflowFrame(s.T(), conn)
	s.Equal("subscription.closed", closed.Method)
	var closedParams struct {
		SubscriptionID string                  `json:"subscriptionId"`
		Error          *runtime.OperationError `json:"error"`
	}
	s.Require().NoError(json.Unmarshal(closed.Params, &closedParams))
	s.Equal(subscriptionID, closedParams.SubscriptionID)
	s.Require().NotNil(closedParams.Error)
	s.Equal("resource_exhausted", closedParams.Error.Code)
	s.Equal("slow_subscriber", closedParams.Error.Reason)
	s.Equal("Observation capacity exceeded", closedParams.Error.Message)
	s.NotContains(closedParams.Error.Message, "slow subscriber")
	s.requireSlowSubscriberHistory(ref)
	s.T().Log("PASS overflowed EventBroadcast maps to one historical event followed by explicit redacted subscription.closed; full 140-fact history preserved")
}
