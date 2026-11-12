package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type typedWorkDriver struct {
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
	cancelReceipt  runtime.CancelReceipt
	promptEntered  chan struct{}
	promptRelease  chan struct{}
	promptFinished chan struct{}
}

func (d *typedWorkDriver) Events() (events.Stream, error) { return d.pipe, nil }
func (*typedWorkDriver) Start(context.Context) (runtime.Capabilities, error) {
	return runtime.Capabilities{"textInput": true}, nil
}

func (*typedWorkDriver) OpenSession(context.Context, runtime.Address, runtime.SessionSpec) (runtime.DriverSession, error) {
	return runtime.DriverSession{
		SessionID:    "typed/work ?#",
		Capabilities: runtime.Capabilities{"textInput": true},
		Source:       events.NewSource(),
	}, nil
}

func (*typedWorkDriver) ResumeSession(context.Context, string, runtime.Address, runtime.SessionSpec) (runtime.DriverSession, error) {
	return runtime.DriverSession{}, errors.New("unexpected resume")
}

func (*typedWorkDriver) LoadSession(context.Context, string, runtime.Address, runtime.SessionSpec, string) error {
	return nil
}
func (*typedWorkDriver) CancelHistoryLoad(context.Context, string, string) error { return nil }

func (d *typedWorkDriver) SendPrompt(ctx context.Context, _ string, input runtime.Input) (runtime.InputReceipt, error) {
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

func (d *typedWorkDriver) AnswerInteraction(_ context.Context, _, _ string, answer any) (runtime.Acceptance, error) {
	d.mu.Lock()
	d.answerCalls++
	d.answerValues = append(d.answerValues, answer)
	evidence := d.answerEvidence
	d.mu.Unlock()
	return evidence, nil
}

func (d *typedWorkDriver) Cancel(context.Context, string) (runtime.CancelReceipt, error) {
	d.mu.Lock()
	d.cancelCalls++
	receipt := d.cancelReceipt
	d.cancelReceipt = runtime.CancelReceipt{Result: "noActiveWork"}
	d.mu.Unlock()
	return receipt, nil
}

func (*typedWorkDriver) CloseSession(context.Context, string) error { return nil }
func (d *typedWorkDriver) Stop(context.Context) error {
	d.pipe.End(nil)
	return nil
}

func (d *typedWorkDriver) setPrompt(receipt runtime.InputReceipt, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.promptReceipt = receipt
	d.promptErr = err
}

func (d *typedWorkDriver) setPromptBlock() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.promptEntered = make(chan struct{}, 1)
	d.promptRelease = make(chan struct{})
	d.promptFinished = make(chan struct{}, 1)
}

func (d *typedWorkDriver) setAnswer(evidence runtime.Acceptance) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.answerEvidence = evidence
}

func (d *typedWorkDriver) setCancel(receipt runtime.CancelReceipt) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cancelReceipt = receipt
}

func (d *typedWorkDriver) state() (prompt, answer, cancel int, answers []any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.promptCalls, d.answerCalls, d.cancelCalls, append([]any(nil), d.answerValues...)
}

func (d *typedWorkDriver) promptInputState() []runtime.Input {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]runtime.Input(nil), d.promptInputs...)
}

func (d *typedWorkDriver) promptChannels() (<-chan struct{}, chan struct{}, <-chan struct{}) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.promptEntered, d.promptRelease, d.promptFinished
}

type typedWorkFactory struct{ driver *typedWorkDriver }

func (typedWorkFactory) Validate(runtime.Config) error { return nil }
func (f typedWorkFactory) New(string, runtime.Config) (runtime.Driver, error) {
	return f.driver, nil
}

type typedWorkFixture struct {
	server *httptest.Server
	driver *typedWorkDriver
	ref    resource.SessionRef
}

