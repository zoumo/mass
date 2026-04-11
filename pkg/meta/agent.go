// Package meta provides metadata storage for OAR agent and workspace records.
package meta

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/open-agent-d/open-agent-d/pkg/spec"
)

// CreateAgentRun stores a new AgentRun record.
// The agent run identity is (Metadata.Workspace, Metadata.Name).
// Returns an error if an agent run with the same (workspace, name) already exists.
func (s *Store) CreateAgentRun(_ context.Context, agent *AgentRun) error {
	if agent.Metadata.Workspace == "" {
		return fmt.Errorf("meta: agent workspace is required")
	}
	if agent.Metadata.Name == "" {
		return fmt.Errorf("meta: agent name is required")
	}
	if agent.Spec.RuntimeClass == "" {
		return fmt.Errorf("meta: agent runtime class is required")
	}

	now := time.Now()
	if agent.Metadata.CreatedAt.IsZero() {
		agent.Metadata.CreatedAt = now
	}
	if agent.Metadata.UpdatedAt.IsZero() {
		agent.Metadata.UpdatedAt = agent.Metadata.CreatedAt
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		wb, err := workspaceBucket(tx, agent.Metadata.Workspace)
		if err != nil {
			return err
		}
		key := []byte(agent.Metadata.Name)
		if existing := wb.Get(key); existing != nil {
			return fmt.Errorf("meta: agent %s/%s already exists",
				agent.Metadata.Workspace, agent.Metadata.Name)
		}
		data, err := json.Marshal(agent)
		if err != nil {
			return fmt.Errorf("meta: marshal agent %s/%s: %w",
				agent.Metadata.Workspace, agent.Metadata.Name, err)
		}
		if err := wb.Put(key, data); err != nil {
			return fmt.Errorf("meta: store agent %s/%s: %w",
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
func (s *Store) GetAgentRun(_ context.Context, workspace, name string) (*AgentRun, error) {
	if workspace == "" {
		return nil, fmt.Errorf("meta: workspace is required")
	}
	if name == "" {
		return nil, fmt.Errorf("meta: agent name is required")
	}

	var agent *AgentRun
	err := s.db.View(func(tx *bolt.Tx) error {
		wb := workspaceBucketRO(tx, workspace)
		if wb == nil {
			return nil // workspace sub-bucket not found → agent doesn't exist
		}
		data := wb.Get([]byte(name))
		if data == nil {
			return nil // not found
		}
		agent = &AgentRun{}
		return json.Unmarshal(data, agent)
	})
	if err != nil {
		return nil, fmt.Errorf("meta: get agent %s/%s: %w", workspace, name, err)
	}
	return agent, nil
}

// ListAgentRuns returns all agent runs matching the optional filter.
//
//   - If filter.Workspace is non-empty, only that workspace's sub-bucket is scanned.
//   - If filter.State is non-empty, only agent runs with that state are returned.
//   - If filter is nil every agent run in every workspace is returned.
func (s *Store) ListAgentRuns(_ context.Context, filter *AgentRunFilter) ([]*AgentRun, error) {
	var result []*AgentRun

	err := s.db.View(func(tx *bolt.Tx) error {
		runs := agentRunsBucket(tx)

		// scanWorkspace iterates a single workspace sub-bucket and appends matching agent runs.
		scanWorkspace := func(wb *bolt.Bucket) error {
			return wb.ForEach(func(_, v []byte) error {
				if v == nil {
					return nil // skip nested buckets
				}
				var a AgentRun
				if err := json.Unmarshal(v, &a); err != nil {
					s.logger.Error("skipping corrupt agentRun record", "error", err)
					return nil
				}
				if filter != nil && filter.State != "" && a.Status.State != filter.State {
					return nil
				}
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
		return nil, fmt.Errorf("meta: list agentRuns: %w", err)
	}
	return result, nil
}

// UpdateAgentRunStatus overwrites the Status field of the identified agent run.
// Returns an error if the agent run does not exist.
func (s *Store) UpdateAgentRunStatus(_ context.Context, workspace, name string, status AgentRunStatus) error {
	if workspace == "" {
		return fmt.Errorf("meta: workspace is required")
	}
	if name == "" {
		return fmt.Errorf("meta: agent name is required")
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		wb, err := workspaceBucket(tx, workspace)
		if err != nil {
			return err
		}
		key := []byte(name)
		data := wb.Get(key)
		if data == nil {
			return fmt.Errorf("meta: agent %s/%s does not exist", workspace, name)
		}
		var agent AgentRun
		if err := json.Unmarshal(data, &agent); err != nil {
			return fmt.Errorf("meta: unmarshal agent %s/%s: %w", workspace, name, err)
		}
		agent.Status = status
		agent.Metadata.UpdatedAt = time.Now()
		updated, err := json.Marshal(&agent)
		if err != nil {
			return fmt.Errorf("meta: marshal agent %s/%s: %w", workspace, name, err)
		}
		if err := wb.Put(key, updated); err != nil {
			return fmt.Errorf("meta: store agent %s/%s: %w", workspace, name, err)
		}
		s.logger.Debug("agentRun status updated",
			"workspace", workspace,
			"name", name,
			"state", status.State)
		return nil
	})
}

// TransitionAgentRunState updates only Status.State when the current state
// matches expected. It preserves shim metadata, error text, and bootstrap data.
// Returns false, nil when the agent exists but is not in the expected state.
func (s *Store) TransitionAgentRunState(_ context.Context, workspace, name string, expected, next spec.Status) (bool, error) {
	if workspace == "" {
		return false, fmt.Errorf("meta: workspace is required")
	}
	if name == "" {
		return false, fmt.Errorf("meta: agent name is required")
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
			return fmt.Errorf("meta: agent %s/%s does not exist", workspace, name)
		}
		var agent AgentRun
		if err := json.Unmarshal(data, &agent); err != nil {
			return fmt.Errorf("meta: unmarshal agent %s/%s: %w", workspace, name, err)
		}
		if agent.Status.State != expected {
			return nil
		}
		agent.Status.State = next
		agent.Metadata.UpdatedAt = time.Now()
		updated, err := json.Marshal(&agent)
		if err != nil {
			return fmt.Errorf("meta: marshal agent %s/%s: %w", workspace, name, err)
		}
		if err := wb.Put(key, updated); err != nil {
			return fmt.Errorf("meta: store agent %s/%s: %w", workspace, name, err)
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
		return fmt.Errorf("meta: workspace is required")
	}
	if name == "" {
		return fmt.Errorf("meta: agent name is required")
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		wb, err := workspaceBucket(tx, workspace)
		if err != nil {
			return err
		}
		key := []byte(name)
		if wb.Get(key) == nil {
			return fmt.Errorf("meta: agent %s/%s does not exist", workspace, name)
		}
		if err := wb.Delete(key); err != nil {
			return fmt.Errorf("meta: delete agent %s/%s: %w", workspace, name, err)
		}
		s.logger.Debug("agentRun deleted", "workspace", workspace, "name", name)
		return nil
	})
}
