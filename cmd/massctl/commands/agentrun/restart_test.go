package agentrun

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

func TestRestartSuccess(t *testing.T) {
	var restarted bool
	mc := newMockClient()
	mc.agentRunOps.restartFn = func(_ context.Context, _ pkgariapi.ObjectKey) (*pkgariapi.AgentRun, error) {
		restarted = true
		return &pkgariapi.AgentRun{}, nil
	}

	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"restart", "run1", "-w", "ws1"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.True(t, restarted, "Restart should have been called")
}
