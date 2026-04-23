package agentrun

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

func TestCreateMissingFlags(t *testing.T) {
	mc := newMockClient()
	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"create"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	assert.Error(t, err, "create without required flags should fail")
}

func TestCreateSuccess(t *testing.T) {
	var created bool
	mc := newMockClient()
	mc.createFn = func(_ context.Context, _ pkgariapi.Object) error {
		created = true
		return nil
	}

	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"create", "--workspace", "ws1", "--name", "run1", "--agent", "agent1", "--no-wait"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.True(t, created, "Create should have been called")
}
