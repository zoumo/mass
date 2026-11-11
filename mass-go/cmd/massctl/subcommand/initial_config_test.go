package subcommand_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/cmd/massctl/subcommand"
)

func TestInitialSessionConfigurationCLI(t *testing.T) {
	for _, action := range []string{"create", "load"} {
		t.Run(action, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				target := body
				if action == "create" {
					target = body["spec"].(map[string]any)
				}
				require.Equal(t, map[string]any{"model": " Native Model ", "effort": " MAX "}, target["config"])
				if action == "load" {
					w.WriteHeader(202)
				} else {
					w.WriteHeader(201)
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()
			args := []string{"--url", server.URL, "-o", "json", "session", action, "configured", "-n", "demo", "--runtime", "agent", "--cwd", "/remote", "--model", " Native Model ", "--effort", " MAX "}
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("invalid config sent") }))
	defer server.Close()
	for _, action := range []string{"create", "load"} {
		for _, flag := range []string{"--model", "--effort"} {
			for _, value := range []string{"bad\x00", string([]byte{0xff})} {
				args := []string{"--url", server.URL, "session", action, "configured", "-n", "one", "--runtime", "agent", "--cwd", "/work", flag, value}
				if action == "load" {
					args = append(args, "--session-id", "native")
				}
				var out, stderr bytes.Buffer
				require.Equal(t, 2, subcommand.Run(t.Context(), args, strings.NewReader(""), &out, &stderr), stderr.String())
			}
		}
	}
}
