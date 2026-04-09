// Package workspace implements workspace preparation handlers.
// This file defines the WorkspaceError type for WorkspaceManager operations.
package workspace

import (
	"fmt"
	"strings"
)

// WorkspaceError is a structured error for WorkspaceManager operations.
// It contains context about the operation phase, workspace identity, source type,
// managed status, and underlying error for targeted diagnostics.
type WorkspaceError struct {
	Phase       string     // "prepare-source", "prepare-hooks", "cleanup-hooks", "cleanup-delete"
	WorkspaceID string     // Target directory path (workspace identifier)
	SourceType  SourceType // Source type being processed (git, emptyDir, local)
	Managed     bool       // Whether workspace is managed by agentd (true for git/emptyDir)
	Message     string     // Human-readable error summary
	Err         error      // Underlying error (GitError, HookError, etc.)
}

// Error implements the error interface.
// Follows GitError/HookError pattern: joins parts with ': ' separator.
func (e *WorkspaceError) Error() string {
	var parts []string
	parts = append(parts, fmt.Sprintf("workspace: %s failed", e.Phase))
	if e.WorkspaceID != "" {
		parts = append(parts, fmt.Sprintf("workspaceID=%s", e.WorkspaceID))
	}
	if e.SourceType != "" {
		parts = append(parts, fmt.Sprintf("sourceType=%s", e.SourceType))
	}
	parts = append(parts, fmt.Sprintf("managed=%v", e.Managed))
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	if e.Err != nil {
		parts = append(parts, fmt.Sprintf("error: %v", e.Err))
	}
	return strings.Join(parts, ": ")
}

// Unwrap returns the underlying error for errors.Is and errors.As.
func (e *WorkspaceError) Unwrap() error {
	return e.Err
}
