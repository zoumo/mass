package claude

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestSessionOptionsDisableAgentQuestions(t *testing.T) {
	features := runtime.ResolveSessionFeatures(
		&runtime.SessionFeatures{AgentQuestions: runtime.Bool(false)},
		supportedSessionFeatures,
	)
	options, err := sessionOptions(features, nil, "", "/work", "claude", nil)
	require.NoError(t, err)
	require.Equal(t, []string{"AskUserQuestion"}, options.DisallowedTools)
}

func TestSessionOptionsPreserveAgentQuestions(t *testing.T) {
	features := runtime.ResolveSessionFeatures(nil, supportedSessionFeatures)
	options, err := sessionOptions(features, nil, "", "/work", "claude", nil)
	require.NoError(t, err)
	require.Empty(t, options.DisallowedTools)
}
