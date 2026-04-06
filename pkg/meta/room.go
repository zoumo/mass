// Package meta provides metadata storage for OAR session/workspace/room records.
package meta

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// RoomFilter defines filter criteria for listing rooms.
type RoomFilter struct {
	// CommunicationMode filters by communication mode. Empty means all modes.
	CommunicationMode CommunicationMode
}

// CreateRoom creates a new room record.
// Returns an error if a room with the same name already exists.
func (s *Store) CreateRoom(ctx context.Context, room *Room) error {
	if room.Name == "" {
		return fmt.Errorf("meta: room name is required")
	}
	if room.CommunicationMode == "" {
		room.CommunicationMode = CommunicationModeBroadcast
	}
	if room.CreatedAt.IsZero() {
		room.CreatedAt = time.Now()
	}
	if room.UpdatedAt.IsZero() {
		room.UpdatedAt = room.CreatedAt
	}

	query := `
		INSERT INTO rooms (name, labels, communication_mode, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`

	_, err := s.db.ExecContext(ctx, query,
		room.Name,
		labelsToJSON(room.Labels),
		room.CommunicationMode,
		room.CreatedAt,
		room.UpdatedAt,
	)

	if err != nil {
		// Check for unique constraint violation.
		if isUniqueViolation(err) {
			return fmt.Errorf("meta: room %s already exists", room.Name)
		}
		return fmt.Errorf("meta: failed to create room %s: %w", room.Name, err)
	}

	s.Logger.Debug("room created", "name", room.Name)

	return nil
}

// GetRoom retrieves a room by name.
// Returns nil if the room doesn't exist.
func (s *Store) GetRoom(ctx context.Context, name string) (*Room, error) {
	if name == "" {
		return nil, fmt.Errorf("meta: room name is required")
	}

	query := `
		SELECT name, labels, communication_mode, created_at, updated_at
		FROM rooms
		WHERE name = ?
	`

	room := &Room{}
	var labelsBytes []byte

	err := s.db.QueryRowContext(ctx, query, name).Scan(
		&room.Name,
		&labelsBytes,
		&room.CommunicationMode,
		&room.CreatedAt,
		&room.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("meta: failed to get room %s: %w", name, err)
	}

	room.Labels = labelsFromJSON(labelsBytes)

	return room, nil
}

// ListRooms retrieves rooms matching the filter.
// If filter is nil, returns all rooms.
func (s *Store) ListRooms(ctx context.Context, filter *RoomFilter) ([]*Room, error) {
	query := `
		SELECT name, labels, communication_mode, created_at, updated_at
		FROM rooms
	`

	var conditions []string
	var args []any

	if filter != nil {
		if filter.CommunicationMode != "" {
			conditions = append(conditions, "communication_mode = ?")
			args = append(args, filter.CommunicationMode)
		}
	}

	if len(conditions) > 0 {
		query += " WHERE " + joinConditions(conditions, " AND ")
	}

	query += " ORDER BY created_at DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("meta: failed to list rooms: %w", err)
	}
	defer rows.Close()

	var rooms []*Room

	for rows.Next() {
		room := &Room{}
		var labelsBytes []byte

		err := rows.Scan(
			&room.Name,
			&labelsBytes,
			&room.CommunicationMode,
			&room.CreatedAt,
			&room.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("meta: failed to scan room row: %w", err)
		}

		room.Labels = labelsFromJSON(labelsBytes)

		rooms = append(rooms, room)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("meta: failed to iterate room rows: %w", err)
	}

	return rooms, nil
}

// DeleteRoom deletes a room by name.
// Sessions referencing this room will have their room field set to NULL (ON DELETE SET NULL).
// Returns an error if the room doesn't exist.
func (s *Store) DeleteRoom(ctx context.Context, name string) error {
	if name == "" {
		return fmt.Errorf("meta: room name is required")
	}

	result, err := s.db.ExecContext(ctx, "DELETE FROM rooms WHERE name = ?", name)
	if err != nil {
		return fmt.Errorf("meta: failed to delete room %s: %w", name, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("meta: failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("meta: room %s does not exist", name)
	}

	s.Logger.Debug("room deleted", "name", name)

	return nil
}