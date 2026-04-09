// Package workspace implements workspace preparation handlers.
// This file tests the HookExecutor implementation.
package workspace

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// Unit Tests: HookError Structure and Methods
// =============================================================================

// TestHookErrorStructure verifies all HookError fields are accessible and correctly typed.
func TestHookErrorStructure(t *testing.T) {
	underlyingErr := errors.New("underlying error")
	output := []byte("stdout and stderr output")

	hookErr := &HookError{
		Phase:       "setup",
		HookIndex:   2,
		Command:     "/usr/bin/make",
		Args:        []string{"install", "PREFIX=/opt"},
		Description: "Install dependencies",
		ExitCode:    1,
		Output:      output,
		Message:     "make install failed",
		Err:         underlyingErr,
	}

	// Verify each field is accessible and correctly typed.
	if hookErr.Phase != "setup" {
		t.Errorf("Phase field: expected 'setup', got '%s'", hookErr.Phase)
	}
	if hookErr.HookIndex != 2 {
		t.Errorf("HookIndex field: expected 2, got %d", hookErr.HookIndex)
	}
	if hookErr.Command != "/usr/bin/make" {
		t.Errorf("Command field: expected '/usr/bin/make', got '%s'", hookErr.Command)
	}
	if len(hookErr.Args) != 2 || hookErr.Args[0] != "install" || hookErr.Args[1] != "PREFIX=/opt" {
		t.Errorf("Args field: expected ['install', 'PREFIX=/opt'], got %v", hookErr.Args)
	}
	if hookErr.Description != "Install dependencies" {
		t.Errorf("Description field: expected 'Install dependencies', got '%s'", hookErr.Description)
	}
	if hookErr.ExitCode != 1 {
		t.Errorf("ExitCode field: expected 1, got %d", hookErr.ExitCode)
	}
	if !bytes.Equal(hookErr.Output, output) {
		t.Errorf("Output field: expected '%s', got '%s'", string(output), string(hookErr.Output))
	}
	if hookErr.Message != "make install failed" {
		t.Errorf("Message field: expected 'make install failed', got '%s'", hookErr.Message)
	}
	if !errors.Is(hookErr.Err, underlyingErr) {
		t.Errorf("Err field: expected underlyingErr, got %v", hookErr.Err)
	}
}

// TestHookErrorErrorMethod verifies Error() output format matches expected pattern.
func TestHookErrorErrorMethod(t *testing.T) {
	tests := []struct {
		name     string
		hookErr  *HookError
		contains []string // substrings that must appear in Error() output
	}{
		{
			name: "full error with all fields",
			hookErr: &HookError{
				Phase:     "setup",
				HookIndex: 0,
				Command:   "npm",
				ExitCode:  1,
				Output:    []byte("npm ERR! install failed"),
				Message:   "npm install failed",
				Err:       errors.New("exit status 1"),
			},
			contains: []string{
				"workspace: hook setup failed",
				"hookIndex=0",
				"command=npm",
				"exit=1",
				"npm install failed",
				"output:",
				"npm ERR! install failed",
				"error:",
			},
		},
		{
			name: "error without command",
			hookErr: &HookError{
				Phase:     "teardown",
				HookIndex: 3,
				ExitCode:  0,
				Message:   "hook execution error",
			},
			contains: []string{
				"workspace: hook teardown failed",
				"hookIndex=3",
				"hook execution error",
			},
		},
		{
			name: "error without output",
			hookErr: &HookError{
				Phase:     "setup",
				HookIndex: 1,
				Command:   "echo",
				ExitCode:  127,
				Message:   "command not found",
				Err:       exec.ErrNotFound,
			},
			contains: []string{
				"workspace: hook setup failed",
				"hookIndex=1",
				"command=echo",
				"exit=127",
				"command not found",
				"error:",
			},
		},
		{
			name: "error with long output truncates",
			hookErr: &HookError{
				Phase:     "setup",
				HookIndex: 0,
				Command:   "test",
				ExitCode:  1,
				Output:    []byte(strings.Repeat("x", 600)), // >500 chars
			},
			contains: []string{
				"workspace: hook setup failed",
				"hookIndex=0",
				"...(truncated)",
			},
		},
		{
			name: "error with zero exit code",
			hookErr: &HookError{
				Phase:     "setup",
				HookIndex: 0,
				Command:   "true",
				ExitCode:  0,
				Message:   "unexpected state",
			},
			contains: []string{
				"workspace: hook setup failed",
				"hookIndex=0",
				"command=true",
				"unexpected state",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errMsg := tt.hookErr.Error()

			// Verify error message contains all expected parts joined by ': '
			for _, substr := range tt.contains {
				if !strings.Contains(errMsg, substr) {
					t.Errorf("Error() output missing expected substring '%s'\nGot: %s", substr, errMsg)
				}
			}

			// Verify parts are joined by ': ' separator (GitError pattern)
			if !strings.Contains(errMsg, ": ") {
				t.Errorf("Error() output should use ': ' separator, got: %s", errMsg)
			}
		})
	}
}

