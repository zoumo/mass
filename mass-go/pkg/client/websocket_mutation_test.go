package client_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type blockingMutationAPI struct {
	transport.API
	entered chan string
	release chan struct{}
}

func (a *blockingMutationAPI) wait(method string) {
	select {
	case a.entered <- method:
	default:
	}
	<-a.release
}

func (a *blockingMutationAPI) CreateSession(context.Context, resource.Session) (resource.Session, error) {
	a.wait("session.create")
	return resource.Session{}, nil
}

func (a *blockingMutationAPI) CreateSessionWithInput(context.Context, resource.SessionCreateInput) (resource.SessionCreated, error) {
	a.wait("session.create")
	return resource.SessionCreated{}, nil
}

func (a *blockingMutationAPI) ResumeSession(context.Context, resource.SessionRef) (resource.Session, error) {
	a.wait("session.resume")
	return resource.Session{}, nil
}

func (a *blockingMutationAPI) ResumeSessionWithInput(context.Context, resource.SessionRef, runtime.Input) (resource.SessionResumed, error) {
	a.wait("session.resume")
	return resource.SessionResumed{}, nil
}

func (a *blockingMutationAPI) CloseSession(context.Context, resource.SessionRef) (resource.Session, error) {
	a.wait("session.close")
	return resource.Session{}, nil
}

func (a *blockingMutationAPI) LoadSession(context.Context, resource.SessionLoad) (resource.SessionLoadAccepted, error) {
	a.wait("session.load")
	return resource.SessionLoadAccepted{}, nil
}

func (a *blockingMutationAPI) CancelHistoryLoad(context.Context, resource.SessionRef, string) (resource.HistoryLoadStatus, error) {
	a.wait("session.cancelLoad")
	return resource.HistoryLoadStatus{}, nil
}

func (a *blockingMutationAPI) CreateRuntime(resource.Runtime) (resource.Runtime, error) {
	a.wait("runtime.create")
	return resource.Runtime{}, nil
}

func (a *blockingMutationAPI) UpdateRuntime(string, resource.RuntimeSpec) (resource.Runtime, error) {
	a.wait("runtime.update")
	return resource.Runtime{}, nil
}

func (a *blockingMutationAPI) StartRuntime(context.Context, string) (resource.Runtime, error) {
	a.wait("runtime.start")
	return resource.Runtime{}, nil
}

func (a *blockingMutationAPI) StopRuntime(context.Context, string) (resource.Runtime, error) {
	a.wait("runtime.stop")
	return resource.Runtime{}, nil
}

func (a *blockingMutationAPI) DeleteRuntime(string) error {
	a.wait("runtime.delete")
	return nil
}

func newBlockingMutationClient(t *testing.T) (*client.Client, *blockingMutationAPI) {
	t.Helper()
	api := &blockingMutationAPI{entered: make(chan string, 1), release: make(chan struct{})}
	server, err := transport.New(api, withTestWriteTimeout(transport.Options{Token: "test-token"}))
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() {
		close(api.release)
		server.Shutdown()
		httpServer.Close()
	})
	wsClient := newTestClient(t, websocketURL(httpServer), "test-token")
	t.Cleanup(func() { _ = wsClient.Close() })
	require.NoError(t, wsClient.Initialize(t.Context(), nil, nil))
	return wsClient, api
}

func TestClientMutatingCallWithUnconfirmedReplyIsOutcomeUnknown(t *testing.T) {
	ref := resource.SessionRef{SessionID: "native"}
	input := runtime.Input{Content: []events.ContentBlock{events.Text("mutate")}}
	session := resource.Session{
		Metadata: resource.Metadata{Namespace: "demo", Name: "mutate"},
		Spec:     resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/work"}},
	}
	createInput := resource.SessionCreateInput{
		Metadata:     session.Metadata,
		Spec:         session.Spec,
		InitialInput: &input,
	}
	load := resource.SessionLoad{
		Metadata:   resource.Metadata{Namespace: "demo", Name: "loaded"},
		RuntimeRef: "agent",
		Cwd:        "/work",
		SessionID:  "native",
	}
	runSpec := resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{}}
	run := resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: runSpec}

	tests := []struct {
		name   string
		method string
		call   func(context.Context, *client.Client) error
	}{
		{name: "create session", method: "session.create", call: func(ctx context.Context, c *client.Client) error {
			return c.CreateSession(ctx, session, nil)
		}},
		{name: "create session with input", method: "session.create", call: func(ctx context.Context, c *client.Client) error {
			return c.CreateSessionWithInput(ctx, createInput, nil)
		}},
		{name: "resume session", method: "session.resume", call: func(ctx context.Context, c *client.Client) error {
			return c.ResumeSession(ctx, ref, nil)
		}},
		{name: "resume session with input", method: "session.resume", call: func(ctx context.Context, c *client.Client) error {
			return c.ResumeSessionWithInput(ctx, ref, input, nil)
		}},
		{name: "close session", method: "session.close", call: func(ctx context.Context, c *client.Client) error {
			return c.CloseSession(ctx, ref, nil)
		}},
		{name: "load session", method: "session.load", call: func(ctx context.Context, c *client.Client) error {
			return c.LoadSession(ctx, load, nil)
		}},
		{name: "cancel history load", method: "session.cancelLoad", call: func(ctx context.Context, c *client.Client) error {
			return c.CancelHistoryLoad(ctx, ref, "load-1", nil)
		}},
		{name: "create runtime", method: "runtime.create", call: func(ctx context.Context, c *client.Client) error {
			return c.CreateRuntime(ctx, run, nil)
		}},
		{name: "update runtime", method: "runtime.update", call: func(ctx context.Context, c *client.Client) error {
			return c.UpdateRuntime(ctx, "agent", runSpec, nil)
		}},
		{name: "start runtime", method: "runtime.start", call: func(ctx context.Context, c *client.Client) error {
			return c.StartRuntime(ctx, "agent", nil)
		}},
		{name: "stop runtime", method: "runtime.stop", call: func(ctx context.Context, c *client.Client) error {
			return c.StopRuntime(ctx, "agent", nil)
		}},
		{name: "delete runtime", method: "runtime.delete", call: func(ctx context.Context, c *client.Client) error {
			return c.DeleteRuntime(ctx, "agent")
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wsClient, api := newBlockingMutationClient(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- test.call(ctx, wsClient) }()
			select {
			case method := <-api.entered:
				require.Equal(t, test.method, method)
			case <-time.After(2 * time.Second):
				t.Fatal("request was not admitted")
			}
			cancel()
			select {
			case err := <-done:
				var public *runtime.OperationError
				require.ErrorAs(t, err, &public)
				require.Equal(t, "outcome_unknown", public.Code)
				require.Equal(t, "operation_unconfirmed", public.Reason)
			case <-time.After(2 * time.Second):
				t.Fatal("canceled wait did not return")
			}
		})
	}
}

func TestClientMutatingCallRejectedBeforeDispatchStaysKnown(t *testing.T) {
	wsClient, _ := newBlockingMutationClient(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := wsClient.CreateSession(ctx, resource.Session{}, nil)
	require.ErrorIs(t, err, context.Canceled)
	var public *runtime.OperationError
	require.NotErrorAs(t, err, &public)
}
