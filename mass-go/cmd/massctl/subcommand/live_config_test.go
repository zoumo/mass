package subcommand_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/cmd/massctl/subcommand"
)

func TestLiveSessionConfigurationCLI(t *testing.T) {
	for _, namespace := range []string{"", "demo"} {
		t.Run("namespace="+namespace, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				path := "/apis/v1/sessions/target/config"
				if namespace != "" {
					path = "/apis/v1/namespaces/demo/sessions/target/config"
				}
				assert.Equal(t, path, r.URL.Path)
				assert.Equal(t, http.MethodPost, r.Method)
				var body map[string]any
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				assert.Equal(t, map[string]any{"id": "model", "value": " Native Model "}, body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"change":{"id":"model","value":" Native Model "},"acceptance":{"sent":true,"nativeAccepted":false}}`))
			}))
			defer server.Close()
			args := []string{"--url", server.URL, "-o", "json", "session", "set-config", "target", "--id", "model", "--value", " Native Model "}
			if namespace != "" {
				args = append(args, "-n", namespace)
			}
			var out, stderr bytes.Buffer
			require.Equal(t, 0, subcommand.Run(t.Context(), args, strings.NewReader(""), &out, &stderr), stderr.String())
			require.JSONEq(t, `{"change":{"id":"model","value":" Native Model "},"acceptance":{"sent":true,"nativeAccepted":false}}`, out.String())
			require.Equal(t, 1, calls)
		})
	}
}

func TestLiveSessionConfigurationCLIValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid change dispatched") }))
	defer server.Close()
	for _, pair := range [][2]string{{"future", "x"}, {"model", ""}, {"effort", "bad\x00"}, {"model", string([]byte{0xff})}} {
		var out, stderr bytes.Buffer
		require.Equal(t, 2, subcommand.Run(t.Context(), []string{"--url", server.URL, "session", "set-config", "native", "--id", pair[0], "--value", pair[1]}, strings.NewReader(""), &out, &stderr), stderr.String())
	}
}
