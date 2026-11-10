package integration_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExpandCodexIdentityReadsHandlesInterleavedOpens(t *testing.T) {
	scenario := map[string]any{"frames": []any{
		codexScenarioOpen(92, "first"),
		codexScenarioOpen(300, "second"),
		codexScenarioResponse(300, "second"),
		codexScenarioResponse(92, "first"),
	}}

	expandCodexIdentityReads(scenario)

	frames := scenario["frames"].([]any)
	require.Len(t, frames, 8)
	require.Equal(t, "thread/read", codexScenarioMethod(frames[3]))
	require.Equal(t, "thread/read", codexScenarioMethod(frames[6]))
	require.Equal(t, "second", codexScenarioThreadID(frames[3]))
	require.Equal(t, "first", codexScenarioThreadID(frames[6]))
	for index, value := range frames {
		require.Equal(t, int(index+1), value.(map[string]any)["ordinal"])
	}
}

func codexScenarioOpen(id float64, threadID string) map[string]any {
	return map[string]any{
		"direction": "c2s",
		"wire": map[string]any{
			"id":     id,
			"method": "thread/start",
			"params": map[string]any{"cwd": "/synthetic"},
		},
	}
}

func codexScenarioResponse(id float64, threadID string) map[string]any {
	return map[string]any{
		"direction": "s2c",
		"wire": map[string]any{
			"id": id,
			"result": map[string]any{
				"thread": map[string]any{
					"id":        threadID,
					"sessionId": threadID,
					"cwd":       "/synthetic",
					"ephemeral": false,
				},
			},
		},
	}
}

func codexScenarioMethod(frame any) string {
	wire, _ := frame.(map[string]any)["wire"].(map[string]any)
	method, _ := wire["method"].(string)
	return method
}

func codexScenarioThreadID(frame any) string {
	wire, _ := frame.(map[string]any)["wire"].(map[string]any)
	params, _ := wire["params"].(map[string]any)
	threadID, _ := params["threadId"].(string)
	return threadID
}
