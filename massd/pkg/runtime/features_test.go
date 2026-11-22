package runtime

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveSessionFeatures(t *testing.T) {
	supported := FeatureSupport{AgentQuestions: true}
	unsupported := FeatureSupport{}
	for _, tc := range []struct {
		name      string
		requested *SessionFeatures
		support   FeatureSupport
		want      bool
	}{
		{"omitted defaults enabled", nil, supported, true},
		{"omitted field defaults enabled", &SessionFeatures{}, supported, true},
		{"explicit enabled", &SessionFeatures{AgentQuestions: Bool(true)}, supported, true},
		{"explicit disabled", &SessionFeatures{AgentQuestions: Bool(false)}, supported, false},
		{"unsupported enabled degrades", nil, unsupported, false},
		{"unsupported stays disabled", &SessionFeatures{AgentQuestions: Bool(false)}, unsupported, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			effective := ResolveSessionFeatures(tc.requested, tc.support)
			require.Equal(t, tc.want, effective.AgentQuestionsEnabled())
		})
	}
}

func TestSessionFeaturesJSONDefaultsAndExplicitFalse(t *testing.T) {
	var omitted SessionFeatures
	require.NoError(t, json.Unmarshal([]byte(`{}`), &omitted))
	require.True(t, omitted.AgentQuestionsEnabled())

	var disabled SessionFeatures
	require.NoError(t, json.Unmarshal([]byte(`{"agentQuestions":false}`), &disabled))
	require.False(t, disabled.AgentQuestionsEnabled())

	raw, err := json.Marshal(ResolveSessionFeatures(nil, FeatureSupport{AgentQuestions: true}))
	require.NoError(t, err)
	require.JSONEq(t, `{"agentQuestions":true}`, string(raw))
}
