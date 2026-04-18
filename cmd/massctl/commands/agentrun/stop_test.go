package agentrun

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

func TestStopSuccess(t *testing.T) {
	var stopped bool
	mc := newMockClient()
	mc.agentRunOps.stopFn = func(_ context.Context, _ pkgariapi.ObjectKey) error {
		stopped = true
		return nil
	}

	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"stop", "run1", "-w", "ws1"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.True(t, stopped, "Stop should have been called")
}
