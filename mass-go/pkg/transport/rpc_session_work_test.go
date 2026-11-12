package transport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type sessionWorkRPCDriver struct {
	pipe *events.EventPipe

	mu             sync.Mutex
	promptCalls    int
	answerCalls    int
	cancelCalls    int
	promptInputs   []runtime.Input
	promptReceipt  runtime.InputReceipt
	promptErr      error
	answerValues   []any
	answerEvidence runtime.Acceptance
	answerErr      error
	cancelReceipt  runtime.CancelReceipt
	cancelErr      error
	promptEntered  chan struct{}
	promptRelease  chan struct{}
	promptFinished chan struct{}
}

func (d *sessionWorkRPCDriver) Events() (events.Stream, error) { return d.pipe, nil }
func (*sessionWorkRPCDriver) Start(context.Context) (runtime.Capabilities, error) {
	return runtime.Capabilities{"textInput": true}, nil
}

func (*sessionWorkRPCDriver) OpenSession(context.Context, runtime.Address, runtime.SessionSpec) (runtime.DriverSession, error) {
	return runtime.DriverSession{
		SessionID:    "work/session ?#",
		Capabilities: runtime.Capabilities{"textInput": true},
		Source:       events.NewSource(),
	}, nil
}

func (*sessionWorkRPCDriver) ResumeSession(context.Context, string, runtime.Address, runtime.SessionSpec) (runtime.DriverSession, error) {
	return runtime.DriverSession{}, errors.New("unexpected resume")
}

func (*sessionWorkRPCDriver) LoadSession(context.Context, string, runtime.Address, runtime.SessionSpec, string) error {
	return nil
}

func (*sessionWorkRPCDriver) CancelHistoryLoad(context.Context, string, string) error { return nil }

func (d *sessionWorkRPCDriver) SendPrompt(ctx context.Context, _ string, input runtime.Input) (runtime.InputReceipt, error) {
	d.mu.Lock()
	d.promptCalls++
	d.promptInputs = append(d.promptInputs, input)
	receipt, err := d.promptReceipt, d.promptErr
	entered, release, finished := d.promptEntered, d.promptRelease, d.promptFinished
	d.mu.Unlock()

	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return runtime.InputReceipt{}, ctx.Err()
		}
	}
	if finished != nil {
		select {
		case finished <- struct{}{}:
		default:
		}
	}
	return receipt, err
}

func (d *sessionWorkRPCDriver) AnswerInteraction(_ context.Context, _, _ string, answer any) (runtime.Acceptance, error) {
	d.mu.Lock()
	d.answerCalls++
	d.answerValues = append(d.answerValues, answer)
	evidence, err := d.answerEvidence, d.answerErr
	d.mu.Unlock()
	return evidence, err
}

func (d *sessionWorkRPCDriver) Cancel(context.Context, string) (runtime.CancelReceipt, error) {
	d.mu.Lock()
	d.cancelCalls++
	receipt, err := d.cancelReceipt, d.cancelErr
	d.cancelReceipt = runtime.CancelReceipt{Result: "noActiveWork"}
	d.mu.Unlock()
	return receipt, err
}

func (*sessionWorkRPCDriver) CloseSession(context.Context, string) error { return nil }
func (d *sessionWorkRPCDriver) Stop(context.Context) error {
	d.pipe.End(nil)
	return nil
}

func (d *sessionWorkRPCDriver) setPromptBlock() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.promptEntered = make(chan struct{}, 1)
	d.promptRelease = make(chan struct{})
	d.promptFinished = make(chan struct{}, 1)
}

func (d *sessionWorkRPCDriver) setPromptResult(receipt runtime.InputReceipt, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.promptReceipt = receipt
	d.promptErr = err
}

func (d *sessionWorkRPCDriver) setAnswerResult(evidence runtime.Acceptance, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.answerEvidence = evidence
	d.answerErr = err
}

func (d *sessionWorkRPCDriver) setCancelResult(receipt runtime.CancelReceipt, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cancelReceipt = receipt
	d.cancelErr = err
}

func (d *sessionWorkRPCDriver) workState() (prompt, answer, cancel int, answers []any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.promptCalls, d.answerCalls, d.cancelCalls, append([]any(nil), d.answerValues...)
}

