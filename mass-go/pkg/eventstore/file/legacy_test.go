package file_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/eventstore/file"
)

func TestLegacyEventSchemaFailsOpenWithoutRewritingAuthority(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any, events.SessionEvent){
		"removed plan": func(raw map[string]any, event events.SessionEvent) {
			raw["type"] = "agent.plan"
			raw["payload"] = map[string]any{
				"planId": "plan-1",
				"entries": []any{map[string]any{
					"content":  "legacy step",
					"priority": "high",
					"status":   "pending",
				}},
			}
		},
		"tool without identity": func(raw map[string]any, event events.SessionEvent) {
			raw["type"] = "agent.tool_call"
			raw["payload"] = map[string]any{
				"id":       "tool",
				"kind":     "execute",
				"name":     "Bash",
				"rawInput": map[string]any{"command": "printf legacy"},
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			st, dir := open(t)
			original := event(t, "native", "item", events.AgentMessage{
				MessageID: "message",
				Content:   []events.ContentBlock{events.Text("current")},
			})
			_, err := st.Append("native", []events.SessionEvent{original})
			require.NoError(t, err)
			require.NoError(t, st.Close())

			path := logPath(dir)
			line := readBytes(t, path)
			var raw map[string]any
			require.NoError(t, json.Unmarshal(line, &raw))
			mutate(raw, original)
			legacy, err := json.Marshal(raw)
			require.NoError(t, err)
			legacy = append(legacy, '\n')
			require.NoError(t, os.WriteFile(path, legacy, 0o600))

			for _, readOnly := range []bool{true, false} {
				opened, err := file.Open(dir, readOnly)
				require.Nil(t, opened)
				require.ErrorIs(t, err, events.ErrLegacyEventSchema)
				var classified *events.LegacyEventSchemaError
				require.ErrorAs(t, err, &classified)
				require.Equal(t, legacy, readBytes(t, path), "opening must not rewrite the authoritative log")
			}
		})
	}
}
