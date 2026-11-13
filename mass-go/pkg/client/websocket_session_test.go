package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type typedSessionDriver struct {
	pipe *events.EventPipe

	mu           sync.Mutex
	next         int
	openInputs   []runtime.Input
	resumeInputs []runtime.Input
	plainOpens   int
	plainResumes int
}

func (d *typedSessionDriver) Events() (events.Stream, error) { return d.pipe, nil }
func (*typedSessionDriver) Start(context.Context) (runtime.Capabilities, error) {
	return runtime.Capabilities{"textInput": true}, nil
}

func (d *typedSessionDriver) OpenSession(_ context.Context, _ runtime.Address, spec runtime.SessionSpec) (runtime.DriverSession, error) {
	d.mu.Lock()
	d.plainOpens++
	d.mu.Unlock()
	return d.nativeSession(spec), nil
}

func (*typedSessionDriver) ValidateInitialInput(input runtime.Input) error {
	_, err := runtime.CopyInput(input)
	return err
}

func (d *typedSessionDriver) OpenSessionWithInput(_ context.Context, _ runtime.Address, spec runtime.SessionSpec, input runtime.Input) (runtime.DriverSession, error) {
	copied, err := runtime.CopyInput(input)
	if err != nil {
		return runtime.DriverSession{}, err
	}
	d.mu.Lock()
	d.openInputs = append(d.openInputs, copied)
	d.mu.Unlock()
	session := d.nativeSession(spec)
	session.InitialInputReceipt = &runtime.InputReceipt{
		Submission: "typed-create",
		Acceptance: runtime.Acceptance{Sent: runtime.Yes()},
	}
	return session, nil
}

func (d *typedSessionDriver) ResumeSession(_ context.Context, id string, _ runtime.Address, spec runtime.SessionSpec) (runtime.DriverSession, error) {
	d.mu.Lock()
	d.plainResumes++
	d.mu.Unlock()
	session := d.nativeSession(spec)
	session.SessionID = id
	return session, nil
}

func (d *typedSessionDriver) ResumeSessionWithInput(_ context.Context, id string, _ runtime.Address, spec runtime.SessionSpec, input runtime.Input) (runtime.DriverSession, error) {
	copied, err := runtime.CopyInput(input)
	if err != nil {
		return runtime.DriverSession{}, err
	}
	d.mu.Lock()
	d.resumeInputs = append(d.resumeInputs, copied)
	d.mu.Unlock()
	session := d.nativeSession(spec)
	session.SessionID = id
	session.InitialInputReceipt = &runtime.InputReceipt{
		Submission: "typed-resume",
		Acceptance: runtime.Acceptance{Sent: runtime.Yes()},
	}
	return session, nil
}

func (d *typedSessionDriver) nativeSession(spec runtime.SessionSpec) runtime.DriverSession {
	d.mu.Lock()
	d.next++
	id := fmt.Sprintf("typed/%d ?#", d.next)
	d.mu.Unlock()
	return runtime.DriverSession{
		SessionID:         id,
		Capabilities:      runtime.Capabilities{"textInput": true},
		EffectiveFeatures: runtime.ResolveSessionFeatures(spec.Features, runtime.FeatureSupport{AgentQuestions: true}),
		Source:            events.NewSource(),
	}
}

func (*typedSessionDriver) LoadSession(context.Context, string, runtime.Address, runtime.SessionSpec, string) error {
	return nil
}
func (*typedSessionDriver) CancelHistoryLoad(context.Context, string, string) error { return nil }
func (*typedSessionDriver) SendPrompt(context.Context, string, runtime.Input) (runtime.InputReceipt, error) {
	return runtime.InputReceipt{}, errors.New("unexpected prompt")
}

func (*typedSessionDriver) Cancel(context.Context, string) (runtime.CancelReceipt, error) {
	return runtime.CancelReceipt{Result: "noActiveWork"}, nil
}

func (*typedSessionDriver) AnswerInteraction(context.Context, string, string, any) (runtime.Acceptance, error) {
	return runtime.Acceptance{}, errors.New("unexpected answer")
}
func (*typedSessionDriver) CloseSession(context.Context, string) error { return nil }
func (d *typedSessionDriver) Stop(context.Context) error {
	d.pipe.End(nil)
	return nil
}

type typedSessionFactory struct{ driver *typedSessionDriver }

