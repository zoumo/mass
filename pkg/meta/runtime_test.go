package meta_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/open-agent-d/open-agent-d/pkg/meta"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
)

// makeAgentTemplate returns a minimal valid AgentTemplate for test use.
func makeAgentTemplate(name string) *meta.AgentTemplate {
	return &meta.AgentTemplate{
		Metadata: meta.ObjectMeta{
			Name: name,
		},
		Spec: meta.AgentTemplateSpec{
			Command: "/usr/bin/my-agent",
			Args:    []string{"--serve"},
			Env: []spec.EnvVar{
				{Name: "FOO", Value: "bar"},
			},
		},
	}
}

// ── SetAgentTemplate ──────────────────────────────────────────────────────────

func TestSetAgentTemplate_CreateNew(t *testing.T) {
	s := tempStore(t)
	rt := makeAgentTemplate("default")

	require.NoError(t, s.SetAgentTemplate(t.Context(), rt))

	got, err := s.GetAgentTemplate(t.Context(), "default")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "default", got.Metadata.Name)
	require.Equal(t, "/usr/bin/my-agent", got.Spec.Command)
	require.False(t, got.Metadata.CreatedAt.IsZero(), "CreatedAt should be set on first write")
	require.False(t, got.Metadata.UpdatedAt.IsZero(), "UpdatedAt should be set on first write")
}

func TestSetAgentTemplate_Upsert(t *testing.T) {
	s := tempStore(t)

	first := makeAgentTemplate("gpu")
	require.NoError(t, s.SetAgentTemplate(t.Context(), first))

	got1, err := s.GetAgentTemplate(t.Context(), "gpu")
	require.NoError(t, err)
	require.NotNil(t, got1)
	createdAt := got1.Metadata.CreatedAt

	// Upsert with a new command.
	second := makeAgentTemplate("gpu")
	second.Spec.Command = "/usr/bin/gpu-agent"
	require.NoError(t, s.SetAgentTemplate(t.Context(), second))

	got2, err := s.GetAgentTemplate(t.Context(), "gpu")
	require.NoError(t, err)
	require.NotNil(t, got2)
	require.Equal(t, "/usr/bin/gpu-agent", got2.Spec.Command, "command should be updated")
	require.Equal(t, createdAt, got2.Metadata.CreatedAt, "CreatedAt should be preserved on upsert")
	require.True(t, got2.Metadata.UpdatedAt.After(createdAt) ||
		got2.Metadata.UpdatedAt.Equal(createdAt),
		"UpdatedAt should be >= CreatedAt after upsert")
}

// ── GetAgentTemplate ──────────────────────────────────────────────────────────

func TestGetAgentTemplate_NotFound(t *testing.T) {
	s := tempStore(t)
	got, err := s.GetAgentTemplate(t.Context(), "ghost")
	require.NoError(t, err)
	require.Nil(t, got)
}

// ── ListAgentTemplates ────────────────────────────────────────────────────────

func TestListAgentTemplates_Empty(t *testing.T) {
	s := tempStore(t)
	rts, err := s.ListAgentTemplates(t.Context())
	require.NoError(t, err)
	require.NotNil(t, rts, "ListAgentTemplates should return a non-nil slice even when empty")
	require.Empty(t, rts)
}

func TestListAgentTemplates_MultipleEntries(t *testing.T) {
	s := tempStore(t)
	require.NoError(t, s.SetAgentTemplate(t.Context(), makeAgentTemplate("alpha")))
	require.NoError(t, s.SetAgentTemplate(t.Context(), makeAgentTemplate("beta")))
	require.NoError(t, s.SetAgentTemplate(t.Context(), makeAgentTemplate("gamma")))

	rts, err := s.ListAgentTemplates(t.Context())
	require.NoError(t, err)
	require.Len(t, rts, 3)

	names := make([]string, 0, len(rts))
	for _, r := range rts {
		names = append(names, r.Metadata.Name)
	}
	require.ElementsMatch(t, []string{"alpha", "beta", "gamma"}, names)
}

// ── DeleteAgentTemplate ───────────────────────────────────────────────────────

func TestDeleteAgentTemplate_Existing(t *testing.T) {
	s := tempStore(t)
	require.NoError(t, s.SetAgentTemplate(t.Context(), makeAgentTemplate("todelete")))
	require.NoError(t, s.DeleteAgentTemplate(t.Context(), "todelete"))

	got, err := s.GetAgentTemplate(t.Context(), "todelete")
	require.NoError(t, err)
	require.Nil(t, got, "deleted agentTemplate should not be retrievable")
}

func TestDeleteAgentTemplate_NoOp(t *testing.T) {
	s := tempStore(t)
	// Deleting a non-existent agentTemplate should be a no-op (no error).
	require.NoError(t, s.DeleteAgentTemplate(t.Context(), "nonexistent"))
}
