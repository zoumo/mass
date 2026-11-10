package codex

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestAgentFeaturesCopiesExplicitRequest(t *testing.T) {
	disabled := false
	features := agentFeatures(&runtime.SessionFeatures{AgentQuestions: &disabled})
	require.NotNil(t, features.AgentQuestions)
	require.False(t, *features.AgentQuestions)
	require.Nil(t, agentFeatures(nil).AgentQuestions)
	require.Nil(t, agentFeatures(&runtime.SessionFeatures{}).AgentQuestions)
}