// TestHookErrorUnwrap verifies Unwrap() returns underlying error for error chaining.
func TestHookErrorUnwrap(t *testing.T) {
	// Test with os.ErrClosed as underlying error.
	hookErr := &HookError{
		Phase:     "setup",
		HookIndex: 0,
		Command:   "test",
		Err:       os.ErrClosed,
	}

	// Verify errors.Is works through Unwrap.
	if !errors.Is(hookErr, os.ErrClosed) {
		t.Error("errors.Is(hookErr, os.ErrClosed) should return true")
	}

	// Verify errors.As can extract underlying error.
	var target *os.PathError
	if errors.As(hookErr, &target) {
		t.Error("errors.As should not extract os.PathError from os.ErrClosed")
	}

	// Test with a simple error as underlying error.
	simpleErr := errors.New("simple underlying error")
	hookErr2 := &HookError{
		Phase:     "teardown",
		HookIndex: 1,
		Command:   "cleanup",
		Err:       simpleErr,
	}

	// Verify errors.Is works.
	if !errors.Is(hookErr2, simpleErr) {
		t.Error("errors.Is(hookErr2, simpleErr) should return true")
	}

	// Test with exec.ErrNotFound as underlying error (real exec error).
	hookErr3 := &HookError{
		Phase:     "setup",
		HookIndex: 0,
		Command:   "missing",
		Err:       exec.ErrNotFound,
	}

	// Verify errors.Is works with exec.ErrNotFound.
	if !errors.Is(hookErr3, exec.ErrNotFound) {
		t.Error("errors.Is(hookErr3, exec.ErrNotFound) should return true")
	}

	// Test with nil underlying error.
	hookErr4 := &HookError{
		Phase:     "setup",
		HookIndex: 0,
		Message:   "no underlying error",
	}
	if hookErr4.Unwrap() != nil {
		t.Error("Unwrap() should return nil when Err field is nil")
	}
}

// =============================================================================
// Unit Tests: ExecuteHooks Edge Cases
// =============================================================================

// TestExecuteHooksEmptyHooks verifies empty hooks array returns nil immediately.
func TestExecuteHooksEmptyHooks(t *testing.T) {
	executor := NewHookExecutor()
	ctx := context.Background()
	tmpDir := t.TempDir()

	tests := []struct {
		name  string
		hooks []Hook
	}{
		{
			name:  "nil hooks",
			hooks: nil,
		},
		{
			name:  "empty hooks array",
			hooks: []Hook{},
		},
		{
			name:  "empty hooks with capacity",
			hooks: make([]Hook, 0, 5),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := executor.ExecuteHooks(ctx, tt.hooks, tmpDir, "setup")
			if err != nil {
				t.Errorf("ExecuteHooks with empty hooks should return nil, got: %v", err)
			}
		})
	}
}

// TestExecuteHooksNonexistentWorkspace verifies error when workspaceDir doesn't exist.
func TestExecuteHooksNonexistentWorkspace(t *testing.T) {
	executor := NewHookExecutor()
	ctx := context.Background()
	hooks := []Hook{{Command: "echo", Args: []string{"test"}}}

	// Use a path that definitely doesn't exist.
	nonexistentDir := filepath.Join(t.TempDir(), "nonexistent", "path")

	err := executor.ExecuteHooks(ctx, hooks, nonexistentDir, "setup")
	if err == nil {
		t.Fatal("ExecuteHooks with nonexistent workspaceDir should return error")
	}

	// Verify error message mentions workspace directory.
	if !strings.Contains(err.Error(), "workspaceDir") {
		t.Errorf("error should mention workspaceDir, got: %v", err)
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error should mention 'does not exist', got: %v", err)
	}
}

