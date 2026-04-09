// Package workspace implements workspace preparation handlers.
// This file defines the LocalHandler implementation for validating local
// directory sources as part of OAR workspace preparation.
package workspace

import (
	"context"
	"fmt"
	"os"
)

// LocalHandler implements SourceHandler for SourceTypeLocal.
// It validates that the source.Local.Path exists and is a directory.
// Unlike GitHandler and EmptyDirHandler, LocalHandler does NOT create or
// manage directories - it returns source.Local.Path directly because
// local workspaces are unmanaged by agentd.
type LocalHandler struct{}

// NewLocalHandler creates a new LocalHandler.
func NewLocalHandler() *LocalHandler {
	return &LocalHandler{}
}

// Prepare validates that source.Local.Path exists and is a directory.
// It returns source.Local.Path directly (NOT targetDir) because local
// workspaces are unmanaged - agentd doesn't create or delete them.
//
// Error handling:
//   - type mismatch: returns error with "cannot handle source type" message
//   - path doesn't exist: returns error with "does not exist" message
//   - path is file (not directory): returns error with "not a directory" message
//   - context cancellation: returns ctx.Err() (though this operation is fast)
func (h *LocalHandler) Prepare(ctx context.Context, source Source, targetDir string) (string, error) {
	if source.Type != SourceTypeLocal {
		return "", fmt.Errorf("workspace: LocalHandler cannot handle source type %q", source.Type)
	}

	// Check for context cancellation before proceeding.
	if ctx.Err() != nil {
		return "", ctx.Err()
	}

	localPath := source.Local.Path

	// Validate path exists via os.Stat.
	info, err := os.Stat(localPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("workspace: local source path %q does not exist", localPath)
		}
		// Other os.Stat errors (permission denied, etc.) - wrap with context.
		return "", fmt.Errorf("workspace: failed to stat local source path %q: %w", localPath, err)
	}

	// Validate path is a directory (not a file).
	if !info.IsDir() {
		return "", fmt.Errorf("workspace: local source path %q is not a directory", localPath)
	}

	// CRITICAL: Return source.Local.Path (NOT targetDir).
	// Local workspaces are unmanaged - agentd doesn't create or delete them.
	return localPath, nil
}
