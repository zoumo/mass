// Package meta provides metadata storage for OAR session/workspace/room records.
package meta

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// WorkspaceFilter defines filter criteria for listing workspaces.
type WorkspaceFilter struct {
	// Status filters by workspace status. Empty means all statuses.
	Status WorkspaceStatus

	// Name filters by workspace name (exact match). Empty means all names.
	Name string

	// HasRefs filters to only workspaces with/without references.
	// true: only workspaces with refs (ref_count > 0).
	// false: only workspaces without refs (ref_count = 0).
	// nil: all workspaces regardless of refs.
	HasRefs *bool
}

// CreateWorkspace creates a new workspace record.
// Returns an error if a workspace with the same ID already exists.
func (s *Store) CreateWorkspace(ctx context.Context, workspace *Workspace) error {
	if workspace.ID == "" {
		return fmt.Errorf("meta: workspace ID is required")
	}
	if workspace.Name == "" {
		return fmt.Errorf("meta: workspace name is required")
	}
	if workspace.Path == "" {
		return fmt.Errorf("meta: workspace path is required")
	}
	if workspace.Status == "" {
		workspace.Status = WorkspaceStatusActive
	}
	if workspace.Source == nil {
		workspace.Source = json.RawMessage("{}")
	}
	if workspace.CreatedAt.IsZero() {
		workspace.CreatedAt = time.Now()
	}
	if workspace.UpdatedAt.IsZero() {
		workspace.UpdatedAt = workspace.CreatedAt
	}

	query := `
		INSERT INTO workspaces (id, name, path, source, status, ref_count, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := s.db.ExecContext(ctx, query,
		workspace.ID,
		workspace.Name,
		workspace.Path,
		workspace.Source,
		workspace.Status,
		workspace.RefCount,
		workspace.CreatedAt,
		workspace.UpdatedAt,
	)
	if err != nil {
		// Check for unique constraint violation.
		if isUniqueViolation(err) {
			return fmt.Errorf("meta: workspace %s already exists", workspace.ID)
		}
		return fmt.Errorf("meta: failed to create workspace %s: %w", workspace.ID, err)
	}

	s.Logger.Debug("workspace created", "id", workspace.ID, "name", workspace.Name)

	return nil
}

// GetWorkspace retrieves a workspace by ID.
// Returns nil if the workspace doesn't exist.
func (s *Store) GetWorkspace(ctx context.Context, id string) (*Workspace, error) {
	if id == "" {
		return nil, fmt.Errorf("meta: workspace ID is required")
	}

	query := `
		SELECT id, name, path, source, status, ref_count, created_at, updated_at
		FROM workspaces
		WHERE id = ?
	`

	workspace := &Workspace{}

	err := s.db.QueryRowContext(ctx, query, id).Scan(
		&workspace.ID,
		&workspace.Name,
		&workspace.Path,
		&workspace.Source,
		&workspace.Status,
		&workspace.RefCount,
		&workspace.CreatedAt,
		&workspace.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("meta: failed to get workspace %s: %w", id, err)
	}

	return workspace, nil
}

// ListWorkspaces retrieves workspaces matching the filter.
// If filter is nil, returns all workspaces.
func (s *Store) ListWorkspaces(ctx context.Context, filter *WorkspaceFilter) ([]*Workspace, error) {
	query := `
		SELECT id, name, path, source, status, ref_count, created_at, updated_at
		FROM workspaces
	`

	var conditions []string
	var args []any

	if filter != nil {
		if filter.Status != "" {
			conditions = append(conditions, "status = ?")
			args = append(args, filter.Status)
		}
		if filter.Name != "" {
			conditions = append(conditions, "name = ?")
			args = append(args, filter.Name)
		}
		if filter.HasRefs != nil {
			if *filter.HasRefs {
				conditions = append(conditions, "ref_count > 0")
			} else {
				conditions = append(conditions, "ref_count = 0")
			}
		}
	}

	if len(conditions) > 0 {
		query += " WHERE " + joinConditions(conditions, " AND ")
	}

	query += " ORDER BY created_at DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("meta: failed to list workspaces: %w", err)
	}
	defer rows.Close()

	var workspaces []*Workspace

	for rows.Next() {
		workspace := &Workspace{}

		err := rows.Scan(
			&workspace.ID,
			&workspace.Name,
			&workspace.Path,
			&workspace.Source,
			&workspace.Status,
			&workspace.RefCount,
			&workspace.CreatedAt,
			&workspace.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("meta: failed to scan workspace row: %w", err)
		}

		workspaces = append(workspaces, workspace)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("meta: failed to iterate workspace rows: %w", err)
	}

	return workspaces, nil
}

// UpdateWorkspaceStatus updates a workspace's status.
// Returns an error if the workspace doesn't exist.
func (s *Store) UpdateWorkspaceStatus(ctx context.Context, id string, status WorkspaceStatus) error {
	if id == "" {
		return fmt.Errorf("meta: workspace ID is required")
	}
	if status == "" {
		return fmt.Errorf("meta: workspace status is required")
	}

	// The trigger handles updated_at automatically.
	query := "UPDATE workspaces SET status = ? WHERE id = ?"

	result, err := s.db.ExecContext(ctx, query, status, id)
	if err != nil {
		return fmt.Errorf("meta: failed to update workspace %s status: %w", id, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("meta: failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("meta: workspace %s does not exist", id)
	}

	s.Logger.Debug("workspace status updated", "id", id, "status", status)

	return nil
}

// DeleteWorkspace deletes a workspace by ID.
// Returns an error if:
// - The workspace doesn't exist
// - The workspace has references (ref_count > 0)
// Returns the number of workspace_refs deleted (should be 0 if ref_count check passes).
func (s *Store) DeleteWorkspace(ctx context.Context, id string) (int, error) {
	if id == "" {
		return 0, fmt.Errorf("meta: workspace ID is required")
	}

	// Check if workspace has references.
	workspace, err := s.GetWorkspace(ctx, id)
	if err != nil {
		return 0, err
	}
	if workspace == nil {
		return 0, fmt.Errorf("meta: workspace %s does not exist", id)
	}
	if workspace.RefCount > 0 {
		return 0, fmt.Errorf("meta: workspace %s has %d references and cannot be deleted", id, workspace.RefCount)
	}

	// Delete any remaining workspace_refs (should be none, but just in case).
	refsResult, err := s.db.ExecContext(ctx, "DELETE FROM workspace_refs WHERE workspace_id = ?", id)
	if err != nil {
		return 0, fmt.Errorf("meta: failed to delete workspace refs for workspace %s: %w", id, err)
	}

	refsRowsAffected, _ := refsResult.RowsAffected()

	// Delete the workspace.
	result, err := s.db.ExecContext(ctx, "DELETE FROM workspaces WHERE id = ?", id)
	if err != nil {
		return 0, fmt.Errorf("meta: failed to delete workspace %s: %w", id, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("meta: failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return int(refsRowsAffected), fmt.Errorf("meta: workspace %s does not exist", id)
	}

	s.Logger.Debug("workspace deleted", "id", id)

	return int(refsRowsAffected), nil
}

// AcquireWorkspace acquires a workspace for a session.
// This creates a workspace_ref entry, which triggers ref_count increment.
// Returns an error if the workspace doesn't exist or is not active.
func (s *Store) AcquireWorkspace(ctx context.Context, workspaceID, sessionID string) error {
	if workspaceID == "" {
		return fmt.Errorf("meta: workspace ID is required")
	}
	if sessionID == "" {
		return fmt.Errorf("meta: session ID is required")
	}

	// Verify workspace exists and is active.
	workspace, err := s.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return err
	}
	if workspace == nil {
		return fmt.Errorf("meta: workspace %s does not exist", workspaceID)
	}
	if workspace.Status != WorkspaceStatusActive {
		return fmt.Errorf("meta: workspace %s is not active (status: %s)", workspaceID, workspace.Status)
	}

	// Insert workspace_ref (trigger will increment ref_count automatically).
	query := `
		INSERT INTO workspace_refs (workspace_id, session_id, created_at)
		VALUES (?, ?, ?)
	`

	_, err = s.db.ExecContext(ctx, query, workspaceID, sessionID, time.Now())
	if err != nil {
		// Check for unique violation (already acquired).
		if isUniqueViolation(err) {
			return fmt.Errorf("meta: workspace %s already acquired by session %s", workspaceID, sessionID)
		}
		return fmt.Errorf("meta: failed to acquire workspace %s for session %s: %w", workspaceID, sessionID, err)
	}

	s.Logger.Debug("workspace acquired", "workspace_id", workspaceID, "session_id", sessionID)

	return nil
}

// ReleaseWorkspace releases a workspace from a session.
// This deletes the workspace_ref entry, which triggers ref_count decrement.
// Returns the new ref_count after release, or an error if:
// - The workspace doesn't exist
// - The session didn't have this workspace acquired
func (s *Store) ReleaseWorkspace(ctx context.Context, workspaceID, sessionID string) (int, error) {
	if workspaceID == "" {
		return 0, fmt.Errorf("meta: workspace ID is required")
	}
	if sessionID == "" {
		return 0, fmt.Errorf("meta: session ID is required")
	}

	// Delete workspace_ref (trigger will decrement ref_count automatically).
	result, err := s.db.ExecContext(ctx, "DELETE FROM workspace_refs WHERE workspace_id = ? AND session_id = ?", workspaceID, sessionID)
	if err != nil {
		return 0, fmt.Errorf("meta: failed to release workspace %s from session %s: %w", workspaceID, sessionID, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("meta: failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return 0, fmt.Errorf("meta: workspace %s was not acquired by session %s", workspaceID, sessionID)
	}

	// Get the new ref_count.
	workspace, err := s.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return 0, err
	}
	if workspace == nil {
		return 0, fmt.Errorf("meta: workspace %s does not exist", workspaceID)
	}

	s.Logger.Debug("workspace released", "workspace_id", workspaceID, "session_id", sessionID, "ref_count", workspace.RefCount)

	return workspace.RefCount, nil
}

// ListWorkspaceRefs returns the session IDs that reference a workspace.
// Used during registry rebuild to populate the Refs debugging list.
func (s *Store) ListWorkspaceRefs(ctx context.Context, workspaceID string) ([]string, error) {
	if workspaceID == "" {
		return nil, fmt.Errorf("meta: workspace ID is required")
	}

	query := `SELECT session_id FROM workspace_refs WHERE workspace_id = ? ORDER BY created_at`

	rows, err := s.db.QueryContext(ctx, query, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("meta: failed to list workspace refs for %s: %w", workspaceID, err)
	}
	defer rows.Close()

	var sessionIDs []string
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			return nil, fmt.Errorf("meta: failed to scan workspace ref row: %w", err)
		}
		sessionIDs = append(sessionIDs, sid)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("meta: failed to iterate workspace ref rows: %w", err)
	}

	return sessionIDs, nil
}

// isUniqueViolation checks if an error is a unique constraint violation.
func isUniqueViolation(err error) bool {
	errMsg := err.Error()
	return containsIgnoreCase(errMsg, "UNIQUE constraint") ||
		containsIgnoreCase(errMsg, "unique constraint") ||
		containsIgnoreCase(errMsg, "already exists")
}
