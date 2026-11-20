package subcommand

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestComposeRequiresBothConfigs(t *testing.T) {
	for _, args := range [][]string{
		{"compose", "--massd-config", "massd.yaml"},
		{"compose", "--core-config", "core.yaml"},
	} {
		command := New()
		command.SetArgs(args)
		require.ErrorContains(t, command.Execute(), "required flag")
	}
}

func TestMassdStartupAndReadiness(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" || r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "/rpc"
	daemon := startStubMassd(t, fmt.Sprintf("printf '%%s\\n' '{\"url\":%q}'\nexec /bin/sleep 10\n", endpoint))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	actual, err := daemon.waitReady(ctx)
	require.NoError(t, err)
	require.Equal(t, endpoint, actual)
	require.GreaterOrEqual(t, requests.Load(), int32(2))
	require.NoError(t, daemon.stop(time.Second))
	require.Equal(t, syscall.SIGTERM, daemon.cmd.ProcessState.Sys().(syscall.WaitStatus).Signal())
}

func TestMassdStartupFailures(t *testing.T) {
	for _, tc := range []struct {
		name, script, failure string
		deadline              time.Duration
		exitCode              int
	}{
		{name: "early exit", script: "exit 42", failure: "massd", deadline: 5 * time.Second, exitCode: 42},
		{name: "invalid startup output", script: "printf 'invalid\\n'; exec /bin/sleep 10", failure: "invalid massd startup record", deadline: 5 * time.Second},
		{name: "startup timeout", script: "exec /bin/sleep 10", failure: "waiting for massd startup: context deadline exceeded", deadline: 500 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			daemon := startStubMassd(t, tc.script)
			ctx, cancel := context.WithTimeout(t.Context(), tc.deadline)
			defer cancel()
			_, err := daemon.waitReady(ctx)
			require.ErrorContains(t, err, tc.failure)
			if tc.exitCode != 0 {
				select {
				case <-daemon.done:
					require.Equal(t, tc.exitCode, daemon.cmd.ProcessState.ExitCode())
				case <-ctx.Done():
					t.Fatal("massd process was not reaped")
				}
			}
		})
	}
}

func TestMassdReadinessTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "/rpc"
	daemon := startStubMassd(t, fmt.Sprintf("printf '%%s\\n' '{\"url\":%q}'\nexec /bin/sleep 30\n", endpoint))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := daemon.waitReady(ctx)
	require.ErrorContains(t, err, "waiting for massd readiness: context deadline exceeded")
}

func TestMassdForcedShutdownWaitsForExit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "/rpc"
	daemon := startStubMassd(t, fmt.Sprintf("trap '' TERM\nprintf '%%s\\n' '{\"url\":%q}'\nexec /bin/sleep 10\n", endpoint))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := daemon.waitReady(ctx)
	require.NoError(t, err)
	require.ErrorContains(t, daemon.stop(10*time.Millisecond), "massd exceeded shutdown timeout")
	require.NotNil(t, daemon.cmd.ProcessState)
}

func startStubMassd(t *testing.T, script string) *massdProcess {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "massd")
	require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh\n"+script+"\n"), 0o700))
	daemon, err := startMassd(binary, filepath.Join(dir, "massd.yaml"), io.Discard)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, daemon.stop(time.Second))
		require.NotNil(t, daemon.cmd.ProcessState)
	})
	return daemon
}
