package file

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

func TestLegacyEventSchemaIsClassifiedInSQLReadPaths(t *testing.T) {
	for name, read := range map[string]func(*Store, events.SessionEvent) error{
		"history page": func(st *Store, event events.SessionEvent) error {
			_, err := st.ListHistory("native", store.HistoryQuery{})
			return err
		},
		"identity retry": func(st *Store, event events.SessionEvent) error {
			_, err := st.Append("native", []events.SessionEvent{event})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			st, err := Open(t.TempDir(), false)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, st.Close()) })

			at := time.Date(2026, 10, 20, 1, 2, 3, 4, time.UTC)
			event, err := events.NewEvent(events.EventBase{
				EventID:     uuid.NewString(),
				Namespace:   "default",
				SessionName: "demo",
				SessionID:   "native",
				Time:        at,
				ItemID:      "message",
			}, events.AgentMessage{MessageID: "message", Content: []events.ContentBlock{events.Text("current")}}, events.NewSource())
			require.NoError(t, err)
			_, err = st.Append("native", []events.SessionEvent{event})
			require.NoError(t, err)

			legacy, err := json.Marshal(map[string]any{
				"eventId":     event.EventID,
				"namespace":   event.Namespace,
				"sessionName": event.SessionName,
				"sessionId":   event.SessionID,
				"time":        event.Time,
				"itemId":      "tool",
				"type":        "agent.tool_call",
				"payload": map[string]any{
					"id":       "tool",
					"kind":     "execute",
					"name":     "Bash",
					"rawInput": map[string]any{"command": "printf legacy"},
				},
			})
			require.NoError(t, err)
			_, err = st.sql.conn.ExecContext(context.Background(),
				"UPDATE event SET payload=? WHERE session_id=? AND event_id=?",
				legacy, "native", event.EventID)
			require.NoError(t, err)

			err = read(st, event)
			require.ErrorIs(t, err, events.ErrLegacyEventSchema)
		})
	}
}
