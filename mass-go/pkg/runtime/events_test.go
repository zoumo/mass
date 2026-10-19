package runtime_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestEventWireContract(t *testing.T) {
	base := runtime.EventBase{EventID: runtime.UUID(), Namespace: "default", SessionName: "demo", SessionID: "native", Time: time.Now().UTC(), ItemID: "tool"}
	call, err := runtime.NewEvent(base, runtime.ToolCall{ID: "tool", Kind: "execute", RawInput: nil}, runtime.NewSource())
	require.NoError(t, err)
	wire, err := json.Marshal(call)
	require.NoError(t, err)
	var value map[string]any
	require.NoError(t, json.Unmarshal(wire, &value))
	payload := value["payload"].(map[string]any)
	delete(payload, "rawInput")
	missing, err := json.Marshal(value)
	require.NoError(t, err)
	var decoded runtime.SessionEvent
	require.Error(t, json.Unmarshal(missing, &decoded), "missing rawInput cannot become explicit null")
	payload["rawInput"] = nil
	valid, err := json.Marshal(value)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(valid, &decoded))
	require.IsType(t, runtime.ToolCall{}, decoded.Payload())
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
	_, err = runtime.NewEvent(base, runtime.AgentMessage{MessageID: "message", Content: []runtime.ContentBlock{runtime.Text("full")}}, runtime.NewSource())
	require.Error(t, err)
}