func newTypedWorkFixture(t *testing.T) *typedWorkFixture {
	t.Helper()
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	driver := &typedWorkDriver{
		pipe:          events.NewEventPipe(32),
		cancelReceipt: runtime.CancelReceipt{Result: "noActiveWork"},
	}
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{
		"fixture": typedWorkFactory{driver: driver},
	}))
	t.Cleanup(func() {
		driver.mu.Lock()
		release := driver.promptRelease
		driver.mu.Unlock()
		if release != nil {
			select {
			case <-release:
			default:
				close(release)
			}
		}
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
		Metadata: resource.Metadata{Namespace: "demo", Name: "typed-work"},
		Spec: resource.SessionSpec{
			RuntimeRef:  "agent",
			SessionSpec: runtime.SessionSpec{Cwd: "/work"},
		},
	})
	require.NoError(t, err)

	endpoint, err := transport.New(svc, transport.Options{Token: "test-token"})
	require.NoError(t, err)
	httpServer := httptest.NewServer(endpoint)
	t.Cleanup(func() {
		endpoint.Shutdown()
		httpServer.Close()
	})
	return &typedWorkFixture{
		server: httpServer,
		driver: driver,
		ref:    resource.SessionRef{SessionID: created.Status.SessionID},
	}
}

func newInitializedWorkClient(t *testing.T, fixture *typedWorkFixture) *client.Client {
	t.Helper()
	wsClient := client.New(websocketURL(fixture.server), "test-token")
	t.Cleanup(func() { _ = wsClient.Close() })
	require.NoError(t, wsClient.Initialize(t.Context(), &client.ClientInfo{Name: "test", Version: "1"}, nil))
	return wsClient
}

func TestClientSessionWorkTypedReceiptsAndOpenAnswer(t *testing.T) {
	fixture := newTypedWorkFixture(t)
	fixture.driver.setPrompt(runtime.InputReceipt{
		Submission: "typed-input",
		Acceptance: runtime.Acceptance{NativeAccepted: runtime.Bool(false)},
	}, nil)
	fixture.driver.setAnswer(runtime.Acceptance{NativeRecorded: runtime.Yes()})
	fixture.driver.setCancel(runtime.CancelReceipt{Result: "noActiveWork"})
	wsClient := newInitializedWorkClient(t, fixture)

	var prompt runtime.InputReceipt
	require.NoError(t, wsClient.SendPrompt(t.Context(), fixture.ref, runtime.Input{
		Content: []events.ContentBlock{events.Text("hello")},
	}, &prompt))
	require.Equal(t, "typed-input", prompt.Submission)
	require.Nil(t, prompt.Acceptance.Sent)
	require.Equal(t, runtime.Bool(false), prompt.Acceptance.NativeAccepted)
	require.Nil(t, prompt.Acceptance.NativeRecorded)
	encoded, err := json.Marshal(prompt)
	require.NoError(t, err)
	require.JSONEq(t, `{"submission":"typed-input","acceptance":{"nativeAccepted":false}}`, string(encoded))
	inputs := fixture.driver.promptInputState()
	require.Len(t, inputs, 1)
	require.Equal(t, "hello", inputs[0].Content[0].Text)

	var nullReceipt runtime.InteractionReceipt
	nameRef := resource.SessionRef{Namespace: "demo", Name: "typed-work"}
	require.NoError(t, wsClient.Answer(t.Context(), nameRef, "interaction-null", nil, &nullReceipt))
	require.Equal(t, "interaction-null", nullReceipt.Interaction)
	require.Nil(t, nullReceipt.Evidence.Sent)
	require.Equal(t, runtime.Yes(), nullReceipt.Evidence.NativeRecorded)

	answer := map[string]any{
		"value": json.Number("9007199254740993"),
		"deep": map[string]any{
			"items": []any{nil, json.Number("9007199254740993")},
		},
	}
	var openReceipt runtime.InteractionReceipt
	require.NoError(t, wsClient.Answer(t.Context(), fixture.ref, "interaction-open", answer, &openReceipt))
	_, answers, _, values := fixture.driver.state()
	require.Equal(t, 2, answers)
	require.Nil(t, values[0])
	got, ok := values[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, json.Number("9007199254740993"), got["value"])
	deep, ok := got["deep"].(map[string]any)
	require.True(t, ok)
	items, ok := deep["items"].([]any)
	require.True(t, ok)
	require.Nil(t, items[0])
	require.Equal(t, json.Number("9007199254740993"), items[1])

	var canceled runtime.CancelReceipt
	require.NoError(t, wsClient.Cancel(t.Context(), fixture.ref, &canceled))
	require.Equal(t, "noActiveWork", canceled.Result)
	require.Empty(t, canceled.Target)
	prompts, answers, cancels, _ := fixture.driver.state()
	require.Equal(t, 1, prompts)
	require.Equal(t, 2, answers)
	require.Equal(t, 1, cancels)
}