func (typedSessionFactory) Validate(runtime.Config) error { return nil }
func (f typedSessionFactory) New(string, runtime.Config) (runtime.Driver, error) {
	return f.driver, nil
}

func newTypedSessionServer(t *testing.T) (*httptest.Server, *typedSessionDriver, *file.Store) {
	t.Helper()
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	driver := &typedSessionDriver{pipe: events.NewEventPipe(32)}
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{
		"fixture": typedSessionFactory{driver: driver},
	}))
	_, err = svc.CreateRuntime(resource.Runtime{
		Metadata: resource.Metadata{Name: "agent"},
		Spec:     resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{}},
	})
	require.NoError(t, err)
	endpoint, err := transport.New(svc, withTestWriteTimeout(transport.Options{Token: "test-token"}))
	require.NoError(t, err)
	httpServer := httptest.NewServer(endpoint)
	t.Cleanup(func() {
		endpoint.Shutdown()
		httpServer.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
		require.NoError(t, st.Close())
	})
	return httpServer, driver, st
}

func TestClientSessionLifecycleAndHistory(t *testing.T) {
	httpServer, driver, st := newTypedSessionServer(t)
	wsClient := newTestClient(t, websocketURL(httpServer), "test-token")
	t.Cleanup(func() { _ = wsClient.Close() })

	initial := runtime.Input{Content: []events.ContentBlock{events.Text("create input")}}
	var created resource.SessionCreated
	require.NoError(t, wsClient.CreateSessionWithInput(t.Context(), resource.SessionCreateInput{
		Metadata:     resource.Metadata{Namespace: "demo", Name: "one"},
		Spec:         resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/work"}},
		InitialInput: &initial,
	}, &created))
	require.NotNil(t, created.InitialInputReceipt)
	require.Equal(t, "typed-create", created.InitialInputReceipt.Submission)
	require.Equal(t, runtime.Yes(), created.InitialInputReceipt.Acceptance.Sent)
	driver.mu.Lock()
	require.Len(t, driver.openInputs, 1)
	require.NotNil(t, driver.openInputs[0].Content[0].Text)
	require.Equal(t, "create input", driver.openInputs[0].Content[0].Text.Text)
	driver.mu.Unlock()

	var byID resource.Session
	require.NoError(t, wsClient.GetSession(t.Context(), resource.SessionRef{SessionID: created.Status.SessionID}, &byID))
	require.Equal(t, created.Session, byID)
	var byName resource.Session
	require.NoError(t, wsClient.GetSession(t.Context(), resource.SessionRef{Namespace: "demo", Name: "one"}, &byName))
	require.Equal(t, created.Session, byName)

	var second resource.Session
	require.NoError(t, wsClient.CreateSession(t.Context(), resource.Session{
		Metadata: resource.Metadata{Namespace: "demo", Name: "two"},
		Spec:     resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/work"}},
	}, &second))
	require.Equal(t, events.Unknown, second.Status.State)
	driver.mu.Lock()
	require.Equal(t, 1, driver.plainOpens)
	driver.mu.Unlock()

	var firstPage resource.SessionPage
	require.NoError(t, wsClient.ListSessions(t.Context(), resource.SessionQuery{Namespace: "demo", Limit: 1}, &firstPage))
	require.Len(t, firstPage.Items, 1)
	require.NotEmpty(t, firstPage.Next)
	var secondPage resource.SessionPage
	require.NoError(t, wsClient.ListSessions(t.Context(), resource.SessionQuery{
		Namespace: "demo", After: firstPage.Next, Limit: 1,
	}, &secondPage))
	require.Len(t, secondPage.Items, 1)
	require.NotEqual(t, firstPage.Items[0].Status.SessionID, secondPage.Items[0].Status.SessionID)

	var closed resource.Session
	require.NoError(t, wsClient.CloseSession(t.Context(), resource.SessionRef{Namespace: "demo", Name: "one"}, &closed))
	require.Equal(t, events.SessionState("offline"), closed.Status.State)

	resumeInput := runtime.Input{Content: []events.ContentBlock{events.Text("resume input")}}
	var resumed resource.SessionResumed
	require.NoError(t, wsClient.ResumeSessionWithInput(t.Context(), resource.SessionRef{SessionID: created.Status.SessionID}, resumeInput, &resumed))
	require.Equal(t, created.Status.SessionID, resumed.Status.SessionID)
	require.NotNil(t, resumed.InitialInputReceipt)
	require.Equal(t, "typed-resume", resumed.InitialInputReceipt.Submission)
	driver.mu.Lock()
	require.Len(t, driver.resumeInputs, 1)
	require.NotNil(t, driver.resumeInputs[0].Content[0].Text)
	require.Equal(t, "resume input", driver.resumeInputs[0].Content[0].Text.Text)
	driver.mu.Unlock()

	event, err := events.NewEvent(events.EventBase{
		EventID: uuid.NewString(), Namespace: "demo", SessionName: "one", SessionID: created.Status.SessionID,
		ItemID: "stored", Time: time.Now().UTC(), Meta: map[string]any{"large": json.Number("9007199254740993")},
	}, events.AgentMessage{MessageID: "stored", Content: []events.ContentBlock{events.Text("stored")}}, events.NewSource())
	require.NoError(t, err)
	_, err = st.Append(created.Status.SessionID, []events.SessionEvent{event})
	require.NoError(t, err)

	var history store.HistoryPage
	require.NoError(t, wsClient.History(t.Context(), resource.SessionRef{SessionID: created.Status.SessionID}, store.HistoryQuery{
		Types: []string{"agent.message"}, Limit: 1,
	}, &history))
	require.Len(t, history.Events, 1)
	require.NotNil(t, history.SyncedEventID)
	require.Equal(t, json.Number("9007199254740993"), history.Events[0].Meta["large"])

	var accepted resource.SessionLoadAccepted
	require.NoError(t, wsClient.LoadSession(t.Context(), resource.SessionLoad{
		Metadata:   resource.Metadata{Namespace: "demo", Name: "loaded"},
		RuntimeRef: "agent",
		Cwd:        "/work",
		SessionID:  "native/load ?#",
		Features:   &runtime.SessionFeatures{},
	}, &accepted))
	require.NotEmpty(t, accepted.LoadID)
	require.Equal(t, events.SessionState("opening"), accepted.Session.Status.State)
	require.NotNil(t, accepted.Session.Status.HistoryLoad)
	require.Equal(t, "accepted", accepted.Session.Status.HistoryLoad.Phase)

	var canceled resource.HistoryLoadStatus
	require.NoError(t, wsClient.CancelHistoryLoad(t.Context(), resource.SessionRef{SessionID: accepted.Session.Status.SessionID}, accepted.LoadID, &canceled))
	require.Equal(t, "canceled", canceled.Phase)

	var plainResumed resource.SessionResumed
	require.NoError(t, wsClient.CloseSession(t.Context(), resource.SessionRef{SessionID: created.Status.SessionID}, &closed))
	require.NoError(t, wsClient.ResumeSession(t.Context(), resource.SessionRef{SessionID: created.Status.SessionID}, &plainResumed))
	require.Nil(t, plainResumed.InitialInputReceipt)
	driver.mu.Lock()
	require.Equal(t, 1, driver.plainResumes)
	driver.mu.Unlock()
}

