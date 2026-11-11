package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"
	nativeclaude "github.com/zoumo/agent-sdk-go/claude"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestFailedCreateCleanupPeer(t *testing.T) {
	dir := os.Getenv("MASS_FAILED_CREATE_CLEANUP_PEER")
	if dir == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(2)
	}
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		os.Exit(3)
	}
	var request struct {
		ID string `json:"request_id"`
	}
	if json.Unmarshal(scanner.Bytes(), &request) != nil || request.ID == "" {
		os.Exit(4)
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
		"type":     "control_response",
		"response": map[string]any{"subtype": "success", "request_id": request.ID, "response": map[string]any{}},
	}); err != nil {
		os.Exit(5)
	}
	// Initialization succeeds, but the peer exits without establishing identity.
	if os.Getenv("MASS_FAILED_CREATE_CLEANUP_EXIT") == "4" {
		os.Exit(4)
	}
	os.Exit(0)
}

func TestFailedCreateEOFReleasesNativeResources(t *testing.T) {
	for _, exit := range []string{"0", "4"} {
		t.Run("exit-"+exit, func(t *testing.T) {
			dir := t.TempDir()
			binary, err := os.Executable()
			require.NoError(t, err)
			launcher := filepath.Join(dir, "peer")
			require.NoError(t, os.WriteFile(launcher, fmt.Appendf(nil, "#!/bin/sh\nexec %q -test.run=^TestFailedCreateCleanupPeer$ -- \"$@\"\n", binary), 0o700))
			driver, err := (Factory{RecoveryDir: filepath.Join(dir, "recovery")}).New("agent", runtime.Config{
				"executable": launcher,
				"envPolicy":  "replace",
				"env":        map[string]any{"MASS_FAILED_CREATE_CLEANUP_PEER": dir, "MASS_FAILED_CREATE_CLEANUP_EXIT": exit},
			})
			require.NoError(t, err)
			stream, err := driver.Events()
			require.NoError(t, err)
			readCtx, stopRead := context.WithCancel(context.Background())
			readDone := make(chan struct{})
			var openedID string
			t.Cleanup(func() {
				if openedID != "" {
					closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
					if closeErr := driver.CloseSession(closeCtx, openedID); closeErr != nil {
						t.Errorf("close unexpectedly opened session: %v", closeErr)
					}
					closeCancel()
				}
				stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer stopCancel()
				if stopErr := driver.Stop(stopCtx); stopErr != nil {
					t.Errorf("stop failed-create runtime: %v", stopErr)
				}
				stopRead()
				select {
				case <-readDone:
				case <-time.After(time.Second):
					t.Error("failed-create event reader cleanup did not finish")
				}
			})
			go func() {
				defer close(readDone)
				for {
					if _, err := stream.Next(readCtx); err != nil {
						return
					}
				}
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			_, err = driver.Start(ctx)
			require.NoError(t, err)
			opened, err := driver.OpenSession(ctx, runtime.Address{Namespace: "default", SessionName: "failed"}, runtime.SessionSpec{Cwd: dir})
			openedID = opened.SessionID
			require.Empty(t, opened.SessionID)
			require.Error(t, err)
			data, readErr := os.ReadFile(filepath.Join(dir, "pid"))
			require.NoError(t, readErr)
			pid, parseErr := strconv.Atoi(string(data))
			require.NoError(t, parseErr)
			require.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH, "SDK failed-open cleanup must reap the original native process")
			require.ErrorIs(t, err, runtime.ErrResourcesReleased, "terminal EOF/exit after completed SDK cleanup must not retain an unknown opening")
		})
	}
}

