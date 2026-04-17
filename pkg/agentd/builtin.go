// Package agentd — built-in agent definitions that ship with the MASS binary.
package agentd

import (
	"context"
	"fmt"
	"log/slog"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	"github.com/zoumo/mass/pkg/agentd/store"
)

// builtinAgents defines agent definitions seeded into the metadata store on
// first startup. Existing agents (by name) are never overwritten — user
// modifications are preserved.
var builtinAgents = []pkgariapi.Agent{
	{
		Metadata: pkgariapi.ObjectMeta{Name: "claude"},
		Spec: pkgariapi.AgentSpec{
			Command: "bunx",
			Args:    []string{"@agentclientprotocol/claude-agent-acp@v0.26.0"},
		},
	},
	{
		Metadata: pkgariapi.ObjectMeta{Name: "codex"},
		Spec: pkgariapi.AgentSpec{
			Command: "bunx",
			Args:    []string{"@zed-industries/codex-acp@0.11.1"},
		},
	},
	{
		Metadata: pkgariapi.ObjectMeta{Name: "gsd-pi"},
		Spec: pkgariapi.AgentSpec{
			Command: "bunx",
			Args:    []string{"gsd-pi-acp@0.1.2"},
		},
	},
}

// EnsureBuiltinAgents seeds all built-in agent definitions into the store.
// Agents that already exist (by name) are skipped.
func EnsureBuiltinAgents(ctx context.Context, s *store.Store, logger *slog.Logger) error {
	for _, ag := range builtinAgents {
		existing, err := s.GetAgent(ctx, ag.Metadata.Name)
		if err != nil {
			return fmt.Errorf("check builtin agent %s: %w", ag.Metadata.Name, err)
		}
		if existing != nil {
			logger.Debug("builtin agent already exists, skipping", "name", ag.Metadata.Name)
			continue
		}
		agCopy := ag
		if err := s.SetAgent(ctx, &agCopy); err != nil {
			return fmt.Errorf("seed builtin agent %s: %w", ag.Metadata.Name, err)
		}
		logger.Info("seeded builtin agent", "name", ag.Metadata.Name)
	}
	return nil
}