func (d *sessionWorkRPCDriver) promptInputState() []runtime.Input {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]runtime.Input(nil), d.promptInputs...)
}

func (d *sessionWorkRPCDriver) promptChannels() (<-chan struct{}, chan struct{}, <-chan struct{}) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.promptEntered, d.promptRelease, d.promptFinished
}

type sessionWorkRPCFactory struct{ driver *sessionWorkRPCDriver }

func (sessionWorkRPCFactory) Validate(runtime.Config) error { return nil }
func (f sessionWorkRPCFactory) New(string, runtime.Config) (runtime.Driver, error) {
	return f.driver, nil
}

type sessionWorkRPCFixture struct {
	server *httptest.Server
	svc    *service.Service
	driver *sessionWorkRPCDriver
	ref    resource.SessionRef
}

func newSessionWorkRPCFixture(t *testing.T) *sessionWorkRPCFixture {
	t.Helper()
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	driver := &sessionWorkRPCDriver{
		pipe:          events.NewEventPipe(32),
		cancelReceipt: runtime.CancelReceipt{Result: "noActiveWork"},
	}
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{
		"fixture": sessionWorkRPCFactory{driver: driver},
	}))
	t.Cleanup(func() {
		driver.mu.Lock()
		release := driver.promptRelease
		driver.mu.Unlock()
		closeSignal(release)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
		require.NoError(t, st.Close())
	})

	_, err = svc.CreateRuntime(resource.Runtime{
		Metadata: resource.Metadata{Name: "agent"},
		Spec:     resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{}},
	})
	require.NoError(t, err)
	_, err = svc.StartRuntime(t.Context(), "agent")
	require.NoError(t, err)
	created, err := svc.CreateSession(t.Context(), resource.Session{
		Metadata: resource.Metadata{Namespace: "demo", Name: "work"},
		Spec: resource.SessionSpec{
			RuntimeRef:  "agent",
			SessionSpec: runtime.SessionSpec{Cwd: "/work"},
		},
	})
	require.NoError(t, err)

	endpoint, err := transport.New(svc, withTestWriteTimeout(transport.Options{Token: "test-token"}))
	require.NoError(t, err)
	httpServer := httptest.NewServer(endpoint)
	t.Cleanup(func() {
		endpoint.Shutdown()
		httpServer.Close()
	})
	return &sessionWorkRPCFixture{
		server: httpServer,
		svc:    svc,
		driver: driver,
		ref:    resource.SessionRef{SessionID: created.Status.SessionID},
	}
}

func sessionWorkRPCConn(t *testing.T, fixture *sessionWorkRPCFixture) *websocket.Conn {
	t.Helper()
	conn := dialRPC(t, fixture.server, rpcHeaders("test-token"))
	sendText(t, conn, initializeMessage(`"init"`, "1"))
	require.Nil(t, readRPC(t, conn).Error)
	return conn
}

func sessionWorkRefJSON(t *testing.T, ref resource.SessionRef) string {
	t.Helper()
	raw, err := json.Marshal(ref)
	require.NoError(t, err)
	return string(raw)
}

func decodeSessionWorkOpenJSON(t *testing.T, raw []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	require.NoError(t, decoder.Decode(&value))
	return value
}

