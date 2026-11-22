package jsonrpc

import (
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInProcessPairWriteTimeout(t *testing.T) {
	client, server, err := NewInProcessPair(20*time.Millisecond, time.Second)
	require.NoError(t, err)
	require.ErrorIs(t, client.WriteObject(json.RawMessage(`{"jsonrpc":"2.0","id":"1","method":"initialize"}`)), os.ErrDeadlineExceeded)
	var message json.RawMessage
	require.ErrorIs(t, server.ReadObject(&message), io.EOF)
	require.ErrorIs(t, client.ReadObject(&message), io.EOF)
	require.NoError(t, server.Close())
}

func TestInProcessPairProtocolValidation(t *testing.T) {
	client, server, err := NewInProcessPair(time.Second, time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	read := make(chan error, 1)
	go func() {
		var message json.RawMessage
		read <- server.ReadObject(&message)
	}()
	require.NoError(t, client.WriteObject(json.RawMessage(`[{"jsonrpc":"2.0","id":"1","method":"initialize"}]`)))
	var response struct {
		Error *errorObject
	}
	require.NoError(t, client.ReadObject(&response))
	require.NotNil(t, response.Error)
	require.Equal(t, int64(-32600), response.Error.Code)
	require.NoError(t, client.WriteObject(json.RawMessage(`{"jsonrpc":"2.0","id":"2","method":"initialize"}`)))
	require.NoError(t, <-read)
}
