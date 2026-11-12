package service_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type pendingRestoreRPCFixture struct {
	svc    *service.Service
	driver *pendingRestoreDriver
	ref    resource.SessionRef
	resume <-chan error
}

func newPendingRestoreRPCFixture(t *testing.T) *pendingRestoreRPCFixture {
	t.Helper()
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, st.Close()) })
	original, err := st.CreateSession(resource.Session{
		Metadata: resource.Metadata{Namespace: "one", Name: "original"},
		Spec:     resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/remote"}},
		Status:   resource.SessionStatus{SessionID: "original", State: "offline"},
	})
	require.NoError(t, err)
	driver := &pendingRestoreDriver{
		recoveryDriver: &recoveryDriver{pipe: events.NewEventPipe(16)},
		entered:        make(chan struct{}),
		canceled:       make(chan struct{}),
		release:        make(chan struct{}),
		unknownOpening: true,
	}
	release := sync.OnceFunc(func() { close(driver.release) })
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fault": pendingRestoreFactory{driver}}))
	t.Cleanup(func() {
		release()
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := svc.Shutdown(cleanup)
		driver.pipe.End(nil)
		select {
		case <-driver.entered:
			assert.Error(t, shutdownErr)
		default:
			assert.NoError(t, shutdownErr)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	_, err = svc.CreateRuntime(resource.Runtime{
		Metadata: resource.Metadata{Name: "agent"},
		Spec:     resource.RuntimeSpec{Type: "fault", Config: runtime.Config{}},
	})
	require.NoError(t, err)
	_, err = svc.StartRuntime(ctx, "agent")
	require.NoError(t, err)
	ref := resource.SessionRef{SessionID: original.Status.SessionID}
	resumed := make(chan error, 1)
	go func() {
		_, resumeErr := svc.ResumeSession(ctx, ref)
		resumed <- resumeErr
	}()
	select {
	case <-driver.entered:
	case <-ctx.Done():
		t.Fatal("restore not entered")
	}
	release()
	return &pendingRestoreRPCFixture{svc: svc, driver: driver, ref: ref, resume: resumed}
}

func pendingRestoreRPCServer(t *testing.T, svc *service.Service) (*httptest.Server, string) {
	t.Helper()
	handler, err := transport.New(svc, transport.Options{Token: "test-token", WriteTimeout: time.Second})
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(func() {
		handler.Shutdown()
		server.Close()
	})
	return server, "ws" + strings.TrimPrefix(server.URL, "http") + "/rpc"
}

type pendingRestoreRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   *struct {
		Code    int64           `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	} `json:"error"`
}

func readPendingRestoreRPC(t *testing.T, conn *websocket.Conn) pendingRestoreRPCResponse {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
	var response pendingRestoreRPCResponse
	_, data, err := conn.ReadMessage()
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &response))
	return response
}

func TestPendingRestoreStopOutcomeUnknownOverWebSocket(t *testing.T) {
	t.Run("raw WebSocket", func(t *testing.T) {
		fixture := newPendingRestoreRPCFixture(t)
		_, endpoint := pendingRestoreRPCServer(t, fixture.svc)
		headers := http.Header{}
		headers.Set("Authorization", "Bearer test-token")
		conn, handshake, err := websocket.DefaultDialer.Dial(endpoint, headers)
		if handshake != nil {
			_ = handshake.Body.Close()
		}
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(`{"jsonrpc":"2.0","id":"init","method":"initialize","params":{"protocolVersion":"1"}}`)))
		initialized := readPendingRestoreRPC(t, conn)
		require.Nil(t, initialized.Error)

		require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(`{"jsonrpc":"2.0","id":"stop","method":"runtime.stop","params":{"runtimeRef":"agent"}}`)))
		response := readPendingRestoreRPC(t, conn)
		require.Equal(t, `"stop"`, string(response.ID))
		require.NotNil(t, response.Error)
		require.Equal(t, int64(-32000), response.Error.Code)
		require.Equal(t, "Runtime stop remains unsettled by retained opening or stream responsibility; inspect state before further action", response.Error.Message)
		var public runtime.OperationError
		require.NoError(t, json.Unmarshal(response.Error.Data, &public))
		require.Equal(t, "outcome_unknown", public.Code)
		require.Equal(t, "runtime_stop_unsettled", public.Reason)
		require.Equal(t, response.Error.Message, public.Message)
		select {
		case resumeErr := <-fixture.resume:
			require.ErrorContains(t, resumeErr, "pending restore release remains unknown")
		case <-time.After(3 * time.Second):
			t.Fatal("restore did not return its unknown result")
		}
	})

	t.Run("typed client", func(t *testing.T) {
		fixture := newPendingRestoreRPCFixture(t)
		_, endpoint := pendingRestoreRPCServer(t, fixture.svc)
		client := newServiceTestClient(t, endpoint, "test-token")
		t.Cleanup(func() { require.NoError(t, client.Close()) })

		err := client.StopRuntime(t.Context(), "agent", nil)
		var public *runtime.OperationError
		require.ErrorAs(t, err, &public)
		require.Equal(t, "outcome_unknown", public.Code)
		require.Equal(t, "runtime_stop_unsettled", public.Reason)
		require.Equal(t, "Runtime stop remains unsettled by retained opening or stream responsibility; inspect state before further action", public.Message)
		select {
		case resumeErr := <-fixture.resume:
			require.ErrorContains(t, resumeErr, "pending restore release remains unknown")
		case <-time.After(3 * time.Second):
			t.Fatal("restore did not return its unknown result")
		}
	})
}