// TestExecuteHooksEmptyCommand verifies defensive error for empty Command field.
func TestExecuteHooksEmptyCommand(t *testing.T) {
	executor := NewHookExecutor()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Hook with empty Command field (should fail even though spec validation prevents this).
	hooks := []Hook{{Command: "", Args: []string{"test"}}}

	err := executor.ExecuteHooks(ctx, hooks, tmpDir, "setup")
	if err == nil {
		t.Fatal("ExecuteHooks with empty Command should return error")
	}

	// Verify error is HookError with context.
	var hookErr *HookError
	if !errors.As(err, &hookErr) {
		t.Errorf("error should be HookError, got: %T", err)
	} else {
		if hookErr.HookIndex != 0 {
			t.Errorf("HookIndex should be 0, got %d", hookErr.HookIndex)
		}
		if hookErr.Phase != "setup" {
			t.Errorf("Phase should be 'setup', got '%s'", hookErr.Phase)
		}
	}
}

// =============================================================================
// Integration Tests: Real Command Execution
// =============================================================================

// TestExecuteHooksSuccess verifies successful hook execution returns nil.
func TestExecuteHooksSuccess(t *testing.T) {
	executor := NewHookExecutor()
	ctx := context.Background()
	tmpDir := t.TempDir()

	hooks := []Hook{
		{Command: "echo", Args: []string{"hello world"}},
	}

	err := executor.ExecuteHooks(ctx, hooks, tmpDir, "setup")
	if err != nil {
		t.Errorf("ExecuteHooks with successful command should return nil, got: %v", err)
	}
}

// TestExecuteHooksFailureWithOutput verifies HookError captures stderr on failure.
func TestExecuteHooksFailureWithOutput(t *testing.T) {
	executor := NewHookExecutor()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// cat nonexistent file will fail with stderr message.
	hooks := []Hook{
		{Command: "cat", Args: []string{"nonexistent-file.txt"}},
	}

	err := executor.ExecuteHooks(ctx, hooks, tmpDir, "setup")
	if err == nil {
		t.Fatal("ExecuteHooks with failing command should return error")
	}

	// Verify error is HookError with all expected fields.
	var hookErr *HookError
	if !errors.As(err, &hookErr) {
		t.Fatalf("error should be HookError, got: %T", err)
	}

	// Verify HookError fields.
	if hookErr.Phase != "setup" {
		t.Errorf("Phase: expected 'setup', got '%s'", hookErr.Phase)
	}
	if hookErr.HookIndex != 0 {
		t.Errorf("HookIndex: expected 0, got %d", hookErr.HookIndex)
	}
	if hookErr.Command != "cat" {
		t.Errorf("Command: expected 'cat', got '%s'", hookErr.Command)
	}
	if hookErr.ExitCode == 0 {
		t.Errorf("ExitCode: expected non-zero, got %d", hookErr.ExitCode)
	}

	// Verify Output contains stderr message like "No such file or directory".
	outputStr := string(hookErr.Output)
	if !strings.Contains(outputStr, "No such file or directory") {
		t.Errorf("Output should contain 'No such file or directory', got: %s", outputStr)
	}
}

// TestExecuteHooksSequentialAbort verifies first failure aborts execution.
func TestExecuteHooksSequentialAbort(t *testing.T) {
	executor := NewHookExecutor()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Marker file that second hook would create if it ran.
	markerFile := filepath.Join(tmpDir, "marker-file.txt")

	hooks := []Hook{
		{Command: "false"}, // Exits with code 1, always fails.
		{Command: "touch", Args: []string{markerFile}},
	}

	err := executor.ExecuteHooks(ctx, hooks, tmpDir, "setup")
	if err == nil {
		t.Fatal("ExecuteHooks should return error when first hook fails")
	}

	// Verify error is HookError from first hook.
	var hookErr *HookError
	if !errors.As(err, &hookErr) {
		t.Fatalf("error should be HookError, got: %T", err)
	}

	if hookErr.HookIndex != 0 {
		t.Errorf("HookIndex should be 0 (first hook), got %d", hookErr.HookIndex)
	}

	// Verify marker file was NOT created (second hook not executed).
	if _, err := os.Stat(markerFile); !os.IsNotExist(err) {
		t.Errorf("marker file should not exist (second hook should not run), found: %s", markerFile)
	}
}

