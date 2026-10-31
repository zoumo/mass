package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/resource"
)

func TestLoadClientSendsOnlyWritableFlatInputAndReturnsReceipt(t *testing.T) {
	var calls atomic.Int32
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "POST", r.Method)
		require.Equal(t, "/apis/v1/sessions/load", r.URL.EscapedPath())
		require.Empty(t, r.URL.RawQuery)
		b, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"metadata":{"namespace":"demo","name":"imported"},"runtimeRef":"agent","cwd":"/remote/not-local","sessionId":"a/b%2F"}`, string(b))
		w.WriteHeader(202)
		fmt.Fprint(w, `{"loadId":"original","session":{"status":{"sessionId":"a/b%2F","historyLoad":{"loadId":"original","phase":"accepted","nativeVerified":false,"nativeResumed":false,"nativeCompleted":false,"deliveryCompleted":false,"cleanupCompleted":false}}},"futureNumber":9007199254740993}`)
	}))
	defer h.Close()
	c := newHTTPClient(t, h.URL, "")
	var out map[string]any
	input := resource.SessionLoad{Metadata: resource.Metadata{Namespace: "demo", Name: "imported", UID: "must-not-send"}, RuntimeRef: "agent", Cwd: "/remote/not-local", SessionID: "a/b%2F"}
	require.NoError(t, c.LoadSession(t.Context(), input, &out))
	require.Equal(t, "original", out["loadId"])
	require.Equal(t, json.Number("9007199254740993"), out["futureNumber"])
	require.Equal(t, int32(1), calls.Load())
}

func TestCancelLoadClientEscapesRawTargetsOnceWithoutLookup(t *testing.T) {
	for _, tc := range []struct {
		ref  resource.SessionRef
		path string
	}{
		{resource.SessionRef{SessionID: "a/b%2F ?#"}, "/apis/v1/sessions/a%2Fb%252F%20%3F%23/history-load/cancel"},
		{resource.SessionRef{Namespace: "demo", Name: "task"}, "/apis/v1/namespaces/demo/sessions/task/history-load/cancel"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			var calls atomic.Int32
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				require.Equal(t, "POST", r.Method)
				require.Equal(t, tc.path, r.URL.EscapedPath())
				require.Empty(t, r.URL.RawQuery)
				b, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.JSONEq(t, `{"loadId":" original/% load "}`, string(b))
				fmt.Fprint(w, `{"loadId":" original/% load ","phase":"canceled","nativeVerified":false,"nativeResumed":false,"nativeCompleted":false,"deliveryCompleted":false,"cleanupCompleted":false,"error":{"code":"outcome_unknown","reason":"cleanup_pending","message":"Inspect status"}}`)
			}))
			defer h.Close()
			var out resource.HistoryLoadStatus
			require.NoError(t, newHTTPClient(t, h.URL, "").CancelHistoryLoad(t.Context(), tc.ref, " original/% load ", &out))
			require.False(t, out.CleanupCompleted)
			require.Equal(t, "cleanup_pending", out.Error.Reason)
			require.Equal(t, int32(1), calls.Load())
		})
	}
}

func TestLoadClientDoesNotRetryUncertainResponseOrCancelOnContextExpiry(t *testing.T) {
	for _, mode := range []string{"uncertain", "expired"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if mode == "expired" {
					cancel()
					<-r.Context().Done()
					return
				}
				w.WriteHeader(500)
				fmt.Fprint(w, `{"code":"outcome_unknown","reason":"operation_unconfirmed","message":"Inspect status"}`)
			}))
			defer h.Close()
			c := newHTTPClient(t, h.URL, "")
			err := c.LoadSession(ctx, resource.SessionLoad{Metadata: resource.Metadata{Namespace: "demo", Name: "imported"}, RuntimeRef: "agent", Cwd: "/remote", SessionID: "native"}, nil)
			require.Error(t, err)
			require.Equal(t, int32(1), calls.Load())
		})
	}
}
