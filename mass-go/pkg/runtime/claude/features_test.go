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
	options := sessionOptions(features, "", "/work", "claude", nil)
	require.Equal(t, []string{"AskUserQuestion"}, options.DisallowedTools)
}

func TestSessionOptionsPreserveAgentQuestions(t *testing.T) {
	features := runtime.ResolveSessionFeatures(nil, supportedSessionFeatures)
	options := sessionOptions(features, "", "/work", "claude", nil)
	require.Empty(t, options.DisallowedTools)
}
