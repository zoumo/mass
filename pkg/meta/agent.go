// Package meta provides metadata storage for OAR session/workspace/room records.
package meta

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// AgentFilter defines filter criteria for listing agents.
type AgentFilter struct {
	// State filters by agent state. Empty means all states.
	State AgentState

	// Room filters by room name. Empty means all rooms.
	Room string
}

// CreateAgent creates a new agent record.
// The room must exist and the workspace must exist.
// The (room, name) combination must be unique.
// Returns an error if any required field is missing or any FK constraint fails.
func (s *Store) CreateAgent(ctx context.Context, agent *Agent) error {
	if agent.ID == "" {
		return fmt.Errorf("meta: agent ID is required")
	}
	if agent.Room == "" {
		return fmt.Errorf("meta: agent room is required")
	}
	if agent.Name == "" {
		return fmt.Errorf("meta: agent name is required")
	}
	if agent.RuntimeClass == "" {
		return fmt.Errorf("meta: agent runtime class is required")
	}
	if agent.WorkspaceID == "" {
		return fmt.Errorf("meta: agent workspace ID is required")
	}
	if agent.State == "" {
		agent.State = AgentStateCreating
	}
	if agent.CreatedAt.IsZero() {
		agent.CreatedAt = time.Now()
	}
	if agent.UpdatedAt.IsZero() {
		agent.UpdatedAt = agent.CreatedAt
	}

	query := `
		INSERT INTO agents (id, room, name, runtime_class, workspace_id,
			description, system_prompt, labels, state, error_message,
			created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := s.db.ExecContext(ctx, query,
		agent.ID,
		agent.Room,
		agent.Name,
		agent.RuntimeClass,
		agent.WorkspaceID,
		agent.Description,
		agent.SystemPrompt,
		labelsToJSON(agent.Labels),
		agent.State,
		agent.ErrorMessage,
		agent.CreatedAt,
		agent.UpdatedAt,
	)

	if err != nil {
		if isFKViolation(err) {
			return fmt.Errorf("meta: foreign key constraint failed for agent %s: %w", agent.ID, err)
		}
		if isUniqueViolation(err) {
			return fmt.Errorf("meta: agent with room=%s name=%s already exists: %w", agent.Room, agent.Name, err)
		}
		return fmt.Errorf("meta: failed to create agent %s: %w", agent.ID, err)
	}

	s.Logger.Info("agent created", "id", agent.ID, "room", agent.Room, "name", agent.Name)

	return nil
}

// GetAgent retrieves an agent by ID.
// Returns nil if the agent doesn't exist.
func (s *Store) GetAgent(ctx context.Context, id string) (*Agent, error) {
	if id == "" {
		return nil, fmt.Errorf("meta: agent ID is required")
	}

	query := `
		SELECT id, room, name, runtime_class, workspace_id,
			description, system_prompt, labels, state, error_message,
			created_at, updated_at
		FROM agents
		WHERE id = ?
	`

	agent := &Agent{}
	var labelsBytes []byte

	err := s.db.QueryRowContext(ctx, query, id).Scan(
		&agent.ID,
		&agent.Room,
		&agent.Name,
		&agent.RuntimeClass,
		&agent.WorkspaceID,
		&agent.Description,
		&agent.SystemPrompt,
		&labelsBytes,
		&agent.State,
		&agent.ErrorMessage,
		&agent.CreatedAt,
		&agent.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("meta: failed to get agent %s: %w", id, err)
	}

	agent.Labels = labelsFromJSON(labelsBytes)

	return agent, nil
}

// GetAgentByRoomName retrieves an agent by its unique (room, name) pair.
// Returns nil if no agent with that combination exists.
func (s *Store) GetAgentByRoomName(ctx context.Context, room, name string) (*Agent, error) {
	if room == "" {
		return nil, fmt.Errorf("meta: room is required")
	}
	if name == "" {
		return nil, fmt.Errorf("meta: agent name is required")
	}

	query := `
		SELECT id, room, name, runtime_class, workspace_id,
			description, system_prompt, labels, state, error_message,
			created_at, updated_at
		FROM agents
		WHERE room = ? AND name = ?
	`

	agent := &Agent{}
	var labelsBytes []byte

	err := s.db.QueryRowContext(ctx, query, room, name).Scan(
		&agent.ID,
		&agent.Room,
		&agent.Name,
		&agent.RuntimeClass,
		&agent.WorkspaceID,
		&agent.Description,
		&agent.SystemPrompt,
		&labelsBytes,
		&agent.State,
		&agent.ErrorMessage,
		&agent.CreatedAt,
		&agent.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("meta: failed to get agent by room=%s name=%s: %w", room, name, err)
	}

	agent.Labels = labelsFromJSON(labelsBytes)

	return agent, nil
}

// ListAgents retrieves agents matching the filter.
// If filter is nil, returns all agents ordered by created_at DESC.
func (s *Store) ListAgents(ctx context.Context, filter *AgentFilter) ([]*Agent, error) {
	query := `
		SELECT id, room, name, runtime_class, workspace_id,
			description, system_prompt, labels, state, error_message,
			created_at, updated_at
		FROM agents
	`

	var conditions []string
	var args []any

	if filter != nil {
		if filter.State != "" {
			conditions = append(conditions, "state = ?")
			args = append(args, filter.State)
		}
		if filter.Room != "" {
			conditions = append(conditions, "room = ?")
			args = append(args, filter.Room)
		}
	}

	if len(conditions) > 0 {
		query += " WHERE " + joinConditions(conditions, " AND ")
	}

	query += " ORDER BY created_at DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("meta: failed to list agents: %w", err)
	}
	defer rows.Close()

	var agents []*Agent

	for rows.Next() {
		agent := &Agent{}
		var labelsBytes []byte

		err := rows.Scan(
			&agent.ID,
			&agent.Room,
			&agent.Name,
			&agent.RuntimeClass,
			&agent.WorkspaceID,
			&agent.Description,
			&agent.SystemPrompt,
			&labelsBytes,
			&agent.State,
			&agent.ErrorMessage,
			&agent.CreatedAt,
			&agent.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("meta: failed to scan agent row: %w", err)
		}

		agent.Labels = labelsFromJSON(labelsBytes)
		agents = append(agents, agent)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("meta: failed to iterate agent rows: %w", err)
	}

	return agents, nil
}

// UpdateAgent updates an agent's state, error message, and/or labels.
// Only non-zero/non-nil fields are modified.
// Returns an error if the agent doesn't exist.
func (s *Store) UpdateAgent(ctx context.Context, id string, state AgentState, errorMessage string, labels map[string]string) error {
	if id == "" {
		return fmt.Errorf("meta: agent ID is required")
	}

	var conditions []string
	var args []any

	if state != "" {
		conditions = append(conditions, "state = ?")
		args = append(args, state)
	}

	if errorMessage != "" {
		conditions = append(conditions, "error_message = ?")
		args = append(args, errorMessage)
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
	query := "UPDATE agents SET " + joinConditions(conditions, ", ") + " WHERE id = ?"
	args = append(args, id)

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("meta: failed to update agent %s: %w", id, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("meta: failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("meta: agent %s does not exist", id)
	}

	s.Logger.Info("agent updated", "id", id, "state", state)

	return nil
}

// DeleteAgent deletes an agent by ID.
// Returns an error if the agent doesn't exist.
func (s *Store) DeleteAgent(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("meta: agent ID is required")
	}

	result, err := s.db.ExecContext(ctx, "DELETE FROM agents WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("meta: failed to delete agent %s: %w", id, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("meta: failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("meta: agent %s does not exist", id)
	}

	s.Logger.Info("agent deleted", "id", id)

	return nil
}


