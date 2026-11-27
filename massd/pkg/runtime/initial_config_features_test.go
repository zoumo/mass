package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSDKOpenOptionsCopiesExplicitFeatures(t *testing.T) {
	spec := SessionSpec{Features: &SessionFeatures{AgentQuestions: Bool(false), RestrictLocalTools: Bool(true)}}
	features := spec.SDKOpenOptions().Features
	require.False(t, features.AgentQuestionsEnabled())
	require.True(t, features.RestrictLocalTools)
	*features.AgentQuestions = true
	require.False(t, spec.Features.AgentQuestionsEnabled())

	defaults := (SessionSpec{}).SDKOpenOptions().Features
	require.Nil(t, defaults.AgentQuestions)
	require.False(t, defaults.RestrictLocalTools)
}
