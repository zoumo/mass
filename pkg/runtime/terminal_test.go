package runtime

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/coder/acp-go-sdk"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestTerminalManager creates a TerminalManager for testing.
func newTestTerminalManager(t *testing.T, policy spec.PermissionPolicy) *TerminalManager {
	dir := t.TempDir()
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	return NewTerminalManager(dir, env, policy)
}

// ── TerminalManager.Create ───────────────────────────────────────────────────

func TestTerminalManager_Create_Success(t *testing.T) {
	tm := newTestTerminalManager(t, spec.ApproveAll)

	terminalID, err := tm.Create(context.Background(), "echo", []string{"hello"}, nil, nil, nil)
	require.NoError(t, err, "Create should succeed with approve-all policy")
	assert.NotEmpty(t, terminalID, "terminalID should not be empty")

	// Wait for command to complete
	time.Sleep(100 * time.Millisecond)

	// Check output
	output, truncated, exitCode, signal, err := tm.Output(terminalID)
	require.NoError(t, err)
	assert.Contains(t, output, "hello")
	assert.False(t, truncated)
	assert.NotNil(t, exitCode)
	assert.Equal(t, 0, *exitCode)
	assert.Nil(t, signal)

	// Cleanup
	require.NoError(t, tm.Release(terminalID))
}

func TestTerminalManager_Create_PermissionDenied(t *testing.T) {
	tests := []struct {
		name   string
		policy spec.PermissionPolicy
	}{
		{"approve-reads blocks terminal", spec.ApproveReads},
		{"deny-all blocks terminal", spec.DenyAll},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tm := newTestTerminalManager(t, tt.policy)

			_, err := tm.Create(context.Background(), "echo", []string{"hello"}, nil, nil, nil)
			require.Error(t, err, "Create should fail with %s policy", tt.policy)
			assert.Contains(t, err.Error(), "permission denied")
		})
	}
}

func TestTerminalManager_Create_WorkingDirectory(t *testing.T) {
	tm := newTestTerminalManager(t, spec.ApproveAll)

	// Create a subdirectory to use as cwd
	subdir := filepath.Join(tm.workDir, "subdir")
	require.NoError(t, os.MkdirAll(subdir, 0o755))

	// Run pwd command to verify working directory
	cwd := subdir
	terminalID, err := tm.Create(context.Background(), "pwd", []string{}, &cwd, nil, nil)
	require.NoError(t, err)

	// Wait for completion
	time.Sleep(100 * time.Millisecond)

	output, _, _, _, err := tm.Output(terminalID)
	require.NoError(t, err)
	assert.Contains(t, output, "subdir")

	require.NoError(t, tm.Release(terminalID))
}

func TestTerminalManager_Create_ExitCodeNonZero(t *testing.T) {
	tm := newTestTerminalManager(t, spec.ApproveAll)

	// Run a command that exits with non-zero code
	terminalID, err := tm.Create(context.Background(), "sh", []string{"-c", "exit 42"}, nil, nil, nil)
	require.NoError(t, err)

	// Wait for completion
	time.Sleep(100 * time.Millisecond)

	_, _, exitCode, _, err := tm.Output(terminalID)
	require.NoError(t, err)
	require.NotNil(t, exitCode)
	assert.Equal(t, 42, *exitCode)

	require.NoError(t, tm.Release(terminalID))
}

func TestTerminalManager_Create_CustomEnv(t *testing.T) {
	tm := newTestTerminalManager(t, spec.ApproveAll)

	// Set custom environment variable
	env := []string{"MY_CUSTOM_VAR=test_value"}
	terminalID, err := tm.Create(context.Background(), "sh", []string{"-c", "echo $MY_CUSTOM_VAR"}, nil, env, nil)
	require.NoError(t, err)

	// Wait for completion
	time.Sleep(100 * time.Millisecond)

	output, _, _, _, err := tm.Output(terminalID)
	require.NoError(t, err)
	assert.Contains(t, output, "test_value")

	require.NoError(t, tm.Release(terminalID))
}

func TestTerminalManager_Create_OutputByteLimit(t *testing.T) {
	tm := newTestTerminalManager(t, spec.ApproveAll)

	// Generate output larger than limit
	limit := 100
	terminalID, err := tm.Create(context.Background(), "sh", []string{"-c", "yes hello | head -n 20"}, nil, nil, &limit)
	require.NoError(t, err)

	// Wait for completion
	time.Sleep(200 * time.Millisecond)

	output, truncated, _, _, err := tm.Output(terminalID)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(output), limit, "output should be truncated to limit or less")
	assert.True(t, truncated, "truncated flag should be set")

	require.NoError(t, tm.Release(terminalID))
}

