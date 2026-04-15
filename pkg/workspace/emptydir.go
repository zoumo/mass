// Package workspace implements workspace preparation handlers.
// This file defines the EmptyDirHandler implementation for creating empty
// directories as part of MASS workspace preparation.
package workspace

import (
	"context"
	"fmt"
	"os"
)

// EmptyDirHandler implements SourceHandler for SourceTypeEmptyDir.
// It creates an empty managed directory for agent workspaces.
type EmptyDirHandler struct{}

// NewEmptyDirHandler creates a new EmptyDirHandler.
func NewEmptyDirHandler() *EmptyDirHandler {
	return &EmptyDirHandler{}
}

// Prepare creates an empty directory at targetDir.
// It validates the source type and creates the directory with os.MkdirAll.
//
// Error handling:
//   - type mismatch: returns error with "cannot handle source type" message
//   - directory creation failure: wraps os error with context
//   - context cancellation: returns ctx.Err() (though this operation is fast)
func (h *EmptyDirHandler) Prepare(ctx context.Context, source Source, targetDir string) (string, error) {
	if source.Type != SourceTypeEmptyDir {
		return "", fmt.Errorf("workspace: EmptyDirHandler cannot handle source type %q", source.Type)
	}

	// Check for context cancellation before proceeding.
	if ctx.Err() != nil {
		return "", ctx.Err()
	}

	// Create the directory with standard permissions (0755: rwxr-xr-x).
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", fmt.Errorf("workspace: failed to create empty directory %q: %w", targetDir, err)
	}

	return targetDir, nil
}
