// Package agentd implements the agent daemon that manages agent runtime lifecycle.
// This file defines the AgentRunManager for agent lifecycle management.
package agentd

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
	"github.com/zoumo/mass/pkg/store"
)

// ErrAgentRunNotFound is returned when an agent does not exist.
type ErrAgentRunNotFound struct {
	Workspace string
	Name      string
}

func (e *ErrAgentRunNotFound) Error() string {
	return fmt.Sprintf("mass: agent %s/%s not found", e.Workspace, e.Name)
}

// ErrDeleteNotStopped is returned when attempting to delete an agent that is
// still active. Agents may only be deleted from stopped or error states.
type ErrDeleteNotStopped struct {
	Workspace string
	Name      string
	State     apiruntime.Status
}

func (e *ErrDeleteNotStopped) Error() string {
	return fmt.Sprintf("mass: cannot delete agent run %s/%s in state %s (agent run must be stopped or error first)",
		e.Workspace, e.Name, e.State)
}

// ErrAgentRunAlreadyExists is returned when creating an agent whose (workspace, name) pair already exists.
type ErrAgentRunAlreadyExists struct {
	Workspace string
	Name      string
}

func (e *ErrAgentRunAlreadyExists) Error() string {
	return fmt.Sprintf("mass: agent with workspace=%s name=%s already exists", e.Workspace, e.Name)
}

// AgentRunManager manages agent lifecycle.
// It wraps store.Store and provides Create/Get/List/UpdateStatus/Delete
// with domain error types and structured logging.
// Agent identity is (workspace, name) — no UUID.
type AgentRunManager struct {
	store  *store.Store
	logger *slog.Logger
}

// NewAgentRunManager creates a new AgentRunManager wrapping the provided store.
// The logger is configured with component=mass.agent for observability.
func NewAgentRunManager(s *store.Store, logger *slog.Logger) *AgentRunManager {
	logger = logger.With("component", "mass.agent")
	return &AgentRunManager{
		store:  s,
		logger: logger,
	}
}

// Create creates a new agent.
// Sets default status.State to apiruntime.StatusCreating if empty.
// Returns ErrAgentRunAlreadyExists if the (workspace, name) pair already exists.
func (m *AgentRunManager) Create(ctx context.Context, agent *pkgariapi.AgentRun) error {
	if agent.Status.State == "" {
		agent.Status.State = apiruntime.StatusCreating
	}

	m.logger.Info("creating agent",
		"workspace", agent.Metadata.Workspace,
		"name", agent.Metadata.Name,
		"state", agent.Status.State)

	if err := m.store.CreateAgentRun(ctx, agent); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return &ErrAgentRunAlreadyExists{
				Workspace: agent.Metadata.Workspace,
				Name:      agent.Metadata.Name,
			}
		}
		m.logger.Error("failed to create agent",
			"workspace", agent.Metadata.Workspace,
			"name", agent.Metadata.Name,
			"error", err)
		return fmt.Errorf("mass: failed to create agent: %w", err)
	}

	m.logger.Info("agent created",
		"workspace", agent.Metadata.Workspace,
		"name", agent.Metadata.Name,
		"state", agent.Status.State)

	return nil
}

// Get retrieves an agent by (workspace, name).
// Returns nil, nil if the agent does not exist.
func (m *AgentRunManager) Get(ctx context.Context, workspace, name string) (*pkgariapi.AgentRun, error) {
	agent, err := m.store.GetAgentRun(ctx, workspace, name)
	if err != nil {
		return nil, fmt.Errorf("mass: failed to get agent: %w", err)
	}
	return agent, nil
}

// GetByWorkspaceName retrieves an agent by its unique (workspace, name) pair.
// Alias for Get — provided for callers that prefer the explicit naming.
// Returns nil, nil if no agent with that combination exists.
func (m *AgentRunManager) GetByWorkspaceName(ctx context.Context, workspace, name string) (*pkgariapi.AgentRun, error) {
	return m.Get(ctx, workspace, name)
}

// List retrieves agents matching the filter.
// If filter is nil, returns all agents.
func (m *AgentRunManager) List(ctx context.Context, filter *pkgariapi.AgentRunFilter) ([]*pkgariapi.AgentRun, error) {
	agents, err := m.store.ListAgentRuns(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("mass: failed to list agents: %w", err)
	}
	return agents, nil
}

// UpdateStatus updates an agent's status (state + optional shim metadata).
// Returns ErrAgentRunNotFound if the agent does not exist.
func (m *AgentRunManager) UpdateStatus(ctx context.Context, workspace, name string, status pkgariapi.AgentRunStatus) error {
	m.logger.Info("updating agent status",
		"workspace", workspace,
		"name", name,
		"state", status.State)

	if err := m.store.UpdateAgentRunStatus(ctx, workspace, name, status); err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return &ErrAgentRunNotFound{Workspace: workspace, Name: name}
		}
		m.logger.Error("failed to update agent status",
			"workspace", workspace,
			"name", name,
			"error", err)
		return fmt.Errorf("mass: failed to update agent status: %w", err)
	}

	m.logger.Info("agent status updated",
		"workspace", workspace,
		"name", name,
		"state", status.State)

	return nil
}

// TransitionState updates an agent state only when the current state matches
// expected. It returns false when the agent exists but was already in another
// state. Other status fields are preserved.
func (m *AgentRunManager) TransitionState(ctx context.Context, workspace, name string, expected, next apiruntime.Status) (bool, error) {
	m.logger.Info("transitioning agent state",
		"workspace", workspace,
		"name", name,
		"from", expected,
		"to", next)

	ok, err := m.store.TransitionAgentRunState(ctx, workspace, name, expected, next)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return false, &ErrAgentRunNotFound{Workspace: workspace, Name: name}
		}
		m.logger.Error("failed to transition agent state",
			"workspace", workspace,
			"name", name,
			"from", expected,
			"to", next,
			"error", err)
		return false, fmt.Errorf("mass: failed to transition agent state: %w", err)
	}
	return ok, nil
}

// Delete deletes an agent by (workspace, name).
// Returns ErrDeleteNotStopped if the agent is not in the stopped or error state.
// Returns ErrAgentRunNotFound if the agent does not exist.
func (m *AgentRunManager) Delete(ctx context.Context, workspace, name string) error {
	current, err := m.store.GetAgentRun(ctx, workspace, name)
	if err != nil {
		return fmt.Errorf("mass: failed to get agent for deletion: %w", err)
	}
	if current == nil {
		return &ErrAgentRunNotFound{Workspace: workspace, Name: name}
	}

	// Only stopped or error agents may be deleted.
	if current.Status.State != apiruntime.StatusStopped && current.Status.State != apiruntime.StatusError {
		m.logger.Warn("delete blocked: agent is still active",
			"workspace", workspace,
			"name", name,
			"state", current.Status.State)
		return &ErrDeleteNotStopped{
			Workspace: workspace,
			Name:      name,
			State:     current.Status.State,
		}
	}

	m.logger.Info("deleting agent",
		"workspace", workspace,
		"name", name,
		"state", current.Status.State)

	if err := m.store.DeleteAgentRun(ctx, workspace, name); err != nil {
		m.logger.Error("failed to delete agent",
			"workspace", workspace,
			"name", name,
			"error", err)
		return fmt.Errorf("mass: failed to delete agent: %w", err)
	}

	m.logger.Info("agent deleted", "workspace", workspace, "name", name)

	return nil
}
