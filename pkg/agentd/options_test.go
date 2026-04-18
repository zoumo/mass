package agentd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOptions_Validate(t *testing.T) {
	t.Parallel()

	t.Run("empty root returns error", func(t *testing.T) {
		err := Options{Root: ""}.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Root must not be empty")
	})

	t.Run("non-empty root passes", func(t *testing.T) {
		err := Options{Root: "/tmp/mass"}.Validate()
		require.NoError(t, err)
	})
}

func TestOptions_PathDerivation(t *testing.T) {
	t.Parallel()
	o := Options{Root: "/tmp/test-mass"}

	assert.Equal(t, "/tmp/test-mass/mass.sock", o.SocketPath())
	assert.Equal(t, "/tmp/test-mass/workspaces", o.WorkspaceRoot())
	assert.Equal(t, "/tmp/test-mass/bundles", o.BundleRoot())
	assert.Equal(t, "/tmp/test-mass/mass.db", o.MetaDBPath())
}
