package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSessionIdleTimeoutConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cmd := newCommand()
	require.NotNil(t, cmd.Flags().Lookup("session-idle-timeout"))
	got, err := readOptions(cmd, filepath.Join(home, ".mass", "config.yaml"))
	require.NoError(t, err)
	require.Equal(t, 10*time.Minute, got.SessionIdleTimeout)

	path := filepath.Join(home, "daemon.yaml")
	require.NoError(t, os.WriteFile(path, []byte("sessionIdleTimeout: 5s\n"), 0o600))
	got, err = readOptions(newCommand(), path)
	require.NoError(t, err)
	require.Equal(t, 5*time.Second, got.SessionIdleTimeout)

	cmd = newCommand()
	require.NoError(t, cmd.Flags().Set("session-idle-timeout", "15s"))
	got, err = readOptions(cmd, path)
	require.NoError(t, err)
	require.Equal(t, 15*time.Second, got.SessionIdleTimeout)

	require.NoError(t, os.WriteFile(path, []byte("sessionIdleTimeout: 0s\n"), 0o600))
	got, err = readOptions(newCommand(), path)
	require.NoError(t, err)
	require.Zero(t, got.SessionIdleTimeout)

	for _, value := range []string{"-1s", "not-a-duration", "5", "true", "[]"} {
		require.NoError(t, os.WriteFile(path, []byte("sessionIdleTimeout: "+value+"\n"), 0o600))
		_, err = readOptions(newCommand(), path)
		require.Error(t, err, value)
	}

	cmd = newCommand()
	require.NoError(t, cmd.Flags().Set("session-idle-timeout", "-1s"))
	_, err = readOptions(cmd, filepath.Join(home, "missing.yaml"))
	require.Error(t, err)
}

func TestRunRejectsNegativeSessionIdleTimeoutBeforeStartup(t *testing.T) {
	err := run(context.Background(), options{SessionIdleTimeout: -time.Second})
	require.ErrorContains(t, err, "session idle timeout must not be negative")
}