// TestExecuteHooksContextCancel verifies context cancellation returns ctx.Err() immediately.
func TestExecuteHooksContextCancel(t *testing.T) {
	executor := NewHookExecutor()
	tmpDir := t.TempDir()

	// Create canceled context before ExecuteHooks call.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately before execution.

	hooks := []Hook{
		{Command: "sleep", Args: []string{"5"}},
	}

	start := time.Now()
	err := executor.ExecuteHooks(ctx, hooks, tmpDir, "setup")
	duration := time.Since(start)

	// Verify returns context.Canceled without 5s delay.
	if !errors.Is(err, context.Canceled) {
		t.Errorf("ExecuteHooks with canceled context should return context.Canceled, got: %v", err)
	}

	// Verify no significant delay (should be immediate, not 5s).
	if duration > 100*time.Millisecond {
		t.Errorf("ExecuteHooks should return immediately on canceled context, took %v", duration)
	}
}

// TestExecuteHooksContextCancelDuringExecution verifies cancellation during long-running hook.
func TestExecuteHooksContextCancelDuringExecution(t *testing.T) {
	executor := NewHookExecutor()
	tmpDir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	hooks := []Hook{
		{Command: "sleep", Args: []string{"5"}},
	}

	start := time.Now()
	err := executor.ExecuteHooks(ctx, hooks, tmpDir, "setup")
	duration := time.Since(start)

	// Verify returns context error (Canceled or DeadlineExceeded).
	if err == nil {
		t.Fatal("ExecuteHooks should return error when context times out")
	}

	// Should be context.DeadlineExceeded or context.Canceled.
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Errorf("Expected context error, got: %v", err)
	}

	// Verify it returned quickly (within timeout + margin), not after 5s.
	if duration > 500*time.Millisecond {
		t.Errorf("ExecuteHooks should abort on context timeout, took %v", duration)
	}
}

// TestExecuteHooksCommandNotFound verifies error when command not in PATH.
func TestExecuteHooksCommandNotFound(t *testing.T) {
	executor := NewHookExecutor()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Use a command name that definitely doesn't exist.
	hooks := []Hook{
		{Command: "nonexistent-command-xyz123"},
	}

	err := executor.ExecuteHooks(ctx, hooks, tmpDir, "setup")
	if err == nil {
		t.Fatal("ExecuteHooks with nonexistent command should return error")
	}

	// Verify error is HookError.
	var hookErr *HookError
	if !errors.As(err, &hookErr) {
		t.Fatalf("error should be HookError, got: %T", err)
	}

	// Verify ExitCode (typically 127 for command not found, or 1 as fallback).
	if hookErr.ExitCode == 0 {
		t.Errorf("ExitCode should be non-zero for command not found, got %d", hookErr.ExitCode)
	}

	// Verify Message or Error output mentions the command.
	errMsg := hookErr.Error()
	if !strings.Contains(errMsg, "nonexistent-command-xyz123") {
		t.Errorf("Error message should mention command name, got: %s", errMsg)
	}
}

