package claude

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
	os.Exit(0)
}

func TestFailedCreateEOFReleasesNativeResources(t *testing.T) {
	dir := t.TempDir()
	binary, err := os.Executable()
	require.NoError(t, err)
	launcher := filepath.Join(dir, "peer")
	require.NoError(t, os.WriteFile(launcher, fmt.Appendf(nil, "#!/bin/sh\nexec %q -test.run=^TestFailedCreateCleanupPeer$ -- \"$@\"\n", binary), 0o700))
	driver, err := (Factory{RecoveryDir: filepath.Join(dir, "recovery")}).New("agent", runtime.Config{
		"executable": launcher,
		"envPolicy":  "replace",
		"env":        map[string]any{"MASS_FAILED_CREATE_CLEANUP_PEER": dir},
	})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, err = driver.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, driver.Stop(context.Background())) })
	opened, err := driver.OpenSession(ctx, runtime.Address{Namespace: "default", SessionName: "failed"}, runtime.SessionSpec{Cwd: dir})
	require.Empty(t, opened.SessionID)
	require.Error(t, err)
	data, readErr := os.ReadFile(filepath.Join(dir, "pid"))
	require.NoError(t, readErr)
	pid, parseErr := strconv.Atoi(string(data))
	require.NoError(t, parseErr)
	require.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH, "SDK failed-open cleanup must reap the original native process")
	require.ErrorIs(t, err, runtime.ErrResourcesReleased, "terminal EOF after completed SDK cleanup must not retain an unknown opening")
}

func TestFailedCreateCleanupRetainsUnconfirmedResources(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
	}{
		{name: "no native completion evidence", cause: errors.New("close failed")},
		{name: "cleanup timeout", cause: &nativeclaude.Error{Op: "session", Err: errors.Join(nativeclaude.ErrUnexpectedEOF, nativeclaude.ErrShutdownTimeout)}},
		{name: "dispatch unknown", cause: &nativeclaude.Error{Op: "session", Err: errors.Join(nativeclaude.ErrUnexpectedEOF, nativeclaude.ErrUnknownOutcome)}},
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
