package events_test

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"os"
	"testing"
)

func TestHarnessEventExamplesRoundTrip(t *testing.T) {
	data, err := os.ReadFile("../../../harness/modules/runtime/event-examples.json")
	require.NoError(t, err)
	var examples struct {
		Events []json.RawMessage `json:"events"`
	}
	require.NoError(t, json.Unmarshal(data, &examples))
	require.Len(t, examples.Events, 17)
	for _, raw := range examples.Events {
		var label struct {
			Type string `json:"type"`
		}
		require.NoError(t, json.Unmarshal(raw, &label))
		t.Run(label.Type, func(t *testing.T) {
			var event events.SessionEvent
			require.NoError(t, json.Unmarshal(raw, &event))
			out, err := json.Marshal(event)
			require.NoError(t, err)
			require.JSONEq(t, string(raw), string(out))
		})
	}
}
