package events_test

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"os"
	"testing"
	"time"
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

func TestConfigNotificationsRejectMissingOrNullFields(t *testing.T) {
	for _, options := range []string{`[{}]`, `[{"group":"g","name":"G"}]`, `[{"group":"g","name":"G","options":null}]`, `[{"group":"g","name":"G","options":[{}]}]`} {
		t.Run(options, func(t *testing.T) {
			raw := `{"eventId":"01234567-89ab-4cde-8fab-0123456789ab","namespace":"one","sessionName":"demo","sessionId":"original","time":"2026-10-20T00:00:00Z","type":"session.config_option_update","payload":{"configOptions":[{"id":"mode","name":"Mode","type":"select","currentValue":"x","options":` + options + `}]}}`
			var e events.SessionEvent
			require.Error(t, json.Unmarshal([]byte(raw), &e))
		})
	}
}

func TestConfigNotificationsRejectTypedNilOptions(t *testing.T) {
	for _, options := range []any{[]events.ConfigOptionValue(nil), []events.ConfigOptionGroup(nil), []events.ConfigOptionGroup{{Group: "g", Name: "G"}}} {
		_, err := events.NewEvent(events.EventBase{EventID: "01234567-89ab-4cde-8fab-0123456789ab", Namespace: "one", SessionName: "demo", SessionID: "original", Time: time.Now()}, events.ConfigOptionUpdate{ConfigOptions: []events.ConfigOption{{ID: "mode", Name: "Mode", Type: "select", CurrentValue: "x", Options: options}}}, events.Source{})
		require.Error(t, err)
	}
}
