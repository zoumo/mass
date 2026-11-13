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

func TestInitialSessionConfigurationCLI(t *testing.T) {
	for _, action := range []string{"create", "load"} {
		t.Run(action, func(t *testing.T) {
			calls := 0
			endpoint := newRPCService(t, "", func(call *rpcTestCall) {
				calls++
				var params map[string]any
				require.NoError(t, json.Unmarshal(call.Params, &params))
				input := params["input"].(map[string]any)
				target := input
				if action == "create" {
					assert.Equal(t, "session.create", call.Method)
					target = input["spec"].(map[string]any)
				} else {
					assert.Equal(t, "session.load", call.Method)
				}
				assert.Equal(t, map[string]any{"model": " Native Model ", "effort": " MAX "}, target["config"])
				call.respond(json.RawMessage(`{}`))
			})
			args := []string{"--url", endpoint, "-o", "json", "session", action, "configured", "-n", "demo", "--runtime", "agent", "--cwd", "/remote", "--model", " Native Model ", "--effort", " MAX "}
			if action == "load" {
				args = append(args, "--session-id", "native")
			}
			var out, stderr bytes.Buffer
			require.Equal(t, 0, subcommand.Run(t.Context(), args, strings.NewReader(""), &out, &stderr), stderr.String())
			require.Equal(t, 1, calls)
		})
	}
}

func TestInitialSessionConfigurationCLIRejectsInvalidBeforeRequest(t *testing.T) {
	var calls int
	endpoint := newRPCService(t, "", func(*rpcTestCall) { calls++ })
	for _, action := range []string{"create", "load"} {
		for _, flag := range []string{"--model", "--effort"} {
			for _, value := range []string{"bad\x00", string([]byte{0xff})} {
				args := []string{"--url", endpoint, "session", action, "configured", "-n", "one", "--runtime", "agent", "--cwd", "/work", flag, value}
				if action == "load" {
					args = append(args, "--session-id", "native")
				}
				var out, stderr bytes.Buffer
				require.Equal(t, 2, subcommand.Run(t.Context(), args, strings.NewReader(""), &out, &stderr), stderr.String())
			}
		}
	}
	require.Zero(t, calls)
}
