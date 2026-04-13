// Package store provides metadata persistence for OAR agent and workspace records.
// This file defines Workspace CRUD methods.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"

	apiari "github.com/zoumo/oar/api/ari"
)

// CreateWorkspace stores a new Workspace record.
// Returns an error if a workspace with the same name already exists.
func (s *Store) CreateWorkspace(_ context.Context, ws *apiari.Workspace) error {
	if ws.Metadata.Name == "" {
		return fmt.Errorf("store: workspace name is required")
	}

	now := time.Now()
	if ws.Metadata.CreatedAt.IsZero() {
		ws.Metadata.CreatedAt = now
	}
	if ws.Metadata.UpdatedAt.IsZero() {
		ws.Metadata.UpdatedAt = ws.Metadata.CreatedAt
	}
	if ws.Status.Phase == "" {
		ws.Status.Phase = apiari.WorkspacePhasePending
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		b := workspacesBucket(tx)
		key := []byte(ws.Metadata.Name)
		if existing := b.Get(key); existing != nil {
			return fmt.Errorf("store: workspace %s already exists", ws.Metadata.Name)
		}
		data, err := json.Marshal(ws)
		if err != nil {
			return fmt.Errorf("store: marshal workspace %s: %w", ws.Metadata.Name, err)
		}
		if err := b.Put(key, data); err != nil {
			return fmt.Errorf("store: store workspace %s: %w", ws.Metadata.Name, err)
		}
		s.logger.Debug("workspace created", "name", ws.Metadata.Name)
		return nil
	})
}

// GetWorkspace retrieves a workspace by name.
// Returns nil, nil if the workspace does not exist.
func (s *Store) GetWorkspace(_ context.Context, name string) (*apiari.Workspace, error) {
	if name == "" {
		return nil, fmt.Errorf("store: workspace name is required")
	}

	var ws *apiari.Workspace
	err := s.db.View(func(tx *bolt.Tx) error {
		data := workspacesBucket(tx).Get([]byte(name))
		if data == nil {
			return nil // not found
		}
		ws = &apiari.Workspace{}
		return json.Unmarshal(data, ws)
	})
	if err != nil {
		return nil, fmt.Errorf("store: get workspace %s: %w", name, err)
	}
	return ws, nil
}

// ListWorkspaces returns all workspaces matching the optional filter.
// If filter is nil every workspace is returned.
func (s *Store) ListWorkspaces(_ context.Context, filter *apiari.WorkspaceFilter) ([]*apiari.Workspace, error) {
	var result []*apiari.Workspace

	err := s.db.View(func(tx *bolt.Tx) error {
		return workspacesBucket(tx).ForEach(func(_, v []byte) error {
			if v == nil {
				return nil // skip nested buckets (none expected here)
			}
			var ws apiari.Workspace
			if err := json.Unmarshal(v, &ws); err != nil {
				s.logger.Error("skipping corrupt workspace record", "error", err)
				return nil
			}
			if filter != nil && filter.Phase != "" && ws.Status.Phase != filter.Phase {
				return nil
			}
			copy := ws
			result = append(result, &copy)
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("store: list workspaces: %w", err)
	}
	return result, nil
}

// UpdateWorkspaceStatus overwrites the Status field of the named workspace.
// Returns an error if the workspace does not exist.
func (s *Store) UpdateWorkspaceStatus(_ context.Context, name string, status apiari.WorkspaceStatus) error {
	if name == "" {
		return fmt.Errorf("store: workspace name is required")
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		b := workspacesBucket(tx)
		key := []byte(name)
		data := b.Get(key)
		if data == nil {
			return fmt.Errorf("store: workspace %s does not exist", name)
		}
		var ws apiari.Workspace
		if err := json.Unmarshal(data, &ws); err != nil {
			return fmt.Errorf("store: unmarshal workspace %s: %w", name, err)
		}
		ws.Status = status
		ws.Metadata.UpdatedAt = time.Now()
		updated, err := json.Marshal(&ws)
		if err != nil {
			return fmt.Errorf("store: marshal workspace %s: %w", name, err)
		}
		if err := b.Put(key, updated); err != nil {
			return fmt.Errorf("store: store workspace %s: %w", name, err)
		}
		s.logger.Debug("workspace status updated", "name", name, "phase", status.Phase)
		return nil
	})
}

// DeleteWorkspace removes the named workspace.
// Returns an error if:
//   - the workspace does not exist.
//   - the workspace still has agents (scan v1/agents/{name} bucket).
func (s *Store) DeleteWorkspace(_ context.Context, name string) error {
	if name == "" {
		return fmt.Errorf("store: workspace name is required")
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		// Check workspace exists.
		b := workspacesBucket(tx)
		if b.Get([]byte(name)) == nil {
			return fmt.Errorf("store: workspace %s does not exist", name)
		}

		// Refuse deletion if agentRuns sub-bucket is non-empty.
		if wb := agentRunsBucket(tx).Bucket([]byte(name)); wb != nil {
			count := 0
			_ = wb.ForEach(func(k, v []byte) error {
				if v != nil {
					count++
				}
				return nil
			})
			if count > 0 {
				return fmt.Errorf("store: workspace %s has %d agent(s) and cannot be deleted", name, count)
			}
		}

		if err := b.Delete([]byte(name)); err != nil {
			return fmt.Errorf("store: delete workspace %s: %w", name, err)
		}
		s.logger.Debug("workspace deleted", "name", name)
		return nil
	})
}
