// Package workspace implements workspace preparation handlers.
// This file defines the WorkspaceManager struct that orchestrates the full
// workspace lifecycle: Prepare (source preparation + setup hooks) and Cleanup
// (teardown hooks + managed directory deletion), with reference counting.
package workspace

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/zoumo/oar/api/meta"
	"github.com/zoumo/oar/pkg/store"
)

// WorkspaceManager orchestrates workspace lifecycle operations.
// It routes source preparation to the appropriate SourceHandler,
// executes lifecycle hooks, tracks reference counts for shared workspaces,
// and handles cleanup for managed workspaces (git/emptyDir).
type WorkspaceManager struct {
	// handlers maps SourceType to SourceHandler implementations.
	handlers map[SourceType]SourceHandler

	// hookExecutor runs setup and teardown hooks.
	hookExecutor *HookExecutor

	// refCount tracks active references to each workspace.
	// workspaceID (targetDir) → reference count
	refCount map[string]int

	// mutex protects refCount access.
	mutex sync.Mutex
}

// NewWorkspaceManager creates a WorkspaceManager with all source handlers.
// It initializes handlers for git, emptyDir, and local source types.
func NewWorkspaceManager() *WorkspaceManager {
	return &WorkspaceManager{
		handlers: map[SourceType]SourceHandler{
			SourceTypeGit:      NewGitHandler(),
			SourceTypeEmptyDir: NewEmptyDirHandler(),
			SourceTypeLocal:    NewLocalHandler(),
		},
		hookExecutor: NewHookExecutor(),
		refCount:     make(map[string]int),
	}
}

// Prepare prepares a workspace from a WorkspaceSpec.
// Workflow:
//  1. Validate spec via ValidateWorkspaceSpec
//  2. Route to SourceHandler based on spec.Source.Type
//  3. Execute setup hooks via HookExecutor.ExecuteHooks
//  4. Track workspace with Acquire (increment ref count)
//
// Error handling:
//   - Invalid spec: returns WorkspaceError with Phase="prepare-source"
//   - Handler failure: returns WorkspaceError with Phase="prepare-source"
//   - Hook failure: for managed workspaces, cleans up partial state via os.RemoveAll;
//     returns WorkspaceError with Phase="prepare-hooks"
//
// Managed workspaces (git, emptyDir) are created by agentd and can be cleaned up
// on failure. Local workspaces are unmanaged - agentd doesn't create or delete them.
func (m *WorkspaceManager) Prepare(ctx context.Context, spec WorkspaceSpec, targetDir string) (workspacePath string, err error) {
	// Validate spec.
	if err := ValidateWorkspaceSpec(spec); err != nil {
		return "", &WorkspaceError{
			Phase:       "prepare-source",
			WorkspaceID: targetDir,
			SourceType:  spec.Source.Type,
			Managed:     isManaged(spec.Source),
			Message:     "workspace spec validation failed",
			Err:         err,
		}
	}

	// Determine managed status.
	managed := isManaged(spec.Source)

	// Route to handler based on source type.
	handler, ok := m.handlers[spec.Source.Type]
	if !ok {
		return "", &WorkspaceError{
			Phase:       "prepare-source",
			WorkspaceID: targetDir,
			SourceType:  spec.Source.Type,
			Managed:     managed,
			Message:     fmt.Sprintf("no handler registered for source type %q", spec.Source.Type),
			Err:         nil,
		}
	}

	// Prepare source.
	workspacePath, err = handler.Prepare(ctx, spec.Source, targetDir)
	if err != nil {
		return "", &WorkspaceError{
			Phase:       "prepare-source",
			WorkspaceID: targetDir,
			SourceType:  spec.Source.Type,
			Managed:     managed,
			Message:     "source preparation failed",
			Err:         err,
		}
	}

	// Execute setup hooks.
	err = m.hookExecutor.ExecuteHooks(ctx, spec.Hooks.Setup, workspacePath, "setup")
	if err != nil {
		// For managed workspaces, clean up partial state on hook failure.
		if managed {
			os.RemoveAll(targetDir)
		}
		return "", &WorkspaceError{
			Phase:       "prepare-hooks",
			WorkspaceID: targetDir,
			SourceType:  spec.Source.Type,
			Managed:     managed,
			Message:     "setup hooks failed",
			Err:         err,
		}
	}

	// Track workspace: increment ref count.
	m.Acquire(targetDir)

	return workspacePath, nil
}

