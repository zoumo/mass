// Package meta provides metadata storage for OAR session/workspace/room records.
package meta

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SessionFilter defines filter criteria for listing sessions.
type SessionFilter struct {
	// State filters by session state. Empty means all states.
	State SessionState

	// WorkspaceID filters by workspace ID. Empty means all workspaces.
	WorkspaceID string

	// Room filters by room name. Empty means all rooms (including none).
	Room string

	// HasRoom filters to only sessions with/without a room.
	// true: only sessions with a room, false: only sessions without a room.
	// nil: all sessions regardless of room association.
	HasRoom *bool
}

// CreateSession creates a new session record.
// The workspace must exist. If room is specified, the room must also exist.
// Returns an error if any foreign key constraint fails.
func (s *Store) CreateSession(ctx context.Context, session *Session) error {
	if session.ID == "" {
		return fmt.Errorf("meta: session ID is required")
	}
	if session.WorkspaceID == "" {
		return fmt.Errorf("meta: workspace ID is required")
	}
	if session.RuntimeClass == "" {
		return fmt.Errorf("meta: runtime class is required")
	}
	if session.State == "" {
		session.State = SessionStateRunning
	}
	if session.CreatedAt.IsZero() {
		session.CreatedAt = time.Now()
	}
	if session.UpdatedAt.IsZero() {
		session.UpdatedAt = session.CreatedAt
	}

	query := `
		INSERT INTO sessions (id, runtime_class, workspace_id, room, room_agent, labels, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	// Use NULL for room if empty (optional room association).
	// The FK constraint on room references rooms(name) ON DELETE SET NULL.
	// Empty string "" would fail FK constraint since no room named "" exists.
	var roomValue any
	if session.Room != "" {
		roomValue = session.Room
	}

	_, err := s.db.ExecContext(ctx, query,
		session.ID,
		session.RuntimeClass,
		session.WorkspaceID,
		roomValue,
		session.RoomAgent,
		labelsToJSON(session.Labels),
		session.State,
		session.CreatedAt,
		session.UpdatedAt,
	)

	if err != nil {
		// Check for foreign key constraint violation.
		if isFKViolation(err) {
			return fmt.Errorf("meta: foreign key constraint failed for session %s: %w", session.ID, err)
		}
		return fmt.Errorf("meta: failed to create session %s: %w", session.ID, err)
	}

	s.Logger.Debug("session created", "id", session.ID, "workspace_id", session.WorkspaceID)

	return nil
}

// GetSession retrieves a session by ID.
// Returns nil if the session doesn't exist.
func (s *Store) GetSession(ctx context.Context, id string) (*Session, error) {
	if id == "" {
		return nil, fmt.Errorf("meta: session ID is required")
	}

	query := `
		SELECT id, runtime_class, workspace_id, room, room_agent, labels, state, created_at, updated_at
		FROM sessions
		WHERE id = ?
	`

	session := &Session{}
	var labelsBytes []byte
	var room sql.NullString

	err := s.db.QueryRowContext(ctx, query, id).Scan(
		&session.ID,
		&session.RuntimeClass,
		&session.WorkspaceID,
		&room,
		&session.RoomAgent,
		&labelsBytes,
		&session.State,
		&session.CreatedAt,
		&session.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("meta: failed to get session %s: %w", id, err)
	}

	// Set room from nullable value.
	if room.Valid {
		session.Room = room.String
	} else {
		session.Room = ""
	}

	session.Labels = labelsFromJSON(labelsBytes)

	return session, nil
}

// ListSessions retrieves sessions matching the filter.
// If filter is nil, returns all sessions.
func (s *Store) ListSessions(ctx context.Context, filter *SessionFilter) ([]*Session, error) {
	query := `
		SELECT id, runtime_class, workspace_id, room, room_agent, labels, state, created_at, updated_at
		FROM sessions
	`

	var conditions []string
	var args []any

	if filter != nil {
		if filter.State != "" {
			conditions = append(conditions, "state = ?")
			args = append(args, filter.State)
		}
		if filter.WorkspaceID != "" {
			conditions = append(conditions, "workspace_id = ?")
			args = append(args, filter.WorkspaceID)
		}
		if filter.Room != "" {
			conditions = append(conditions, "room = ?")
			args = append(args, filter.Room)
		}
		if filter.HasRoom != nil {
			if *filter.HasRoom {
				conditions = append(conditions, "room IS NOT NULL")
			} else {
				conditions = append(conditions, "room IS NULL")
			}
		}
	}

	if len(conditions) > 0 {
		query += " WHERE " + joinConditions(conditions, " AND ")
	}

	query += " ORDER BY created_at DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("meta: failed to list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []*Session

	for rows.Next() {
		session := &Session{}
		var labelsBytes []byte
		var room sql.NullString

		err := rows.Scan(
			&session.ID,
			&session.RuntimeClass,
			&session.WorkspaceID,
			&room,
			&session.RoomAgent,
			&labelsBytes,
			&session.State,
			&session.CreatedAt,
			&session.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("meta: failed to scan session row: %w", err)
		}

		if room.Valid {
			session.Room = room.String
		} else {
			session.Room = ""
		}
		session.Labels = labelsFromJSON(labelsBytes)

		sessions = append(sessions, session)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("meta: failed to iterate session rows: %w", err)
	}

	return sessions, nil
}

// UpdateSession updates a session's state and/or labels.
// Only non-empty fields in the update are modified.
// Returns an error if the session doesn't exist.
func (s *Store) UpdateSession(ctx context.Context, id string, state SessionState, labels map[string]string) error {
	if id == "" {
		return fmt.Errorf("meta: session ID is required")
	}

	// Build dynamic update query based on what's being updated.
	var conditions []string
	var args []any

	if state != "" {
		conditions = append(conditions, "state = ?")
		args = append(args, state)
	}

	if labels != nil {
		conditions = append(conditions, "labels = ?")
		args = append(args, labelsToJSON(labels))
	}

	if len(conditions) == 0 {
		// Nothing to update.
		return nil
	}

	// The trigger handles updated_at automatically.
	query := "UPDATE sessions SET " + joinConditions(conditions, ", ") + " WHERE id = ?"
	args = append(args, id)

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("meta: failed to update session %s: %w", id, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("meta: failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("meta: session %s does not exist", id)
	}

	s.Logger.Debug("session updated", "id", id, "state", state)

	return nil
}

// DeleteSession deletes a session by ID.
// Also removes any workspace_refs for this session (trigger handles ref_count).
// Returns an error if the session doesn't exist.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("meta: session ID is required")
	}

	// Delete workspace_refs first (cascade would handle this, but we do it explicitly).
	// The trigger on workspace_refs will decrement ref_count automatically.
	_, err := s.db.ExecContext(ctx, "DELETE FROM workspace_refs WHERE session_id = ?", id)
	if err != nil {
		return fmt.Errorf("meta: failed to delete workspace refs for session %s: %w", id, err)
	}

	// Delete the session.
	result, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("meta: failed to delete session %s: %w", id, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("meta: failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("meta: session %s does not exist", id)
	}

	s.Logger.Debug("session deleted", "id", id)

	return nil
}

// joinConditions joins conditions with a separator.
func joinConditions(conditions []string, sep string) string {
	result := ""
	for i, c := range conditions {
		if i > 0 {
			result += sep
		}
		result += c
	}
	return result
}

// isFKViolation checks if an error is a foreign key constraint violation.
func isFKViolation(err error) bool {
	// SQLite foreign key error message format:
	// "FOREIGN KEY constraint failed"
	errMsg := err.Error()
	return containsIgnoreCase(errMsg, "FOREIGN KEY constraint")
}

// containsIgnoreCase checks if s contains substr case-insensitively.
func containsIgnoreCase(s, substr string) bool {
	sLower := lower(s)
	substrLower := lower(substr)
	return len(sLower) >= len(substrLower) && contains(sLower, substrLower)
}

// lower returns the lowercase of s.
func lower(s string) string {
	b := make([]byte, len(s))
	for i := range s {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

// contains checks if s contains substr.
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}