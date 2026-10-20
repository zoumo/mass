package events_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/zoumo/mass/mass-go/pkg/events"
)

type EventsSuite struct{ suite.Suite }

func (eventsSuite *EventsSuite) TestEventWireContract() {
	t := eventsSuite.T()
	base := events.EventBase{EventID: events.UUID(), Namespace: "default", SessionName: "demo", SessionID: "native", Time: time.Now().UTC(), ItemID: "tool"}
	call, err := events.NewEvent(base, events.ToolCall{ID: "tool", Kind: "execute", RawInput: nil}, events.NewSource())
	require.NoError(t, err)
	wire, err := json.Marshal(call)
	require.NoError(t, err)
	var value map[string]any
	require.NoError(t, json.Unmarshal(wire, &value))
	payload := value["payload"].(map[string]any)
	delete(payload, "rawInput")
	missing, err := json.Marshal(value)
	require.NoError(t, err)
	var decoded events.SessionEvent
	require.Error(t, json.Unmarshal(missing, &decoded), "missing rawInput cannot become explicit null")
	payload["rawInput"] = nil
	valid, err := json.Marshal(value)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(valid, &decoded))
	require.IsType(t, events.ToolCall{}, decoded.Payload())
	value["type"] = "agent.message"
	wrong, err := json.Marshal(value)
	require.NoError(t, err)
	require.Error(t, json.Unmarshal(wrong, &decoded), "wire type and payload cannot be mixed")
	value["type"] = "session.state_update"
	value["payload"] = map[string]any{"state": nil}
	missing, err = json.Marshal(value)
	require.NoError(t, err)
	require.Error(t, json.Unmarshal(missing, &decoded), "unknown state still requires previousState member")
	base.Meta = map[string]any{"bad": make(chan int)}
	_, err = events.NewEvent(base, events.AgentMessage{MessageID: "message", Content: []events.ContentBlock{events.Text("full")}}, events.NewSource())
	require.Error(t, err)
}

func TestEventsSuite(t *testing.T) { suite.Run(t, new(EventsSuite)) }
