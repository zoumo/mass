package transport_test

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

type sessionRPCDriver struct {
	pipe *events.EventPipe

	mu              sync.Mutex
	next            int
	openInputs      []*runtime.Input
	resumeInputs    []*runtime.Input
	plainOpens      int
	plainResumes    int
	loadEntry       chan struct{}
	loadRelease     chan struct{}
	blockNativeLoad bool
}

func newSessionRPCDriver() *sessionRPCDriver {
	return &sessionRPCDriver{
		pipe:        events.NewEventPipe(32),
		loadEntry:   make(chan struct{}, 1),
		loadRelease: make(chan struct{}),
	}
}

func (d *sessionRPCDriver) Events() (events.Stream, error) { return d.pipe, nil }
func (d *sessionRPCDriver) Start(context.Context) (runtime.Capabilities, error) {
	return runtime.Capabilities{"textInput": true}, nil
}

func (d *sessionRPCDriver) OpenSession(_ context.Context, _ runtime.Address, spec runtime.SessionSpec) (runtime.DriverSession, error) {
	d.mu.Lock()
	d.plainOpens++
	d.mu.Unlock()
	return d.nativeSession(spec), nil
}

func (d *sessionRPCDriver) ValidateInitialInput(input runtime.Input) error {
	_, err := runtime.CopyInput(input)
	return err
}

func (d *sessionRPCDriver) OpenSessionWithInput(_ context.Context, _ runtime.Address, spec runtime.SessionSpec, input runtime.Input) (runtime.DriverSession, error) {
	copied, err := runtime.CopyInput(input)
	if err != nil {
		return runtime.DriverSession{}, err
	}
	d.mu.Lock()
	d.openInputs = append(d.openInputs, &copied)
	d.mu.Unlock()
	session := d.nativeSession(spec)
	session.InitialInputReceipt = &runtime.InputReceipt{
		Submission: "create-submission",
		Acceptance: runtime.Acceptance{Sent: runtime.Yes()},
	}
	return session, nil
}

func (d *sessionRPCDriver) ResumeSession(_ context.Context, id string, _ runtime.Address, spec runtime.SessionSpec) (runtime.DriverSession, error) {
	d.mu.Lock()
	d.plainResumes++
	d.mu.Unlock()
	session := d.nativeSession(spec)
	session.SessionID = id
	return session, nil
}

func (d *sessionRPCDriver) ResumeSessionWithInput(_ context.Context, id string, _ runtime.Address, spec runtime.SessionSpec, input runtime.Input) (runtime.DriverSession, error) {
	copied, err := runtime.CopyInput(input)
	if err != nil {
		return runtime.DriverSession{}, err
	}
	d.mu.Lock()
	d.resumeInputs = append(d.resumeInputs, &copied)
	d.mu.Unlock()
	session := d.nativeSession(spec)
	session.SessionID = id
	session.InitialInputReceipt = &runtime.InputReceipt{
		Submission: "resume-submission",
		Acceptance: runtime.Acceptance{Sent: runtime.Yes()},
	}
	return session, nil
}

func (d *sessionRPCDriver) nativeSession(spec runtime.SessionSpec) runtime.DriverSession {
	d.mu.Lock()
	d.next++
	id := fmt.Sprintf("native/%d ?#", d.next)
	d.mu.Unlock()
	return runtime.DriverSession{
		SessionID:         id,
		Capabilities:      runtime.Capabilities{"textInput": true},
		EffectiveFeatures: runtime.ResolveSessionFeatures(spec.Features, runtime.FeatureSupport{AgentQuestions: true}),
		Source:            events.NewSource(),
	}
}

func (d *sessionRPCDriver) LoadSession(ctx context.Context, _ string, _ runtime.Address, _ runtime.SessionSpec, _ string) error {
	if d.blockNativeLoad {
		select {
		case d.loadEntry <- struct{}{}:
		default:
		}
		select {
		case <-d.loadRelease:
		case <-ctx.Done():
		}
	}
	return nil
}

