// Package store provides metadata persistence for OAR agent and workspace records.
// This file defines Agent CRUD methods (agent definition / named runtime configuration).
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"

	pkgariapi "github.com/zoumo/oar/pkg/ari/api"
)

// agentsBucket returns the v1/agents bucket from the given transaction.
func agentsBucket(tx *bolt.Tx) *bolt.Bucket {
	return tx.Bucket(bucketV1).Bucket(bucketAgents)
}

// SetAgent upserts an Agent record keyed by Metadata.Name.
// On first write, CreatedAt is set to now. On every write, UpdatedAt is set to now.
func (s *Store) SetAgent(_ context.Context, ag *pkgariapi.Agent) error {
	if ag.Metadata.Name == "" {
		return fmt.Errorf("store: agent name is required")
	}

	now := time.Now()

	return s.db.Update(func(tx *bolt.Tx) error {
		rb := agentsBucket(tx)
		if rb == nil {
			return fmt.Errorf("store: v1/agents bucket not found")
		}
		key := []byte(ag.Metadata.Name)

		// Preserve CreatedAt from the existing record on upsert.
		if existing := rb.Get(key); existing != nil {
			var prev pkgariapi.Agent
			if err := json.Unmarshal(existing, &prev); err == nil {
				if !prev.Metadata.CreatedAt.IsZero() {
					ag.Metadata.CreatedAt = prev.Metadata.CreatedAt
				}
			}
		}

		if ag.Metadata.CreatedAt.IsZero() {
			ag.Metadata.CreatedAt = now
		}
		ag.Metadata.UpdatedAt = now

		data, err := json.Marshal(ag)
		if err != nil {
			return fmt.Errorf("store: marshal agent %s: %w", ag.Metadata.Name, err)
		}
		if err := rb.Put(key, data); err != nil {
			return fmt.Errorf("store: store agent %s: %w", ag.Metadata.Name, err)
		}
		s.logger.Info("agent set", "name", ag.Metadata.Name)
		return nil
	})
}

// GetAgent retrieves an Agent by name.
// Returns nil, nil if the agent does not exist.
func (s *Store) GetAgent(_ context.Context, name string) (*pkgariapi.Agent, error) {
	if name == "" {
		return nil, fmt.Errorf("store: agent name is required")
	}

	var ag *pkgariapi.Agent
	err := s.db.View(func(tx *bolt.Tx) error {
		rb := agentsBucket(tx)
		if rb == nil {
			return nil // bucket absent → not found
		}
		data := rb.Get([]byte(name))
		if data == nil {
			return nil // not found
		}
		ag = &pkgariapi.Agent{}
		return json.Unmarshal(data, ag)
	})
	if err != nil {
		return nil, fmt.Errorf("store: get agent %s: %w", name, err)
	}
	return ag, nil
}

// ListAgents returns all Agent records stored in v1/agents.
// Returns an empty (non-nil) slice when no agents are stored.
func (s *Store) ListAgents(_ context.Context) ([]*pkgariapi.Agent, error) {
	var result []*pkgariapi.Agent

	err := s.db.View(func(tx *bolt.Tx) error {
		rb := agentsBucket(tx)
		if rb == nil {
			return nil // bucket absent → empty list
		}
		return rb.ForEach(func(_, v []byte) error {
			if v == nil {
				return nil // skip nested buckets
			}
			var ag pkgariapi.Agent
			if err := json.Unmarshal(v, &ag); err != nil {
				s.logger.Error("skipping corrupt agent record", "error", err)
				return nil
			}
			copy := ag
			result = append(result, &copy)
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("store: list agents: %w", err)
	}
	if result == nil {
		result = []*pkgariapi.Agent{}
	}
	return result, nil
}

// DeleteAgent removes the identified Agent.
// No-op if the agent does not exist.
func (s *Store) DeleteAgent(_ context.Context, name string) error {
	if name == "" {
		return fmt.Errorf("store: agent name is required")
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		rb := agentsBucket(tx)
		if rb == nil {
			return nil // bucket absent → no-op
		}
		key := []byte(name)
		if rb.Get(key) == nil {
			return nil // not found → no-op
		}
		if err := rb.Delete(key); err != nil {
			return fmt.Errorf("store: delete agent %s: %w", name, err)
		}
		s.logger.Info("agent deleted", "name", name)
		return nil
	})
}
