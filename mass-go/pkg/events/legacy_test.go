package events_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
)

func TestLegacyEventSchemaIsClassifiedBeforeUnionDecode(t *testing.T) {
	for name, wire := range map[string]string{
		"removed plan":                  `{"eventId":"01234567-89ab-4cde-8fab-0123456789ab","namespace":"one","sessionName":"demo","sessionId":"native","time":"2026-10-20T00:00:00Z","itemId":"plan","type":"agent.plan","payload":{"planId":"plan-1","entries":[]}}`,
		"tool call without identity":    `{"eventId":"01234567-89ab-4cde-8fab-0123456789ab","namespace":"one","sessionName":"demo","sessionId":"native","time":"2026-10-20T00:00:00Z","itemId":"tool","type":"agent.tool_call","payload":{"id":"tool","kind":"execute","name":"Bash","rawInput":{}}}`,
		"tool result without identity":  `{"eventId":"01234567-89ab-4cde-8fab-0123456789ab","namespace":"one","sessionName":"demo","sessionId":"native","time":"2026-10-20T00:00:00Z","itemId":"tool","type":"agent.tool_result","payload":{"id":"tool","kind":"execute","name":"Bash","status":"completed"}}`,
		"tool call with null provider":  `{"eventId":"01234567-89ab-4cde-8fab-0123456789ab","namespace":"one","sessionName":"demo","sessionId":"native","time":"2026-10-20T00:00:00Z","itemId":"tool","type":"agent.tool_call","payload":{"id":"tool","kind":"execute","toolName":"Bash","provider":null,"rawInput":{}}}`,
		"tool result with numeric name": `{"eventId":"01234567-89ab-4cde-8fab-0123456789ab","namespace":"one","sessionName":"demo","sessionId":"native","time":"2026-10-20T00:00:00Z","itemId":"tool","type":"agent.tool_result","payload":{"id":"tool","kind":"execute","toolName":1,"provider":"agent","status":"completed"}}`,
		"tool call with empty provider": `{"eventId":"01234567-89ab-4cde-8fab-0123456789ab","namespace":"one","sessionName":"demo","sessionId":"native","time":"2026-10-20T00:00:00Z","itemId":"tool","type":"agent.tool_call","payload":{"id":"tool","kind":"execute","toolName":"Bash","provider":"","rawInput":{}}}`,
		"tool result with empty name":   `{"eventId":"01234567-89ab-4cde-8fab-0123456789ab","namespace":"one","sessionName":"demo","sessionId":"native","time":"2026-10-20T00:00:00Z","itemId":"tool","type":"agent.tool_result","payload":{"id":"tool","kind":"execute","toolName":"","provider":"agent","status":"completed"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var event events.SessionEvent
			err := events.DecodeSessionEvent([]byte(wire), &event)
			require.ErrorIs(t, err, events.ErrLegacyEventSchema)
			var legacy *events.LegacyEventSchemaError
			require.ErrorAs(t, err, &legacy)
		})
	}
}

func TestCurrentToolIdentityIsNotClassifiedAsLegacy(t *testing.T) {
	raw := `{"eventId":"01234567-89ab-4cde-8fab-0123456789ab","namespace":"one","sessionName":"demo","sessionId":"native","time":"2026-10-20T00:00:00Z","itemId":"tool","type":"agent.tool_call","payload":{"id":"tool","kind":"execute","toolName":"Bash","provider":"agent","rawInput":{}}}`
	var event events.SessionEvent
	require.NoError(t, events.DecodeSessionEvent([]byte(raw), &event))
	require.NotErrorIs(t, events.CheckLegacyEventSchema(json.RawMessage(raw)), events.ErrLegacyEventSchema)
}
