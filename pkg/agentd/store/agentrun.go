// Package store provides metadata persistence for MASS agent and workspace records.
// This file defines AgentRun CRUD methods.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
)

// CreateAgentRun stores a new AgentRun record.
// The agent run identity is (Metadata.Workspace, Metadata.Name).
// Returns an error if an agent run with the same (workspace, name) already exists.
func (s *Store) CreateAgentRun(_ context.Context, agent *pkgariapi.AgentRun) error {
	if agent.Metadata.Workspace == "" {
		return fmt.Errorf("store: agent workspace is required")
	}
	if agent.Metadata.Name == "" {
		return fmt.Errorf("store: agent name is required")
	}
	if agent.Spec.Agent == "" {
		return fmt.Errorf("store: agent run requires an agent definition name")
	}

	now := time.Now()
	if agent.Metadata.CreatedAt.IsZero() {
		agent.Metadata.CreatedAt = now
	}
	if agent.Metadata.UpdatedAt.IsZero() {
		agent.Metadata.UpdatedAt = agent.Metadata.CreatedAt
	}
	agent.Kind = pkgariapi.KindAgentRun

	return s.db.Update(func(tx *bolt.Tx) error {
		wb, err := workspaceBucket(tx, agent.Metadata.Workspace)
		if err != nil {
			return err
		}
		key := []byte(agent.Metadata.Name)
		if existing := wb.Get(key); existing != nil {
			return &ResourceError{Op: "create", Resource: "agent", Key: agent.Metadata.Workspace + "/" + agent.Metadata.Name, Err: ErrAlreadyExists}
		}
		data, err := json.Marshal(agent)
		if err != nil {
			return fmt.Errorf("store: marshal agent %s/%s: %w",
				agent.Metadata.Workspace, agent.Metadata.Name, err)
		}
		if err := wb.Put(key, data); err != nil {
			return fmt.Errorf("store: store agent %s/%s: %w",
				agent.Metadata.Workspace, agent.Metadata.Name, err)
		}
		s.logger.Debug("agentRun created",
			"workspace", agent.Metadata.Workspace,
			"name", agent.Metadata.Name)
		return nil
	})
}

