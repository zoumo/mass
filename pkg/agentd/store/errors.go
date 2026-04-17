// Package store implements the bbolt-backed storage layer for agentd.
// This file defines typed errors for store operations.
package store

import "errors"

// Sentinel errors for store operations.
var (
	// ErrAlreadyExists indicates the resource already exists.
	ErrAlreadyExists = errors.New("store: already exists")
	// ErrNotFound indicates the resource does not exist.
	ErrNotFound = errors.New("store: not found")
)

// ResourceError is a structured error for store operations.
// It wraps a sentinel (ErrAlreadyExists / ErrNotFound) with resource context.
// Use errors.Is(err, store.ErrAlreadyExists) or errors.Is(err, store.ErrNotFound)
// to check the error type.
type ResourceError struct {
	Op       string // "create", "update", "delete", "transition"
	Resource string // "workspace", "agent"
	Key      string // "ws-name" or "workspace/agent-name"
	Err      error  // sentinel (ErrAlreadyExists / ErrNotFound)
}

// Error implements the error interface.
func (e *ResourceError) Error() string {
	return e.Op + " " + e.Resource + " " + e.Key + ": " + e.Err.Error()
}

// Unwrap returns the underlying sentinel for errors.Is/errors.As.
func (e *ResourceError) Unwrap() error { return e.Err }
