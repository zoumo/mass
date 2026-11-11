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
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"
	nativeclaude "github.com/zoumo/agent-sdk-go/claude"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestFailedCreateCleanupPeer(t *testing.T) {
	dir := os.Getenv("MASS_FAILED_CREATE_CLEANUP_PEER")
	if dir == "" {
		return
	}
	sessionID := ""
	for _, arg := range os.Args[1:] {
		if value, ok := strings.CutPrefix(arg, "--session-id="); ok {
			sessionID = value
		}
	}
	if _, err := uuid.Parse(sessionID); err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(3)
	}
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		os.Exit(4)
	}
	var request struct {
		ID string `json:"request_id"`
	}
	if json.Unmarshal(scanner.Bytes(), &request) != nil || request.ID == "" {
		os.Exit(5)
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
		"type":     "control_response",
		"response": map[string]any{"subtype": "success", "request_id": request.ID, "response": map[string]any{}},
	}); err != nil {
		os.Exit(6)
	}
	if os.Getenv("MASS_FAILED_CREATE_CLEANUP_WAIT") == "1" {
		for scanner.Scan() {
		}
		os.Exit(0)
	}
	release := os.Getenv("MASS_FAILED_CREATE_CLEANUP_RELEASE")
	for {
		if _, err := os.Stat(release); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	// Initialization succeeds and Open returns the assigned identity before the
	// peer exits without any later init frame or user input.
	if os.Getenv("MASS_FAILED_CREATE_CLEANUP_EXIT") == "4" {
		os.Exit(7)
	}
	os.Exit(0)
}

func TestOpenWithoutInputThenCloseReleasesNativeProcess(t *testing.T) {
	dir := t.TempDir()
	driver, observed := assignedSessionDriver(t, dir, "1", "0")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, err := driver.Start(ctx)
	require.NoError(t, err)
	opened, err := driver.OpenSession(ctx, runtime.Address{Namespace: "default", SessionName: "close"}, runtime.SessionSpec{Cwd: dir})
	require.NoError(t, err)
	_, err = uuid.Parse(opened.SessionID)
	require.NoError(t, err, "Open must return the SDK-assigned native UUID before init")
	requireAssignedForwardingGap(t, ctx, observed)
	require.NoError(t, driver.CloseSession(ctx, opened.SessionID))
	require.NoError(t, driver.Stop(ctx))
	requireProcessReaped(t, dir)
}

func TestOpenWithoutInputObservesNativeExit(t *testing.T) {
	for _, exit := range []string{"0", "4"} {
		t.Run("exit-"+exit, func(t *testing.T) {
			dir := t.TempDir()
			driver, observed := assignedSessionDriver(t, dir, "", exit)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			_, err := driver.Start(ctx)
			require.NoError(t, err)
			opened, err := driver.OpenSession(ctx, runtime.Address{Namespace: "default", SessionName: "failed"}, runtime.SessionSpec{Cwd: dir})
			require.NoError(t, err)
			_, err = uuid.Parse(opened.SessionID)
			require.NoError(t, err, "Open must return the SDK-assigned native UUID before init")
			requireAssignedForwardingGap(t, ctx, observed)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "release"), nil, 0o600))
			select {
			case <-opened.Observation.Done():
			case <-ctx.Done():
				t.Fatal("native exit did not end observation")
			}
			require.Error(t, opened.Observation.Err(), "unexpected native exit must not be reported as normal completion")
			requireProcessReaped(t, dir)
			require.Error(t, driver.CloseSession(ctx, opened.SessionID), "an exited native process has no successful close proof")
		})
	}
}

func assignedSessionDriver(t *testing.T, dir, wait, exit string) (*Driver, <-chan events.SessionEvent) {
	t.Helper()
	binary, err := os.Executable()
	require.NoError(t, err)
	launcher := filepath.Join(dir, "peer")
	require.NoError(t, os.WriteFile(launcher, fmt.Appendf(nil, "#!/bin/sh\nexec %q -test.run=^TestFailedCreateCleanupPeer$ -- \"$@\"\n", binary), 0o700))
	driver, err := (Factory{RecoveryDir: filepath.Join(dir, "recovery")}).New("agent", runtime.Config{
		"executable": launcher,
		"envPolicy":  "replace",
		"env": map[string]any{
			"MASS_FAILED_CREATE_CLEANUP_PEER":    dir,
			"MASS_FAILED_CREATE_CLEANUP_WAIT":    wait,
			"MASS_FAILED_CREATE_CLEANUP_EXIT":    exit,
			"MASS_FAILED_CREATE_CLEANUP_RELEASE": filepath.Join(dir, "release"),
		},
	})
	require.NoError(t, err)
	stream, err := driver.Events()
	require.NoError(t, err)
	readCtx, stopRead := context.WithCancel(context.Background())
	readDone := make(chan struct{})
	observed := make(chan events.SessionEvent, 8)
	go func() {
		defer close(readDone)
		for {
			event, err := stream.Next(readCtx)
			if err != nil {
				return
			}
			select {
			case observed <- event:
			case <-readCtx.Done():
				return
			}
		}
	}()
	t.Cleanup(func() {
		stopRead()
		select {
		case <-readDone:
		case <-time.After(time.Second):
			t.Error("owned native event consumer did not stop")
		}
	})
	t.Cleanup(func() {
		if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
			t.Errorf("release owned native peer: %v", err)
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := driver.Stop(cleanup); err != nil {
			t.Logf("owned driver terminal stop result: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "pid")); err == nil {
			requireProcessReaped(t, dir)
		}
	})
	return driver.(*Driver), observed
}

func requireAssignedForwardingGap(t *testing.T, ctx context.Context, observed <-chan events.SessionEvent) {
	t.Helper()
	select {
	case event := <-observed:
		diagnostic, ok := event.Payload().(events.SessionError)
		require.True(t, ok)
		require.Equal(t, "native_relationship_gap", diagnostic.Reason)
		meta, ok := event.Meta["claude"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "forwarding-not-confirmed", meta["observationReason"])
		require.Equal(t, "initialize-forwardSubagentText-is-a-request", meta["evidence"])
	case <-ctx.Done():
		t.Fatal("assigned Session did not deliver its forwarding diagnostic")
	}
}

func requireProcessReaped(t *testing.T, dir string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "pid"))
	require.NoError(t, err)
	pid, err := strconv.Atoi(string(data))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
	}, 5*time.Second, time.Millisecond, "the native process must be reaped")
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