// GetAgentRun retrieves an agent run by (workspace, name).
// Returns nil, nil if the agent run does not exist.
func (s *Store) GetAgentRun(_ context.Context, workspace, name string) (*pkgariapi.AgentRun, error) {
	if workspace == "" {
		return nil, fmt.Errorf("store: workspace is required")
	}
	if name == "" {
		return nil, fmt.Errorf("store: agent name is required")
	}

	var agent *pkgariapi.AgentRun
	err := s.db.View(func(tx *bolt.Tx) error {
		wb := workspaceBucketRO(tx, workspace)
		if wb == nil {
			return nil // workspace sub-bucket not found → agent doesn't exist
		}
		data := wb.Get([]byte(name))
		if data == nil {
			return nil // not found
		}
		agent = &pkgariapi.AgentRun{}
		if err := json.Unmarshal(data, agent); err != nil {
			return err
		}
		agent.Kind = pkgariapi.KindAgentRun
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: get agent %s/%s: %w", workspace, name, err)
	}
	return agent, nil
}

// ListAgentRuns returns all agent runs matching the optional filter.
//
//   - If filter.Workspace is non-empty, only that workspace's sub-bucket is scanned.
//   - If filter.Status is non-empty, only agent runs with that state are returned.
//   - If filter is nil every agent run in every workspace is returned.
func (s *Store) ListAgentRuns(_ context.Context, filter *pkgariapi.AgentRunFilter) ([]*pkgariapi.AgentRun, error) {
	var result []*pkgariapi.AgentRun

	err := s.db.View(func(tx *bolt.Tx) error {
		runs := agentRunsBucket(tx)

		// scanWorkspace iterates a single workspace sub-bucket and appends matching agent runs.
		scanWorkspace := func(wb *bolt.Bucket) error {
			return wb.ForEach(func(_, v []byte) error {
				if v == nil {
					return nil // skip nested buckets
				}
				var a pkgariapi.AgentRun
				if err := json.Unmarshal(v, &a); err != nil {
					s.logger.Error("skipping corrupt agentRun record", "error", err)
					return nil
				}
				if filter != nil && filter.Status != "" && a.Status.Status != filter.Status {
					return nil
				}
				a.Kind = pkgariapi.KindAgentRun
				copy := a
				result = append(result, &copy)
				return nil
			})
		}

		if filter != nil && filter.Workspace != "" {
			// Only scan the requested workspace.
			wb := runs.Bucket([]byte(filter.Workspace))
			if wb == nil {
				return nil // no agent runs in this workspace
			}
			return scanWorkspace(wb)
		}

		// Scan all workspace sub-buckets.
		return runs.ForEach(func(k, v []byte) error {
			if v != nil {
				return nil // skip leaf values (shouldn't exist at this level)
			}
			wb := runs.Bucket(k)
			if wb == nil {
				return nil
			}
			return scanWorkspace(wb)
		})
	})
	if err != nil {
		return nil, fmt.Errorf("store: list agentRuns: %w", err)
	}
	return result, nil
}

// UpdateAgentRunStatus overwrites the Status field of the identified agent run.
// Returns an error if the agent run does not exist.
func (s *Store) UpdateAgentRunStatus(_ context.Context, workspace, name string, status pkgariapi.AgentRunStatus) error {
	if workspace == "" {
		return fmt.Errorf("store: workspace is required")
	}
	if name == "" {
		return fmt.Errorf("store: agent name is required")
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		wb, err := workspaceBucket(tx, workspace)
		if err != nil {
			return err
		}
		key := []byte(name)
		data := wb.Get(key)
		if data == nil {
			return &ResourceError{Op: "update", Resource: "agent", Key: workspace + "/" + name, Err: ErrNotFound}
		}
		var agent pkgariapi.AgentRun
		if err := json.Unmarshal(data, &agent); err != nil {
			return fmt.Errorf("store: unmarshal agent %s/%s: %w", workspace, name, err)
		}
		agent.Status = status
		agent.Metadata.UpdatedAt = time.Now()
		updated, err := json.Marshal(&agent)
		if err != nil {
			return fmt.Errorf("store: marshal agent %s/%s: %w", workspace, name, err)
		}
		if err := wb.Put(key, updated); err != nil {
			return fmt.Errorf("store: store agent %s/%s: %w", workspace, name, err)
		}
		s.logger.Debug("agentRun status updated",
			"workspace", workspace,
			"name", name,
			"state", status.Status)
		return nil
	})
}

// UpdateAgentRunState updates only Status.Status and Status.ErrorMessage,
// preserving all other status fields (PID, SocketPath, StateDir, etc.).
func (s *Store) UpdateAgentRunState(_ context.Context, workspace, name string, state apiruntime.Status, errMsg string) error {
	if workspace == "" {
		return fmt.Errorf("store: workspace is required")
	}
	if name == "" {
		return fmt.Errorf("store: agent name is required")
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		wb, err := workspaceBucket(tx, workspace)
		if err != nil {
			return err
		}
		key := []byte(name)
		data := wb.Get(key)
		if data == nil {
			return &ResourceError{Op: "update-state", Resource: "agent", Key: workspace + "/" + name, Err: ErrNotFound}
		}
		var agent pkgariapi.AgentRun
		if err := json.Unmarshal(data, &agent); err != nil {
			return fmt.Errorf("store: unmarshal agent %s/%s: %w", workspace, name, err)
		}
		agent.Status.Status = state
		agent.Status.ErrorMessage = errMsg
		agent.Metadata.UpdatedAt = time.Now()
		updated, err := json.Marshal(&agent)
		if err != nil {
			return fmt.Errorf("store: marshal agent %s/%s: %w", workspace, name, err)
		}
		if err := wb.Put(key, updated); err != nil {
			return fmt.Errorf("store: store agent %s/%s: %w", workspace, name, err)
		}
		s.logger.Debug("agentRun state updated",
			"workspace", workspace,
			"name", name,
			"state", state)
		return nil
	})
}

