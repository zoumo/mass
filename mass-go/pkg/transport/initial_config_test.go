package transport_test

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/transport"
)

func TestInitialSessionConfigurationRPCRejectsInvalidBeforeEffects(t *testing.T) {
	endpoint, err := transport.New(nil, withTestWriteTimeout(transport.Options{LocalOnly: true}))
	require.NoError(t, err)
	server := httptest.NewServer(endpoint)
	t.Cleanup(func() {
		endpoint.Shutdown()
		server.Close()
	})
	conn := dialRPC(t, server, nil)
	sendText(t, conn, initializeMessage(`"init"`, "1"))
	require.Nil(t, readRPC(t, conn).Error)

	cases := []string{`null`, `1`, `"model"`, `[]`, `{"model":null}`, `{"effort":null}`, `{"model":true}`, `{"effort":42}`, `{"model":"x\u0000y"}`, `{"effort":"\ud800"}`, `{"model":"\udc00"}`, `{"future":"x"}`}
	for _, action := range []string{"create", "load"} {
		for index, config := range cases {
			t.Run(fmt.Sprintf("%s/%d", action, index), func(t *testing.T) {
				id := fmt.Sprintf("%s-%d", action, index)
				if action == "create" {
					sendCall(t, conn, id, "session.create", `{"input":{"metadata":{"namespace":"one","name":"configured"},"spec":{"runtimeRef":"missing","cwd":"/work","config":`+config+`}}}`)
				} else {
					sendCall(t, conn, id, "session.load", `{"input":{"metadata":{"namespace":"one","name":"configured"},"runtimeRef":"missing","cwd":"/work","sessionId":"native","config":`+config+`}}`)
				}
				response := readRPC(t, conn)
				require.NotNil(t, response.Error)
				require.Equal(t, int64(-32602), response.Error.Code)
				require.Equal(t, fmt.Sprintf("%q", id), string(response.ID))
			})
		}
	}

	invalidUTF8 := dialRPC(t, server, nil)
	sendText(t, invalidUTF8, initializeMessage(`"init-invalid-utf8"`, "1"))
	require.Nil(t, readRPC(t, invalidUTF8).Error)
	sendText(t, invalidUTF8, `{"jsonrpc":"2.0","id":"invalid-utf8","method":"session.create","params":{"input":{"metadata":{"namespace":"one","name":"configured"},"spec":{"runtimeRef":"missing","cwd":"/work","config":{"model":"`+string([]byte{0xff})+`"}}}}`)
	_, _, err = invalidUTF8.ReadMessage()
	var closeErr *websocket.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, websocket.CloseInvalidFramePayloadData, closeErr.Code)
}
