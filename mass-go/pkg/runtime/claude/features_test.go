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
	options := sessionOpenOptions(runtime.SessionSpec{Cwd: "/work"}, features, nil)
	require.False(t, options.Features.AgentQuestionsEnabled())
}

func TestSessionOptionsPreserveAgentQuestions(t *testing.T) {
	features := runtime.ResolveSessionFeatures(nil, supportedSessionFeatures)
	options := sessionOpenOptions(runtime.SessionSpec{Cwd: "/work"}, features, nil)
	require.True(t, options.Features.AgentQuestionsEnabled())
}
