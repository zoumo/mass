package jsonrpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestNewStreamRequiresWriteTimeout(t *testing.T) {
	_, err := NewStream(&websocket.Conn{}, RoleClient, 0)
	require.ErrorContains(t, err, "write timeout")

	_, err = NewStream(nil, RoleClient, 1)
	require.ErrorContains(t, err, "WebSocket connection")
}

func newStreamPair(t *testing.T) (*Stream, *websocket.Conn) {
	t.Helper()
	serverConn := make(chan *websocket.Conn, 1)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serverConn <- conn
	}))
	t.Cleanup(httpServer.Close)
	client, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http"), nil)
	if response != nil {
		_ = response.Body.Close()
	}
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	peer := <-serverConn
	stream, err := NewStream(peer, RoleServer, time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })
	return stream, client
}

func TestStreamWriteObjectRejectsInvalidUTF8(t *testing.T) {
	stream, peer := newStreamPair(t)
	err := stream.WriteObject(struct {
		Value string `json:"value"`
	}{Value: "has-\xff"})
	if err == nil {
		require.NoError(t, peer.SetReadDeadline(time.Now().Add(time.Second)))
		_, data, readErr := peer.ReadMessage()
		require.NoError(t, readErr)
		t.Fatalf("lossy value reached peer: %q", data)
	}
	require.Error(t, err)
}

type surrogateMarshaler struct{}

func (surrogateMarshaler) MarshalJSON() ([]byte, error) {
	return []byte(`"\uD800"`), nil
}

func TestStreamWriteObjectRejectsRawSurrogate(t *testing.T) {
	stream, _ := newStreamPair(t)
	err := stream.WriteObject(struct {
		Value surrogateMarshaler `json:"value"`
	}{})
	require.ErrorIs(t, err, ErrLossyJSON)

	raw := json.RawMessage(`{"value":"\uD800"}`)
	require.ErrorIs(t, stream.WriteObject(raw), ErrLossyJSON)
}