func TestRPCSessionWorkReceiptsAndOpenNullAnswer(t *testing.T) {
	fixture := newSessionWorkRPCFixture(t)
	fixture.driver.setPromptResult(runtime.InputReceipt{
		Submission: "input-1",
		Acceptance: runtime.Acceptance{NativeAccepted: runtime.Bool(false)},
	}, nil)
	fixture.driver.setAnswerResult(runtime.Acceptance{NativeRecorded: runtime.Yes()}, nil)
	fixture.driver.setCancelResult(runtime.CancelReceipt{
		Target: "work-1",
		Result: "controlAcknowledged",
	}, nil)
	conn := sessionWorkRPCConn(t, fixture)
	nameRef := sessionWorkRefJSON(t, resource.SessionRef{Namespace: "demo", Name: "work"})

	sendCall(t, conn, "prompt", "session.prompt", fmt.Sprintf(`{
		"session":%s,
		"input":{"content":[{"type":"text","text":"hello"}]}
	}`, sessionWorkRefJSON(t, fixture.ref)))
	prompt := readRPC(t, conn)
	require.Nil(t, prompt.Error)
	require.JSONEq(t, `{"submission":"input-1","acceptance":{"nativeAccepted":false}}`, string(prompt.Result))
	require.NotContains(t, string(prompt.Result), `"sent"`)
	require.NotContains(t, string(prompt.Result), `"nativeRecorded"`)
	inputs := fixture.driver.promptInputState()
	require.Len(t, inputs, 1)
	require.Len(t, inputs[0].Content, 1)
	require.Equal(t, "text", inputs[0].Content[0].Type)
	require.Equal(t, "hello", inputs[0].Content[0].Text)

	openAnswer := `{"value":9007199254740993,"deep":{"items":[null,9007199254740993]}}`
	sendCall(t, conn, "answer-open", "session.answer", fmt.Sprintf(`{
		"session":%s,
		"interaction":"interaction-1",
		"answer":%s
	}`, nameRef, openAnswer))
	answer := readRPC(t, conn)
	require.Nil(t, answer.Error)
	require.JSONEq(t, `{"interaction":"interaction-1","evidence":{"nativeRecorded":true}}`, string(answer.Result))
	_, _, _, values := fixture.driver.workState()
	require.Len(t, values, 1)
	got, ok := values[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, json.Number("9007199254740993"), got["value"])
	deep, ok := got["deep"].(map[string]any)
	require.True(t, ok)
	items, ok := deep["items"].([]any)
	require.True(t, ok)
	require.Nil(t, items[0])
	require.Equal(t, json.Number("9007199254740993"), items[1])
	require.Equal(t, decodeSessionWorkOpenJSON(t, []byte(openAnswer)), values[0])

	sendCall(t, conn, "answer-null", "session.answer", fmt.Sprintf(`{
		"session":%s,
		"interaction":"interaction-null",
		"answer":null
	}`, nameRef))
	nullAnswer := readRPC(t, conn)
	require.Nil(t, nullAnswer.Error)
	require.JSONEq(t, `{"interaction":"interaction-null","evidence":{"nativeRecorded":true}}`, string(nullAnswer.Result))
	_, _, _, values = fixture.driver.workState()
	require.Len(t, values, 2)
	require.Nil(t, values[1])

	sendCall(t, conn, "cancel", "session.cancel", fmt.Sprintf(`{"session":%s}`, nameRef))
	canceled := readRPC(t, conn)
	require.Nil(t, canceled.Error)
	require.JSONEq(t, `{"target":"work-1","result":"controlAcknowledged"}`, string(canceled.Result))

	fixture.driver.setCancelResult(runtime.CancelReceipt{Result: "noActiveWork"}, nil)
	sendCall(t, conn, "cancel-empty", "session.cancel", fmt.Sprintf(`{"session":%s}`, sessionWorkRefJSON(t, fixture.ref)))
	emptyCancel := readRPC(t, conn)
	require.Nil(t, emptyCancel.Error)
	require.JSONEq(t, `{"result":"noActiveWork"}`, string(emptyCancel.Result))
	require.NotContains(t, string(emptyCancel.Result), `"target"`)

	prompts, answers, cancels, _ := fixture.driver.workState()
	require.Equal(t, 1, prompts)
	require.Equal(t, 2, answers)
	require.Equal(t, 2, cancels)
}