func (*sessionRPCDriver) CancelHistoryLoad(context.Context, string, string) error { return nil }
func (*sessionRPCDriver) SendPrompt(context.Context, string, runtime.Input) (runtime.InputReceipt, error) {
	return runtime.InputReceipt{}, errors.New("unexpected prompt")
}

func (*sessionRPCDriver) Cancel(context.Context, string) (runtime.CancelReceipt, error) {
	return runtime.CancelReceipt{Result: "noActiveWork"}, nil
}

func (*sessionRPCDriver) AnswerInteraction(context.Context, string, string, any) (runtime.Acceptance, error) {
	return runtime.Acceptance{}, errors.New("unexpected answer")
}
func (*sessionRPCDriver) CloseSession(context.Context, string) error { return nil }
func (d *sessionRPCDriver) Stop(context.Context) error {
	d.pipe.End(nil)
	return nil
}

type sessionRPCFactory struct{ driver *sessionRPCDriver }

func (sessionRPCFactory) Validate(runtime.Config) error { return nil }
func (f sessionRPCFactory) New(string, runtime.Config) (runtime.Driver, error) {
	return f.driver, nil
}

type sessionRPCFixture struct {
	server *httptest.Server
	svc    *service.Service
	store  *file.Store
	driver *sessionRPCDriver
}

func newSessionRPCFixture(t *testing.T, blockLoad bool) *sessionRPCFixture {
	t.Helper()
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	driver := newSessionRPCDriver()
	driver.blockNativeLoad = blockLoad
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{
		"fixture": sessionRPCFactory{driver: driver},
	}))
	_, err = svc.CreateRuntime(resource.Runtime{
		Metadata: resource.Metadata{Name: "agent"},
		Spec:     resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{}},
	})
	require.NoError(t, err)
	endpoint, err := transport.New(svc, transport.Options{Token: "test-token"})
	require.NoError(t, err)
	httpServer := httptest.NewServer(endpoint)
	fixture := &sessionRPCFixture{server: httpServer, svc: svc, store: st, driver: driver}
	t.Cleanup(func() {
		closeSignal(driver.loadRelease)
		endpoint.Shutdown()
		httpServer.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
		require.NoError(t, st.Close())
	})
	return fixture
}

