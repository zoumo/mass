package agentrun

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

func TestCancelSuccess(t *testing.T) {
	var canceled bool
	mc := newMockClient()
	mc.agentRunOps.cancelFn = func(_ context.Context, _ pkgariapi.ObjectKey) error {
		canceled = true
		return nil
	}

	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"cancel", "run1", "-w", "ws1"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.True(t, canceled, "Cancel should have been called")
}
