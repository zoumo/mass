package codex

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestThreadConfigDisablesAgentQuestions(t *testing.T) {
	features := runtime.ResolveSessionFeatures(
		&runtime.SessionFeatures{AgentQuestions: runtime.Bool(false)},
		supportedSessionFeatures,
	)
	config, ok := threadConfig(features).Get()
	require.True(t, ok)
	require.JSONEq(t, `false`, string(config["tools.experimental_request_user_input.enabled"]))
}

func TestThreadConfigOmittedWhenAgentQuestionsEnabled(t *testing.T) {
	features := runtime.ResolveSessionFeatures(nil, supportedSessionFeatures)
	_, ok := threadConfig(features).Get()
	require.False(t, ok)
}
