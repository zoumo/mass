// Package ari implements the Agent Runtime Interface (ARI) JSON-RPC server.
// This file defines the Registry for tracking workspace metadata.
package ari

import (
	"sync"

	"github.com/open-agent-d/open-agent-d/pkg/workspace"
)

// WorkspaceMeta tracks metadata for a prepared workspace.
// It is stored in the Registry and used for workspace/list responses.
type WorkspaceMeta struct {
	// Id is the unique UUID assigned to this workspace.
	Id string

	// Name is the workspace name from spec.Metadata.Name.
	Name string

	// Path is the absolute path to the prepared workspace directory.
	Path string

	// Spec is the original WorkspaceSpec used to prepare this workspace.
	Spec workspace.WorkspaceSpec

	// Status is the current workspace state (e.g., "ready", "preparing", "error").
	Status string

	// RefCount is the number of active sessions referencing this workspace.
	// Cleanup fails if RefCount > 0.
	RefCount int

	// Refs is the list of session IDs referencing this workspace.
	// Used for debugging and workspace/list response.
	Refs []string
}

// Registry tracks workspace metadata for the ARI server.
// It provides thread-safe access to workspaceId → WorkspaceMeta mapping.
type Registry struct {
	// mu protects the workspaces map.
	mu sync.RWMutex

	// workspaces maps workspaceId (UUID) to WorkspaceMeta.
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
		Id:        id,
		Name:      name,
		Path:      path,
		Spec:      spec,
		Status:    "ready",
		RefCount:  0,
		Refs:      []string{},
	}
	r.mu.Unlock()
}

// Get retrieves workspace metadata by ID.
// Returns nil if workspaceId not found.
// Thread-safe via mutex read lock.
func (r *Registry) Get(id string) *WorkspaceMeta {
	r.mu.RLock()
	meta := r.workspaces[id]
	r.mu.RUnlock()
	return meta
}

// List returns all registered workspace metadata.
// Returns a slice of WorkspaceMeta (not pointers) for safe access.
// Thread-safe via mutex read lock.
func (r *Registry) List() []WorkspaceMeta {
	r.mu.RLock()
	list := make([]WorkspaceMeta, 0, len(r.workspaces))
	for _, meta := range r.workspaces {
		list = append(list, *meta)
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
// Adds the session ID to the Refs list for debugging.
// Thread-safe via mutex lock.
func (r *Registry) Acquire(id string, sessionID string) {
	r.mu.Lock()
	if meta := r.workspaces[id]; meta != nil {
		meta.RefCount++
		meta.Refs = append(meta.Refs, sessionID)
	}
	r.mu.Unlock()
}

// Release decrements the reference count for a workspace.
// Removes the session ID from the Refs list.
// Returns the reference count after decrement.
// Thread-safe via mutex lock.
func (r *Registry) Release(id string, sessionID string) int {
	r.mu.Lock()
	count := 0
	if meta := r.workspaces[id]; meta != nil && meta.RefCount > 0 {
		meta.RefCount--
		count = meta.RefCount
		// Remove sessionID from Refs list.
		newRefs := make([]string, 0, len(meta.Refs))
		for _, ref := range meta.Refs {
			if ref != sessionID {
				newRefs = append(newRefs, ref)
			}
		}
		meta.Refs = newRefs
	}
	r.mu.Unlock()
	return count
}