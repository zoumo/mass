package subcommand_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/cmd/massctl/subcommand"
)

func TestTicket05ArchiveCommandIsAbsent(t *testing.T) {
	var dispatched atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		dispatched.Add(1)
	}))
	t.Cleanup(server.Close)

	var stdout, stderr bytes.Buffer
	code := subcommand.Run(t.Context(), []string{"--url", server.URL, "session", "archive", "demo"}, nil, &stdout, &stderr)

	require.Equal(t, 2, code, stderr.String())
	require.Zero(t, dispatched.Load())
}
