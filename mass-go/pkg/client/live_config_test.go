package client_test

import (
	"context"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type liveConfigClientAPI struct {
	transport.API
	calls atomic.Int32
}

func (a *liveConfigClientAPI) SetSessionConfig(context.Context, resource.SessionRef, runtime.ConfigChange) (runtime.ConfigReceipt, error) {
	a.calls.Add(1)
	return runtime.ConfigReceipt{}, &runtime.OperationError{
		Code:    "outcome_unknown",
		Reason:  "set_config_unknown",
		Message: "do not resend",
	}
}

func TestLiveSessionConfigurationClientValidationAndNoRetry(t *testing.T) {
	api := &liveConfigClientAPI{}
	server, err := transport.New(api, withTestWriteTimeout(transport.Options{LocalOnly: true}))
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() {
		server.Shutdown()
		httpServer.Close()
	})
	c, err := client.New(websocketURL(httpServer), "", client.Options{
		WriteTimeout:              time.Second,
		SubscriptionQueueCapacity: 1,
	})
	require.NoError(t, err)
	defer c.Close()
	ref := resource.SessionRef{SessionID: "native"}
	for _, change := range []runtime.ConfigChange{{ID: "future", Value: "x"}, {ID: agent.ConfigModel}, {ID: agent.ConfigModel, Value: "bad\x00"}, {ID: agent.ConfigEffort, Value: string([]byte{0xff})}} {
		require.Error(t, c.SetSessionConfig(context.Background(), ref, change, nil))
	}
	require.Zero(t, api.calls.Load())
	var receipt runtime.ConfigReceipt
	err = c.SetSessionConfig(t.Context(), ref, runtime.ConfigChange{ID: agent.ConfigModel, Value: "default"}, &receipt)
	var public *runtime.OperationError
	require.ErrorAs(t, err, &public)
	require.Equal(t, "outcome_unknown", public.Code)
	require.Equal(t, "set_config_unknown", public.Reason)
	require.Zero(t, receipt)
	require.EqualValues(t, 1, api.calls.Load())
}
