package subcommand_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/cmd/massctl/subcommand"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestSessionLoadCLIPrintsAcceptedReceiptWithoutLocalPathAccessOrFollowup(t *testing.T) {
	var calls atomic.Int32
	endpoint := newRPCService(t, "", func(call *rpcTestCall) {
		calls.Add(1)
		assert.Equal(t, "session.load", call.Method)
		assert.JSONEq(t, `{"input":{"metadata":{"namespace":"demo","name":"imported"},"runtimeRef":"agent","cwd":"/agent/does-not-exist-locally","sessionId":"a/b%2F"}}`, string(call.Params))
		call.respond(json.RawMessage(`{"loadId":"original","session":{"status":{"sessionId":"a/b%2F","historyLoad":{"loadId":"original","phase":"accepted","nativeVerified":false,"nativeResumed":false,"nativeCompleted":false,"deliveryCompleted":false,"cleanupCompleted":false}}}}`))
	})
	for _, output := range [][]string{nil, {"-o", "json"}} {
		var out, stderr bytes.Buffer
		args := append([]string{"--url", endpoint}, output...)
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
		args     []string
		expected string
	}{
		{[]string{"session", "cancel-load", "a/b%2F"}, `{"session":{"sessionId":"a/b%2F"},"loadId":" original/% load "}`},
		{[]string{"session", "cancel-load", "task", "-n", "demo"}, `{"session":{"namespace":"demo","name":"task"},"loadId":" original/% load "}`},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var calls atomic.Int32
			endpoint := newRPCService(t, "", func(call *rpcTestCall) {
				calls.Add(1)
				assert.Equal(t, "session.cancelLoad", call.Method)
				assert.JSONEq(t, tc.expected, string(call.Params))
				call.respond(json.RawMessage(`{"loadId":" original/% load ","phase":"canceled","nativeVerified":false,"nativeResumed":false,"nativeCompleted":false,"deliveryCompleted":false,"cleanupCompleted":false}`))
			})
			var out, stderr bytes.Buffer
			args := append([]string{"--url", endpoint}, tc.args...)
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
	var dispatched atomic.Int32
	endpoint := newRPCService(t, "", func(*rpcTestCall) { dispatched.Add(1) })
	valid := []string{"session", "load", "imported", "-n", "demo", "--runtime", "agent", "--cwd", "/remote", "--session-id", "native"}
	for _, extra := range [][]string{{"--system-prompt", "x"}, {"--additional-dir", "/x"}, {"--wait"}, {"--session-id", "other"}} {
		var out, stderr bytes.Buffer
		args := append([]string{"--url", endpoint}, valid...)
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
		assert.Equal(t, 2, subcommand.Run(t.Context(), append([]string{"--url", endpoint}, args...), strings.NewReader(""), &out, &stderr), stderr.String())
	}
	assert.Zero(t, dispatched.Load())
}

func TestLoadCLIUnconfirmedResponseNeverRetriesOrImplicitlyCancels(t *testing.T) {
	for _, mode := range []string{"timeout", "interrupt", "uncertain"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var calls atomic.Int32
			endpoint := newRPCService(t, "", func(call *rpcTestCall) {
				calls.Add(1)
				assert.Equal(t, "session.load", call.Method)
				switch mode {
				case "uncertain":
					call.fail(runtime.OperationError{Code: "outcome_unknown", Reason: "operation_unconfirmed", Message: "Inspect status"})
				case "interrupt":
					cancel()
				}
			})
			var out, stderr bytes.Buffer
			args := []string{"--url", endpoint, "-o", "json", "--timeout", "100ms", "session", "load", "imported", "-n", "demo", "--runtime", "agent", "--cwd", "/remote", "--session-id", "native"}
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
