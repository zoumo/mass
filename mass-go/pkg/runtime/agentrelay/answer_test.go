package agentrelay

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestParseAnswerRejectsUnsupportedFields(t *testing.T) {
	for name, answer := range map[string]map[string]any{
		"permission extra": {"outcome": "selected", "optionId": "allow-once", "content": map[string]any{}},
		"permission action": {
			"outcome":  "selected",
			"optionId": "allow-once",
			"action":   "accept",
		},
		"form extra": {"action": "accept", "content": map[string]any{}, "optionId": "allow-once"},
		"form typo":  {"action": "cancel", "typo": 1},
		"form outcome": {
			"action":  "accept",
			"content": map[string]any{},
			"outcome": "selected",
		},
	} {
		t.Run(name, func(t *testing.T) {
			kind := agent.InteractionForm
			if answer["outcome"] != nil && answer["optionId"] != nil {
				kind = agent.InteractionPermission
			}
			_, err := parseAnswer(kind, answer)
			require.ErrorIs(t, err, runtime.ErrInvalidArgument)
		})
	}
}

func TestParseAnswerValidatesFormContentBeforeProviderCall(t *testing.T) {
	for name, content := range map[string]any{
		"nil":        nil,
		"array":      []any{"not", "an", "object"},
		"string":     "not an object",
		"non-finite": map[string]any{"value": math.NaN()},
		"unsupported": map[string]any{
			"value": func() {},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseAnswer(agent.InteractionForm, map[string]any{"action": "accept", "content": content})
			require.ErrorIs(t, err, runtime.ErrInvalidArgument)
		})
	}

	content := map[string]any{
		"answers": map[string]any{
			"format": map[string]any{"answers": []any{"json"}},
		},
	}
	parsed, err := parseAnswer(agent.InteractionForm, map[string]any{"action": "accept", "content": content})
	require.NoError(t, err)
	require.Equal(t, agent.FormAnswer{Outcome: agent.FormAccept, Content: content}, parsed)
}

func TestParseAnswerRetainsNativeQuestionAnswerMap(t *testing.T) {
	content := map[string]any{"answers": map[string]string{"Which format?": "json"}}
	answer, err := parseAnswer(agent.InteractionForm, map[string]any{"action": "accept", "content": content})
	require.NoError(t, err)
	require.Equal(t, agent.FormAnswer{Outcome: agent.FormAccept, Content: content}, answer)
}

func TestParseAnswerRejectsContentForDeclineAndCancel(t *testing.T) {
	for _, action := range []string{"decline", "cancel"} {
		t.Run(action, func(t *testing.T) {
			_, err := parseAnswer(agent.InteractionForm, map[string]any{
				"action":  action,
				"content": map[string]any{},
			})
			require.ErrorIs(t, err, runtime.ErrInvalidArgument)
		})
	}
}

func TestParseAnswerMapsPermissionShape(t *testing.T) {
	parsed, err := parseAnswer(agent.InteractionPermission, map[string]any{
		"outcome":  "selected",
		"optionId": "allow-once",
	})
	require.NoError(t, err)
	require.Equal(t, agent.PermissionAnswer{
		Outcome:  agent.PermissionSelected,
		OptionID: "allow-once",
	}, parsed)
}

func TestParseAnswerUsesLiveKind(t *testing.T) {
	url, err := parseAnswer(agent.InteractionURL, map[string]any{"action": "accept"})
	require.NoError(t, err)
	require.Equal(t, agent.URLAnswer{Outcome: agent.URLAccept}, url)
	form, err := parseAnswer(agent.InteractionForm, map[string]any{"action": "accept", "content": map[string]any{"tenant": "demo"}})
	require.NoError(t, err)
	require.Equal(t, agent.FormAnswer{Outcome: agent.FormAccept, Content: map[string]any{"tenant": "demo"}}, form)
	permission, err := parseAnswer(agent.InteractionPermission, map[string]any{"outcome": "selected", "optionId": "cancel"})
	require.NoError(t, err)
	require.Equal(t, agent.PermissionAnswer{Outcome: agent.PermissionSelected, OptionID: "cancel"}, permission)
}
