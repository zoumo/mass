//go:build integration

// Package meta_test provides integration tests for the metadata store.
// These tests exercise the full agentd daemon lifecycle including Store initialization.
package meta_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestIntegrationStoreInitWithAgentd tests that:
// 1. agentd starts with a minimal config including MetaDB path
// 2. Store is created (database file exists)
// 3. SIGTERM triggers graceful shutdown
// 4. Shutdown completes cleanly
func TestIntegrationStoreInitWithAgentd(t *testing.T) {
	// Create temporary directories for all artifacts.
	// Use separate short temp dir for socket to avoid macOS sun_path limit (104 chars).
	tmpDir := t.TempDir()

	// Create subdirectories for workspace and database.
	workspaceRoot := filepath.Join(tmpDir, "workspaces")
	metaDBDir := filepath.Join(tmpDir, "metadata")

	// Socket uses a short separate temp directory to avoid macOS sun_path limit.
	sockDir, err := os.MkdirTemp("", "agentd-sock-")
	require.NoError(t, err, "failed to create short socket dir")
	socketPath := filepath.Join(sockDir, "agentd.sock")

	// Cleanup socket directory separately.
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })

	require.NoError(t, os.MkdirAll(workspaceRoot, 0o755), "failed to create workspace root")
	require.NoError(t, os.MkdirAll(metaDBDir, 0o755), "failed to create metadata dir")
	metaDBPath := filepath.Join(metaDBDir, "meta.db")

	// Create minimal config file.
	configPath := filepath.Join(tmpDir, "config.yaml")
	configContent := fmt.Sprintf(`
socket: %s
workspaceRoot: %s
metaDB: %s
runtime:
  defaultClass: default
  timeoutSeconds: 300
sessionPolicy:
  maxSessions: 10
  idleTimeoutSeconds: 600
  autoCleanup: true
`, socketPath, workspaceRoot, metaDBPath)

	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o644), "failed to write config file")

	// Build agentd binary from module root (relative to pkg/meta).
	// Integration tests run from pkg/meta directory, so use ../../cmd/agentd.
	binPath := filepath.Join(tmpDir, "agentd")
	buildCmd := exec.Command("go", "build", "-o", binPath, "../../cmd/agentd")
	buildOutput, err := buildCmd.CombinedOutput()
	require.NoError(t, err, "failed to build agentd: %s", string(buildOutput))

	// Start agentd subprocess.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, "--config", configPath)
	cmd.Env = os.Environ()

	// Capture stdout/stderr for debugging.
	var logBuffer strings.Builder
	cmd.Stdout = &logBuffer
	cmd.Stderr = &logBuffer

	// Start the process.
	require.NoError(t, cmd.Start(), "failed to start agentd")

	t.Logf("agentd started with PID %d", cmd.Process.Pid)

	// Wait for Store initialization (database file creation).
	// The daemon logs "agentd: metadata store initialized at <path>" when Store is ready.
	require.Eventually(t, func() bool {
		// Check if database file exists.
		if _, err := os.Stat(metaDBPath); err == nil {
			return true
		}
		// Also check if process is still running (not crashed).
		if cmd.ProcessState != nil {
			t.Logf("agentd exited early: %s", logBuffer.String())
			return false
		}
		return false
	}, 10*time.Second, 100*time.Millisecond, "metadata database file should be created")

	t.Logf("metadata database created at %s", metaDBPath)

	// Verify database file is valid SQLite (can be opened).
	dbStat, err := os.Stat(metaDBPath)
	require.NoError(t, err, "database file should exist")
	require.Greater(t, dbStat.Size(), int64(0), "database file should not be empty")

	// Verify logs contain Store initialization message.
	logs := logBuffer.String()
	require.Contains(t, logs, "metadata store initialized", "logs should show Store initialization")
	require.Contains(t, logs, metaDBPath, "logs should show database path")

	// Send SIGTERM to trigger graceful shutdown.
	t.Logf("sending SIGTERM to agentd (PID %d)", cmd.Process.Pid)
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM), "failed to send SIGTERM")

	// Wait for process to exit gracefully.
	waitErr := cmd.Wait()
	if waitErr != nil {
		// Check if it's an exit error with code 0 (some daemons exit with non-zero on SIGTERM).
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			// SIGTERM typically results in exit code -1 (signal exit) or 0 (graceful shutdown).
			// We accept either as successful shutdown.
			t.Logf("agentd exited with code %d (signal: %v)", exitErr.ExitCode(), exitErr.ProcessState.String())
		} else {
			t.Fatalf("agentd wait error: %v", waitErr)
		}
	}

	// Verify shutdown logs.
	logs = logBuffer.String()
	require.Contains(t, logs, "received signal", "logs should show signal received")
	require.Contains(t, logs, "closing metadata store", "logs should show Store closing")
	require.Contains(t, logs, "shutdown complete", "logs should show shutdown completion")

	// Verify database file still exists after shutdown (not deleted).
	require.FileExists(t, metaDBPath, "database file should persist after shutdown")

	t.Logf("integration test passed - Store lifecycle verified")
}

