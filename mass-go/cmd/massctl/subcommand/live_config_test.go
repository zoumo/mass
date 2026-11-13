package subcommand_test

import (
	"bytes"
	"encoding/json"
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
			endpoint := newRPCService(t, "", func(call *rpcTestCall) {
				calls++
				assert.Equal(t, "session.setConfig", call.Method)
				expected := `{"session":{"sessionId":"target"},"change":{"id":"model","value":" Native Model "}}`
				if namespace != "" {
					expected = `{"session":{"namespace":"demo","name":"target"},"change":{"id":"model","value":" Native Model "}}`
				}
				assert.JSONEq(t, expected, string(call.Params))
				call.respond(json.RawMessage(`{"change":{"id":"model","value":" Native Model "},"acceptance":{"sent":true,"nativeAccepted":false}}`))
			})
			args := []string{"--url", endpoint, "-o", "json", "session", "set-config", "target", "--id", "model", "--value", " Native Model "}
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
	var calls int
	endpoint := newRPCService(t, "", func(*rpcTestCall) { calls++ })
	for _, pair := range [][2]string{{"future", "x"}, {"model", ""}, {"effort", "bad\x00"}, {"model", string([]byte{0xff})}} {
		var out, stderr bytes.Buffer
		require.Equal(t, 2, subcommand.Run(t.Context(), []string{"--url", endpoint, "session", "set-config", "native", "--id", pair[0], "--value", pair[1]}, strings.NewReader(""), &out, &stderr), stderr.String())
	}
	require.Zero(t, calls)
}
