package store_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	apiari "github.com/zoumo/oar/api/ari"
	apiruntime "github.com/zoumo/oar/pkg/runtime-spec/api"
)

// makeAgent returns a minimal valid Agent for test use.
func makeAgent(name string) *apiari.Agent {
	return &apiari.Agent{
		Metadata: apiari.ObjectMeta{
			Name: name,
		},
		Spec: apiari.AgentSpec{
			Command: "/usr/bin/my-agent",
			Args:    []string{"--serve"},
			Env: []apiruntime.EnvVar{
				{Name: "FOO", Value: "bar"},
			},
		},
	}
}

// ── SetAgent ──────────────────────────────────────────────────────────

func TestSetAgent_CreateNew(t *testing.T) {
	s := tempStore(t)
	rt := makeAgent("default")

	require.NoError(t, s.SetAgent(t.Context(), rt))

	got, err := s.GetAgent(t.Context(), "default")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "default", got.Metadata.Name)
	require.Equal(t, "/usr/bin/my-agent", got.Spec.Command)
	require.False(t, got.Metadata.CreatedAt.IsZero(), "CreatedAt should be set on first write")
	require.False(t, got.Metadata.UpdatedAt.IsZero(), "UpdatedAt should be set on first write")
}

func TestSetAgent_Upsert(t *testing.T) {
	s := tempStore(t)

	first := makeAgent("gpu")
	require.NoError(t, s.SetAgent(t.Context(), first))

	got1, err := s.GetAgent(t.Context(), "gpu")
	require.NoError(t, err)
	require.NotNil(t, got1)
	createdAt := got1.Metadata.CreatedAt

	second := makeAgent("gpu")
	second.Spec.Command = "/usr/bin/gpu-agent"
	require.NoError(t, s.SetAgent(t.Context(), second))

	got2, err := s.GetAgent(t.Context(), "gpu")
	require.NoError(t, err)
	require.NotNil(t, got2)
	require.Equal(t, "/usr/bin/gpu-agent", got2.Spec.Command, "command should be updated")
	require.Equal(t, createdAt, got2.Metadata.CreatedAt, "CreatedAt should be preserved on upsert")
	require.True(t, got2.Metadata.UpdatedAt.After(createdAt) ||
		got2.Metadata.UpdatedAt.Equal(createdAt),
		"UpdatedAt should be >= CreatedAt after upsert")
}

// ── GetAgent ──────────────────────────────────────────────────────────

func TestGetAgent_NotFound(t *testing.T) {
	s := tempStore(t)
	got, err := s.GetAgent(t.Context(), "ghost")
	require.NoError(t, err)
	require.Nil(t, got)
}

// ── ListAgents ────────────────────────────────────────────────────────

func TestListAgents_Empty(t *testing.T) {
	s := tempStore(t)
	rts, err := s.ListAgents(t.Context())
	require.NoError(t, err)
	require.NotNil(t, rts, "ListAgents should return a non-nil slice even when empty")
	require.Empty(t, rts)
}

func TestListAgents_MultipleEntries(t *testing.T) {
	s := tempStore(t)
	require.NoError(t, s.SetAgent(t.Context(), makeAgent("alpha")))
	require.NoError(t, s.SetAgent(t.Context(), makeAgent("beta")))
	require.NoError(t, s.SetAgent(t.Context(), makeAgent("gamma")))

	rts, err := s.ListAgents(t.Context())
	require.NoError(t, err)
	require.Len(t, rts, 3)

	names := make([]string, 0, len(rts))
	for _, r := range rts {
		names = append(names, r.Metadata.Name)
	}
	require.ElementsMatch(t, []string{"alpha", "beta", "gamma"}, names)
}

// ── DeleteAgent ───────────────────────────────────────────────────────

func TestDeleteAgent_Existing(t *testing.T) {
	s := tempStore(t)
	require.NoError(t, s.SetAgent(t.Context(), makeAgent("todelete")))
	require.NoError(t, s.DeleteAgent(t.Context(), "todelete"))

	got, err := s.GetAgent(t.Context(), "todelete")
	require.NoError(t, err)
	require.Nil(t, got, "deleted agent should not be retrievable")
}

func TestDeleteAgent_NoOp(t *testing.T) {
	s := tempStore(t)
	require.NoError(t, s.DeleteAgent(t.Context(), "nonexistent"))
}