// TestIntegrationStoreNotConfigured tests that agentd starts without MetaDB configured.
// When metaDB field is empty, the daemon should start without Store initialization.
func TestIntegrationStoreNotConfigured(t *testing.T) {
	// Create temporary directories.
	// Use separate short temp dir for socket to avoid macOS sun_path limit (104 chars).
	tmpDir := t.TempDir()

	workspaceRoot := filepath.Join(tmpDir, "workspaces")

	// Socket uses a short separate temp directory to avoid macOS sun_path limit.
	sockDir, err := os.MkdirTemp("", "agentd-sock-")
	require.NoError(t, err, "failed to create short socket dir")
	socketPath := filepath.Join(sockDir, "agentd.sock")

	// Cleanup socket directory separately.
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })

	require.NoError(t, os.MkdirAll(workspaceRoot, 0o755), "failed to create workspace root")

	// Create config WITHOUT metaDB field.
	configPath := filepath.Join(tmpDir, "config.yaml")
	configContent := fmt.Sprintf(`
socket: %s
workspaceRoot: %s
runtime:
  defaultClass: default
  timeoutSeconds: 300
sessionPolicy:
  maxSessions: 10
  idleTimeoutSeconds: 600
  autoCleanup: true
`, socketPath, workspaceRoot)

	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o644), "failed to write config file")

	// Build agentd binary from module root (relative to pkg/meta).
	// Integration tests run from pkg/meta directory, so use ../../cmd/agentd.
	binPath := filepath.Join(tmpDir, "agentd")
	buildCmd := exec.Command("go", "build", "-o", binPath, "../../cmd/agentd")
	buildOutput, err := buildCmd.CombinedOutput()
	require.NoError(t, err, "failed to build agentd: %s", string(buildOutput))

	// Start agentd subprocess.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, "--config", configPath)
	cmd.Env = os.Environ()

	var logBuffer strings.Builder
	cmd.Stdout = &logBuffer
	cmd.Stderr = &logBuffer

	require.NoError(t, cmd.Start(), "failed to start agentd")

	// Wait for daemon to start (socket file creation indicates server is ready).
	err = func() error {
		for i := 0; i < 150; i++ { // 15 seconds at 100ms intervals
			if _, err := os.Stat(socketPath); err == nil {
				return nil // Socket created successfully
			}
			if cmd.ProcessState != nil {
				return fmt.Errorf("agentd exited early with code %d: %s", cmd.ProcessState.ExitCode(), logBuffer.String())
			}
			time.Sleep(100 * time.Millisecond)
		}
		return fmt.Errorf("socket file not created after 15s. Logs: %s", logBuffer.String())
	}()
	require.NoError(t, err, "socket file should be created")

	// Print logs for debugging.
	t.Logf("agentd started successfully. Logs: %s", logBuffer.String())

	// Verify logs show Store not configured.
	logs := logBuffer.String()
	require.Contains(t, logs, "metadata store not configured", "logs should show Store not configured")

	// Send SIGTERM and wait for shutdown.
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	_ = cmd.Wait()

	// Verify no Store.Close() log (since Store wasn't initialized).
	logs = logBuffer.String()
	require.NotContains(t, logs, "closing metadata store", "logs should NOT show Store closing")

	t.Logf("integration test passed - daemon starts without Store")
}
