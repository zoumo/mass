package service_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/claude"
	"github.com/zoumo/mass/mass-go/pkg/runtime/codex"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

func TestOpeningCleanupClaudeInitializeEOF(t *testing.T) {
	dir := t.TempDir()
	peer := filepath.Join(dir, "peer")
	pidFile := filepath.Join(dir, "pid")
	require.NoError(t, os.WriteFile(peer, []byte("#!/bin/sh\necho $$ > '"+pidFile+"'\nread -r request\nexit 1\n"), 0o700))
	st, err := file.Open(filepath.Join(dir, "store"), false)
	require.NoError(t, err)
	defer st.Close()
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"claude": claude.Factory{RecoveryDir: filepath.Join(dir, "recovery")}}))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "claude", Config: runtime.Config{"executable": peer}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(ctx, "agent")
	require.NoError(t, err)
	created, err := svc.CreateSessionWithInput(ctx, resource.SessionCreateInput{
		Metadata:     resource.Metadata{Namespace: "default", Name: "first"},
		Spec:         resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: dir}},
		InitialInput: &runtime.Input{Content: []events.ContentBlock{events.Text("hello")}},
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "initialize", "release classification must retain the native failure")
	require.Empty(t, created.Status.SessionID)
	require.Nil(t, created.InitialInputReceipt)
	t.Logf("opening failure preserved: %v", err)
	pidText, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidText)))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH }, time.Second, time.Millisecond, "SDK must reap its owned child before reporting release")
	page, err := svc.ListSessions(resource.SessionQuery{})
	require.NoError(t, err)
	require.Empty(t, page.Items)
	_, err = svc.StopRuntime(ctx, "agent")
	require.NoError(t, err, "failed opening must not retain a reservation after SDK startup cleanup")
	require.NoError(t, svc.Shutdown(ctx))
	require.NoError(t, svc.Shutdown(ctx))
}

// Both adapters use the real SDK and a local process boundary, never a model.
func TestOpeningCleanupRuntimeFailures(t *testing.T) {
	for _, kind := range []string{"claude", "codex"} {
		for _, mode := range []string{"eof", "start-failure", "timeout"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				dir := t.TempDir()
				peer, pidFile, ready := filepath.Join(dir, "peer"), filepath.Join(dir, "pid"), filepath.Join(dir, "ready")
				if mode != "start-failure" {
					ending := "exit 1\n"
					if mode == "timeout" {
						ending = "echo ready > '" + ready + "'\nwhile read -r request; do :; done\n"
					}
					require.NoError(t, os.WriteFile(peer, []byte("#!/bin/sh\necho $$ > '"+pidFile+"'\nread -r request\n"+ending), 0o700))
				}
				var factory runtime.Factory = claude.Factory{RecoveryDir: filepath.Join(dir, "recovery")}
				if kind == "codex" {
					factory = codex.Factory{RecoveryDir: filepath.Join(dir, "recovery")}
				}
				driver, err := factory.New("agent", runtime.Config{"executable": peer})
				require.NoError(t, err)
				t.Cleanup(func() {
					cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					require.NoError(t, driver.Stop(cleanup))
				})
				stream, err := driver.Events()
				require.NoError(t, err)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if mode == "timeout" {
					var expire context.CancelFunc
					ctx, expire = context.WithTimeout(ctx, 2*time.Second)
					defer expire()
				}
				_, failure := driver.Start(ctx)
				if failure == nil {
					_, failure = driver.OpenSession(ctx, runtime.Address{Namespace: "default", SessionName: "first"}, runtime.SessionSpec{Cwd: dir})
				}
				require.Error(t, failure)
				if kind == "claude" {
					require.ErrorIs(t, failure, runtime.ErrResourcesReleased)
				}
				if mode == "timeout" {
					require.ErrorIs(t, failure, context.DeadlineExceeded)
					_, err = os.Stat(ready)
					require.NoError(t, err, "peer reached initialization before timeout")
				}
				ctx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cleanupCancel()
				require.NoError(t, driver.Stop(ctx))
				require.NoError(t, driver.Stop(ctx))
				_, err = stream.Next(ctx)
				require.ErrorIs(t, err, io.EOF)
				if mode != "start-failure" {
					text, err := os.ReadFile(pidFile)
					require.NoError(t, err)
					pid, err := strconv.Atoi(strings.TrimSpace(string(text)))
					require.NoError(t, err)
					require.Eventually(t, func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH }, time.Second, time.Millisecond)
				}
			})
		}
	}
}

func TestOpeningCleanupConcurrentStopShutdown(t *testing.T) {
	dir := t.TempDir()
	peer, ready, pidFile := filepath.Join(dir, "peer"), filepath.Join(dir, "ready"), filepath.Join(dir, "pid")
	require.NoError(t, os.WriteFile(peer, []byte("#!/bin/sh\necho $$ > '"+pidFile+"'\nread -r request\necho ready > '"+ready+"'\nwhile read -r request; do :; done\n"), 0o700))
	st, err := file.Open(filepath.Join(dir, "store"), false)
	require.NoError(t, err)
	defer st.Close()
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"claude": claude.Factory{RecoveryDir: filepath.Join(dir, "recovery")}}))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "claude", Config: runtime.Config{"executable": peer}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(ctx, "agent")
	require.NoError(t, err)
	opening := make(chan error, 1)
	go func() {
		_, e := svc.CreateSession(ctx, resource.Session{Metadata: resource.Metadata{Namespace: "default", Name: "first"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: dir}}})
		opening <- e
	}()
	require.Eventually(t, func() bool { _, e := os.Stat(ready); return e == nil }, 5*time.Second, time.Millisecond)
	stopped, shutdown := make(chan error, 1), make(chan error, 1)
	go func() { _, e := svc.StopRuntime(ctx, "agent"); stopped <- e }()
	go func() { shutdown <- svc.Shutdown(ctx) }()
	require.Error(t, <-opening)
	require.NoError(t, <-stopped)
	require.NoError(t, <-shutdown)
	require.NoError(t, svc.Shutdown(ctx))
	r, err := svc.GetRuntime("agent")
	require.NoError(t, err)
	require.Equal(t, "stopped", r.Status.State)
	page, err := svc.ListSessions(resource.SessionQuery{})
	require.NoError(t, err)
	require.Empty(t, page.Items)
	text, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(text)))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH }, time.Second, time.Millisecond)
}
