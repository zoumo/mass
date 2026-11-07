package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEqualNativePath(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "real")
	other := filepath.Join(root, "other")
	alias := filepath.Join(root, "alias")
	require.NoError(t, os.Mkdir(target, 0o700))
	require.NoError(t, os.Mkdir(other, 0o700))
	require.NoError(t, os.Symlink(target, alias))

	require.True(t, EqualNativePath(alias, target))
	require.True(t, EqualNativePath(target, filepath.Join(target, ".")))
	require.True(t, EqualNativePath(filepath.Join(root, "missing"), filepath.Join(root, "missing")))
	require.False(t, EqualNativePath(target, other))
	require.False(t, EqualNativePath("relative", target))
	require.False(t, EqualNativePath(target, filepath.Join(root, "missing")))
}
