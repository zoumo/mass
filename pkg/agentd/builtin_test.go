package agentd

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

func TestEnsureBuiltinAgents_SeedsAll(t *testing.T) {
	t.Parallel()
	s := newTestMetaStore(t)
	ctx := context.Background()

	err := EnsureBuiltinAgents(ctx, s, slog.Default())
	require.NoError(t, err)

	// All 3 built-in agents should exist.
	for _, name := range []string{"claude", "codex", "gsd-pi"} {
		ag, err := s.GetAgent(ctx, name)
		require.NoError(t, err, "GetAgent(%s)", name)
		require.NotNil(t, ag, "agent %s should exist", name)
		assert.Equal(t, name, ag.Metadata.Name)
	}
}

func TestEnsureBuiltinAgents_SkipsExisting(t *testing.T) {
	t.Parallel()
	s := newTestMetaStore(t)
	ctx := context.Background()

	// Pre-create "claude" with custom command.
	custom := pkgariapi.Agent{
		Metadata: pkgariapi.ObjectMeta{Name: "claude"},
		Spec:     pkgariapi.AgentSpec{Command: "my-custom-claude"},
	}
	require.NoError(t, s.SetAgent(ctx, &custom))

	err := EnsureBuiltinAgents(ctx, s, slog.Default())
	require.NoError(t, err)

	// "claude" should retain custom command (not overwritten).
	ag, err := s.GetAgent(ctx, "claude")
	require.NoError(t, err)
	assert.Equal(t, "my-custom-claude", ag.Spec.Command, "existing agent should not be overwritten")

	// Other agents should still be seeded.
	for _, name := range []string{"codex", "gsd-pi"} {
		ag, err := s.GetAgent(ctx, name)
		require.NoError(t, err)
		require.NotNil(t, ag, "agent %s should be seeded", name)
	}
}

func TestEnsureBuiltinAgents_Idempotent(t *testing.T) {
	t.Parallel()
	s := newTestMetaStore(t)
	ctx := context.Background()

	// Run twice — second call should be no-op.
	require.NoError(t, EnsureBuiltinAgents(ctx, s, slog.Default()))
	require.NoError(t, EnsureBuiltinAgents(ctx, s, slog.Default()))

	ag, err := s.GetAgent(ctx, "claude")
	require.NoError(t, err)
	require.NotNil(t, ag)
}