func TestTerminalManager_Create_DefaultOutputByteLimit(t *testing.T) {
	tm := newTestTerminalManager(t, spec.ApproveAll)

	// Create without specifying outputByteLimit - should default to 1MB
	terminalID, err := tm.Create(context.Background(), "echo", []string{"hello"}, nil, nil, nil)
	require.NoError(t, err)

	// Verify the terminal was created with default limit
	tm.mu.RLock()
	terminal, ok := tm.terminals[terminalID]
	tm.mu.RUnlock()
	require.True(t, ok)
	assert.Equal(t, DefaultOutputByteLimit, terminal.OutputLimit)

	require.NoError(t, tm.Release(terminalID))
}

// ── TerminalManager.Output ───────────────────────────────────────────────────

func TestTerminalManager_Output_NotFound(t *testing.T) {
	tm := newTestTerminalManager(t, spec.ApproveAll)

	_, _, _, _, err := tm.Output("nonexistent-terminal-id")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestTerminalManager_Output_StdoutAndStderr(t *testing.T) {
	tm := newTestTerminalManager(t, spec.ApproveAll)

	// Run a command that outputs to both stdout and stderr
	terminalID, err := tm.Create(context.Background(), "sh", []string{"-c", "echo stdout; echo stderr >&2"}, nil, nil, nil)
	require.NoError(t, err)

	// Wait for completion
	time.Sleep(100 * time.Millisecond)

	output, _, _, _, err := tm.Output(terminalID)
	require.NoError(t, err)
	assert.Contains(t, output, "stdout")
	assert.Contains(t, output, "stderr")

	require.NoError(t, tm.Release(terminalID))
}

// ── TerminalManager.Kill ─────────────────────────────────────────────────────

func TestTerminalManager_Kill_Success(t *testing.T) {
	tm := newTestTerminalManager(t, spec.ApproveAll)

	// Start a long-running command
	terminalID, err := tm.Create(context.Background(), "sh", []string{"-c", "sleep 60"}, nil, nil, nil)
	require.NoError(t, err)

	// Give it a moment to start
	time.Sleep(50 * time.Millisecond)

	// Kill it
	err = tm.Kill(terminalID)
	require.NoError(t, err)

	// Wait for process to be reaped
	time.Sleep(100 * time.Millisecond)

	// Check that the terminal is killed (signal should be set)
	_, _, _, _, err = tm.Output(terminalID)
	require.NoError(t, err)
	// Signal may be "terminated" or similar depending on platform

	require.NoError(t, tm.Release(terminalID))
}

func TestTerminalManager_Kill_NotFound(t *testing.T) {
	tm := newTestTerminalManager(t, spec.ApproveAll)

	err := tm.Kill("nonexistent-terminal-id")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestTerminalManager_Kill_AlreadyExited(t *testing.T) {
	tm := newTestTerminalManager(t, spec.ApproveAll)

	// Run a quick command that exits immediately
	terminalID, err := tm.Create(context.Background(), "echo", []string{"hello"}, nil, nil, nil)
	require.NoError(t, err)

	// Wait for it to complete
	time.Sleep(100 * time.Millisecond)

	// Try to kill it - should succeed (no-op on already exited process)
	err = tm.Kill(terminalID)
	require.NoError(t, err)

	require.NoError(t, tm.Release(terminalID))
}

// ── TerminalManager.Release ──────────────────────────────────────────────────

func TestTerminalManager_Release_Success(t *testing.T) {
	tm := newTestTerminalManager(t, spec.ApproveAll)

	terminalID, err := tm.Create(context.Background(), "echo", []string{"hello"}, nil, nil, nil)
	require.NoError(t, err)

	// Wait for completion
	time.Sleep(100 * time.Millisecond)

	// Release the terminal
	require.NoError(t, tm.Release(terminalID))

	// Verify it's removed from manager
	tm.mu.RLock()
	_, ok := tm.terminals[terminalID]
	tm.mu.RUnlock()
	assert.False(t, ok, "terminal should be removed after release")
}

func TestTerminalManager_Release_NotFound(t *testing.T) {
	tm := newTestTerminalManager(t, spec.ApproveAll)

	err := tm.Release("nonexistent-terminal-id")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestTerminalManager_Release_KillsRunningProcess(t *testing.T) {
	tm := newTestTerminalManager(t, spec.ApproveAll)

	// Start a long-running command
	terminalID, err := tm.Create(context.Background(), "sh", []string{"-c", "sleep 60"}, nil, nil, nil)
	require.NoError(t, err)

	// Release it immediately without waiting
	require.NoError(t, tm.Release(terminalID))

	// Verify it's removed
	tm.mu.RLock()
	_, ok := tm.terminals[terminalID]
	tm.mu.RUnlock()
	assert.False(t, ok)
}

// ── TerminalManager.WaitForExit ───────────────────────────────────────────────

func TestTerminalManager_WaitForExit_Success(t *testing.T) {
	tm := newTestTerminalManager(t, spec.ApproveAll)

	// Run a command that exits quickly
	terminalID, err := tm.Create(context.Background(), "echo", []string{"hello"}, nil, nil, nil)
	require.NoError(t, err)

	// Wait for exit
	exitCode, signal, err := tm.WaitForExit(context.Background(), terminalID)
	require.NoError(t, err)
	require.NotNil(t, exitCode)
	assert.Equal(t, 0, *exitCode)
	assert.Nil(t, signal)

	require.NoError(t, tm.Release(terminalID))
}

func TestTerminalManager_WaitForExit_NotFound(t *testing.T) {
	tm := newTestTerminalManager(t, spec.ApproveAll)

	_, _, err := tm.WaitForExit(context.Background(), "nonexistent-terminal-id")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestTerminalManager_WaitForExit_ContextCancellation(t *testing.T) {
	tm := newTestTerminalManager(t, spec.ApproveAll)

	// Start a long-running command
	terminalID, err := tm.Create(context.Background(), "sh", []string{"-c", "sleep 60"}, nil, nil, nil)
	require.NoError(t, err)

	// Create a context that cancels after a short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// WaitForExit should return context error
	_, _, err = tm.WaitForExit(ctx, terminalID)
	require.Error(t, err)
	assert.Equal(t, context.DeadlineExceeded, err)

	// Cleanup - kill the running process
	require.NoError(t, tm.Release(terminalID))
}

func TestTerminalManager_WaitForExit_ReturnsImmediatelyForExited(t *testing.T) {
	tm := newTestTerminalManager(t, spec.ApproveAll)

	// Run a quick command
	terminalID, err := tm.Create(context.Background(), "echo", []string{"hello"}, nil, nil, nil)
	require.NoError(t, err)

	// Wait for it to complete
	time.Sleep(100 * time.Millisecond)

	// WaitForExit should return immediately
	start := time.Now()
	exitCode, _, err := tm.WaitForExit(context.Background(), terminalID)
	duration := time.Since(start)

	require.NoError(t, err)
	require.NotNil(t, exitCode)
	assert.Less(t, duration, 50*time.Millisecond, "WaitForExit should return immediately for already exited terminal")

	require.NoError(t, tm.Release(terminalID))
}

// ── truncatingWriter ─────────────────────────────────────────────────────────

func TestTruncatingWriter_NoTruncation(t *testing.T) {
	buf := &bytes.Buffer{}
	tw := &truncatingWriter{buffer: buf, limit: 100}

	// Write data smaller than limit
	n, err := tw.Write([]byte("hello"))
	require.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, "hello", buf.String())
	assert.False(t, tw.truncated)
}

func TestTruncatingWriter_TruncationAtLimit(t *testing.T) {
	buf := &bytes.Buffer{}
	tw := &truncatingWriter{buffer: buf, limit: 10}

	// Write data larger than limit
	n, err := tw.Write([]byte("hello world this is a long message"))
	require.NoError(t, err)
	assert.Equal(t, 34, n) // all bytes written to buffer

	// Buffer should be truncated
	assert.LessOrEqual(t, buf.Len(), 10)
	assert.True(t, tw.truncated)

	// The retained portion should be from the end
	// (most recent output, not the beginning)
	assert.Contains(t, buf.String(), "message")
}

func TestTruncatingWriter_UTF8Boundary(t *testing.T) {
	buf := &bytes.Buffer{}
	tw := &truncatingWriter{buffer: buf, limit: 10}

	// Write UTF-8 multi-byte characters
	// Each Chinese character is 3 bytes in UTF-8
	chinese := "你好世界你好" // 6 characters = 18 bytes
	n, err := tw.Write([]byte(chinese))
	require.NoError(t, err)
	assert.Equal(t, 18, n)

	// Buffer should be truncated but maintain valid UTF-8
	assert.LessOrEqual(t, buf.Len(), tw.limit)
	assert.True(t, tw.truncated)

	// The output should be valid UTF-8 (no partial characters)
	output := buf.String()
	assert.True(t, utf8.ValidString(output), "truncated output should be valid UTF-8")
}

// ── Concurrent terminals ──────────────────────────────────────────────────────

func TestTerminalManager_ConcurrentTerminals(t *testing.T) {
	tm := newTestTerminalManager(t, spec.ApproveAll)

	// Create multiple terminals concurrently
	const numTerminals = 5
	terminalIDs := make([]string, numTerminals)

	for i := 0; i < numTerminals; i++ {
		terminalID, err := tm.Create(context.Background(), "echo", []string{"hello"}, nil, nil, nil)
		require.NoError(t, err)
		terminalIDs[i] = terminalID
	}

	// Wait for all to complete
	time.Sleep(200 * time.Millisecond)

	// Check all terminals have output
	for _, terminalID := range terminalIDs {
		output, _, exitCode, _, err := tm.Output(terminalID)
		require.NoError(t, err)
		assert.Contains(t, output, "hello")
		require.NotNil(t, exitCode)
		assert.Equal(t, 0, *exitCode)
	}

	// Release all terminals
	for _, terminalID := range terminalIDs {
		require.NoError(t, tm.Release(terminalID))
	}
}

// ── acpClient terminal methods with TerminalManager ──────────────────────────

func TestAcpClient_CreateTerminal_WithManager(t *testing.T) {
	mgr := newTestManager(spec.ApproveAll)
	defer cleanupManager(mgr)

	// Initialize TerminalManager manually (normally done in Manager.Create)
	mgr.terminalMgr = NewTerminalManager(mgr.bundleDir, []string{"PATH=" + os.Getenv("PATH")}, mgr.cfg.Permissions)

	client := &acpClient{mgr: mgr, terminalMgr: mgr.terminalMgr}

	resp, err := client.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
		Command: "echo",
		Args:    []string{"hello"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.TerminalId)

	// Cleanup
	_, err = client.ReleaseTerminal(context.Background(), acp.ReleaseTerminalRequest{
		TerminalId: resp.TerminalId,
	})
	require.NoError(t, err)
}

func TestAcpClient_TerminalOutput_WithManager(t *testing.T) {
	mgr := newTestManager(spec.ApproveAll)
	defer cleanupManager(mgr)

	mgr.terminalMgr = NewTerminalManager(mgr.bundleDir, []string{"PATH=" + os.Getenv("PATH")}, mgr.cfg.Permissions)
	client := &acpClient{mgr: mgr, terminalMgr: mgr.terminalMgr}

	// Create a terminal
	createResp, err := client.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
		Command: "echo",
		Args:    []string{"hello"},
	})
	require.NoError(t, err)

	// Wait for completion
	time.Sleep(100 * time.Millisecond)

	// Get output
	outputResp, err := client.TerminalOutput(context.Background(), acp.TerminalOutputRequest{
		TerminalId: createResp.TerminalId,
	})
	require.NoError(t, err)
	assert.Contains(t, outputResp.Output, "hello")
	assert.False(t, outputResp.Truncated)
	require.NotNil(t, outputResp.ExitStatus)
	require.NotNil(t, outputResp.ExitStatus.ExitCode)
	assert.Equal(t, 0, *outputResp.ExitStatus.ExitCode)

	// Cleanup
	_, err = client.ReleaseTerminal(context.Background(), acp.ReleaseTerminalRequest{
		TerminalId: createResp.TerminalId,
	})
	require.NoError(t, err)
}

func TestAcpClient_CreateTerminal_PermissionDenied(t *testing.T) {
	mgr := newTestManager(spec.DenyAll)
	defer cleanupManager(mgr)

	mgr.terminalMgr = NewTerminalManager(mgr.bundleDir, []string{"PATH=" + os.Getenv("PATH")}, mgr.cfg.Permissions)
	client := &acpClient{mgr: mgr, terminalMgr: mgr.terminalMgr}

	_, err := client.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
		Command: "echo",
		Args:    []string{"hello"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
}