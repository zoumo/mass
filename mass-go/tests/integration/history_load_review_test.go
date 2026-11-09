package integration_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

func TestClaudeRetainedToolsRejectReuseAfterCompletion(t *testing.T) {
	for _, kind := range []string{"call", "result"} {
		t.Run(kind, func(t *testing.T) {
			call := map[string]any{"type": "tool_use", "id": "tool-1", "name": "Bash", "input": map[string]any{"command": "synthetic"}}
			result := map[string]any{"type": "tool_result", "tool_use_id": "tool-1", "content": "retained output"}
			rows := ticket07Rows()
			rows[1]["message"] = map[string]any{"role": "assistant", "content": []any{call}}
			rows = append(rows, map[string]any{"type": "user", "uuid": "result-row", "parentUuid": "last", "message": map[string]any{"role": "user", "content": []any{result}}})
			role, block := "assistant", call
			if kind == "result" {
				role, block = "user", result
			}
			rows = append(rows, map[string]any{"type": role, "uuid": "duplicate-row", "parentUuid": "result-row", "message": map[string]any{"role": role, "content": []any{block}}})
			_, url, cwd, _ := ticket07HTTP(t, "", rows)
			_, ref := ticket07Load(t, url, cwd)
			status := ticket07Status(t, url, ref, "failed")
			c := newHTTPClient(t, url, "")
			require.Eventually(t, func() bool {
				return c.GetSession(ctx(t), ref, &status) == nil && status.Status.HistoryLoad.CleanupCompleted
			}, 5*time.Second, time.Millisecond)
			require.False(t, status.Status.HistoryLoad.DeliveryCompleted)
			var page store.HistoryPage
			require.NoError(t, c.History(ctx(t), ref, store.HistoryQuery{}, &page))
			require.Len(t, page.Events, 3, "only the first call and its first result may be committed")
			require.IsType(t, events.ToolCall{}, page.Events[1].Payload())
			got := page.Events[2].Payload().(events.ToolResult)
			require.Equal(t, "Bash", got.ToolName)
			require.Equal(t, []events.ContentBlock{events.Text("retained output")}, got.Content)
			require.Equal(t, "result-row", page.Events[2].Replay.NativeRecordID)
		})
	}
}
