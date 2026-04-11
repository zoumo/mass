// Package meta provides metadata storage for OAR agent and workspace records.
// This file defines the AgentTemplate entity and its CRUD methods on *Store.
package meta

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/open-agent-d/open-agent-d/pkg/spec"
)

// ────────────────────────────────────────────────────────────────────────────
// AgentTemplate model
// ────────────────────────────────────────────────────────────────────────────

// AgentTemplateSpec describes how to launch an agent process for this template.
type AgentTemplateSpec struct {
	// Command is the ACP agent executable.
	Command string `json:"command"`

	// Args are the command-line arguments passed to Command.
	Args []string `json:"args,omitempty"`

	// Env is the list of environment variable overrides applied to the process.
	Env []spec.EnvVar `json:"env,omitempty"`

	// StartupTimeoutSeconds is the maximum time (in seconds) to wait for the
	// agent shim to reach idle state. Nil means use the daemon default.
	StartupTimeoutSeconds *int `json:"startupTimeoutSeconds,omitempty"`
}

// AgentTemplate represents an agent template entity record.
// Identity is Metadata.Name — globally unique across all agent templates.
type AgentTemplate struct {
	// Metadata holds identity and lifecycle fields.
	Metadata ObjectMeta `json:"metadata"`

	// Spec describes the desired launch configuration.
	Spec AgentTemplateSpec `json:"spec"`
}

// ────────────────────────────────────────────────────────────────────────────
// CRUD methods
// ────────────────────────────────────────────────────────────────────────────

// agentTemplatesBucket returns the v1/agents bucket from the given transaction.
func agentTemplatesBucket(tx *bolt.Tx) *bolt.Bucket {
	return tx.Bucket(bucketV1).Bucket(bucketAgentTemplates)
}

// SetAgentTemplate upserts an AgentTemplate record keyed by Metadata.Name.
// On first write, CreatedAt is set to now. On every write, UpdatedAt is set to now.
func (s *Store) SetAgentTemplate(_ context.Context, rt *AgentTemplate) error {
	if rt.Metadata.Name == "" {
		return fmt.Errorf("meta: agentTemplate name is required")
	}

	now := time.Now()

	return s.db.Update(func(tx *bolt.Tx) error {
		rb := agentTemplatesBucket(tx)
		if rb == nil {
			return fmt.Errorf("meta: v1/agents bucket not found")
		}
		key := []byte(rt.Metadata.Name)

		// Preserve CreatedAt from the existing record on upsert.
		if existing := rb.Get(key); existing != nil {
			var prev AgentTemplate
			if err := json.Unmarshal(existing, &prev); err == nil {
				if !prev.Metadata.CreatedAt.IsZero() {
					rt.Metadata.CreatedAt = prev.Metadata.CreatedAt
				}
			}
		}

		if rt.Metadata.CreatedAt.IsZero() {
			rt.Metadata.CreatedAt = now
		}
		rt.Metadata.UpdatedAt = now

		data, err := json.Marshal(rt)
		if err != nil {
			return fmt.Errorf("meta: marshal agentTemplate %s: %w", rt.Metadata.Name, err)
		}
		if err := rb.Put(key, data); err != nil {
			return fmt.Errorf("meta: store agentTemplate %s: %w", rt.Metadata.Name, err)
		}
		s.logger.Info("agentTemplate set", "name", rt.Metadata.Name)
		return nil
	})
}

// GetAgentTemplate retrieves an AgentTemplate by name.
// Returns nil, nil if the agent template does not exist.
func (s *Store) GetAgentTemplate(_ context.Context, name string) (*AgentTemplate, error) {
	if name == "" {
		return nil, fmt.Errorf("meta: agentTemplate name is required")
	}

	var rt *AgentTemplate
	err := s.db.View(func(tx *bolt.Tx) error {
		rb := agentTemplatesBucket(tx)
		if rb == nil {
			return nil // bucket absent → not found
		}
		data := rb.Get([]byte(name))
		if data == nil {
			return nil // not found
		}
		rt = &AgentTemplate{}
		return json.Unmarshal(data, rt)
	})
	if err != nil {
		return nil, fmt.Errorf("meta: get agentTemplate %s: %w", name, err)
	}
	return rt, nil
}

// ListAgentTemplates returns all AgentTemplate records stored in v1/agents.
// Returns an empty (non-nil) slice when no agent templates are stored.
func (s *Store) ListAgentTemplates(_ context.Context) ([]*AgentTemplate, error) {
	var result []*AgentTemplate

	err := s.db.View(func(tx *bolt.Tx) error {
		rb := agentTemplatesBucket(tx)
		if rb == nil {
			return nil // bucket absent → empty list
		}
		return rb.ForEach(func(_, v []byte) error {
			if v == nil {
				return nil // skip nested buckets
			}
			var rt AgentTemplate
			if err := json.Unmarshal(v, &rt); err != nil {
				s.logger.Error("skipping corrupt agentTemplate record", "error", err)
				return nil
			}
			copy := rt
			result = append(result, &copy)
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("meta: list agentTemplates: %w", err)
	}
	if result == nil {
		result = []*AgentTemplate{}
	}
	return result, nil
}

// DeleteAgentTemplate removes the identified AgentTemplate.
// No-op if the agent template does not exist.
func (s *Store) DeleteAgentTemplate(_ context.Context, name string) error {
	if name == "" {
		return fmt.Errorf("meta: agentTemplate name is required")
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		rb := agentTemplatesBucket(tx)
		if rb == nil {
			return nil // bucket absent → no-op
		}
		key := []byte(name)
		if rb.Get(key) == nil {
			return nil // not found → no-op
		}
		if err := rb.Delete(key); err != nil {
			return fmt.Errorf("meta: delete agentTemplate %s: %w", name, err)
		}
		s.logger.Info("agentTemplate deleted", "name", name)
		return nil
	})
}
