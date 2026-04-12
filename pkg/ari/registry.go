// Package ari implements the Agent Runtime Interface (ARI) JSON-RPC server.
// This file defines the Registry for tracking workspace metadata.
package ari

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/zoumo/oar/api/meta"
	"github.com/zoumo/oar/pkg/store"
	"github.com/zoumo/oar/pkg/workspace"
)

// WorkspaceMeta tracks metadata for a prepared workspace.
// It is stored in the Registry and used for workspace/list responses.
type WorkspaceMeta struct {
	// Id is the unique identifier assigned to this workspace (name in new model).
	Id string

	// Name is the workspace name from spec.Metadata.Name.
	Name string

	// Path is the absolute path to the prepared workspace directory.
	Path string

	// Spec is the original WorkspaceSpec used to prepare this workspace.
	Spec workspace.WorkspaceSpec

	// Status is the current workspace state (e.g., "ready", "preparing", "error").
	Status string

	// RefCount is the number of active agents referencing this workspace.
	// Cleanup fails if RefCount > 0.
	RefCount int

	// Refs is the list of agent keys referencing this workspace.
	// Used for debugging and workspace/list response.
	Refs []string
}

// Registry tracks workspace metadata for the ARI server.
// It provides thread-safe access to workspaceId → WorkspaceMeta mapping.
type Registry struct {
	// mu protects the workspaces map.
	mu sync.RWMutex

	// workspaces maps workspaceId (name) to WorkspaceMeta.
	workspaces map[string]*WorkspaceMeta
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		workspaces: make(map[string]*WorkspaceMeta),
	}
}

// Add registers a new workspace with its metadata.
// Thread-safe via mutex lock.
func (r *Registry) Add(id, name, path string, spec workspace.WorkspaceSpec) {
	r.mu.Lock()
	r.workspaces[id] = &WorkspaceMeta{
		Id:       id,
		Name:     name,
		Path:     path,
		Spec:     spec,
		Status:   "ready",
		RefCount: 0,
		Refs:     []string{},
	}
	r.mu.Unlock()
}

// Get retrieves workspace metadata by ID.
// Returns nil if workspaceId not found.
// Thread-safe via mutex read lock.
func (r *Registry) Get(id string) *WorkspaceMeta {
	r.mu.RLock()
	wsMeta := r.workspaces[id]
	r.mu.RUnlock()
	return wsMeta
}

// List returns all registered workspace metadata.
// Returns a slice of WorkspaceMeta (not pointers) for safe access.
// Thread-safe via mutex read lock.
func (r *Registry) List() []WorkspaceMeta {
	r.mu.RLock()
	list := make([]WorkspaceMeta, 0, len(r.workspaces))
	for _, wsMeta := range r.workspaces {
		list = append(list, *wsMeta)
	}
	r.mu.RUnlock()
	return list
}

// Remove deletes a workspace from the registry.
// Thread-safe via mutex lock.
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	delete(r.workspaces, id)
	r.mu.Unlock()
}

// Acquire increments the reference count for a workspace.
// Adds the agentKey to the Refs list for debugging.
// Thread-safe via mutex lock.
func (r *Registry) Acquire(id, agentKey string) {
	r.mu.Lock()
	if m := r.workspaces[id]; m != nil {
		m.RefCount++
		m.Refs = append(m.Refs, agentKey)
	}
	r.mu.Unlock()
}

// Release decrements the reference count for a workspace.
// Removes the agentKey from the Refs list.
// Returns the reference count after decrement.
// Thread-safe via mutex lock.
func (r *Registry) Release(id, agentKey string) int {
	r.mu.Lock()
	count := 0
	if m := r.workspaces[id]; m != nil && m.RefCount > 0 {
		m.RefCount--
		count = m.RefCount
		// Remove agentKey from Refs list.
		newRefs := make([]string, 0, len(m.Refs))
		for _, ref := range m.Refs {
			if ref != agentKey {
				newRefs = append(newRefs, ref)
			}
		}
		m.Refs = newRefs
	}
	r.mu.Unlock()
	return count
}

// RebuildFromDB loads all ready workspaces from the metadata store and
// repopulates the registry. This is called once during daemon startup after
// recovery completes so that workspace/list and workspace/cleanup work across
// daemon restarts.
func (r *Registry) RebuildFromDB(s *store.Store) error {
	ctx := context.Background()

	workspaces, err := s.ListWorkspaces(ctx, &meta.WorkspaceFilter{
		Phase: meta.WorkspacePhaseReady,
	})
	if err != nil {
		return fmt.Errorf("ari: rebuild registry: list workspaces: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, ws := range workspaces {
		// Deserialize Source JSON into workspace.Source.
		var src workspace.Source
		if ws.Spec.Source != nil && len(ws.Spec.Source) > 0 && string(ws.Spec.Source) != "{}" {
			if err := json.Unmarshal(ws.Spec.Source, &src); err != nil {
				return fmt.Errorf("ari: rebuild registry: unmarshal source for workspace %s: %w",
					ws.Metadata.Name, err)
			}
		}

		name := ws.Metadata.Name
		path := ws.Status.Path

		spec := workspace.WorkspaceSpec{
			Metadata: workspace.WorkspaceMetadata{Name: name},
			Source:   src,
		}

		// In the new model, refs are tracked in-memory only; start with empty refs.
		r.workspaces[name] = &WorkspaceMeta{
			Id:       name,
			Name:     name,
			Path:     path,
			Spec:     spec,
			Status:   "ready",
			RefCount: 0,
			Refs:     []string{},
		}
	}

	return nil
}
