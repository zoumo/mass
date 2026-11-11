package client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestLiveSessionConfigurationClientValidationAndNoRetry(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"code":"outcome_unknown","reason":"set_config_unknown","message":"do not resend"}`))
	}))
	defer server.Close()
	c := client.New(server.URL, "")
	defer c.Close()
	ref := resource.SessionRef{SessionID: "native"}
	for _, change := range []runtime.ConfigChange{{ID: "future", Value: "x"}, {ID: agent.ConfigModel}, {ID: agent.ConfigModel, Value: "bad\x00"}, {ID: agent.ConfigEffort, Value: string([]byte{0xff})}} {
		require.Error(t, c.SetSessionConfig(context.Background(), ref, change, nil))
	}
	require.Zero(t, calls.Load())
	var receipt runtime.ConfigReceipt
	err := c.SetSessionConfig(t.Context(), ref, runtime.ConfigChange{ID: agent.ConfigModel, Value: "default"}, &receipt)
	var public *runtime.OperationError
	require.ErrorAs(t, err, &public)
	require.Equal(t, "outcome_unknown", public.Code)
	require.Equal(t, "set_config_unknown", public.Reason)
	require.Zero(t, receipt)
	require.EqualValues(t, 1, calls.Load())
}
