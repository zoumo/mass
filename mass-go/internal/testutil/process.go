// Package testutil supports offline transport integration tests.
package testutil

import (
	"context"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
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

// WaitCreation observes an operation without transferring its lifetime to a wait.
func WaitCreation(t *testing.T, worker rt.Runtime, ref, phase string) rt.CreationOperation {
	t.Helper()
	var op rt.CreationOperation
	require.Eventually(t, func() bool {
		var err error
		op, err = worker.GetOperation(ref)
		return err == nil && op.Phase == phase
	}, 6*time.Second, time.Millisecond, "creation did not reach %s", phase)
	return op
}
func OpenSession(t *testing.T, worker rt.Runtime, ctx context.Context, options rt.SessionOptions) rt.Session {
	t.Helper()
	op, err := worker.CreateSession(ctx, options)
	require.NoError(t, err)
	op = WaitCreation(t, worker, op.OperationRef, "opened")
	require.NotNil(t, op.Key)
	session, err := worker.GetSession(*op.Key)
	require.NoError(t, err)
	return session
}
