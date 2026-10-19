package httpserver

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestLiveSSE(t *testing.T) {
	s, err := New(nil)
	require.NoError(t, err)
	defer s.Close()
	b := &binding{name: "demo", watchers: map[chan json.RawMessage]struct{}{}}
	s.sessions["demo"] = b
	h := httptest.NewServer(s.Handler())
	defer h.Close()
	resp, err := http.Post(h.URL+"/api/v1/agentd/jsonrpc", "application/json", bytes.NewBufferString(`{"jsonrpc":"2.0","id":"watch","method":"agentd/session/watch_event","params":{"sessionName":"demo"}}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	projector := rt.NewEventProjector("demo", "/work")
	observation := rt.Event{SessionID: "native-sse", Message: &rt.MessagePayload{MessageRef: "m", Role: "assistant", Stage: "delta", Content: rt.TextContent("preview", "delta", "partial", nil)}}
	preview, err := projector.Project(observation)
	require.NoError(t, err)
	require.NoError(t, b.publish(preview))
	observation.Message.Stage = "completed"
	observation.Message.Content = rt.TextContent("complete", "completed", "complete", nil)
	full, err := projector.Project(observation)
	require.NoError(t, err)
	require.NoError(t, b.publish(full))
	b.mu.Lock()
	for ch := range b.watchers {
		close(ch)
		delete(b.watchers, ch)
	}
	b.mu.Unlock()
	scan := bufio.NewScanner(resp.Body)
	require.True(t, scan.Scan())
	data, err := json.Marshal(preview)
	require.NoError(t, err)
	require.Equal(t, "data: "+string(data), scan.Text())
	require.True(t, scan.Scan())
	require.Empty(t, scan.Text())
	require.True(t, scan.Scan())
	require.Len(t, b.history, 1)
	require.Equal(t, "data: "+string(b.history[0]), scan.Text())
	require.True(t, scan.Scan())
	require.Empty(t, scan.Text())
	require.False(t, scan.Scan())
	require.NoError(t, scan.Err())
}

func rpcCall(t *testing.T, url, method string, params any) map[string]json.RawMessage {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "test", "method": method, "params": params})
	require.NoError(t, err)
	resp, err := http.Post(url+"/api/v1/agentd/jsonrpc", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	var result map[string]json.RawMessage
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	return result
}

func TestHTTPDiscoveryAndValidation(t *testing.T) {
	s, err := New([]rt.Registration{{Name: "claude", Type: "claude", Config: rt.Config{Executable: "/not-invoked", Dir: "/synthetic", Env: []string{}}}})
	require.NoError(t, err)
	defer s.Close()
	h := httptest.NewServer(s.Handler())
	defer h.Close()
	result := rpcCall(t, h.URL, "agentd/runtime/list", map[string]any{})
	require.NotContains(t, result, "error")
	require.Contains(t, string(result["result"]), "registered")
	require.NotContains(t, string(result["result"]), "not-invoked")
	for _, p := range []any{map[string]any{"sessionName": "x", "spec": map[string]string{"runtimeName": "claude", "cwd": "/wrong"}}, map[string]string{"extra": "field"}} {
		require.Contains(t, rpcCall(t, h.URL, "agentd/session/new", p), "error")
	}
	require.Contains(t, rpcCall(t, h.URL, "missing", map[string]any{}), "error")
	resp, err := http.Post(h.URL+"/api/v1/agentd/jsonrpc", "application/json", bytes.NewBufferString(`{"jsonrpc":"2.0","id":true,"method":"agentd/status"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	var bad map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&bad))
	require.Nil(t, bad["id"])
}

func TestDisconnectNeverCommitsCompletion(t *testing.T) {
	p := rt.NewEventProjector("demo", "/work")
	b := &binding{name: "demo", watchers: map[chan json.RawMessage]struct{}{}}
	watcher := make(chan json.RawMessage, 4)
	b.watchers[watcher] = struct{}{}
	b.disconnect(p, "native", "attachment_unavailable")
	require.Empty(t, b.history)
	var state map[string]any
	require.NoError(t, json.Unmarshal(<-watcher, &state))
	require.Equal(t, "session.state_update", state["type"])
	require.NotContains(t, state, "seq")
	require.Nil(t, state["payload"].(map[string]any)["state"])
	var failure map[string]any
	require.NoError(t, json.Unmarshal(<-watcher, &failure))
	require.Equal(t, "session.error", failure["type"])
	require.NotContains(t, failure, "seq")
}

func TestClaudeHTTPOpenAndHistory(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "mock-claude")
	output, err := exec.Command("go", "build", "-o", binary, "github.com/zoumo/agent-sdk-go/claude/cmd/mock").CombinedOutput()
	require.NoError(t, err, string(output))
	wires := []struct{ direction, channel, data string }{
		{"client", "control", `{"type":"control_request","request_id":"init","request":{"subtype":"initialize","hooks":null,"forwardSubagentText":true}}`},
		{"server", "control", `{"type":"control_response","response":{"subtype":"success","request_id":"init","response":{}}}`},
		{"client", "protocol", `{"type":"user","message":{"role":"user","content":"hello"},"parent_tool_use_id":null,"session_id":""}`},
		{"server", "protocol", `{"type":"system","subtype":"init","session_id":"native-http"}`},
		{"server", "protocol", `{"type":"assistant","session_id":"native-http","message":{"id":"m","role":"assistant","content":[{"type":"text","text":"reply"}]}}`},
		{"server", "protocol", `{"type":"result","subtype":"success","session_id":"native-http","is_error":false}`},
	}
	frames := []map[string]any{}
	for i, w := range wires {
		frames = append(frames, map[string]any{"ordinal": i + 1, "direction": w.direction, "channel": w.channel, "data": json.RawMessage(w.data)})
	}
	data, err := json.Marshal(map[string]any{"version": 1, "provenance": "synthetic", "frames": frames})
	require.NoError(t, err)
	scenario := filepath.Join(dir, "scenario.json")
	require.NoError(t, os.WriteFile(scenario, data, 0600))
	s, err := New([]rt.Registration{{Name: "claude", Type: "claude", Config: rt.Config{Executable: binary, Dir: dir, Env: []string{"CLAUDE_MOCK_SCENARIO=" + scenario}}}})
	require.NoError(t, err)
	defer s.Close()
	h := httptest.NewServer(s.Handler())
	defer h.Close()
	created := rpcCall(t, h.URL, "agentd/session/new", map[string]any{"sessionName": "demo", "spec": map[string]string{"runtimeName": "claude", "cwd": dir}, "initialInput": "hello"})
	require.NotContains(t, created, "error", fmt.Sprint(created))
	params := map[string]string{"sessionName": "demo"}
	require.Eventually(t, func() bool {
		result := rpcCall(t, h.URL, "agentd/session/status", params)
		var view map[string]any
		_ = json.Unmarshal(result["result"], &view)
		return view["sessionId"] == "native-http"
	}, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		result := rpcCall(t, h.URL, "agentd/session/get_event", params)
		return bytes.Contains(result["result"], []byte("reply"))
	}, 5*time.Second, 10*time.Millisecond)
	result := rpcCall(t, h.URL, "agentd/session/get_event", params)
	var history struct {
		Events []struct {
			SchemaVersion int    `json:"schemaVersion"`
			SessionName   string `json:"sessionName"`
			SessionID     string `json:"sessionId"`
			Type          string `json:"type"`
			Seq           uint64 `json:"seq"`
		}
	}
	require.NoError(t, json.Unmarshal(result["result"], &history))
	require.Len(t, history.Events, 1, "only complete facts belong in history")
	require.Equal(t, 2, history.Events[0].SchemaVersion)
	require.Equal(t, "demo", history.Events[0].SessionName)
	require.Equal(t, "native-http", history.Events[0].SessionID)
	require.Equal(t, "agent.message", history.Events[0].Type)
	require.Equal(t, uint64(1), history.Events[0].Seq)
	require.Contains(t, rpcCall(t, h.URL, "agentd/session/new", map[string]any{"sessionName": "demo", "spec": map[string]string{"runtimeName": "claude", "cwd": dir}}), "error")
	require.NotContains(t, rpcCall(t, h.URL, "agentd/session/archive", params), "error")
}
