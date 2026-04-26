// Package agentd implements the agent daemon that manages agent runtime lifecycle.
// This file defines the AgentRunManager for agent lifecycle management.
package agentd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/zoumo/mass/pkg/agentd/store"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
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
	Phase     apiruntime.Phase
}

func (e *ErrDeleteNotStopped) Error() string {
	return fmt.Sprintf("mass: cannot delete agent run %s/%s in phase %s (agent run must be stopped or error first)",
		e.Workspace, e.Name, e.Phase)
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
// Sets default status.Phase to apiruntime.PhaseCreating if empty.
// Returns ErrAgentRunAlreadyExists if the (workspace, name) pair already exists.
func (m *AgentRunManager) Create(ctx context.Context, agent *pkgariapi.AgentRun) error {
	if agent.Status.Phase == "" {
		agent.Status.Phase = apiruntime.PhaseCreating
	}

	m.logger.Info("creating agent",
		"workspace", agent.Metadata.Workspace,
		"name", agent.Metadata.Name,
		"phase", agent.Status.Phase)

	if err := m.store.CreateAgentRun(ctx, agent); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
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
		"phase", agent.Status.Phase)

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

// UpdateStatus updates an agent's status (state + optional run metadata).
// Returns ErrAgentRunNotFound if the agent does not exist.
func (m *AgentRunManager) UpdateStatus(ctx context.Context, workspace, name string, status pkgariapi.AgentRunStatus) error {
	m.logger.Info("updating agent status",
		"workspace", workspace,
		"name", name,
		"phase", status.Phase)

	if err := m.store.UpdateAgentRunStatus(ctx, workspace, name, status); err != nil {
		if errors.Is(err, store.ErrNotFound) {
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
		"phase", status.Phase)

	return nil
}

// UpdatePhase updates only Status.Phase and Status.ErrorMessage,
// preserving all other status fields (PID, SocketPath, StateDir, etc.).
func (m *AgentRunManager) UpdatePhase(ctx context.Context, workspace, name string, state apiruntime.Phase, errMsg string) error {
	m.logger.Info("updating agent phase",
		"workspace", workspace,
		"name", name,
		"phase", state)

	if err := m.store.UpdateAgentRunPhase(ctx, workspace, name, state, errMsg); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &ErrAgentRunNotFound{Workspace: workspace, Name: name}
		}
		m.logger.Error("failed to update agent phase",
			"workspace", workspace,
			"name", name,
			"error", err)
		return fmt.Errorf("mass: failed to update agent phase: %w", err)
	}

	return nil
}

// UpdateSessionInfo updates SessionID and EventPath, preserving all other status fields.
func (m *AgentRunManager) UpdateSessionInfo(ctx context.Context, workspace, name, sessionID, eventPath string) error {
	if err := m.store.UpdateAgentRunSessionInfo(ctx, workspace, name, sessionID, eventPath); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &ErrAgentRunNotFound{Workspace: workspace, Name: name}
		}
		return fmt.Errorf("mass: failed to update session info: %w", err)
	}
	return nil
}

// TransitionPhase updates an agent phase only when the current phase matches
// expected. It returns false when the agent exists but was already in another
// phase. Other status fields are preserved.
func (m *AgentRunManager) TransitionPhase(ctx context.Context, workspace, name string, expected, next apiruntime.Phase) (bool, error) {
	m.logger.Info("transitioning agent phase",
		"workspace", workspace,
		"name", name,
		"from", expected,
		"to", next)

	ok, err := m.store.TransitionAgentRunPhase(ctx, workspace, name, expected, next)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, &ErrAgentRunNotFound{Workspace: workspace, Name: name}
		}
		m.logger.Error("failed to transition agent phase",
			"workspace", workspace,
			"name", name,
			"from", expected,
			"to", next,
			"error", err)
		return false, fmt.Errorf("mass: failed to transition agent phase: %w", err)
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
	if current.Status.Phase != apiruntime.PhaseStopped && current.Status.Phase != apiruntime.PhaseError {
		m.logger.Warn("delete blocked: agent is still active",
			"workspace", workspace,
			"name", name,
			"phase", current.Status.Phase)
		return &ErrDeleteNotStopped{
			Workspace: workspace,
			Name:      name,
			Phase:     current.Status.Phase,
		}
	}

	m.logger.Info("deleting agent",
		"workspace", workspace,
		"name", name,
		"phase", current.Status.Phase)

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
