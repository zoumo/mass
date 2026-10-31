package subcommand_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/cmd/massctl/subcommand"
)

func TestSessionLoadCLIPrintsAcceptedReceiptWithoutLocalPathAccessOrFollowup(t *testing.T) {
	var calls atomic.Int32
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/apis/v1/sessions/load", r.URL.EscapedPath())
		b, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.JSONEq(t, `{"metadata":{"namespace":"demo","name":"imported"},"runtimeRef":"agent","cwd":"/agent/does-not-exist-locally","sessionId":"a/b%2F"}`, string(b))
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"loadId":"original","session":{"status":{"sessionId":"a/b%2F","historyLoad":{"loadId":"original","phase":"accepted","nativeVerified":false,"nativeResumed":false,"nativeCompleted":false,"deliveryCompleted":false,"cleanupCompleted":false}}}}`)
	}))
	defer h.Close()
	for _, output := range [][]string{nil, {"-o", "json"}} {
		var out, stderr bytes.Buffer
		args := append([]string{"--url", h.URL}, output...)
		args = append(args, "session", "load", "imported", "-n", "demo", "--runtime", "agent", "--cwd", "/agent/does-not-exist-locally", "--session-id", "a/b%2F")
		assert.Equal(t, 0, subcommand.Run(t.Context(), args, strings.NewReader(""), &out, &stderr), stderr.String())
		var got map[string]any
		require.NoError(t, json.Unmarshal(out.Bytes(), &got))
		assert.Equal(t, "original", got["loadId"])
		require.Contains(t, out.String(), `"accepted"`)
		require.NotContains(t, out.String(), `"completed"`)
		assert.Empty(t, stderr.String())
	}
	assert.Equal(t, int32(2), calls.Load())
}

func TestCancelLoadCLIUsesOneRequestWithOriginalLoadIDAndPrintsFalseCleanup(t *testing.T) {
	for _, tc := range []struct {
		args []string
		path string
	}{
		{[]string{"session", "cancel-load", "a/b%2F"}, "/apis/v1/sessions/a%2Fb%252F/history-load/cancel"},
		{[]string{"session", "cancel-load", "task", "-n", "demo"}, "/apis/v1/namespaces/demo/sessions/task/history-load/cancel"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			var calls atomic.Int32
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				assert.Equal(t, "POST", r.Method)
				assert.Equal(t, tc.path, r.URL.EscapedPath())
				b, err := io.ReadAll(r.Body)
				assert.NoError(t, err)
				assert.JSONEq(t, `{"loadId":" original/% load "}`, string(b))
				fmt.Fprint(w, `{"loadId":" original/% load ","phase":"canceled","nativeVerified":false,"nativeResumed":false,"nativeCompleted":false,"deliveryCompleted":false,"cleanupCompleted":false}`)
			}))
			defer h.Close()
			var out, stderr bytes.Buffer
			args := append([]string{"--url", h.URL}, tc.args...)
			args = append(args, "--load-id", " original/% load ")
			assert.Equal(t, 0, subcommand.Run(t.Context(), args, strings.NewReader(""), &out, &stderr), stderr.String())
			var got map[string]any
			require.NoError(t, json.Unmarshal(out.Bytes(), &got))
			assert.Equal(t, false, got["cleanupCompleted"])
			assert.Equal(t, " original/% load ", got["loadId"])
			assert.Equal(t, int32(1), calls.Load())
		})
	}
}

func TestLoadCLIInvalidFlagsNeverDispatch(t *testing.T) {
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Errorf("invalid input dispatched: %s", r.URL) }))
	defer h.Close()
	valid := []string{"session", "load", "imported", "-n", "demo", "--runtime", "agent", "--cwd", "/remote", "--session-id", "native"}
	for _, extra := range [][]string{{"--system-prompt", "x"}, {"--additional-dir", "/x"}, {"--wait"}, {"--session-id", "other"}} {
		var out, stderr bytes.Buffer
		args := append([]string{"--url", h.URL}, valid...)
		args = append(args, extra...)
		assert.Equal(t, 2, subcommand.Run(t.Context(), args, strings.NewReader(""), &out, &stderr), stderr.String())
	}
	for _, args := range [][]string{
		{"session", "load", "imported"},
		{"session", "load", "imported", "-n", "demo", "--runtime", "agent", "--cwd", "/remote", "--session-id", ""},
		{"session", "load", "imported", "-n", "demo", "--runtime", "agent", "--cwd", "/remote", "--session-id", ".."},
		{"session", "cancel-load", "task"},
		{"session", "cancel-load", "task", "--load-id", ""},
	} {
		var out, stderr bytes.Buffer
		assert.Equal(t, 2, subcommand.Run(t.Context(), append([]string{"--url", h.URL}, args...), strings.NewReader(""), &out, &stderr), stderr.String())
	}
}

func TestLoadCLIUnconfirmedResponseNeverRetriesOrImplicitlyCancels(t *testing.T) {
	for _, mode := range []string{"timeout", "interrupt", "uncertain"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			release := make(chan struct{})
			var calls atomic.Int32
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				assert.Equal(t, "POST", r.Method)
				assert.Equal(t, "/apis/v1/sessions/load", r.URL.EscapedPath())
				_, _ = io.Copy(io.Discard, r.Body)
				if mode == "uncertain" {
					w.WriteHeader(http.StatusInternalServerError)
					fmt.Fprint(w, `{"code":"outcome_unknown","reason":"operation_unconfirmed","message":"Inspect status"}`)
					return
				}
				if mode == "interrupt" {
					cancel()
				}
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer h.Close()
			defer close(release)
			var out, stderr bytes.Buffer
			args := []string{"--url", h.URL, "-o", "json", "--timeout", "100ms", "session", "load", "imported", "-n", "demo", "--runtime", "agent", "--cwd", "/remote", "--session-id", "native"}
			exit := subcommand.Run(ctx, args, strings.NewReader(""), &out, &stderr)
			if mode == "interrupt" {
				assert.Equal(t, 130, exit, stderr.String())
			} else {
				assert.Equal(t, 1, exit, stderr.String())
			}
			assert.Empty(t, out.String())
			var got map[string]any
			require.NoError(t, json.Unmarshal(stderr.Bytes(), &got))
			assert.Equal(t, "outcome_unknown", got["code"])
			assert.Equal(t, int32(1), calls.Load())
		})
	}
}