func TestRPCSessionWorkInvalidParamsDoNotDispatch(t *testing.T) {
	fixture := newSessionWorkRPCFixture(t)
	conn := sessionWorkRPCConn(t, fixture)
	ref := sessionWorkRefJSON(t, fixture.ref)

	for _, test := range []struct {
		name   string
		method string
		params string
	}{
		{name: "prompt missing session", method: "session.prompt", params: `{"input":{"content":[{"type":"text","text":"x"}]}}`},
		{name: "prompt null session", method: "session.prompt", params: `{"session":null,"input":{"content":[{"type":"text","text":"x"}]}}`},
		{name: "prompt mixed ref", method: "session.prompt", params: `{"session":{"sessionId":"work-session","namespace":"demo","name":"work"},"input":{"content":[{"type":"text","text":"x"}]}}`},
		{name: "prompt missing input", method: "session.prompt", params: fmt.Sprintf(`{"session":%s}`, ref)},
		{name: "prompt null input", method: "session.prompt", params: fmt.Sprintf(`{"session":%s,"input":null}`, ref)},
		{name: "prompt missing content", method: "session.prompt", params: fmt.Sprintf(`{"session":%s,"input":{}}`, ref)},
		{name: "prompt null content", method: "session.prompt", params: fmt.Sprintf(`{"session":%s,"input":{"content":null}}`, ref)},
		{name: "prompt object content", method: "session.prompt", params: fmt.Sprintf(`{"session":%s,"input":{"content":{}}}`, ref)},
		{name: "prompt invalid block", method: "session.prompt", params: fmt.Sprintf(`{"session":%s,"input":{"content":[{"type":"text"}]}}`, ref)},
		{name: "prompt unknown block field", method: "session.prompt", params: fmt.Sprintf(`{"session":%s,"input":{"content":[{"type":"text","text":"x","extra":true}]}}`, ref)},
		{name: "prompt unknown input field", method: "session.prompt", params: fmt.Sprintf(`{"session":%s,"input":{"content":[{"type":"text","text":"x"}],"extra":true}}`, ref)},
		{name: "prompt unknown top field", method: "session.prompt", params: fmt.Sprintf(`{"session":%s,"input":{"content":[{"type":"text","text":"x"}]},"extra":true}`, ref)},
		{name: "answer missing answer", method: "session.answer", params: fmt.Sprintf(`{"session":%s,"interaction":"i"}`, ref)},
		{name: "answer missing interaction", method: "session.answer", params: fmt.Sprintf(`{"session":%s,"answer":null}`, ref)},
		{name: "answer null interaction", method: "session.answer", params: fmt.Sprintf(`{"session":%s,"interaction":null,"answer":null}`, ref)},
		{name: "answer empty interaction", method: "session.answer", params: fmt.Sprintf(`{"session":%s,"interaction":"","answer":null}`, ref)},
		{name: "answer unknown field", method: "session.answer", params: fmt.Sprintf(`{"session":%s,"interaction":"i","answer":null,"extra":true}`, ref)},
		{name: "cancel missing session", method: "session.cancel", params: `{}`},
		{name: "cancel null session", method: "session.cancel", params: `{"session":null}`},
		{name: "cancel unknown field", method: "session.cancel", params: fmt.Sprintf(`{"session":%s,"wait":true}`, ref)},
	} {
		t.Run(test.name, func(t *testing.T) {
			id := "invalid-" + test.name
			sendCall(t, conn, id, test.method, test.params)
			requireRPCError(t, readRPC(t, conn), -32602, fmt.Sprintf("%q", id), "")
		})
	}

	prompts, answers, cancels, _ := fixture.driver.workState()
	require.Zero(t, prompts)
	require.Zero(t, answers)
	require.Zero(t, cancels)

	sendCall(t, conn, "empty-input", "session.prompt", fmt.Sprintf(`{
		"session":%s,
		"input":{"content":[]}
	}`, ref))
	requireServiceRPCError(t, readRPC(t, conn), "invalid_argument", "invalid_request")
	prompts, _, _, _ = fixture.driver.workState()
	require.Zero(t, prompts, "Service validation must precede Driver dispatch")

	sendCall(t, conn, "unknown", "session.unknown", `{}`)
	requireRPCError(t, readRPC(t, conn), -32601, `"unknown"`, "")
}

