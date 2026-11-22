package transport_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/massd/pkg/resource"
	"github.com/zoumo/mass/massd/pkg/runtime"
	"github.com/zoumo/mass/massd/pkg/transport"
)

type injectAPI struct {
	transport.API
	calls   atomic.Int32
	receipt runtime.InjectReceipt
	err     error
}

func (a *injectAPI) Inject(context.Context, resource.SessionRef, runtime.Input) (runtime.InjectReceipt, error) {
	a.calls.Add(1)
	return a.receipt, a.err
}

func TestRPCInjectEvidenceAndError(t *testing.T) {
	for _, tc := range []struct {
		name     string
		evidence runtime.Acceptance
		code     string
	}{
		{"unknown_fields_absent", runtime.Acceptance{}, ""},
		{"not_applied", runtime.Acceptance{Sent: runtime.Bool(false), NativeAccepted: runtime.Bool(false), NativeRecorded: runtime.Bool(false)}, ""},
		{"unknown", runtime.Acceptance{Sent: runtime.Yes()}, "outcome_unknown"},
		{"rejected", runtime.Acceptance{Sent: runtime.Yes(), NativeAccepted: runtime.Bool(false)}, "native_rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &injectAPI{receipt: runtime.InjectReceipt{Acceptance: tc.evidence}}
			if tc.code != "" {
				api.err = &runtime.OperationError{Code: tc.code, Reason: "synthetic", Message: "Synthetic outcome"}
			}
			endpoint, err := transport.New(api, withTestWriteTimeout(transport.Options{}))
			require.NoError(t, err)
			server := httptest.NewServer(endpoint)
			t.Cleanup(func() { endpoint.Shutdown(); server.Close() })
			conn := dialRPC(t, server, nil)
			sendText(t, conn, initializeMessage(`"init"`, "1"))
			require.Nil(t, readRPC(t, conn).Error)
			sendCall(t, conn, "inject", "session.inject", `{"session":{"sessionId":"native"},"input":{"content":[{"type":"text","text":"context"}]}}`)
			response := readRPC(t, conn)
			var receipt runtime.InjectReceipt
			if tc.code == "" {
				require.Nil(t, response.Error)
				require.NoError(t, json.Unmarshal(response.Result, &receipt))
			} else {
				require.Empty(t, response.Result)
				require.EqualValues(t, -32000, response.Error.Code)
				var data struct {
					runtime.OperationError
					Receipt runtime.InjectReceipt
				}
				require.NoError(t, json.Unmarshal(response.Error.Data, &data))
				require.Equal(t, tc.code, data.Code)
				receipt = data.Receipt
			}
			require.Equal(t, api.receipt, receipt)
			require.EqualValues(t, 1, api.calls.Load())
		})
	}
}

func TestRPCInjectRejectsMalformedInput(t *testing.T) {
	api := &injectAPI{}
	endpoint, err := transport.New(api, withTestWriteTimeout(transport.Options{}))
	require.NoError(t, err)
	server := httptest.NewServer(endpoint)
	t.Cleanup(func() { endpoint.Shutdown(); server.Close() })
	conn := dialRPC(t, server, nil)
	sendText(t, conn, initializeMessage(`"init"`, "1"))
	require.Nil(t, readRPC(t, conn).Error)
	for i, params := range []string{
		`{}`, `{"session":null,"input":{}}`,
		`{"session":{"sessionId":"native"},"input":null}`,
		`{"session":{"sessionId":"native"},"input":{}}`,
		`{"session":{"sessionId":"native"},"input":{"content":null}}`,
		`{"session":{"sessionId":"native"},"input":{"content":[],"extra":true}}`,
		`{"session":{"sessionId":"native","name":"mixed"},"input":{"content":[]}}`,
		`{"session":{"sessionId":"native"},"input":{"content":[{"type":"text","text":42}]}}`,
	} {
		id := strconv.Itoa(i)
		sendCall(t, conn, id, "session.inject", params)
		response := readRPC(t, conn)
		require.NotNil(t, response.Error)
		require.EqualValues(t, -32602, response.Error.Code)
		require.Empty(t, response.Result)
	}
	require.Zero(t, api.calls.Load())
}