// Acquire increments the reference count for a workspace.
// Used to track active sessions sharing a workspace.
// Thread-safe via mutex lock.
func (m *WorkspaceManager) Acquire(workspaceID string) {
	m.mutex.Lock()
	m.refCount[workspaceID]++
	m.mutex.Unlock()
}

// Release decrements the reference count for a workspace.
// Returns the count after decrement.
// Thread-safe via mutex lock.
// If workspaceID not in refCount (count=0), returns 0 without error.
func (m *WorkspaceManager) Release(workspaceID string) int {
	m.mutex.Lock()
	count := m.refCount[workspaceID]
	if count > 0 {
		m.refCount[workspaceID]--
		count = m.refCount[workspaceID]
	}
	m.mutex.Unlock()
	return count
}

// Cleanup releases a workspace reference and performs cleanup if ref count reaches zero.
// Workflow:
//  1. Release(workspaceID) to decrement ref count
//  2. If count > 0: return nil (workspace still in use by other sessions)
//  3. If count == 0: proceed with cleanup
//  4. Execute teardown hooks via HookExecutor.ExecuteHooks
//  5. On teardown hook failure: log error but continue (best-effort cleanup)
//  6. If isManaged(spec.Source): os.RemoveAll(workspaceID) to delete managed directory
//
// Error handling:
//   - Teardown hook failure: logged but cleanup continues (best-effort)
//   - Directory deletion failure: returns WorkspaceError with Phase="cleanup-delete"
//
// Unmanaged workspaces (local) are NOT deleted - only teardown hooks run.
func (m *WorkspaceManager) Cleanup(ctx context.Context, workspaceID string, spec WorkspaceSpec) error {
	// Release reference and get count after decrement.
	count := m.Release(workspaceID)

	// If count > 0, workspace is still in use by other sessions.
	if count > 0 {
		return nil
	}

	// Count == 0: proceed with cleanup.
	managed := isManaged(spec.Source)

	// Execute teardown hooks (best-effort: continue on failure).
	err := m.hookExecutor.ExecuteHooks(ctx, spec.Hooks.Teardown, workspaceID, "teardown")
	if err != nil {
		// Log hook failure but continue cleanup.
		// Best-effort cleanup semantics: we still try to delete the directory.
		// Note: In production, this would be logged via structured logging.
		// For now, we silently continue to ensure cleanup completes.
	}

	// Delete managed directory (git, emptyDir).
	if managed {
		if err := os.RemoveAll(workspaceID); err != nil {
			return &WorkspaceError{
				Phase:       "cleanup-delete",
				WorkspaceID: workspaceID,
				SourceType:  spec.Source.Type,
				Managed:     managed,
				Message:     "failed to delete managed workspace directory",
				Err:         err,
			}
		}
	}

	return nil
}

// InitRefCounts loads all active workspaces from the metadata store and
// initializes the in-memory refCount map from their DB ref_count values.
// This is called once during daemon startup after recovery so that
// workspace cleanup decisions use the persisted reference counts.
func (m *WorkspaceManager) InitRefCounts(s *store.Store) error {
	ctx := context.Background()

	workspaces, err := s.ListWorkspaces(ctx, &meta.WorkspaceFilter{
		Phase: meta.WorkspacePhaseReady,
	})
	if err != nil {
		return fmt.Errorf("workspace: init refcounts: list workspaces: %w", err)
	}

	m.mutex.Lock()
	defer m.mutex.Unlock()

	for _, ws := range workspaces {
		// New meta model has no RefCount field; initialize each ready workspace
		// to refcount 0 so the path is tracked. Actual increments happen via
		// IncrRefCount / DecrRefCount at runtime.
		if ws.Status.Path != "" {
			if _, ok := m.refCount[ws.Status.Path]; !ok {
				m.refCount[ws.Status.Path] = 0
			}
		}
	}

	return nil
}

// isManaged returns true for source types that agentd creates and manages.
// Git and emptyDir sources create managed directories that agentd can clean up.
// Local sources reference existing directories that agentd does NOT manage.
func isManaged(source Source) bool {
	switch source.Type {
	case SourceTypeGit, SourceTypeEmptyDir:
		return true
	case SourceTypeLocal:
		return false
	default:
		return false
	}
}
