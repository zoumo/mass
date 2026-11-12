package subcommand_test

import (
	"bytes"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/cmd/massctl/subcommand"
)

func TestTicket05ArchiveCommandIsAbsent(t *testing.T) {
	var dispatched atomic.Int32
	endpoint := newRPCService(t, "", func(*rpcTestCall) { dispatched.Add(1) })
	var stdout, stderr bytes.Buffer
	code := subcommand.Run(t.Context(), []string{"--url", endpoint, "session", "archive", "demo"}, nil, &stdout, &stderr)
	require.Equal(t, 2, code, stderr.String())
	require.Zero(t, dispatched.Load())
}