func TestClientSessionValidatesReferencesAndBusinessErrors(t *testing.T) {
	httpServer, _, _ := newTypedSessionServer(t)
	wsClient := newTestClient(t, websocketURL(httpServer), "test-token")
	t.Cleanup(func() { _ = wsClient.Close() })

	for _, ref := range []resource.SessionRef{
		{},
		{SessionID: "id", Namespace: "demo"},
		{SessionID: "id", Name: "name"},
		{Namespace: "demo"},
		{Name: "name"},
		{Namespace: "../bad", Name: "name"},
	} {
		err := wsClient.GetSession(t.Context(), ref, nil)
		require.Error(t, err)
	}
	require.Error(t, wsClient.ListSessions(t.Context(), resource.SessionQuery{Limit: -1}, nil))
	require.Error(t, wsClient.History(t.Context(), resource.SessionRef{SessionID: "id"}, store.HistoryQuery{Types: []string{}}, nil))
	require.Error(t, wsClient.CancelHistoryLoad(t.Context(), resource.SessionRef{SessionID: "id"}, "", nil))
	require.Error(t, wsClient.LoadSession(t.Context(), resource.SessionLoad{SessionID: "."}, nil))

	err := wsClient.GetSession(t.Context(), resource.SessionRef{SessionID: "missing"}, nil)
	var operation *runtime.OperationError
	require.ErrorAs(t, err, &operation)
	require.Equal(t, "not_found", operation.Code)
	require.Equal(t, "resource_not_found", operation.Reason)
	require.NotEmpty(t, operation.Message)
}
