// Package agentd implements the agent daemon that manages agent runtime lifecycle.
// This file defines the AgentManager for agent lifecycle management.
package agentd

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/open-agent-d/open-agent-d/pkg/meta"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
)

// ErrAgentNotFound is returned when an agent does not exist.
type ErrAgentNotFound struct {
	Workspace string
	Name      string
}

func (e *ErrAgentNotFound) Error() string {
	return fmt.Sprintf("agentd: agent %s/%s not found", e.Workspace, e.Name)
}

// ErrDeleteNotStopped is returned when attempting to delete an agent that is
// still active. Agents may only be deleted from stopped or error states.
type ErrDeleteNotStopped struct {
	Workspace string
	Name      string
	State     spec.Status
}

func (e *ErrDeleteNotStopped) Error() string {
	return fmt.Sprintf("agentd: cannot delete agent %s/%s in state %s (agent must be stopped or error first)",
		e.Workspace, e.Name, e.State)
}

// ErrAgentAlreadyExists is returned when creating an agent whose (workspace, name) pair already exists.
type ErrAgentAlreadyExists struct {
	Workspace string
	Name      string
}

func (e *ErrAgentAlreadyExists) Error() string {
	return fmt.Sprintf("agentd: agent with workspace=%s name=%s already exists", e.Workspace, e.Name)
}

// AgentManager manages agent lifecycle.
// It wraps meta.Store and provides Create/Get/List/UpdateStatus/Delete
// with domain error types and structured logging.
// Agent identity is (workspace, name) — no UUID.
type AgentManager struct {
	store  *meta.Store
	logger *slog.Logger
}

// NewAgentManager creates a new AgentManager wrapping the provided store.
// The logger is configured with component=agentd.agent for observability.
func NewAgentManager(store *meta.Store) *AgentManager {
	logger := slog.Default().With("component", "agentd.agent")
	return &AgentManager{
		store:  store,
		logger: logger,
	}
}

// Create creates a new agent.
// Sets default status.State to spec.StatusCreating if empty.
// Returns ErrAgentAlreadyExists if the (workspace, name) pair already exists.
func (m *AgentManager) Create(ctx context.Context, agent *meta.AgentRun) error {
	if agent.Status.State == "" {
		agent.Status.State = spec.StatusCreating
	}

	m.logger.Info("creating agent",
		"workspace", agent.Metadata.Workspace,
		"name", agent.Metadata.Name,
		"state", agent.Status.State)

	if err := m.store.CreateAgentRun(ctx, agent); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return &ErrAgentAlreadyExists{
				Workspace: agent.Metadata.Workspace,
				Name:      agent.Metadata.Name,
			}
		}
		m.logger.Error("failed to create agent",
			"workspace", agent.Metadata.Workspace,
			"name", agent.Metadata.Name,
			"error", err)
		return fmt.Errorf("agentd: failed to create agent: %w", err)
	}

	m.logger.Info("agent created",
		"workspace", agent.Metadata.Workspace,
		"name", agent.Metadata.Name,
		"state", agent.Status.State)

	return nil
}

// Get retrieves an agent by (workspace, name).
// Returns nil, nil if the agent does not exist.
func (m *AgentManager) Get(ctx context.Context, workspace, name string) (*meta.AgentRun, error) {
	agent, err := m.store.GetAgentRun(ctx, workspace, name)
	if err != nil {
		return nil, fmt.Errorf("agentd: failed to get agent: %w", err)
	}
	return agent, nil
}

// GetByWorkspaceName retrieves an agent by its unique (workspace, name) pair.
// Alias for Get — provided for callers that prefer the explicit naming.
// Returns nil, nil if no agent with that combination exists.
func (m *AgentManager) GetByWorkspaceName(ctx context.Context, workspace, name string) (*meta.AgentRun, error) {
	return m.Get(ctx, workspace, name)
}

// List retrieves agents matching the filter.
// If filter is nil, returns all agents.
func (m *AgentManager) List(ctx context.Context, filter *meta.AgentRunFilter) ([]*meta.AgentRun, error) {
	agents, err := m.store.ListAgentRuns(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("agentd: failed to list agents: %w", err)
	}
	return agents, nil
}

// UpdateStatus updates an agent's status (state + optional shim metadata).
// Returns ErrAgentNotFound if the agent does not exist.
func (m *AgentManager) UpdateStatus(ctx context.Context, workspace, name string, status meta.AgentRunStatus) error {
	m.logger.Info("updating agent status",
		"workspace", workspace,
		"name", name,
		"state", status.State)

	if err := m.store.UpdateAgentRunStatus(ctx, workspace, name, status); err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return &ErrAgentNotFound{Workspace: workspace, Name: name}
		}
		m.logger.Error("failed to update agent status",
			"workspace", workspace,
			"name", name,
			"error", err)
		return fmt.Errorf("agentd: failed to update agent status: %w", err)
	}

	m.logger.Info("agent status updated",
		"workspace", workspace,
		"name", name,
		"state", status.State)

	return nil
}

// Delete deletes an agent by (workspace, name).
// Returns ErrDeleteNotStopped if the agent is not in the stopped or error state.
// Returns ErrAgentNotFound if the agent does not exist.
func (m *AgentManager) Delete(ctx context.Context, workspace, name string) error {
	// Fetch current agent to validate deletion preconditions.
	current, err := m.store.GetAgentRun(ctx, workspace, name)
	if err != nil {
		return fmt.Errorf("agentd: failed to get agent for deletion: %w", err)
	}
	if current == nil {
		return &ErrAgentNotFound{Workspace: workspace, Name: name}
	}

	// Only stopped or error agents may be deleted.
	if current.Status.State != spec.StatusStopped && current.Status.State != spec.StatusError {
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
		return fmt.Errorf("agentd: failed to delete agent: %w", err)
	}

	m.logger.Info("agent deleted", "workspace", workspace, "name", name)

	return nil
}
