package vertical_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMetadataBackendStartupFailures(t *testing.T) {
	for _, test := range []struct{ name, config, path string }{
		{"unknown", "metaStore:\n  backend: invalid\n", ""},
		{"empty", "metaStore:\n  backend: \"\"\n", ""},
		{"default-unavailable", "", "metadata-authority.sqlite"},
		{"sqlite-unavailable", "metaStore:\n  backend: sqlite\n", "metadata-authority.sqlite"},
		{"file-unavailable", "metaStore:\n  backend: file\n", ".store.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			config := filepath.Join(dir, "daemon.yaml")
			require.NoError(t, os.WriteFile(config, []byte("listen: 127.0.0.1:0\nlocal: true\n"+test.config), 0o600))
			if test.path != "" {
				require.NoError(t, os.Mkdir(filepath.Join(dir, test.path), 0o700))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, app, "--config", config, "--data", dir)
			var out bytes.Buffer
			cmd.Stdout = &out
			err := cmd.Run()
			require.Error(t, err)
			require.NoError(t, ctx.Err(), "must fail promptly without waiting for cancellation")
			require.Empty(t, out.String(), "failed backend must not publish a ready listener")
			if test.path != "" {
				info, err := os.Stat(filepath.Join(dir, test.path))
				require.NoError(t, err)
				require.True(t, info.IsDir(), "unavailable authority must remain untouched")
			}
		})
	}
}
