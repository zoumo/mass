package codex

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
			_, _, err := parseAnswer(answer)
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
			_, _, err := parseAnswer(map[string]any{"action": "accept", "content": content})
			require.ErrorIs(t, err, runtime.ErrInvalidArgument)
		})
	}

	content := map[string]any{
		"answers": map[string]any{
			"format": map[string]any{"answers": []any{"json"}},
		},
	}
	parsed, kind, err := parseAnswer(map[string]any{"action": "accept", "content": content})
	require.NoError(t, err)
	require.Equal(t, agent.InteractionForm, kind)
	require.Equal(t, agent.FormAnswer{Outcome: agent.FormAccept, Content: content}, parsed)
}

func TestParseAnswerRejectsContentForDeclineAndCancel(t *testing.T) {
	for _, action := range []string{"decline", "cancel"} {
		t.Run(action, func(t *testing.T) {
			_, _, err := parseAnswer(map[string]any{
				"action":  action,
				"content": map[string]any{},
			})
			require.ErrorIs(t, err, runtime.ErrInvalidArgument)
		})
	}
}

func TestParseAnswerMapsPermissionShape(t *testing.T) {
	parsed, kind, err := parseAnswer(map[string]any{
		"outcome":  "selected",
		"optionId": "allow-once",
	})
	require.NoError(t, err)
	require.Equal(t, agent.InteractionPermission, kind)
	require.Equal(t, agent.PermissionAnswer{
		Outcome:  agent.PermissionSelected,
		OptionID: "allow-once",
	}, parsed)
}
