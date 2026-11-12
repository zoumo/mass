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
		{input: "ws://127.0.0.1:24368", want: "ws://127.0.0.1:24368/rpc", ok: true},
		{input: "wss://example.test/", want: "wss://example.test/rpc", ok: true},
		{input: "ws://example.test/rpc", want: "ws://example.test/rpc", ok: true},
		{input: "http://127.0.0.1:24368", ok: false},
		{input: "https://example.test/", ok: false},
		{input: "http://example.test/apis", ok: false},
		{input: "http://example.test?token=x", ok: false},
		{input: "ws://example.test/apis", ok: false},
		{input: "ws://example.test/rpc?token=x", ok: false},
	} {
		t.Run(test.input, func(t *testing.T) {
			endpoint, err := websocketEndpoint(test.input)
			if !test.ok {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, endpoint)
		})
	}
}
