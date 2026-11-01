package service_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/claude"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

// This subprocess is a synthetic native protocol peer, not a model. It never
// emits system/init and never disconnects voluntarily after initialize.
func TestPendingNativeCreatePeer(t *testing.T) {
	dir := os.Getenv("MASS_PENDING_CREATE_PEER")
	if dir == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(2)
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var frame struct {
			Type    string `json:"type"`
			ID      string `json:"request_id"`
			Request struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
		}
		if os.Getenv("MASS_PENDING_EXPLICIT") == "true" && json.Unmarshal(scanner.Bytes(), &frame) == nil && frame.Type == "user" {
			var input struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(scanner.Bytes(), &input) != nil || input.Message.Content != "caller first" {
				os.Exit(5)
			}
			if _, err := os.Stat(filepath.Join(dir, "explicit-input")); err == nil {
				os.Exit(6)
			}
			if os.WriteFile(filepath.Join(dir, "explicit-input"), scanner.Bytes(), 0o600) != nil {
				os.Exit(7)
			}
			continue
		}
		if json.Unmarshal(scanner.Bytes(), &frame) != nil || frame.Type != "control_request" || frame.Request.Subtype != "initialize" {
			_ = os.WriteFile(filepath.Join(dir, "unexpected-input"), scanner.Bytes(), 0o600)
			os.Exit(3)
		}
		_, _ = fmt.Fprintf(os.Stdout, `{"type":"control_response","response":{"subtype":"success","request_id":%q,"response":{}}}`+"\n", frame.ID)
		if err := os.WriteFile(filepath.Join(dir, "initialized"), []byte("acknowledged; no native identity"), 0o600); err != nil {
			os.Exit(4)
		}
	}
	_ = os.WriteFile(filepath.Join(dir, "stdin-closed"), []byte("EOF"), 0o600)
	os.Exit(0)
}

func TestPendingNativeCreateOwnerStop(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		for _, shutdown := range []bool{false, true} {
			t.Run(fmt.Sprintf("shutdown=%t/explicit=%t", shutdown, explicit), func(t *testing.T) {
				dir := t.TempDir()
				binary, err := os.Executable()
				require.NoError(t, err)
				peer := filepath.Join(dir, "peer")
				require.NoError(t, os.WriteFile(peer, fmt.Appendf(nil, "#!/bin/sh\nexec %q -test.run=^TestPendingNativeCreatePeer$ -- \"$@\"\n", binary), 0o700))
				st, err := file.Open(filepath.Join(dir, "store"), false)
				require.NoError(t, err)
				reg := runtime.NewRegistry(map[string]runtime.Factory{"claude": claude.Factory{RecoveryDir: filepath.Join(dir, "recovery")}})
				svc := service.New(st, st, reg)
				pid := 0
				t.Cleanup(func() {
					// Emergency cleanup is not evidence that product stop succeeded.
					if pid != 0 && syscall.Kill(pid, 0) == nil {
						t.Logf("emergency cleanup of peer pid=%d", pid)
						assert.NoError(t, syscall.Kill(pid, syscall.SIGKILL))
					}
					if pid != 0 {
						if assert.Eventually(t, func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }, 5*time.Second, 10*time.Millisecond, "peer must be reaped even after test failure") {
							t.Logf("resource ledger: peer pid=%d absent (ESRCH)", pid)
						}
					}
					cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					assert.NoError(t, svc.Shutdown(cleanup))
					assert.NoError(t, st.Close())
				})
				_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "pending"}, Spec: resource.RuntimeSpec{Type: "claude", Config: runtime.Config{"executable": peer, "envPolicy": "replace", "env": map[string]any{"MASS_PENDING_CREATE_PEER": dir, "MASS_PENDING_EXPLICIT": strconv.FormatBool(explicit)}}}})
				require.NoError(t, err)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_, err = svc.StartRuntime(ctx, "pending")
				require.NoError(t, err)
				caller, abandon := context.WithCancel(ctx)
				defer abandon()
				created := make(chan error, 1)
				go func() {
					request := resource.Session{Metadata: resource.Metadata{Namespace: "default", Name: "pending"}, Spec: resource.SessionSpec{RuntimeRef: "pending", SessionSpec: runtime.SessionSpec{Cwd: dir}}}
					var err error
					if explicit {
						_, err = svc.CreateSessionWithInput(caller, resource.SessionCreateInput{Metadata: request.Metadata, Spec: request.Spec, InitialInput: &runtime.Input{Content: []events.ContentBlock{events.Text("caller first")}}})
					} else {
						_, err = svc.CreateSession(caller, request)
					}
					created <- err
				}()
				require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(dir, "pid")); return err == nil }, 5*time.Second, 10*time.Millisecond)
				data, err := os.ReadFile(filepath.Join(dir, "pid"))
				require.NoError(t, err)
				pid, err = strconv.Atoi(string(data))
				require.NoError(t, err)
				t.Logf("resource ledger: native peer pid=%d started", pid)
				require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(dir, "initialized")); return err == nil }, 5*time.Second, 10*time.Millisecond)
				if explicit {
					require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(dir, "explicit-input")); return err == nil }, 5*time.Second, 10*time.Millisecond)
				}
				abandon()
				select {
				case err := <-created:
					require.ErrorContains(t, err, "outcome_unknown")
				case <-ctx.Done():
					t.Fatal("create caller did not return")
				}
				// Caller abandonment must not revoke admitted native work.
				require.Never(t, func() bool { return syscall.Kill(pid, 0) != nil }, 150*time.Millisecond, 10*time.Millisecond)
				_, err = svc.GetSession(resource.SessionRef{Namespace: "default", Name: "pending"})
				require.ErrorIs(t, err, store.ErrNotFound)
				stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer stopCancel()
				if shutdown {
					err = svc.Shutdown(stopCtx)
				} else {
					_, err = svc.StopRuntime(stopCtx, "pending")
				}
				require.NoError(t, err, "original owner must settle a live peer withholding native init")
				require.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH, "product stop must reap the native process")
				require.FileExists(t, filepath.Join(dir, "stdin-closed"))
				require.NoFileExists(t, filepath.Join(dir, "unexpected-input"))
				if explicit {
					require.FileExists(t, filepath.Join(dir, "explicit-input"))
				} else {
					require.NoFileExists(t, filepath.Join(dir, "explicit-input"))
				}
				r, err := svc.GetRuntime("pending")
				require.NoError(t, err)
				require.Equal(t, "stopped", r.Status.State)
				_, err = svc.GetSession(resource.SessionRef{Namespace: "default", Name: "pending"})
				require.ErrorIs(t, err, store.ErrNotFound)
			})
		}
	}
}
