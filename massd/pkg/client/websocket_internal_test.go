package client

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWebSocketEndpoint(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "ws://127.0.0.1:24368", want: "ws://127.0.0.1:24368/jsonrpc", ok: true},
		{input: "wss://example.test/", want: "wss://example.test/jsonrpc", ok: true},
		{input: "ws://example.test/jsonrpc", want: "ws://example.test/jsonrpc", ok: true},
		{input: "ws://example.test/jsonrpc/", want: "ws://example.test/jsonrpc", ok: true},
		{input: "wss://example.test/massd", want: "wss://example.test/massd/jsonrpc", ok: true},
		{input: "wss://example.test/apps/massd/", want: "wss://example.test/apps/massd/jsonrpc", ok: true},
		{input: "wss://example.test/apps/massd/jsonrpc", want: "wss://example.test/apps/massd/jsonrpc", ok: true},
		{input: "wss://example.test/apps/massd/jsonrpc/", want: "wss://example.test/apps/massd/jsonrpc", ok: true},
		{input: "ws://[::1]:24368/massd", want: "ws://[::1]:24368/massd/jsonrpc", ok: true},
		{input: "http://127.0.0.1:24368", ok: false},
		{input: "https://example.test/", ok: false},
		{input: "ws:///massd", ok: false},
		{input: "ws://:24368/jsonrpc", ok: false},
		{input: "ws://user:password@example.test/massd", ok: false},
		{input: "ws://example.test/jsonrpc?token=x", ok: false},
		{input: "ws://example.test/jsonrpc?", ok: false},
		{input: "ws://example.test/jsonrpc#fragment", ok: false},
		{input: "ws://example.test/jsonrpc#", ok: false},
		{input: "ws://example.test//massd", ok: false},
		{input: "ws://example.test/massd//", ok: false},
		{input: "ws://example.test/apps/../massd", ok: false},
		{input: "ws://example.test/apps/./massd", ok: false},
		{input: "ws://example.test/apps%2fmassd", ok: false},
		{input: "ws://example.test/apps%5Cmassd", ok: false},
	} {
		t.Run(test.input, func(t *testing.T) {
			endpoint, err := WebSocketEndpoint(test.input)
			if !test.ok {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, endpoint)
		})
	}
}
