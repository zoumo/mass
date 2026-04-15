// Package workspace defines the MASS Workspace Specification types and handlers.
package workspace

import "context"

// SourceHandler prepares a workspace directory from a Source.
type SourceHandler interface {
	Prepare(ctx context.Context, source Source, targetDir string) (workspacePath string, err error)
}
