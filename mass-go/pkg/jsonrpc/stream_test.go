package jsonrpc

import (
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestNewStreamRequiresWriteTimeout(t *testing.T) {
	_, err := NewStream(&websocket.Conn{}, RoleClient, 0)
	require.ErrorContains(t, err, "write timeout")

	_, err = NewStream(nil, RoleClient, 1)
	require.ErrorContains(t, err, "WebSocket connection")
}
