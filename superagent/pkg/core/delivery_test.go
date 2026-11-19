package core

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPermanentHandshakeRejection(t *testing.T) {
	for _, tc := range []struct {
		message string
		blocked bool
	}{
		{"WebSocket handshake failed: 401 Unauthorized", true},
		{"WebSocket handshake failed: 404 Not Found", true},
		{"WebSocket handshake failed: 429 Too Many Requests", false},
		{"WebSocket handshake failed: 503 Service Unavailable", false},
		{"connection refused", false},
	} {
		t.Run(tc.message, func(t *testing.T) {
			require.Equal(t, tc.blocked, permanentHandshakeRejection(errors.New(tc.message)))
		})
	}
}
