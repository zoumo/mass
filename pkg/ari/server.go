// Package ari implements the Agent Runtime Interface (ARI) JSON-RPC server.
// This file is a minimal compilable stub; the full handler implementation
// will be written in S03.
// TODO(S03): full handler implementation
package ari

import (
	"context"

	"github.com/open-agent-d/open-agent-d/pkg/agentd"
	"github.com/open-agent-d/open-agent-d/pkg/meta"
	"github.com/open-agent-d/open-agent-d/pkg/workspace"
)

// Server is a JSON-RPC 2.0 server that exposes workspace/* and agent/* methods
// over a Unix-domain socket.
// TODO(S03): full handler implementation
type Server struct {
	manager        *workspace.WorkspaceManager
	registry       *Registry
	agents         *agentd.AgentManager
	processes      *agentd.ProcessManager
	runtimeClasses *agentd.RuntimeClassRegistry
	config         agentd.Config
	store          *meta.Store
	socketPath     string
	baseDir        string
}

// New creates a Server with the provided dependencies.
// Call Serve to begin accepting connections.
// TODO(S03): full handler implementation
func New(
	manager *workspace.WorkspaceManager,
	registry *Registry,
	agents *agentd.AgentManager,
	processes *agentd.ProcessManager,
	runtimeClasses *agentd.RuntimeClassRegistry,
	config agentd.Config,
	store *meta.Store,
	socketPath, baseDir string,
) *Server {
	return &Server{
		manager:        manager,
		registry:       registry,
		agents:         agents,
		processes:      processes,
		runtimeClasses: runtimeClasses,
		config:         config,
		store:          store,
		socketPath:     socketPath,
		baseDir:        baseDir,
	}
}

// Serve starts the JSON-RPC server and blocks until Shutdown is called.
// TODO(S03): full handler implementation
func (s *Server) Serve() error {
	return nil
}

// Shutdown gracefully stops the server.
// TODO(S03): full handler implementation
func (s *Server) Shutdown(_ context.Context) error {
	return nil
}
