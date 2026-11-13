package transport_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/transport"
)

func TestHistoryAndStreamOpenAPI(t *testing.T) {
	data, err := transport.OpenAPI()
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	paths := doc["paths"].(map[string]any)
	for _, base := range []string{"/apis/v1/sessions/{sessionId}", "/apis/v1/namespaces/{namespace}/sessions/{nameOrID}"} {
		require.Contains(t, paths, base+"/events")
		require.Contains(t, paths, base+"/events/stream")
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	union, ok := schemas["SessionEvent"].(map[string]any)
	require.True(t, ok)
	branches := union["oneOf"].([]any)
	require.Len(t, branches, 17)
	types := make([]string, 0, len(branches))
	payloadNames := make([]string, 0, len(branches))
	for _, raw := range branches {
		branch := raw.(map[string]any)
		properties := branch["properties"].(map[string]any)
		kind := properties["type"].(map[string]any)["const"].(string)
		types = append(types, kind)
		payloadNames = append(payloadNames, properties["payload"].(map[string]any)["$ref"].(string))
	}
	slices.Sort(types)
	require.Equal(t, []string{
		"agent.message",
		"agent.message_chunk",
		"agent.thinking",
		"agent.thinking_chunk",
		"agent.tool_call",
		"agent.tool_call_chunk",
		"agent.tool_result",
		"session.available_commands_update",
		"session.config_option_update",
		"session.error",
		"session.history_replay",
		"session.info_update",
		"session.interaction_closed",
		"session.interaction_requested",
		"session.state_update",
		"session.usage_update",
		"user.message",
	}, types)
	require.NotContains(t, types, "agent.plan")

	for _, name := range []string{"ToolCallPayload", "ToolResultPayload"} {
		schema := schemas[name].(map[string]any)
		properties := schema["properties"].(map[string]any)
		require.NotContains(t, properties, "name")
		require.Subset(t, schema["required"], []any{"id", "kind", "toolName", "provider"})
		for _, field := range []string{"toolName", "provider"} {
			fieldSchema := properties[field].(map[string]any)
			require.Equal(t, "string", fieldSchema["type"])
			require.InDelta(t, 1, fieldSchema["minLength"], 0)
		}
	}

	contentSchemas := 0
	for _, ref := range payloadNames {
		schema := schemas[ref[len("#/components/schemas/"):]].(map[string]any)
		properties, _ := schema["properties"].(map[string]any)
		content, ok := properties["content"].(map[string]any)
		if !ok {
			continue
		}
		require.Equal(t, "array", content["type"], ref)
		require.Equal(t, map[string]any{"$ref": "#/components/schemas/ContentBlock"}, content["items"], ref)
		contentSchemas++
	}
	require.Equal(t, 8, contentSchemas)
	input := schemas["Input"].(map[string]any)["properties"].(map[string]any)["content"].(map[string]any)
	require.Equal(t, map[string]any{"$ref": "#/components/schemas/ContentBlock"}, input["items"])
	require.Len(t, schemas["ContentBlock"].(map[string]any)["oneOf"], 5)
}