func TestClientSessionWorkPreservesUnknownOutcome(t *testing.T) {
	fixture := newTypedWorkFixture(t)
	public := &runtime.OperationError{
		Code:    "outcome_unknown",
		Reason:  "provider_result_unknown",
		Message: "Operation outcome is unknown",
	}
	fixture.driver.setPrompt(runtime.InputReceipt{}, public)
	wsClient := newInitializedWorkClient(t, fixture)

	err := wsClient.SendPrompt(t.Context(), fixture.ref, runtime.Input{
		Content: []events.ContentBlock{events.Text("uncertain")},
	}, nil)
	var got *runtime.OperationError
	require.ErrorAs(t, err, &got)
	require.Equal(t, public, got)
	prompts, _, _, _ := fixture.driver.state()
	require.Equal(t, 1, prompts)
}

func TestClientLocalTimeoutLeavesPromptOwnedByService(t *testing.T) {
	fixture := newTypedWorkFixture(t)
	fixture.driver.setPromptBlock()
	fixture.driver.setPrompt(runtime.InputReceipt{Submission: "timeout-input"}, nil)
	wsClient := newInitializedWorkClient(t, fixture)
	entered, release, finished := fixture.driver.promptChannels()

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- wsClient.SendPrompt(ctx, fixture.ref, runtime.Input{
			Content: []events.ContentBlock{events.Text("timeout")},
		}, nil)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("prompt was not admitted")
	}
	select {
	case err := <-done:
		var public *runtime.OperationError
		require.ErrorAs(t, err, &public)
		require.Equal(t, "outcome_unknown", public.Code)
		require.Equal(t, "operation_unconfirmed", public.Reason)
	case <-time.After(2 * time.Second):
		t.Fatal("local timeout did not end the client wait")
	}

	close(release)
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("local timeout canceled admitted Service work")
	}
	prompts, _, _, _ := fixture.driver.state()
	require.Equal(t, 1, prompts, "late completion must not trigger an automatic retry")
}

func TestClientConnectionCloseLeavesPromptOwnedByService(t *testing.T) {
	fixture := newTypedWorkFixture(t)
	fixture.driver.setPromptBlock()
	fixture.driver.setPrompt(runtime.InputReceipt{Submission: "closed-input"}, nil)
	wsClient := newInitializedWorkClient(t, fixture)
	entered, release, finished := fixture.driver.promptChannels()

	done := make(chan error, 1)
	go func() {
		done <- wsClient.SendPrompt(context.Background(), fixture.ref, runtime.Input{
			Content: []events.ContentBlock{events.Text("disconnect")},
		}, nil)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("prompt was not admitted")
	}
	require.NoError(t, wsClient.Close())
	select {
	case err := <-done:
		var public *runtime.OperationError
		require.ErrorAs(t, err, &public)
		require.Equal(t, "outcome_unknown", public.Code)
	case <-time.After(2 * time.Second):
		t.Fatal("connection close did not end the client wait")
	}

	close(release)
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("connection close canceled admitted Service work")
	}
	prompts, _, _, _ := fixture.driver.state()
	require.Equal(t, 1, prompts)
}
