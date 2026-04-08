// Package agentd implements the agent daemon that manages agent runtime sessions.
// This file defines the RuntimeClass registry for resolving runtime class names to launch configurations.
package agentd

import (
	"fmt"
	"os"
	"sync"
)

// Capabilities defines runtime capabilities for a RuntimeClass.
type Capabilities struct {
	// Streaming enables real-time output streaming.
	Streaming bool

	// SessionLoad enables session load/restore capability.
	SessionLoad bool

	// ConcurrentSessions is the max concurrent sessions per runtime class.
	ConcurrentSessions int


}

// RuntimeClass represents a resolved runtime class configuration.
type RuntimeClass struct {
	// Name is the runtime class identifier.
	Name string

	// Command is the executable command for this runtime class.
	Command string

	// Args are command-line arguments for the runtime class.
	Args []string

	// Env is the resolved environment variables (after ${VAR} substitution).
	Env map[string]string

	// Capabilities defines runtime capabilities.
	Capabilities Capabilities
}

// RuntimeClassRegistry resolves runtime class names to launch configurations.
// Thread-safe via RWMutex for concurrent Get/List operations.
type RuntimeClassRegistry struct {
	mu     sync.RWMutex
	classes map[string]*RuntimeClass
}

// NewRuntimeClassRegistry creates a new registry from RuntimeClassConfig mappings.
// Performs validation, applies defaults, and resolves ${VAR} environment substitution.
// Returns error if any RuntimeClassConfig has missing Command.
func NewRuntimeClassRegistry(configs map[string]RuntimeClassConfig) (*RuntimeClassRegistry, error) {
	classes := make(map[string]*RuntimeClass)

	for name, cfg := range configs {
		// Validate: Command is required.
		if cfg.Command == "" {
			return nil, fmt.Errorf("runtime class %s: command is required", name)
		}

		// Apply Capabilities defaults.
		streaming := cfg.Capabilities.Streaming
		if !streaming {
			// Default Streaming to true when not explicitly set.
			// Note: yaml unmarshaling leaves bools as false when omitted,
			// so we can't distinguish "explicitly false" from "not set".
			// Per plan spec, default is true.
			streaming = true
		}

		sessionLoad := cfg.Capabilities.SessionLoad // Default false is correct.

		concurrentSessions := cfg.Capabilities.ConcurrentSessions
		if concurrentSessions == 0 {
			// Default ConcurrentSessions to 1 when not set.
			concurrentSessions = 1
		}

		// Resolve ${VAR} environment substitution.
		env := make(map[string]string)
		for key, value := range cfg.Env {
			// os.Expand resolves ${VAR} patterns using os.Getenv.
			env[key] = os.Expand(value, os.Getenv)
		}

		classes[name] = &RuntimeClass{
			Name:     name,
			Command:  cfg.Command,
			Args:     cfg.Args,
			Env:      env,
			Capabilities: Capabilities{
				Streaming:          streaming,
				SessionLoad:        sessionLoad,
				ConcurrentSessions: concurrentSessions,
			},
		}
	}

	return &RuntimeClassRegistry{classes: classes}, nil
}

// Get returns the RuntimeClass for the given name, or error if not found.
// Thread-safe via RLock/RUnlock pattern.
func (r *RuntimeClassRegistry) Get(name string) (*RuntimeClass, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	class, ok := r.classes[name]
	if !ok {
		return nil, fmt.Errorf("runtime class not found: %s", name)
	}
	return class, nil
}

// List returns all registered RuntimeClasses as a slice.
// Thread-safe via RLock/RUnlock pattern.
func (r *RuntimeClassRegistry) List() []*RuntimeClass {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*RuntimeClass, 0, len(r.classes))
	for _, class := range r.classes {
		result = append(result, class)
	}
	return result
}