// Package testutil supports offline transport integration tests.
package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TrackProcess execs only the test-built SDK mock and records its owned PID.
func TrackProcess(t *testing.T, executable string) (string, func()) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix process ownership check")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	wrapper := filepath.Join(dir, "mock-wrapper")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	script := "#!/bin/sh\nprintf '%s' \"$$\" > " + quote(pidFile) + "\nexec " + quote(executable) + " \"$@\"\n"
	require.NoError(t, os.WriteFile(wrapper, []byte(script), 0700))
	return wrapper, func() {
		t.Helper()
		data, err := os.ReadFile(pidFile)
		require.NoError(t, err)
		pid, err := strconv.Atoi(string(data))
		require.NoError(t, err)
		require.Eventually(t, func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH }, 2*time.Second, 10*time.Millisecond, "owned mock process survived Close")
	}
}