// UpdateAgentRunSessionInfo updates SessionID and EventPath, preserving all other status fields.
func (s *Store) UpdateAgentRunSessionInfo(_ context.Context, workspace, name, sessionID, eventPath string) error {
	if workspace == "" {
		return fmt.Errorf("store: workspace is required")
	}
	if name == "" {
		return fmt.Errorf("store: agent name is required")
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		wb, err := workspaceBucket(tx, workspace)
		if err != nil {
			return err
		}
		key := []byte(name)
		data := wb.Get(key)
		if data == nil {
			return &ResourceError{Op: "update-session", Resource: "agent", Key: workspace + "/" + name, Err: ErrNotFound}
		}
		var agent pkgariapi.AgentRun
		if err := json.Unmarshal(data, &agent); err != nil {
			return fmt.Errorf("store: unmarshal agent %s/%s: %w", workspace, name, err)
		}
		agent.Status.SessionID = sessionID
		agent.Status.EventPath = eventPath
		agent.Metadata.UpdatedAt = time.Now()
		updated, err := json.Marshal(&agent)
		if err != nil {
			return fmt.Errorf("store: marshal agent %s/%s: %w", workspace, name, err)
		}
		if err := wb.Put(key, updated); err != nil {
			return fmt.Errorf("store: store agent %s/%s: %w", workspace, name, err)
		}
		return nil
	})
}

// TransitionAgentRunState updates only Status.Status when the current state
// matches expected. It preserves run metadata, error text, and all other fields.
// Returns false, nil when the agent exists but is not in the expected state.
func (s *Store) TransitionAgentRunState(_ context.Context, workspace, name string, expected, next apiruntime.Status) (bool, error) {
	if workspace == "" {
		return false, fmt.Errorf("store: workspace is required")
	}
	if name == "" {
		return false, fmt.Errorf("store: agent name is required")
	}

	var transitioned bool
	err := s.db.Update(func(tx *bolt.Tx) error {
		wb, err := workspaceBucket(tx, workspace)
		if err != nil {
			return err
		}
		key := []byte(name)
		data := wb.Get(key)
		if data == nil {
			return &ResourceError{Op: "transition", Resource: "agent", Key: workspace + "/" + name, Err: ErrNotFound}
		}
		var agent pkgariapi.AgentRun
		if err := json.Unmarshal(data, &agent); err != nil {
			return fmt.Errorf("store: unmarshal agent %s/%s: %w", workspace, name, err)
		}
		if agent.Status.Status != expected {
			return nil
		}
		agent.Status.Status = next
		agent.Metadata.UpdatedAt = time.Now()
		updated, err := json.Marshal(&agent)
		if err != nil {
			return fmt.Errorf("store: marshal agent %s/%s: %w", workspace, name, err)
		}
		if err := wb.Put(key, updated); err != nil {
			return fmt.Errorf("store: store agent %s/%s: %w", workspace, name, err)
		}
		transitioned = true
		s.logger.Debug("agentRun state transitioned",
			"workspace", workspace,
			"name", name,
			"from", expected,
			"to", next)
		return nil
	})
	if err != nil {
		return false, err
	}
	return transitioned, nil
}

// DeleteAgentRun removes the identified agent run.
// Returns an error if the agent run does not exist.
func (s *Store) DeleteAgentRun(_ context.Context, workspace, name string) error {
	if workspace == "" {
		return fmt.Errorf("store: workspace is required")
	}
	if name == "" {
		return fmt.Errorf("store: agent name is required")
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		wb, err := workspaceBucket(tx, workspace)
		if err != nil {
			return err
		}
		key := []byte(name)
		if wb.Get(key) == nil {
			return &ResourceError{Op: "delete", Resource: "agent", Key: workspace + "/" + name, Err: ErrNotFound}
		}
		if err := wb.Delete(key); err != nil {
			return fmt.Errorf("store: delete agent %s/%s: %w", workspace, name, err)
		}
		s.logger.Debug("agentRun deleted", "workspace", workspace, "name", name)
		return nil
	})
}
