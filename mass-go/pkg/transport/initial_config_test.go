package transport_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

// The missing Runtime makes any accidentally accepted request observable as a
// different error. No Driver may be constructed for invalid configuration.
func TestInitialSessionConfigurationHTTPRejectsInvalidBeforeEffects(t *testing.T) {
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	defer st.Close()
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": httpConfigFactory{}}))
	defer func() { require.NoError(t, svc.Shutdown(t.Context())) }()
	h, err := transport.New(svc, transport.Options{LocalOnly: true})
	require.NoError(t, err)
	defer h.Shutdown()
	server := httptest.NewServer(h)
	defer server.Close()
	cases := []string{`null`, `1`, `"model"`, `[]`, `{"model":null}`, `{"effort":null}`, `{"model":true}`, `{"effort":42}`, `{"model":"x\u0000y"}`, `{"effort":"\ud800"}`, `{"model":"\udc00"}`, "{\"model\":\"" + string([]byte{0xff}) + "\"}", `{"future":"x"}`}
	for _, action := range []string{"create", "load"} {
		for index, config := range cases {
			t.Run(fmt.Sprintf("%s/%d", action, index), func(t *testing.T) {
				path, body := "/apis/v1/sessions", `{"metadata":{"namespace":"one","name":"configured"},"spec":{"runtimeRef":"missing","cwd":"/work","config":`+config+`}}`
				if action == "load" {
					path += "/load"
					body = `{"metadata":{"namespace":"one","name":"configured"},"runtimeRef":"missing","cwd":"/work","sessionId":"native","config":` + config + `}`
				}
				req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+path, strings.NewReader(body))
				require.NoError(t, err)
				req.Header.Set("Content-Type", "application/json")
				res, err := http.DefaultClient.Do(req)
				require.NoError(t, err)
				defer res.Body.Close()
				raw, err := io.ReadAll(res.Body)
				require.NoError(t, err)
				require.Equal(t, 400, res.StatusCode, "%s", raw)
				page, err := svc.ListSessions(resource.SessionQuery{})
				require.NoError(t, err)
				require.Empty(t, page.Items)
			})
		}
	}
}
