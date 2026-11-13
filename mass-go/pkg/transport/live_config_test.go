package transport_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type liveConfigAPI struct {
	transport.API
	calls atomic.Int32
}

func (a *liveConfigAPI) SetSessionConfig(_ context.Context, _ resource.SessionRef, change runtime.ConfigChange) (runtime.ConfigReceipt, error) {
	a.calls.Add(1)
	return runtime.ConfigReceipt{
		Change:     change,
		Acceptance: agent.Acceptance{Sent: runtime.Yes(), NativeAccepted: runtime.Bool(false)},
	}, nil
}

func newLiveConfigRPCFixture(t *testing.T) (*liveConfigAPI, *httptest.Server) {
	t.Helper()
	api := &liveConfigAPI{}
	endpoint, err := transport.New(api, withTestWriteTimeout(transport.Options{LocalOnly: true}))
	require.NoError(t, err)
	server := httptest.NewServer(endpoint)
	t.Cleanup(func() {
		endpoint.Shutdown()
		server.Close()
	})
	return api, server
}

func TestLiveSessionConfigurationRPCValidation(t *testing.T) {
	api, server := newLiveConfigRPCFixture(t)
	conn := dialRPC(t, server, nil)
	sendText(t, conn, initializeMessage(`"init"`, "1"))
	require.Nil(t, readRPC(t, conn).Error)

	cases := []struct {
		change string
		code   int64
	}{
		{`null`, -32602},
		{`[]`, -32602},
		{`{}`, -32602},
		{`{"id":"model"}`, -32602},
		{`{"id":"effort","value":null}`, -32602},
		{`{"id":"effort","value":42}`, -32602},
		{`{"id":"future","value":"x"}`, -32602},
		{`{"id":"model","value":""}`, -32602},
		{`{"id":"model","value":"x\u0000y"}`, -32602},
		{`{"id":"effort","value":"\ud800"}`, -32602},
		{`{"id":"model","value":"\udc00"}`, -32602},
		{`{"id":"model","value":"x","extra":true}`, -32602},
		{`{"id":"model","id":"effort","value":"x"}`, -32600},
	}
	for i, tc := range cases {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			id := fmt.Sprintf("invalid-%d", i)
			sendCall(t, conn, id, "session.setConfig", fmt.Sprintf(`{"session":{"sessionId":"native"},"change":%s}`, tc.change))
			response := readRPC(t, conn)
			require.NotNil(t, response.Error)
			require.Equal(t, tc.code, response.Error.Code)
			wantID := fmt.Sprintf("%q", id)
			if tc.code == -32600 {
				// Envelope-level rejection cannot safely trust the duplicated ID field.
				wantID = "null"
			}
			require.Equal(t, wantID, string(response.ID))
		})
	}

	invalidUTF8 := dialRPC(t, server, nil)
	sendText(t, invalidUTF8, initializeMessage(`"init-invalid-utf8"`, "1"))
	require.Nil(t, readRPC(t, invalidUTF8).Error)
	sendText(t, invalidUTF8, `{"jsonrpc":"2.0","id":"invalid-utf8","method":"session.setConfig","params":{"session":{"sessionId":"native"},"change":{"id":"model","value":"`+string([]byte{0xff})+`"}}}`)
	_, _, err := invalidUTF8.ReadMessage()
	var closeErr *websocket.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, websocket.CloseInvalidFramePayloadData, closeErr.Code)
	require.Zero(t, api.calls.Load())
}

func TestLiveSessionConfigurationRPCWireReceipt(t *testing.T) {
	api, server := newLiveConfigRPCFixture(t)
	for _, session := range []string{
		`{"sessionId":"native"}`,
		`{"namespace":"one","name":"name"}`,
	} {
		conn := dialRPC(t, server, nil)
		sendText(t, conn, initializeMessage(`"init"`, "1"))
		require.Nil(t, readRPC(t, conn).Error)
		sendCall(t, conn, "set", "session.setConfig", fmt.Sprintf(`{
			"session":%s,
			"change":{"id":"model","value":" Native Model "}
		}`, session))
		response := readRPC(t, conn)
		require.Nil(t, response.Error)
		require.JSONEq(t, `{
			"change":{"id":"model","value":" Native Model "},
			"acceptance":{"sent":true,"nativeAccepted":false}
		}`, string(response.Result))
	}
	require.EqualValues(t, 2, api.calls.Load())
}