// TestExecuteHooksLastHookFails verifies earlier hooks completed when last fails.
func TestExecuteHooksLastHookFails(t *testing.T) {
	executor := NewHookExecutor()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Marker file that first hook creates.
	markerFile := filepath.Join(tmpDir, "first-hook-marker.txt")

	hooks := []Hook{
		{Command: "touch", Args: []string{markerFile}}, // Succeeds.
		{Command: "false"}, // Fails.
	}

	err := executor.ExecuteHooks(ctx, hooks, tmpDir, "setup")
	if err == nil {
		t.Fatal("ExecuteHooks should return error when last hook fails")
	}

	// Verify error is HookError from second hook.
	var hookErr *HookError
	if !errors.As(err, &hookErr) {
		t.Fatalf("error should be HookError, got: %T", err)
	}

	if hookErr.HookIndex != 1 {
		t.Errorf("HookIndex should be 1 (second hook), got %d", hookErr.HookIndex)
	}

	// Verify first hook's marker file WAS created (first hook succeeded).
	if _, err := os.Stat(markerFile); os.IsNotExist(err) {
		t.Errorf("marker file should exist (first hook should succeed), missing: %s", markerFile)
	}
}

// TestExecuteHooksSingleHook verifies single hook execution without sequential interaction.
func TestExecuteHooksSingleHook(t *testing.T) {
	executor := NewHookExecutor()
	ctx := context.Background()
	tmpDir := t.TempDir()

	tests := []struct {
		name    string
		hook    Hook
		wantErr bool
	}{
		{
			name:    "single successful hook",
			hook:    Hook{Command: "echo", Args: []string{"test"}},
			wantErr: false,
		},
		{
			name:    "single failing hook",
			hook:    Hook{Command: "false"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := executor.ExecuteHooks(ctx, []Hook{tt.hook}, tmpDir, "setup")

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				var hookErr *HookError
				if !errors.As(err, &hookErr) {
					t.Fatalf("error should be HookError, got: %T", err)
				}
				if hookErr.HookIndex != 0 {
					t.Errorf("HookIndex should be 0 for single hook, got %d", hookErr.HookIndex)
				}
			} else {
				if err != nil {
					t.Errorf("expected nil, got: %v", err)
				}
			}
		})
	}
}

// TestExecuteHooksMultipleHooksAllSuccess verifies all hooks complete in sequence.
func TestExecuteHooksMultipleHooksAllSuccess(t *testing.T) {
	executor := NewHookExecutor()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create marker files to verify each hook ran.
	marker1 := filepath.Join(tmpDir, "hook1-marker.txt")
	marker2 := filepath.Join(tmpDir, "hook2-marker.txt")
	marker3 := filepath.Join(tmpDir, "hook3-marker.txt")

	hooks := []Hook{
		{Command: "touch", Args: []string{marker1}},
		{Command: "touch", Args: []string{marker2}},
		{Command: "touch", Args: []string{marker3}},
	}

	err := executor.ExecuteHooks(ctx, hooks, tmpDir, "setup")
	if err != nil {
		t.Fatalf("ExecuteHooks with all successful hooks should return nil, got: %v", err)
	}

	// Verify all marker files exist (all hooks executed).
	for i, marker := range []string{marker1, marker2, marker3} {
		if _, err := os.Stat(marker); os.IsNotExist(err) {
			t.Errorf("hook %d marker file should exist: %s", i+1, marker)
		}
	}
}

// TestExecuteHooksOutputCapture verifies stdout and stderr are captured.
func TestExecuteHooksOutputCapture(t *testing.T) {
	executor := NewHookExecutor()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Command that writes to both stdout and stderr.
	// Use printf for stdout and redirect to stderr for stderr output.
	hooks := []Hook{
		{
			Command: "sh",
			Args:    []string{"-c", "printf 'stdout-output' && printf 'stderr-output' >&2"},
		},
	}

	err := executor.ExecuteHooks(ctx, hooks, tmpDir, "setup")
	if err != nil {
		t.Fatalf("expected nil, got: %v", err)
	}

	// Success case doesn't return HookError, so we test failure case for output capture.
	hooksFailing := []Hook{
		{
			Command: "sh",
			Args:    []string{"-c", "printf 'stdout-before-fail' && printf 'stderr-before-fail' >&2 && exit 1"},
		},
	}

	err = executor.ExecuteHooks(ctx, hooksFailing, tmpDir, "setup")
	if err == nil {
		t.Fatal("expected error from failing command")
	}

	var hookErr *HookError
	if !errors.As(err, &hookErr) {
		t.Fatalf("error should be HookError, got: %T", err)
	}

	outputStr := string(hookErr.Output)
	if !strings.Contains(outputStr, "stdout-before-fail") {
		t.Errorf("Output should contain stdout, got: %s", outputStr)
	}
	if !strings.Contains(outputStr, "stderr-before-fail") {
		t.Errorf("Output should contain stderr, got: %s", outputStr)
	}
}

