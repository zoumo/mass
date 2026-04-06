// Package workspace implements workspace preparation handlers.
// This file defines the HookExecutor implementation for executing workspace
// lifecycle hooks (setup and teardown) as part of OAR workspace preparation.
package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// HookError is a structured error for hook execution failures.
// It contains context about the operation phase, which hook failed,
// the command that was run, captured output, and underlying error.
type HookError struct {
	Phase       string   // "setup" or "teardown"
	HookIndex   int      // 0-based index of the failed hook
	Command     string   // Executable that was run
	Args        []string // Command-line arguments
	Description string   // Human-readable hook description
	ExitCode    int      // Process exit code (0 if not applicable)
	Output      []byte   // Combined stdout+stderr captured from command
	Message     string   // Human-readable error summary
	Err         error    // Underlying error (exec.ExitError, etc.)
}

// Error implements the error interface.
// Follows GitError pattern: joins parts with ': ' separator.
func (e *HookError) Error() string {
	var parts []string
	parts = append(parts, fmt.Sprintf("workspace: hook %s failed", e.Phase))
	parts = append(parts, fmt.Sprintf("hookIndex=%d", e.HookIndex))
	if e.Command != "" {
		parts = append(parts, fmt.Sprintf("command=%s", e.Command))
	}
	if e.ExitCode != 0 {
		parts = append(parts, fmt.Sprintf("exit=%d", e.ExitCode))
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	if len(e.Output) > 0 {
		// Include captured output for debugging.
		// Truncate very long output to keep error message readable.
		outputStr := string(e.Output)
		if len(outputStr) > 500 {
			outputStr = outputStr[:500] + "...(truncated)"
		}
		parts = append(parts, fmt.Sprintf("output: %s", outputStr))
	}
	if e.Err != nil {
		parts = append(parts, fmt.Sprintf("error: %v", e.Err))
	}
	return strings.Join(parts, ": ")
}

// Unwrap returns the underlying error for errors.Is and errors.As.
func (e *HookError) Unwrap() error {
	return e.Err
}

// HookExecutor executes workspace lifecycle hooks sequentially.
// It runs setup hooks after source preparation and teardown hooks before
// workspace destruction. Hooks execute in array order with abort-on-failure
// behavior — first failure stops execution and returns HookError.
type HookExecutor struct{}

// NewHookExecutor creates a new HookExecutor.
func NewHookExecutor() *HookExecutor {
	return &HookExecutor{}
}

// ExecuteHooks runs a sequence of hooks in order, aborting on first failure.
// Each hook command executes with workspaceDir as its working directory.
// Combined output (stdout+stderr) is captured and included in HookError on failure.
//
// Parameters:
//   - ctx: Context for cancellation support
//   - hooks: Array of Hook commands to execute
//   - workspaceDir: Working directory for hook commands (must exist)
//   - phase: "setup" or "teardown" for error context
//
// Returns nil on success, HookError on failure, or ctx.Err() if canceled.
//
// Execution behavior:
//   - Empty hooks array (nil or len==0) returns nil immediately (no hooks = success)
//   - Hooks execute sequentially in array order (0, 1, 2, ...)
//   - First failure aborts execution and returns HookError with HookIndex
//   - Context cancellation is checked before constructing HookError
//   - Defensive workspaceDir existence check before loop
func (h *HookExecutor) ExecuteHooks(ctx context.Context, hooks []Hook, workspaceDir string, phase string) error {
	// Defensive check: workspaceDir must exist before running hooks.
	if _, err := os.Stat(workspaceDir); err != nil {
		return fmt.Errorf("workspace: hook %s cannot execute: workspaceDir %q does not exist: %w", phase, workspaceDir, err)
	}

	// Empty hooks array = no hooks to run = success.
	if len(hooks) == 0 || hooks == nil {
		return nil
	}

	// Execute hooks sequentially, aborting on first failure.
	for i, hook := range hooks {
		// Build command with context for cancellation support.
		//nolint:gosec // hook.Command and hook.Args are constructed from validated workspace config
		cmd := exec.CommandContext(ctx, hook.Command, hook.Args...)
		cmd.Dir = workspaceDir // Run in workspace directory

		// Capture combined stdout+stderr for diagnostics.
		output, err := cmd.CombinedOutput()
		if err != nil {
			// Check for context cancellation first.
			if ctx.Err() != nil {
				return ctx.Err()
			}

			// Construct structured HookError with full context.
			exitCode := getExitCode(err)
			return &HookError{
				Phase:       phase,
				HookIndex:   i,
				Command:     hook.Command,
				Args:        hook.Args,
				Description: hook.Description,
				ExitCode:    exitCode,
				Output:      output,
				Message:     fmt.Sprintf("hook %d (%s) failed with exit %d", i, hook.Command, exitCode),
				Err:         err,
			}
		}
	}

	// All hooks executed successfully.
	return nil
}