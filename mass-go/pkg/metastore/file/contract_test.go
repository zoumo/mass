package file_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/metastore/file"
)

func TestCorruptFileFailsClosed(t *testing.T) {
	dir := t.TempDir()
	s, err := file.Open(dir, false)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	path := filepath.Join(dir, ".store.json")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(b, []byte(" trailing garbage")...), 0o600))
	_, err = file.Open(dir, true)
	require.Error(t, err)
	_, err = file.Open(dir, false)
	require.Error(t, err, "writer must not discard or repair unreadable authority silently")
}