// TestExecuteHooksPhaseContext verifies phase is included in HookError.
func TestExecuteHooksPhaseContext(t *testing.T) {
	executor := NewHookExecutor()
	ctx := context.Background()
	tmpDir := t.TempDir()

	tests := []struct {
		name  string
		phase string
	}{
		{name: "setup phase", phase: "setup"},
		{name: "teardown phase", phase: "teardown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hooks := []Hook{{Command: "false"}}
			err := executor.ExecuteHooks(ctx, hooks, tmpDir, tt.phase)

			var hookErr *HookError
			if !errors.As(err, &hookErr) {
				t.Fatalf("error should be HookError, got: %T", err)
			}

			if hookErr.Phase != tt.phase {
				t.Errorf("Phase: expected '%s', got '%s'", tt.phase, hookErr.Phase)
			}

			// Verify Error() message includes phase.
			if !strings.Contains(hookErr.Error(), tt.phase) {
				t.Errorf("Error() should include phase '%s', got: %s", tt.phase, hookErr.Error())
			}
		})
	}
}

// TestExecuteHooksArgsPreserved verifies Args are correctly passed to command.
func TestExecuteHooksArgsPreserved(t *testing.T) {
	executor := NewHookExecutor()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// For sh -c script arg0 arg1, shell receives arg0 as $0 and arg1 as $1.
	// We make the command fail to capture output in HookError.
	hooks := []Hook{
		{
			Command: "sh",
			Args:    []string{"-c", "printf '%s %s' \"$0\" \"$1\" && exit 1", "arg1", "arg2"},
		},
	}

	err := executor.ExecuteHooks(ctx, hooks, tmpDir, "setup")
	if err == nil {
		t.Fatal("expected error to capture output")
	}

	var hookErr *HookError
	if !errors.As(err, &hookErr) {
		t.Fatalf("error should be HookError, got: %T", err)
	}

	// Verify output contains the args passed via positional parameters.
	outputStr := strings.TrimSpace(string(hookErr.Output))
	if outputStr != "arg1 arg2" {
		t.Errorf("expected output 'arg1 arg2', got: '%s'", outputStr)
	}
}

// TestExecuteHooksWorkingDirectory verifies hooks run in workspaceDir.
func TestExecuteHooksWorkingDirectory(t *testing.T) {
	executor := NewHookExecutor()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Hook that outputs its current working directory and fails so we can capture output.
	hooks := []Hook{
		{
			Command: "sh",
			Args:    []string{"-c", "pwd && exit 1"},
		},
	}

	err := executor.ExecuteHooks(ctx, hooks, tmpDir, "setup")
	if err == nil {
		t.Fatal("expected error to capture output")
	}

	var hookErr *HookError
	if !errors.As(err, &hookErr) {
		t.Fatalf("error should be HookError, got: %T", err)
	}

	// Verify pwd output matches tmpDir.
	outputStr := strings.TrimSpace(string(hookErr.Output))
	if outputStr != tmpDir {
		t.Errorf("pwd should output workspaceDir '%s', got: '%s'", tmpDir, outputStr)
	}
}

// TestExecuteHooksDescriptionPreserved verifies Description is in HookError.
func TestExecuteHooksDescriptionPreserved(t *testing.T) {
	executor := NewHookExecutor()
	ctx := context.Background()
	tmpDir := t.TempDir()

	hooks := []Hook{
		{
			Command:     "false",
			Description: "Critical setup step for database initialization",
		},
	}

	err := executor.ExecuteHooks(ctx, hooks, tmpDir, "setup")
	if err == nil {
		t.Fatal("expected error")
	}

	var hookErr *HookError
	if !errors.As(err, &hookErr) {
		t.Fatalf("error should be HookError, got: %T", err)
	}

	if hookErr.Description != "Critical setup step for database initialization" {
		t.Errorf("Description: expected 'Critical setup step for database initialization', got '%s'", hookErr.Description)
	}
}
