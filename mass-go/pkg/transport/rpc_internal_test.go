package transport

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRPCConnectionDuplicateInflightID(t *testing.T) {
	connection := &rpcConnection{inflight: map[string]struct{}{}}
	require.True(t, connection.begin("id"))
	require.False(t, connection.begin("id"))
	connection.end("id")
	require.True(t, connection.begin("id"))
}