func TestRPCSessionWorkLongPromptAllowsConcurrentCancelAndAnswer(t *testing.T) {
	fixture := newSessionWorkRPCFixture(t)
	fixture.driver.setPromptBlock()
	fixture.driver.setPromptResult(runtime.InputReceipt{Submission: "late-input"}, nil)
	fixture.driver.setAnswerResult(runtime.Acceptance{Sent: runtime.Yes()}, nil)
	fixture.driver.setCancelResult(runtime.CancelReceipt{Target: "work-1", Result: "controlAcknowledged"}, nil)
	conn := sessionWorkRPCConn(t, fixture)
	entered, release, finished := fixture.driver.promptChannels()
	ref := sessionWorkRefJSON(t, fixture.ref)

	sendCall(t, conn, "prompt", "session.prompt", fmt.Sprintf(`{
		"session":%s,
		"input":{"content":[{"type":"text","text":"long"}]}
	}`, ref))
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("prompt was not admitted")
	}

	sendCall(t, conn, "cancel", "session.cancel", fmt.Sprintf(`{"session":%s}`, ref))
	sendCall(t, conn, "answer", "session.answer", fmt.Sprintf(`{
		"session":%s,
		"interaction":"interaction-1",
		"answer":{"outcome":"selected","optionId":"allow-once"}
	}`, ref))
	responses := map[string]rpcResponse{}
	for len(responses) < 2 {
		response := readRPC(t, conn)
		responses[string(response.ID)] = response
	}
	require.Nil(t, responses[`"cancel"`].Error)
	require.JSONEq(t, `{"target":"work-1","result":"controlAcknowledged"}`, string(responses[`"cancel"`].Result))
	require.Nil(t, responses[`"answer"`].Error)
	require.JSONEq(t, `{"interaction":"interaction-1","evidence":{"sent":true}}`, string(responses[`"answer"`].Result))

	close(release)
	prompt := readRPC(t, conn)
	require.Equal(t, `"prompt"`, string(prompt.ID))
	require.Nil(t, prompt.Error)
	require.JSONEq(t, `{"submission":"late-input","acceptance":{}}`, string(prompt.Result))
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("long prompt did not finish")
	}
	prompts, answers, cancels, _ := fixture.driver.workState()
	require.Equal(t, 1, prompts)
	require.Equal(t, 1, answers)
	require.Equal(t, 1, cancels)
}

func TestRPCSessionWorkUnknownOutcomePreservesServiceError(t *testing.T) {
	fixture := newSessionWorkRPCFixture(t)
	public := &runtime.OperationError{
		Code:    "outcome_unknown",
		Reason:  "provider_result_unknown",
		Message: "Operation outcome is unknown",
	}
	fixture.driver.setPromptResult(runtime.InputReceipt{}, public)
	conn := sessionWorkRPCConn(t, fixture)

	sendCall(t, conn, "unknown-outcome", "session.prompt", fmt.Sprintf(`{
		"session":%s,
		"input":{"content":[{"type":"text","text":"uncertain"}]}
	}`, sessionWorkRefJSON(t, fixture.ref)))
	response := readRPC(t, conn)
	require.NotNil(t, response.Error)
	require.Equal(t, int64(-32000), response.Error.Code)
	require.Empty(t, response.Result)
	var data runtime.OperationError
	require.NoError(t, json.Unmarshal(response.Error.Data, &data))
	require.Equal(t, *public, data)
	require.Equal(t, public.Message, response.Error.Message)
	prompts, _, _, _ := fixture.driver.workState()
	require.Equal(t, 1, prompts, "unknown outcome must not be retried")
}

func TestRPCSessionWorkConnectionCloseDoesNotCancelAdmittedPrompt(t *testing.T) {
	fixture := newSessionWorkRPCFixture(t)
	fixture.driver.setPromptBlock()
	fixture.driver.setPromptResult(runtime.InputReceipt{Submission: "detached-input"}, nil)
	conn := sessionWorkRPCConn(t, fixture)
	entered, release, finished := fixture.driver.promptChannels()

	sendCall(t, conn, "prompt", "session.prompt", fmt.Sprintf(`{
		"session":%s,
		"input":{"content":[{"type":"text","text":"disconnect"}]}
	}`, sessionWorkRefJSON(t, fixture.ref)))
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("prompt was not admitted")
	}
	require.NoError(t, conn.Close())
	close(release)
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("admitted prompt was canceled with its connection")
	}
	prompts, _, _, _ := fixture.driver.workState()
	require.Equal(t, 1, prompts, "disconnect must not cause an automatic retry")
}