func closeSignal(ch chan struct{}) {
	if ch == nil {
		return
	}
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func TestRPCSessionLifecycleHistoryAndExplicitReferences(t *testing.T) {
	fixture := newSessionRPCFixture(t, false)
	conn := dialRPC(t, fixture.server, rpcHeaders("test-token"))
	sendText(t, conn, initializeMessage(`"init"`, "1"))
	require.Nil(t, readRPC(t, conn).Error)

	sendCall(t, conn, "create-input", "session.create", `{
		"input":{
			"metadata":{"namespace":"demo","name":"one"},
			"spec":{"runtimeRef":"agent","cwd":"/work","features":{"agentQuestions":false}},
			"initialInput":{"content":[{"type":"text","text":"explicit create"}]}
		}
	}`)
	createdResponse := readRPC(t, conn)
	require.Nil(t, createdResponse.Error)
	var created resource.SessionCreated
	require.NoError(t, json.Unmarshal(createdResponse.Result, &created))
	require.Equal(t, "demo", created.Metadata.Namespace)
	require.Equal(t, "one", created.Metadata.Name)
	require.NotEmpty(t, created.Status.SessionID)
	require.NotNil(t, created.InitialInputReceipt)
	require.Equal(t, "create-submission", created.InitialInputReceipt.Submission)
	require.Equal(t, runtime.Yes(), created.InitialInputReceipt.Acceptance.Sent)
	fixture.driver.mu.Lock()
	require.Len(t, fixture.driver.openInputs, 1)
	require.Equal(t, "explicit create", fixture.driver.openInputs[0].Content[0].Text)
	fixture.driver.mu.Unlock()

	sendCall(t, conn, "create-empty", "session.create", `{
		"input":{"metadata":{"namespace":"demo","name":"two"},"spec":{"runtimeRef":"agent","cwd":"/work"}}
	}`)
	require.Nil(t, readRPC(t, conn).Error)
	fixture.driver.mu.Lock()
	require.Equal(t, 1, fixture.driver.plainOpens)
	fixture.driver.mu.Unlock()

	sendCall(t, conn, "get-id", "session.get", fmt.Sprintf(`{"session":{"sessionId":%q}}`, created.Status.SessionID))
	byID := readRPC(t, conn)
	require.Nil(t, byID.Error)
	var gotByID resource.Session
	require.NoError(t, json.Unmarshal(byID.Result, &gotByID))
	require.Equal(t, created.Session, gotByID)
	require.NotContains(t, string(byID.Result), "initialInputReceipt")

	sendCall(t, conn, "get-name", "session.get", `{"session":{"namespace":"demo","name":"one"}}`)
	byName := readRPC(t, conn)
	require.Nil(t, byName.Error)
	var gotByName resource.Session
	require.NoError(t, json.Unmarshal(byName.Result, &gotByName))
	require.Equal(t, created.Session, gotByName)

	sendCall(t, conn, "list-first", "session.list", `{"query":{"namespace":"demo","page":{"limit":1}}}`)
	firstList := readRPC(t, conn)
	require.Nil(t, firstList.Error)
	var firstPage resource.SessionPage
	require.NoError(t, json.Unmarshal(firstList.Result, &firstPage))
	require.Len(t, firstPage.Items, 1)
	require.NotEmpty(t, firstPage.Next)

	sendCall(t, conn, "list-next", "session.list", fmt.Sprintf(`{"query":{"namespace":"demo","page":{"after":%q,"limit":1}}}`, firstPage.Next))
	secondList := readRPC(t, conn)
	require.Nil(t, secondList.Error)
	var secondPage resource.SessionPage
	require.NoError(t, json.Unmarshal(secondList.Result, &secondPage))
	require.Len(t, secondPage.Items, 1)
	require.NotEqual(t, firstPage.Items[0].Status.SessionID, secondPage.Items[0].Status.SessionID)

	sendCall(t, conn, "close-name", "session.close", `{"session":{"namespace":"demo","name":"one"}}`)
	closedResponse := readRPC(t, conn)
	require.Nil(t, closedResponse.Error)
	var closed resource.Session
	require.NoError(t, json.Unmarshal(closedResponse.Result, &closed))
	require.Equal(t, events.SessionState("offline"), closed.Status.State)

	sendCall(t, conn, "resume-input", "session.resume", fmt.Sprintf(`{
		"session":{"sessionId":%q},
		"input":{"initialInput":{"content":[{"type":"text","text":"explicit resume"}]}}
	}`, created.Status.SessionID))
	resumedResponse := readRPC(t, conn)
	require.Nil(t, resumedResponse.Error)
	var resumed resource.SessionResumed
	require.NoError(t, json.Unmarshal(resumedResponse.Result, &resumed))
	require.Equal(t, created.Status.SessionID, resumed.Status.SessionID)
	require.NotNil(t, resumed.InitialInputReceipt)
	require.Equal(t, "resume-submission", resumed.InitialInputReceipt.Submission)
	fixture.driver.mu.Lock()
	require.Len(t, fixture.driver.resumeInputs, 1)
	require.Equal(t, "explicit resume", fixture.driver.resumeInputs[0].Content[0].Text)
	fixture.driver.mu.Unlock()

	event, err := events.NewEvent(events.EventBase{
		EventID: uuid.NewString(), Namespace: "demo", SessionName: "one", SessionID: created.Status.SessionID,
		ItemID: "stored", Time: time.Now().UTC(), Meta: map[string]any{"large": json.Number("9007199254740993")},
	}, events.AgentMessage{MessageID: "stored", Content: []events.ContentBlock{events.Text("stored")}}, events.NewSource())
	require.NoError(t, err)
	_, err = fixture.store.Append(created.Status.SessionID, []events.SessionEvent{event})
	require.NoError(t, err)

	sendCall(t, conn, "history", "session.history", fmt.Sprintf(`{
		"session":{"sessionId":%q},
		"query":{"types":["agent.message"],"limit":1}
	}`, created.Status.SessionID))
	historyResponse := readRPC(t, conn)
	require.Nil(t, historyResponse.Error)
	var history store.HistoryPage
	require.NoError(t, json.Unmarshal(historyResponse.Result, &history))
	require.Len(t, history.Events, 1)
	require.NotNil(t, history.SyncedEventID)
	require.Contains(t, string(historyResponse.Result), `"large":9007199254740993`)

	sendCall(t, conn, "close-id", "session.close", fmt.Sprintf(`{"session":{"sessionId":%q}}`, created.Status.SessionID))
	require.Nil(t, readRPC(t, conn).Error)
	sendCall(t, conn, "resume-plain", "session.resume", fmt.Sprintf(`{"session":{"sessionId":%q}}`, created.Status.SessionID))
	require.Nil(t, readRPC(t, conn).Error)
	fixture.driver.mu.Lock()
	require.Equal(t, 1, fixture.driver.plainResumes)
	fixture.driver.mu.Unlock()
}

func TestRPCSessionInvalidParamsBusinessErrorsAndKnownMethods(t *testing.T) {
	fixture := newSessionRPCFixture(t, false)

	beforeInit := dialRPC(t, fixture.server, rpcHeaders("test-token"))
	sendCall(t, beforeInit, "before", "session.get", `{"session":{"sessionId":"native/1 ?#"}}`)
	requireRPCError(t, readRPC(t, beforeInit), -32001, `"before"`, "not_initialized")

	conn := dialRPC(t, fixture.server, rpcHeaders("test-token"))
	sendText(t, conn, initializeMessage(`"init"`, "1"))
	require.Nil(t, readRPC(t, conn).Error)

	for _, test := range []struct {
		name   string
		method string
		params string
	}{
		{name: "create missing input", method: "session.create", params: `{}`},
		{name: "create null input", method: "session.create", params: `{"input":null}`},
		{name: "create missing metadata", method: "session.create", params: `{"input":{"spec":{"runtimeRef":"agent","cwd":"/work"}}}`},
		{name: "create missing name", method: "session.create", params: `{"input":{"metadata":{"namespace":"demo"},"spec":{"runtimeRef":"agent","cwd":"/work"}}}`},
		{name: "create readonly metadata", method: "session.create", params: `{"input":{"metadata":{"namespace":"demo","name":"bad","uid":"system"},"spec":{"runtimeRef":"agent","cwd":"/work"}}}`},
		{name: "create readonly status", method: "session.create", params: `{"input":{"metadata":{"namespace":"demo","name":"bad"},"spec":{"runtimeRef":"agent","cwd":"/work"},"status":{}}}`},
		{name: "create null spec", method: "session.create", params: `{"input":{"metadata":{"namespace":"demo","name":"bad"},"spec":null}}`},
		{name: "create null initial input", method: "session.create", params: `{"input":{"metadata":{"namespace":"demo","name":"bad"},"spec":{"runtimeRef":"agent","cwd":"/work"},"initialInput":null}}`},
		{name: "get missing session", method: "session.get", params: `{}`},
		{name: "get null session", method: "session.get", params: `{"session":null}`},
		{name: "get mixed union", method: "session.get", params: `{"session":{"sessionId":"native","namespace":"demo","name":"one"}}`},
		{name: "get partial name union", method: "session.get", params: `{"session":{"namespace":"demo"}}`},
		{name: "get null id", method: "session.get", params: `{"session":{"sessionId":null}}`},
		{name: "get empty id", method: "session.get", params: `{"session":{"sessionId":""}}`},
		{name: "get unknown ref field", method: "session.get", params: `{"session":{"sessionId":"native","extra":true}}`},
		{name: "list readonly session id", method: "session.list", params: `{"query":{"sessionId":"native"}}`},
		{name: "list unknown query field", method: "session.list", params: `{"query":{"kind":"one"}}`},
		{name: "list null query", method: "session.list", params: `{"query":null}`},
		{name: "list unknown page field", method: "session.list", params: `{"query":{"page":{"kind":"one"}}}`},
		{name: "list zero limit", method: "session.list", params: `{"query":{"page":{"limit":0}}}`},
		{name: "resume null input", method: "session.resume", params: `{"session":{"sessionId":"native"},"input":null}`},
		{name: "resume null initial input", method: "session.resume", params: `{"session":{"sessionId":"native"},"input":{"initialInput":null}}`},
		{name: "resume unknown input field", method: "session.resume", params: `{"session":{"sessionId":"native"},"input":{"input":"text"}}`},
		{name: "close unknown field", method: "session.close", params: `{"session":{"sessionId":"native"},"wait":true}`},
		{name: "load missing input", method: "session.load", params: `{}`},
		{name: "load null input", method: "session.load", params: `{"input":null}`},
		{name: "load readonly metadata", method: "session.load", params: `{"input":{"metadata":{"namespace":"demo","name":"loaded","uid":"system"},"runtimeRef":"agent","cwd":"/work","sessionId":"native"}}`},
		{name: "load readonly status", method: "session.load", params: `{"input":{"metadata":{"namespace":"demo","name":"loaded"},"runtimeRef":"agent","cwd":"/work","sessionId":"native","status":{}}}`},
		{name: "load missing runtime", method: "session.load", params: `{"input":{"metadata":{"namespace":"demo","name":"loaded"},"cwd":"/work","sessionId":"native"}}`},
		{name: "load missing session id", method: "session.load", params: `{"input":{"metadata":{"namespace":"demo","name":"loaded"},"runtimeRef":"agent","cwd":"/work"}}`},
		{name: "cancel load missing id", method: "session.cancelLoad", params: `{"session":{"sessionId":"native"}}`},
		{name: "cancel load null id", method: "session.cancelLoad", params: `{"session":{"sessionId":"native"},"loadId":null}`},
		{name: "cancel load unknown field", method: "session.cancelLoad", params: `{"session":{"sessionId":"native"},"loadId":"original","force":true}`},
		{name: "history unknown query field", method: "session.history", params: `{"session":{"sessionId":"native"},"query":{"direction":"next"}}`},
		{name: "history empty types", method: "session.history", params: `{"session":{"sessionId":"native"},"query":{"types":[]}}`},
		{name: "history invalid type", method: "session.history", params: `{"session":{"sessionId":"native"},"query":{"types":["made.up"]}}`},
		{name: "history null types", method: "session.history", params: `{"session":{"sessionId":"native"},"query":{"types":null}}`},
		{name: "history zero limit", method: "session.history", params: `{"session":{"sessionId":"native"},"query":{"limit":0}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			id := "invalid-" + test.name
			sendCall(t, conn, id, test.method, test.params)
			requireRPCError(t, readRPC(t, conn), -32602, fmt.Sprintf("%q", id), "")
		})
	}

	sendCall(t, conn, "invalid-id", "session.get", `{"session":{"sessionId":"."}}`)
	requireServiceRPCError(t, readRPC(t, conn), "invalid_argument", "invalid_session_id")

	sendCall(t, conn, "uuid", "session.create", `{"input":{"metadata":{"namespace":"demo","name":"00000000-0000-0000-0000-000000000000"},"spec":{"runtimeRef":"agent","cwd":"/work"}}}`)
	requireServiceRPCError(t, readRPC(t, conn), "invalid_argument", "session_name_is_uuid")

	sendCall(t, conn, "seed", "session.create", `{"input":{"metadata":{"namespace":"demo","name":"duplicate"},"spec":{"runtimeRef":"agent","cwd":"/work"}}}`)
	require.Nil(t, readRPC(t, conn).Error)
	sendCall(t, conn, "duplicate", "session.create", `{"input":{"metadata":{"namespace":"demo","name":"duplicate"},"spec":{"runtimeRef":"agent","cwd":"/work"}}}`)
	requireServiceRPCError(t, readRPC(t, conn), "conflict", "resource_conflict")

	sendCall(t, conn, "unknown-session", "session.prompt", `{"session":{"sessionId":"native"},"input":{"content":[]}}`)
	requireRPCError(t, readRPC(t, conn), -32601, `"unknown-session"`, "")
}

type blockingSessionAPI struct {
	transport.API
	entered chan struct{}
	release chan struct{}
}

func (a *blockingSessionAPI) CreateSessionWithInput(context.Context, resource.SessionCreateInput) (resource.SessionCreated, error) {
	select {
	case a.entered <- struct{}{}:
	default:
	}
	<-a.release
	return resource.SessionCreated{Session: resource.Session{Status: resource.SessionStatus{SessionID: "slow"}}}, nil
}

func (*blockingSessionAPI) ListSessions(resource.SessionQuery) (resource.SessionPage, error) {
	return resource.SessionPage{Items: []resource.Session{}}, nil
}

func TestRPCSlowSessionCallDoesNotBlockReadLoop(t *testing.T) {
	api := &blockingSessionAPI{entered: make(chan struct{}, 1), release: make(chan struct{})}
	server, err := transport.New(api, transport.Options{Token: "test-token"})
	require.NoError(t, err)
	t.Cleanup(server.Shutdown)
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	conn := dialRPC(t, httpServer, rpcHeaders("test-token"))
	sendText(t, conn, initializeMessage(`"init"`, "1"))
	require.Nil(t, readRPC(t, conn).Error)

	sendCall(t, conn, "slow", "session.create", `{"input":{"metadata":{"namespace":"demo","name":"slow"},"spec":{"runtimeRef":"agent","cwd":"/work"},"initialInput":{"content":[{"type":"text","text":"slow"}]}}}`)
	select {
	case <-api.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("slow create was not dispatched")
	}

	sendCall(t, conn, "fast", "session.list", `{"query":{"page":{"limit":1}}}`)
	fast := readRPC(t, conn)
	require.Nil(t, fast.Error)
	require.Equal(t, `"fast"`, string(fast.ID))
	require.Contains(t, string(fast.Result), `"items":[]`)

	close(api.release)
	slow := readRPC(t, conn)
	require.Nil(t, slow.Error)
	require.Equal(t, `"slow"`, string(slow.ID))
	require.Contains(t, string(slow.Result), `"sessionId":"slow"`)
}

func TestRPCSessionLoadReturnsAcceptanceWhileNativeLoadContinues(t *testing.T) {
	fixture := newSessionRPCFixture(t, true)
	_, err := fixture.svc.StartRuntime(t.Context(), "agent")
	require.NoError(t, err)
	conn := dialRPC(t, fixture.server, rpcHeaders("test-token"))
	sendText(t, conn, initializeMessage(`"init"`, "1"))
	require.Nil(t, readRPC(t, conn).Error)

	sendCall(t, conn, "load", "session.load", `{
		"input":{
			"metadata":{"namespace":"demo","name":"loaded"},
			"runtimeRef":"agent",
			"cwd":"/work",
			"sessionId":"native/load ?#",
			"features":{}
		}
	}`)
	accepted := readRPC(t, conn)
	require.Nil(t, accepted.Error)
	var receipt resource.SessionLoadAccepted
	require.NoError(t, json.Unmarshal(accepted.Result, &receipt))
	require.NotEmpty(t, receipt.LoadID)
	require.Equal(t, "native/load ?#", receipt.Session.Status.SessionID)
	require.Equal(t, events.SessionState("opening"), receipt.Session.Status.State)
	require.NotNil(t, receipt.Session.Status.HistoryLoad)
	require.Equal(t, "accepted", receipt.Session.Status.HistoryLoad.Phase)

	select {
	case <-fixture.driver.loadEntry:
	case <-time.After(2 * time.Second):
		t.Fatal("native load did not start")
	}

	sendCall(t, conn, "during-load", "session.get", `{"session":{"namespace":"demo","name":"loaded"}}`)
	duringLoad := readRPC(t, conn)
	require.Nil(t, duringLoad.Error)
	require.Contains(t, string(duringLoad.Result), `"phase":"accepted"`)

	sendCall(t, conn, "cancel", "session.cancelLoad", fmt.Sprintf(`{"session":{"sessionId":"native/load ?#"},"loadId":%q}`, receipt.LoadID))
	canceled := readRPC(t, conn)
	require.Nil(t, canceled.Error)
	var status resource.HistoryLoadStatus
	require.NoError(t, json.Unmarshal(canceled.Result, &status))
	require.Equal(t, "canceled", status.Phase)

	closeSignal(fixture.driver.loadRelease)
}