func TestFailedCreateCleanupTerminalExit(t *testing.T) {
	for _, command := range []string{"exit 4", "kill -TERM $$"} {
		t.Run(command, func(t *testing.T) {
			err := exec.CommandContext(t.Context(), "/bin/sh", "-c", command).Run()
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit)
			require.NotNil(t, exit.ProcessState)
			terminal := &nativeclaude.Error{Op: "session", Err: &nativeclaude.Error{Op: "process", Err: exit}}
			cleanup := &agent.Error{Code: agent.ErrorUnavailable, Reason: "close_failed", Cause: terminal}
			openErr := &agent.Error{Reason: "initialize_wait_failed", Cause: terminal}
			require.False(t, failedNativeCleanup(errors.Join(openErr, cleanup)))
			require.ErrorIs(t, cleanup, exit, "release classification must preserve the original native failure")
			incomplete := &agent.Error{Reason: "close_failed", Cause: &nativeclaude.Error{Op: "session", Err: errors.Join(terminal.Err, nativeclaude.ErrShutdownTimeout)}}
			require.True(t, failedNativeCleanup(errors.Join(openErr, incomplete)))
			require.True(t, failedNativeCleanup(errors.Join(cleanup, incomplete)), "a completed close must not hide another failed close")
			unconfirmed := &agent.Error{Reason: "close_outcome_unknown", Cause: &nativeclaude.Error{Op: "close", Err: nativeclaude.ErrUnknownOutcome}}
			require.True(t, failedNativeCleanup(errors.Join(cleanup, unconfirmed)), "a completed close must not hide another unconfirmed close")
		})
	}
}

func TestFailedCreateCleanupRetainsBusinessUncertaintyAfterRelease(t *testing.T) {
	uncertain := &nativeclaude.DispatchError{Op: "initialize", State: nativeclaude.OutcomeUnknown, Err: context.DeadlineExceeded}
	startup := &nativeclaude.Error{Op: "initialize", Err: uncertain, OutcomeUnknown: true}
	openErr := &agent.Error{Code: agent.ErrorOutcomeUnknown, Reason: "open_outcome_unknown", Cause: startup}
	// native initialize always completes failed-startup Close before returning.
	require.False(t, failedNativeCleanup(openErr))
	require.ErrorIs(t, openErr, nativeclaude.ErrUnknownOutcome)
	require.ErrorIs(t, openErr, context.DeadlineExceeded)

	for _, businessFailure := range []error{uncertain, errors.New("native terminal failure"), nativeclaude.ErrUnexpectedEOF} {
		closed := &agent.Error{Reason: "close_outcome_unknown", Cause: &nativeclaude.Error{Op: "session", Err: businessFailure}}
		require.False(t, failedNativeCleanup(errors.Join(openErr, closed)), "session terminal is returned only after cleaned")
		require.ErrorIs(t, closed, businessFailure, "release must not erase the original business failure")
		unconfirmed := &agent.Error{Reason: "close_failed", Cause: errors.New("another close failed")}
		require.True(t, failedNativeCleanup(errors.Join(closed, unconfirmed)))
		require.True(t, failedNativeCleanup(errors.Join(unconfirmed, closed)))
		incomplete := &agent.Error{Reason: "close_failed", Cause: &nativeclaude.Error{Op: "session", Err: errors.Join(businessFailure, nativeclaude.ErrShutdownTimeout)}}
		require.True(t, failedNativeCleanup(errors.Join(openErr, incomplete)))
	}
}

func TestFailedCreateCleanupRetainsUnconfirmedResources(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
	}{
		{name: "no native completion evidence", cause: errors.New("close failed")},
		{name: "cleanup timeout", cause: &nativeclaude.Error{Op: "session", Err: errors.Join(nativeclaude.ErrUnexpectedEOF, nativeclaude.ErrShutdownTimeout)}},
		{name: "close outcome unknown", cause: &nativeclaude.Error{Op: "close", Err: nativeclaude.ErrUnknownOutcome}},
		{name: "EOF beside unconfirmed close", cause: errors.Join(&nativeclaude.Error{Op: "session", Err: nativeclaude.ErrUnexpectedEOF}, errors.New("another cleanup failure"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cleanup := &agent.Error{Code: agent.ErrorUnavailable, Reason: "close_failed", Cause: tc.cause}
			require.True(t, failedNativeCleanup(errors.Join(&agent.Error{Reason: "initialize_wait_failed"}, cleanup)))
		})
	}
	// Startup can fail before the unified provider obtains a native session.
	// Its cleanup timeout must also prevent an erroneous release proof.
	require.True(t, failedNativeCleanup(&agent.Error{Reason: "open_failed", Cause: nativeclaude.ErrShutdownTimeout}))
}
