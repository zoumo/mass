package codex

import (
	"testing"

	"github.com/stretchr/testify/require"
	sdk "github.com/zoumo/agent-sdk-go/codex"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestSessionOptionsDisableAgentQuestions(t *testing.T) {
	features := runtime.ResolveSessionFeatures(
		&runtime.SessionFeatures{AgentQuestions: runtime.Bool(false)},
		supportedSessionFeatures,
	)
	var options sdk.SessionOptions
	applySessionFeatures(&options, features)
	require.Equal(t, map[sdk.Tool]bool{sdk.ToolRequestUserInput: false}, options.Tools)
}

func TestSessionOptionsPreserveAgentQuestions(t *testing.T) {
	features := runtime.ResolveSessionFeatures(nil, supportedSessionFeatures)
	var options sdk.SessionOptions
	applySessionFeatures(&options, features)
	require.Empty(t, options.Tools)
}
