// Package agentd implements the agent daemon that manages agent runtime sessions.
// This file defines the RuntimeClass type and its constructor from a meta.Runtime entity.
package agentd

import (
	"github.com/open-agent-d/open-agent-d/pkg/meta"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
)

// RuntimeClass represents a resolved runtime class configuration.
type RuntimeClass struct {
	// Name is the runtime class identifier.
	Name string

	// Command is the executable command for this runtime class.
	Command string

	// Args are command-line arguments for the runtime class.
	Args []string

	// Env is the list of environment variables for the runtime class.
	Env []spec.EnvVar
}

// NewRuntimeClassFromMeta constructs a RuntimeClass from a meta.Runtime record.
func NewRuntimeClassFromMeta(r *meta.Runtime) *RuntimeClass {
	return &RuntimeClass{
		Name:    r.Metadata.Name,
		Command: r.Spec.Command,
		Args:    r.Spec.Args,
		Env:     r.Spec.Env,
	}
}
