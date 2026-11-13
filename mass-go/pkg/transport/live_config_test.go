package transport_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type liveConfigAPI struct {
	transport.API
	calls atomic.Int32
}

func (a *liveConfigAPI) ResolveSession(ns, name string) (resource.SessionRef, error) {
	return resource.SessionRef{Namespace: ns, Name: name}, nil
}

func (a *liveConfigAPI) SetSessionConfig(context.Context, resource.SessionRef, runtime.ConfigChange) (runtime.ConfigReceipt, error) {
	a.calls.Add(1)
	return runtime.ConfigReceipt{}, nil
}

func TestLiveSessionConfigurationHTTPValidation(t *testing.T) {
	api := &liveConfigAPI{}
	h, err := transport.New(api, transport.Options{LocalOnly: true})
	require.NoError(t, err)
	defer h.Shutdown()
	cases := []string{`null`, `[]`, `{}`, `{"id":"model"}`, `{"id":"effort","value":null}`, `{"id":"effort","value":42}`, `{"id":"future","value":"x"}`, `{"id":"model","value":""}`, `{"id":"model","value":"x\u0000y"}`, `{"id":"effort","value":"\ud800"}`, `{"id":"model","value":"\udc00"}`, "{\"id\":\"model\",\"value\":\"" + string([]byte{0xff}) + "\"}", `{"id":"model","value":"x","extra":true}`, `{"id":"model","id":"effort","value":"x"}`}
	for _, path := range []string{"/apis/v1/sessions/native/config", "/apis/v1/namespaces/one/sessions/name/config"} {
		for i, body := range cases {
			t.Run(fmt.Sprintf("%s/%d", path, i), func(t *testing.T) {
				request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
				request.RemoteAddr = "127.0.0.1:12345"
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				h.ServeHTTP(response, request)
				require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
				require.Zero(t, api.calls.Load())
			})
		}
	}
}
